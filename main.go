// ternly — a single-binary coding agent that auto-discovers every model you can
// reach (paid APIs, free tiers, local servers) and routes each task to the
// cheapest one that can do it well.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/graph"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/memory"
	"github.com/rajasatyajit/ternly/internal/session"
	"github.com/rajasatyajit/ternly/internal/tools"
	"github.com/rajasatyajit/ternly/internal/tui"
)

var version = "0.1.0"

type fileConfig struct {
	Mode    string         `json:"mode"`
	Budget  float64        `json:"budget"`
	Verify  *string        `json:"verify"`
	NoLocal bool           `json:"no_local"`
	Model   string         `json:"model"`
	Tiers   map[string]int `json:"tiers"`
	Limits  struct {
		Steps       *int     `json:"steps"`
		TurnMinutes *float64 `json:"turn_minutes"`
		TurnUSD     *float64 `json:"turn_usd"`
	} `json:"limits"`
	Checkpoints     *bool `json:"checkpoints"`
	CheckpointCapMB *int  `json:"checkpoint_cap_mb"`
	AutoResume      *bool `json:"auto_resume"`
	CodeGraph       *bool `json:"code_graph"`
	Memory          *bool `json:"memory"`         // long-term memory (default on)
	MemoryBudget    *int  `json:"memory_budget"`  // tokens of notes injected per turn (default 600)
	MemoryVectors   *bool `json:"memory_vectors"` // use a local Ollama embedding model if present (default on)
	MemoryEnrich    *bool `json:"memory_enrich"`  // the cheapest model writes other wordings per note (default on)
	Providers       []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		BaseURL string `json:"base_url"`
		KeyEnv  string `json:"key_env"`
		Local   bool   `json:"local"`
	} `json:"providers"`
}

func main() { os.Exit(run()) }

func run() int {
	var (
		prompt     = flag.String("p", "", "run one prompt headlessly and print the answer")
		model      = flag.String("model", "", "pin a model (provider/id or substring); default: auto-route")
		mode       = flag.String("mode", "", "permissions: ask | edits | yolo")
		budget     = flag.Float64("budget", -1, "hard USD spend cap per session (0 = none)")
		verify     = flag.String("verify", "", "command run after edits (default: auto-detected; 'off' disables)")
		localOnly  = flag.Bool("local-only", false, "use only local models (zero cost, fully offline)")
		noLocal    = flag.Bool("no-local", false, "ignore local model servers")
		noSandbox  = flag.Bool("no-sandbox", false, "run shell commands without bubblewrap")
		noNet      = flag.Bool("no-net", false, "deny network to shell commands (bubblewrap)")
		projectMCP = flag.Bool("project-mcp", false, "also start MCP servers from ./.mcp.json (untrusted repo config)")
		listModels = flag.Bool("models", false, "list discovered models and exit")
		dir        = flag.String("C", ".", "workspace directory")
		showVer    = flag.Bool("version", false, "print version")
		cont       = flag.Bool("c", false, "continue the most recent session in this directory")
		resumeID   = flag.String("resume", "", "resume session `id` (bare --resume: choose one)")
		newSession = flag.Bool("new", false, "start a new session instead of auto-resuming the last one")
	)
	flag.BoolVar(cont, "continue", false, "same as -c")
	os.Args = append(os.Args[:1], bareResume(os.Args[1:])...)
	flag.Parse()
	if *showVer {
		fmt.Println("ternly", version)
		return 0
	}
	if *prompt == "" && flag.NArg() > 0 {
		*prompt = strings.Join(flag.Args(), " ")
	}

	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".config", "ternly")
	cacheDir := filepath.Join(home, ".cache", "ternly")
	dataDir := filepath.Join(home, ".local", "share", "ternly") // sessions (M2)
	var notes []string
	for _, d := range [][2]string{{filepath.Join(home, ".config", "vane"), cfgDir}, {filepath.Join(home, ".cache", "vane"), cacheDir}} {
		switch moved, err := migrateLegacy(d[0], d[1]); { // pre-rename installs
		case err != nil:
			notes = append(notes, "migrating "+d[0]+": "+err.Error())
		case moved:
			notes = append(notes, "migrated "+d[0]+" → "+d[1])
		}
	}
	for _, d := range []string{cacheDir, dataDir} {
		_ = os.MkdirAll(d, 0o700) // must exist so the sandbox can mask it
	}

	var fc fileConfig
	if b, err := os.ReadFile(filepath.Join(cfgDir, "config.json")); err == nil {
		if err := json.Unmarshal(b, &fc); err != nil {
			notes = append(notes, "config.json: "+err.Error())
		}
	}
	pick := func(flagV, cfgV, def string) string {
		if flagV != "" {
			return flagV
		}
		if cfgV != "" {
			return cfgV
		}
		return def
	}
	permMode := pick(*mode, fc.Mode, "ask")
	if permMode != "ask" && permMode != "edits" && permMode != "yolo" {
		fmt.Fprintln(os.Stderr, "invalid --mode; use ask, edits or yolo")
		return 2
	}

	keys, warn := discover.LoadKeys(cfgDir)
	notes = append(notes, warn...)
	var scrub []string
	for _, p := range discover.Builtins {
		scrub = append(scrub, p.EnvKeys...)
	}
	extra := make([]discover.Provider, 0, len(fc.Providers))
	for _, p := range fc.Providers {
		kind := p.Kind
		if kind == "" {
			kind = "openai"
		}
		var envs []string
		if p.KeyEnv != "" {
			envs = []string{p.KeyEnv}
			scrub = append(scrub, p.KeyEnv)
		}
		extra = append(extra, discover.Provider{ID: p.ID, Name: orStr(p.Name, p.ID), Kind: kind, BaseURL: p.BaseURL, EnvKeys: envs, Local: p.Local})
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	dopts := discover.Options{CacheDir: cacheDir, Keys: keys, Extra: extra, NoLocal: *noLocal || fc.NoLocal, LocalOnly: *localOnly, Overrides: fc.Tiers}
	discoverFn := func() ([]*discover.Model, []string) { return discover.Discover(ctx, dopts) }

	if *listModels {
		ms, w := discoverFn()
		for _, x := range w {
			fmt.Fprintln(os.Stderr, "warning:", x)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "TIER\tTOOLS\tPRICE $/Mtok\tCTX\tMODEL")
		for _, m := range ms {
			fmt.Fprintf(tw, "T%d\t%v\t%s\t%d\t%s\n", m.Tier, m.Tools, discover.Price(m), m.Ctx, m.Key())
		}
		tw.Flush()
		return 0
	}

	pol := tools.NewPolicy(permMode, nil)
	sb := tools.NewSandbox(!*noSandbox, *noNet, scrub)
	sb.Mask = []string{cfgDir, cacheDir, dataDir}                              // keys, checkpoints and sessions stay out of reach of model-run commands
	if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil { // e.g. GOCACHE in /tmp: the sandbox's /tmp is private
		if gc := strings.TrimSpace(string(out)); filepath.IsAbs(gc) && !sb.Writable(*dir, gc) {
			if os.MkdirAll(gc, 0o700) == nil {
				sb.Binds = append(sb.Binds, gc)
			}
		}
	}
	if !*noSandbox && sb.Bwrap == "" {
		notes = append(notes, "bubblewrap not found — shell commands run unsandboxed (sudo pacman -S bubblewrap)")
	}
	reg, err := tools.NewRegistry(*dir, pol, sb, tools.NewRedactor(keys))
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace:", err)
		return 2
	}
	if reg.Root == home || reg.Root == "/" {
		notes = append(notes, "workspace is "+reg.Root+" — consider running ternly inside a project directory")
	}

	mcpPaths := []string{filepath.Join(cfgDir, "mcp.json")}
	if *projectMCP {
		mcpPaths = append(mcpPaths, filepath.Join(reg.Root, ".mcp.json"))
	} else if _, err := os.Stat(filepath.Join(reg.Root, ".mcp.json")); err == nil {
		notes = append(notes, "./.mcp.json found but not started (repo-supplied servers are untrusted); use --project-mcp")
	}
	servers, mw := tools.LoadMCP(ctx, reg, mcpPaths)
	notes = append(notes, mw...)
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	router := discover.NewRouter()
	var emit func(agent.Event)
	ag := agent.New(reg, router, func(e agent.Event) { emit(e) })
	vcmd := agent.DetectVerify(reg.Root)
	if fc.Verify != nil {
		vcmd = *fc.Verify
	}
	if *verify != "" {
		vcmd = *verify
	}
	if vcmd == "off" {
		vcmd = ""
	}
	ag.SetVerify(vcmd)
	ag.Budget = fc.Budget
	if *budget >= 0 {
		ag.Budget = *budget
	}
	if v := fc.Limits.Steps; v != nil {
		ag.Limits.Steps = *v
	}
	if v := fc.Limits.TurnMinutes; v != nil {
		ag.Limits.Time = time.Duration(*v * float64(time.Minute))
	}
	if v := fc.Limits.TurnUSD; v != nil {
		ag.Limits.TurnUSD = *v
	}
	project, err := session.OpenProject(dataDir, reg.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		return 2
	}
	var repo *checkpoint.Repo
	if fc.Checkpoints == nil || *fc.Checkpoints {
		if project.AdoptedKey != "" { // the workspace moved: its checkpoints move with its sessions
			_ = os.Rename(filepath.Join(cacheDir, "checkpoints", project.AdoptedKey+".git"), filepath.Join(cacheDir, "checkpoints", project.Key+".git"))
		}
		if repo, err = checkpoint.OpenRepo(reg.Root, cacheDir, project.Key); err != nil {
			notes = append(notes, "checkpoints off ("+err.Error()+") — /undo and /rewind can only rewind the conversation")
			repo = nil
		}
	}
	mgr := &session.Manager{Project: project, Agent: ag, Repo: repo, Policy: pol, Router: router}
	var gs *graph.Service
	if _, err := os.Stat(filepath.Join(reg.Root, "go.mod")); err == nil && (fc.CodeGraph == nil || *fc.CodeGraph) {
		if project.AdoptedKey != "" { // the graph records its root path: rebuild rather than move
			_ = os.RemoveAll(filepath.Join(cacheDir, "graphs", "projects", project.AdoptedKey))
		}
		gs = graph.NewService(reg.Root, cacheDir, project.Key, func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
			return sb.Output(ctx, dir, env, argv...) // go list compiles repo code: sandboxed
		})
		gs.Start(ctx) // the first thing ternly does in a codebase: load or build its graph
		for _, t := range graph.Tools(gs) {
			reg.Add(t)
		}
		ag.AddInstructions(graph.Guidance)
	}
	var mem *memory.Memory
	if fc.Memory == nil || *fc.Memory {
		if mem, err = memory.Open(project.Dir, filepath.Join(dataDir, "user")); err != nil {
			notes = append(notes, "memory off: "+err.Error())
			mem = nil
		}
	}
	if mem != nil {
		defer mem.Close()
		mem.Root, mem.Redact, mem.Suspicious = reg.Root, reg.Redact.Apply, tools.Suspicious
		mem.Notify = func(s string) { ag.Emit(agent.Event{Kind: agent.EvStatus, Text: s}) }
		mem.SessionID = func() string {
			if s := mgr.Current(); s != nil {
				return s.ID
			}
			return ""
		}
		if fc.MemoryBudget != nil {
			mem.Budget = *fc.MemoryBudget
		}
		if gs != nil {
			mem.Related = func(file string) []string {
				g, _, err := gs.Graph(ctx, 50*time.Millisecond) // never wait for a first build here
				if err != nil || g == nil {
					return nil
				}
				var out []string
				for _, r := range g.RelatedFiles(file) {
					out = append(out, r.File)
				}
				return out
			}
		}
		ag.Mem = mem
		for _, t := range memory.Tools(mem) {
			reg.Add(t)
		}
		ag.AddInstructions(memory.Guidance)
		if fc.MemoryEnrich == nil || *fc.MemoryEnrich {
			mem.SetEnricher(&memory.LLMEnricher{Model: "cheapest", Complete: func(ctx context.Context, system, user string) (string, error) {
				select { // discovery runs in the background
				case <-router.Ready():
				case <-ctx.Done():
					return "", ctx.Err()
				}
				um := router.Utility(4000)
				if um == nil {
					return "", errors.New("no model for enrichment")
				}
				out, u, err := llm.Collect(llm.New(um.Provider.Endpoint()).Stream(ctx, llm.Request{Model: um.ID, System: system,
					Messages: []llm.Message{{Role: "user", Content: user}}, MaxTokens: 160}))
				ag.Commit(agent.Record{T: "usage", Usage: &u, Cost: um.Cost(u)}) // counted like any other spend
				return out, err
			}})
		}
		if (fc.MemoryVectors == nil || *fc.MemoryVectors) && !*noLocal && !fc.NoLocal {
			go func() { // a local embedding model, if Ollama has one (never a cloud model)
				base := "http://127.0.0.1:11434"
				if h := keys["OLLAMA_HOST"]; h != "" {
					base = strings.TrimRight(h, "/")
					if !strings.HasPrefix(base, "http") {
						base = "http://" + base
					}
				}
				if e := memory.FindOllama(ctx, base); e != nil {
					mem.SetEmbedder(e)
				}
			}()
		}
	}
	if *resumeID == "?" && *prompt != "" {
		list, _ := project.List()
		fmt.Fprintln(os.Stderr, "choose a session: ternly --resume <id> -p …")
		for _, m := range list {
			fmt.Fprintf(os.Stderr, "  %s  %-40s %s ago · %d turns · $%.4f%s\n", m.ID, orStr(m.Title, "(untitled)"), time.Since(m.Active).Round(time.Minute), m.Turns, m.Cost, map[bool]string{true: " · open elsewhere"}[m.Locked])
		}
		return 2
	}
	auto := *prompt == "" && !*newSession && (fc.AutoResume == nil || *fc.AutoResume) // headless one-shots never auto-resume
	sess, st, banner, err := chooseSession(project, *cont, *resumeID, auto && *resumeID == "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	restoredPin, err := mgr.Attach(sess, st, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session:", err)
		return 2
	}
	defer mgr.Close("paused") // Esc/Ctrl+C/exit: resumable later; /stop closes it as stopped first
	if *mode != "" {          // CLI flags win over restored settings
		pol.SetMode(permMode)
	}
	if *verify != "" {
		ag.SetVerify(vcmd)
	}
	if *budget >= 0 {
		lim, _ := ag.Caps()
		ag.SetCaps(lim, *budget)
	}
	if st.Settings == nil {
		mgr.SaveSettings() // a new session records the settings it starts with
	}
	capMB := 2048
	if fc.CheckpointCapMB != nil {
		capMB = *fc.CheckpointCapMB
	}
	// Started once events can be shown: drift since the session paused (or a
	// baseline snapshot so the first edit doesn't wait), then checkpoint GC and the disk cap.
	background := func() {
		go func() {
			if cs := mgr.Drift(ctx, st); len(cs) > 0 {
				ag.Emit(agent.Event{Kind: agent.EvStatus, Text: fmt.Sprintf("%d file(s) changed outside this session since it paused; the model is told on your next prompt", len(cs))})
			} else if ag.CP != nil {
				_, _ = ag.CP.Snapshot(ctx)
			}
			if repo == nil {
				return
			}
			_ = repo.GC(ctx, project.Saved)
			if msg, err := repo.EnforceCap(ctx, int64(capMB)<<20, pruneOrder(project, sess.ID)); msg != "" && err == nil {
				ag.Emit(agent.Event{Kind: agent.EvStatus, Text: msg})
			}
		}()
	}
	pin := pick(*model, restoredPin, fc.Model)

	if *prompt != "" {
		code := headless(ctx, ag, router, discoverFn, pin, *prompt, &emit, notes, background)
		mgr.AutoTitle(ctx)
		return code
	}

	app := &tui.App{Agent: ag, Router: router, Reg: reg, Discover: func() ([]*discover.Model, []string) {
		ms, w := discoverFn()
		if pin != "" {
			router.SetModels(ms)
			if _, err := router.Pin(pin); err != nil {
				w = append(w, err.Error())
			}
		}
		return ms, w
	}, Notes: notes, Version: version, Sessions: mgr, Banner: banner, Pick: *resumeID == "?", Memory: mem, Theme: os.Getenv("TERNLY_THEME")}
	m := tui.New(app, darkTerminal()) // a first guess; the terminal's own answer arrives as a message
	p := tea.NewProgram(m, tea.WithContext(ctx))
	emit = func(e agent.Event) { p.Send(tuiMsg(e)) }
	pol.Ask = tui.Asker(p)
	background()
	if _, err := p.Run(); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	l := ag.Ledger()
	if l.Turns > 0 {
		fmt.Printf("ternly: %d turns · $%.4f · %d in / %d out tokens · %.0f%% cache hits\n",
			l.Turns, l.Cost, l.Usage.In+l.Usage.CacheRead+l.Usage.CacheWrite, l.Usage.Out, l.CacheRate()*100)
	}
	return 0
}

// bareResume turns a value-less --resume (last, or followed by another flag)
// into --resume=?, meaning "let me choose".
func bareResume(args []string) []string {
	for i, a := range args {
		if a == "--" {
			break
		}
		if (a == "-resume" || a == "--resume") && (i+1 == len(args) || strings.HasPrefix(args[i+1], "-")) {
			args[i] = "-resume=?"
		}
	}
	return args
}

// chooseSession opens the session to start with: an explicit id, the latest
// (-c), the auto-resume pick, or a new one. The banner describes the choice.
func chooseSession(p *session.Project, cont bool, id string, auto bool) (*session.Session, agent.State, string, error) {
	list, _ := p.List()
	switch {
	case id != "" && id != "?":
		s, st, err := p.Open(id)
		if err == session.ErrLocked {
			err = fmt.Errorf("session %s is open in another ternly process; start with --new and /fork %s to branch it", id, id)
		}
		return s, st, resumedBanner(st, s.Meta()), err
	case cont:
		if len(list) == 0 {
			break
		}
		if list[0].Locked {
			return nil, agent.State{}, "", fmt.Errorf("the latest session (%s) is open in another ternly process; use --new, then /fork %s", list[0].ID, list[0].ID)
		}
		s, st, err := p.Open(list[0].ID)
		return s, st, resumedBanner(st, list[0]), err
	case auto || id == "?": // bare --resume: start as auto-resume would, then the TUI opens the picker
		pick, locked, stopped := session.Resumable(list)
		if pick != nil {
			if s, st, err := p.Open(pick.ID); err == nil {
				return s, st, resumedBanner(st, *pick), nil
			}
		}
		if locked != nil {
			s, err := p.Create()
			return s, agent.State{}, fmt.Sprintf("session “%s” is open in another ternly process — started a new one; /fork %s branches it", orStr(locked.Title, locked.ID), locked.ID), err
		}
		if stopped != nil {
			s, err := p.Create()
			return s, agent.State{}, fmt.Sprintf("your last session “%s” was stopped %s ago — started a new one; /resume %s reopens it", orStr(stopped.Title, stopped.ID), time.Since(stopped.Active).Round(time.Minute), stopped.ID), err
		}
	}
	s, err := p.Create()
	return s, agent.State{}, "", err
}

func resumedBanner(st agent.State, m session.Meta) string {
	return fmt.Sprintf("↺ resumed “%s” · %s ago · %d turns · $%.4f — /sessions to switch, /new for a fresh one",
		orStr(st.Title, m.ID), time.Since(st.Active).Round(time.Minute), st.Ledger.Turns, st.Ledger.Cost)
}

// pruneOrder lists sessions whose checkpoints the disk cap may drop: oldest
// activity first, never the current one or one open in another process.
func pruneOrder(p *session.Project, current string) []string {
	list, _ := p.List()
	var ids []string
	for i := len(list) - 1; i >= 0; i-- {
		if m := list[i]; m.ID != current && !m.Locked {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// tuiMsg converts agent events into the TUI's message type.
func tuiMsg(e agent.Event) tea.Msg { return tui.AgentMsg(e) }

func headless(ctx context.Context, ag *agent.Agent, router *discover.Router, disc func() ([]*discover.Model, []string), pin, prompt string, emit *func(agent.Event), notes []string, background func()) int {
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	ms, w := disc()
	for _, x := range w {
		fmt.Fprintln(os.Stderr, "warning:", x)
	}
	router.SetModels(ms)
	if pin != "" {
		if _, err := router.Pin(pin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	failed := false
	*emit = func(e agent.Event) {
		switch e.Kind {
		case agent.EvText:
			fmt.Print(e.Text)
		case agent.EvModel:
			fmt.Fprintf(os.Stderr, "◆ %s (T%d, %s) — %s\n", e.Model.Key(), e.Model.Tier, discover.Price(e.Model), e.Reason)
		case agent.EvToolStart:
			fmt.Fprintf(os.Stderr, "  → %s %s\n", e.Tool, e.Text)
		case agent.EvVerify:
			fmt.Fprintf(os.Stderr, "  ▸ verify: %s\n", e.Text)
		case agent.EvToolEnd:
			if !e.OK {
				fmt.Fprintf(os.Stderr, "  ✗ %s failed: %s\n", e.Tool, strings.SplitN(strings.TrimSpace(e.Text), "\n", 2)[0])
			} else if e.Tool == "verify" {
				fmt.Fprintf(os.Stderr, "  ✓ verified in %s\n", e.Elapsed.Round(time.Millisecond))
			}
		case agent.EvStatus:
			fmt.Fprintln(os.Stderr, "  ↻", e.Text)
		case agent.EvError:
			failed = true
			fmt.Fprintln(os.Stderr, "error:", e.Text)
		case agent.EvDone:
			fmt.Fprintf(os.Stderr, "\n$%.4f · %.0f%% cache hits\n", e.Ledger.Cost, e.Ledger.CacheRate()*100)
		}
	}
	background()
	ictx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	t0 := time.Now()
	ag.Run(ictx, prompt)
	fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(t0).Round(time.Millisecond))
	if failed {
		return 1
	}
	return 0
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// darkTerminal avoids an OSC 11 terminal query (it stalls on terminals that
// never answer). TERNLY_THEME=light|dark overrides; COLORFGBG is used when set.
func darkTerminal() bool {
	switch os.Getenv("TERNLY_THEME") {
	case "light":
		return false
	case "dark":
		return true
	}
	if v := os.Getenv("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		bg := parts[len(parts)-1]
		return bg != "7" && bg != "15"
	}
	return true
}

// migrateLegacy moves a pre-rename (vane) directory to its ternly location once.
// It never overwrites: if anything exists at newDir (even a dangling symlink) or
// its existence can't be determined, the old directory is left untouched.
// rename(2) also refuses to replace a non-empty directory, which closes the race
// between the check and the move.
func migrateLegacy(oldDir, newDir string) (bool, error) {
	if _, err := os.Lstat(newDir); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if fi, err := os.Stat(oldDir); err != nil || !fi.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return false, err
	}
	return true, nil
}
