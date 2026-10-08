// ternly — a single-binary coding agent that auto-discovers every model you can
// reach (paid APIs, free tiers, local servers) and routes each task to the
// cheapest one that can do it well.
package main

import (
	"bufio"
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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/capability"
	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/deps"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/graph"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/mcpremote"
	"github.com/rajasatyajit/ternly/internal/memory"
	"github.com/rajasatyajit/ternly/internal/plugins"
	"github.com/rajasatyajit/ternly/internal/session"
	"github.com/rajasatyajit/ternly/internal/status"
	"github.com/rajasatyajit/ternly/internal/tools"
	"github.com/rajasatyajit/ternly/internal/tui"
)

var version = "0.1.0"

type fileConfig struct {
	Mode              string                `json:"mode"`
	Budget            float64               `json:"budget"`
	Verify            *string               `json:"verify"`
	Reasoning         string                `json:"reasoning"`          // auto (default), off, low, medium, high (ADR 015)
	ReasoningLevels   []discover.EffortRule `json:"reasoning_levels"`   // per-model level map, ahead of the built-in one (ADR 016)
	ReasoningWatchdog *watchdogSetting      `json:"reasoning_watchdog"` // {tokens, seconds} before a step with no text or tool call is interrupted (0: default, -1: off); a bare number is tokens (issue #2)
	NoLocal           bool                  `json:"no_local"`
	Model             string                `json:"model"`
	Tiers             map[string]int        `json:"tiers"`
	Routing           routingConfig         `json:"routing"` // "v1", "v2" or {version, time_value_usd_per_hour, background_eval} (ADR 018)
	Limits            struct {
		Steps       *int     `json:"steps"`
		TurnMinutes *float64 `json:"turn_minutes"`
		TurnUSD     *float64 `json:"turn_usd"`
	} `json:"limits"`
	Checkpoints     *bool         `json:"checkpoints"`
	Levers          string        `json:"levers"`        // Phase C levers, comma-separated (ADR 029); TERNLY_LEVERS overrides
	Deterministic   *bool         `json:"deterministic"` // temperature 0 + a seed, and a response cache (ADR 029); TERNLY_DETERMINISTIC=1
	CheckpointCapMB *int          `json:"checkpoint_cap_mb"`
	AutoResume      *bool         `json:"auto_resume"`
	CodeGraph       *bool         `json:"code_graph"`
	Memory          *bool         `json:"memory"`         // long-term memory (default on)
	MemoryBudget    *int          `json:"memory_budget"`  // tokens of notes injected per turn (default 600)
	MemoryVectors   *bool         `json:"memory_vectors"` // use a local Ollama embedding model if present (default on)
	MemoryEnrich    enrichSetting `json:"memory_enrich"`  // other wordings per note: true/"local" (default), "remote", false
	Plugins         *bool         `json:"plugins"`        // plugins, skills, agents, rules (default on)
	Suggestions     *bool         `json:"suggestions"`    // suggest capabilities a task needs (default on)
	CatalogSources  *struct {
		Marketplaces []string `json:"marketplaces"` // extra raw marketplace.json URLs
		MCPRegistry  string   `json:"mcp_registry"` // another MCP registry base URL
		NPM          *bool    `json:"npm"`          // search npm's mcp-server keyword (default on)
		Offline      bool     `json:"offline"`      // never refresh (use what's already indexed)
	} `json:"catalog_sources"`
	Providers []struct {
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
		reasoning  = flag.String("reasoning", "", "reasoning budget: auto (routing decides), off (model default), low, medium, high")
		localOnly  = flag.Bool("local-only", false, "use only local models (zero cost, fully offline)")
		noLocal    = flag.Bool("no-local", false, "ignore local model servers")
		noSandbox  = flag.Bool("no-sandbox", false, "run shell commands without bubblewrap")
		accessible = flag.Bool("accessible", false, "screen-reader mode: linear output, plain text, nothing animates (also TERNLY_SCREEN_READER=1)")
		noNet      = flag.Bool("no-net", false, "deny network to shell commands (bubblewrap)")
		projectMCP = flag.Bool("project-mcp", false, "also start MCP servers from ./.mcp.json (untrusted repo config)")
		listModels = flag.Bool("models", false, "list discovered models and exit")
		routing    = flag.String("routing", "", "router: v1 (price only) or v2 (expected cost to finish; ADR 018); default from config")
		dir        = flag.String("C", ".", "workspace directory")
		showVer    = flag.Bool("version", false, "print version")
		mcpLogin   = flag.String("mcp-login", "", "log in to a remote MCP server (OAuth in the browser) and exit")
		noMemory   = flag.Bool("no-memory", false, "no long-term memory this run (neither recalled nor saved)")
		cont       = flag.Bool("c", false, "continue the most recent session in this directory")
		resumeID   = flag.String("resume", "", "resume session `id` (bare --resume: choose one)")
		newSession = flag.Bool("new", false, "start a new session instead of auto-resuming the last one")
		evalMode   = flag.Bool("eval", false, "measure how often --model fabricates (seeded traps; see docs/adr/012) and record its tier")
		evalRuns   = flag.Int("eval-runs", 1, "with --eval: runs of each trap")
		evalOnly   = flag.String("eval-only", "", "with --eval: only traps whose name matches this regexp")
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
	if *evalMode {
		if *model == "" {
			fmt.Fprintln(os.Stderr, "--eval needs --model (the model to measure)")
			return 2
		}
		var pass []string // what each trap's child run inherits
		if *localOnly {
			pass = append(pass, "--local-only")
		}
		if *noLocal {
			pass = append(pass, "--no-local")
		}
		if *budget >= 0 {
			pass = append(pass, "--budget", fmt.Sprint(*budget))
		}
		return runEval(dataDir, *model, pass, *evalRuns, *evalOnly)
	}
	inEval := os.Getenv("TERNLY_EVAL") == "1" // a trap run: no personal memory, no suggestions

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

	dopts := discover.Options{CacheDir: cacheDir, Keys: keys, Extra: extra, NoLocal: *noLocal || fc.NoLocal, LocalOnly: *localOnly, Overrides: fc.Tiers,
		Measured: measurements(filepath.Join(dataDir, "capability")), NoNet: *noNet}
	var stc *status.Core // the ADR 021 producer; set once the agent exists
	discoverAll := func(ctx context.Context) ([]*discover.Model, []discover.Connection, []string) {
		ms, conns, w := discover.DiscoverAll(ctx, dopts)
		if stc != nil {
			stc.SetConnections(conns)
		}
		return ms, conns, w
	}
	discoverFn := func() ([]*discover.Model, []string) { ms, _, w := discoverAll(ctx); return ms, w }

	if *listModels {
		ms, conns, w := discoverAll(ctx)
		for _, x := range w {
			fmt.Fprintln(os.Stderr, "warning:", x)
		}
		turn := agent.DefaultLimits.Time
		if v := fc.Limits.TurnMinutes; v != nil {
			turn = time.Duration(*v * float64(time.Minute))
		}
		cm, err := fc.Routing.costModel(*routing, dataDir, turn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		r := discover.NewRouter()
		r.SetModels(ms)
		r.SetCostModel(cm)
		if cm != nil { // each model's background-evaluation state, from the ledger
			r.SetEvalStatus(backgroundEvals(fc.Routing.BackgroundEval, *localOnly, dataDir, r, nil, nil).Status)
		}
		why := r.Explanations(modelsCtx)
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		head := "TIER\tTOOLS\tPRICE $/Mtok\tCTX\tBASIS\t"
		if why != nil {
			head += "ROUTING v2: RANK T1/T2/T3, P, T2 TASK (20k context)\t"
		}
		head += "MODEL" // last: scripts (and the e2e preflight) read the key from the end
		fmt.Fprintln(tw, head)
		for _, m := range ms {
			basis := m.Basis
			if ms := m.Measure; ms != nil {
				basis = fmt.Sprintf("measured(%s,%s,%druns;pass=%.0f%%[%.0f-%.0f];fab=%.0f%%,mem=%.0f%%,bait=%.0f%%)", ms.Source, ms.Measured.Format("2006-01-02"), ms.Runs, 100*ms.Pass, 100*ms.PassLo, 100*ms.PassHi, 100*ms.Fabrication, 100*ms.MemoryMisuse, 100*ms.Susceptibility)
				if ms.Baitable {
					basis += ",baitable"
				}
			}
			line := fmt.Sprintf("T%d\t%v\t%s\t%d\t%s\t", m.Tier, m.Tools, discover.Price(m), m.Ctx, basis)
			if w := why[m]; w != nil {
				line += w.Short() + "\t"
			} else if why != nil {
				line += "– (no tool calling, or too little context)\t"
			}
			line += m.Key()
			fmt.Fprintln(tw, line)
		}
		tw.Flush()
		if why == nil {
			fmt.Println("\nrouting v1 (price only); --routing v2 or \"routing\": \"v2\" ranks by expected cost to finish (ADR 018)")
		} else {
			fmt.Printf("\nrouting v2: score = (money + quota + $%.0f/h × time) / p(success); /models why <model> in the TUI explains one\n", cm.TimeValue)
		}
		fmt.Println() // how each model is reached (ADR 022)
		for _, l := range status.Lines(status.New(r, nil, conns, nil).Snapshot().Connections, discover.FindCLIs(), time.Now()) {
			fmt.Println(l)
		}
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
	if sb.Bwrap == "" { // ADR 016: no sandbox → every command asks, plugin code off
		pol.Unsandboxed = true
		notes = append(notes, unsandboxedNote(*noSandbox))
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
	servers, remotes, mw := tools.LoadMCP(ctx, reg, mcpPaths)
	notes = append(notes, mw...)
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	remote := &mcpremote.Manager{Reg: reg, Dir: filepath.Join(dataDir, "mcp"), Version: version, Redact: reg.Redact.Add}
	defer remote.Close()
	if *mcpLogin != "" {
		return mcpLoginCLI(ctx, remote, remotes, *mcpLogin)
	}
	switch {
	case len(remotes) > 0 && *noNet:
		notes = append(notes, fmt.Sprintf("--no-net: %d remote MCP server(s) not started", len(remotes)))
	case len(remotes) > 0:
		notes = append(notes, remote.Start(ctx, remotes)...)
	}

	router := discover.NewRouter()
	var emit func(agent.Event)
	ag := agent.New(reg, router, func(e agent.Event) { emit(e) })
	stc = status.New(router, ag, nil, discoverAll)
	vcmd := agent.DetectVerify(reg.Root)
	if fc.Verify != nil {
		vcmd = *fc.Verify
	}
	if *verify != "" {
		vcmd = *verify
	}
	ag.SetVerify(vcmd) // "off" disables it; "" leaves the coverage checks (ADR 015)
	ag.Budget = fc.Budget
	ag.Reasoning = orStr(*reasoning, fc.Reasoning)
	ag.EffortRules = fc.ReasoningLevels
	if v := fc.ReasoningWatchdog; v != nil {
		ag.Watchdog = agent.Watchdog{Tokens: v.Tokens, Idle: time.Duration(v.Seconds * float64(time.Second))}
		if v.legacy {
			notes = append(notes, fmt.Sprintf("reasoning_watchdog: a bare number now counts tokens, not stream chunks (%d tokens); write {\"tokens\": %d, \"seconds\": 300} to set both", v.Tokens, v.Tokens))
		}
	}
	switch ag.Reasoning {
	case "", "auto", "off", "low", "medium", "high":
	default:
		fmt.Fprintln(os.Stderr, "--reasoning: auto, off, low, medium or high")
		return 2
	}
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
	if cm, err := fc.Routing.costModel(*routing, dataDir, ag.Limits.Time); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	} else {
		router.SetCostModel(cm)
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
	pol.Checkpointed = repo != nil // a restricted model's edits are allowed only when undoable (ADR 020)
	mgr := &session.Manager{Project: project, Agent: ag, Repo: repo, Policy: pol, Router: router}
	factChecks := os.Getenv("TERNLY_NO_FACT_CHECKS") != "1" // measurement only: the eval's A/B of these checks
	ag.NoFactChecks = !factChecks
	levers := fc.Levers
	if v, ok := os.LookupEnv("TERNLY_LEVERS"); ok { // the task suite's A/B arms
		levers = v
	}
	if lv, toolLevers, err := agent.ParseLevers(levers); err != nil {
		fmt.Fprintln(os.Stderr, "levers:", err)
		return 2
	} else {
		ag.Levers = lv
		ag.Deterministic = (fc.Deterministic != nil && *fc.Deterministic) || os.Getenv("TERNLY_DETERMINISTIC") == "1"
		if ag.Deterministic && os.Getenv("TERNLY_RESPONSE_CACHE") != "0" {
			dir := os.Getenv("TERNLY_RESPONSE_CACHE_DIR")
			if dir == "" {
				dir = filepath.Join(cacheDir, "responses")
			}
			ag.Responses = &llm.Cache{Dir: dir}
		}
		for _, t := range toolLevers {
			reg.OutlineReads = reg.OutlineReads || t == "outline_reads"
			reg.NoSchemaRepair = reg.NoSchemaRepair || t == "no_schema_repair"
		}
	}
	if !*noNet && factChecks { // packages and versions the model adds are looked up (ADR 012)
		ag.DepCheck = &deps.Checker{HTTP: llm.HTTP}
	}
	var gs *graph.Service
	if (fc.CodeGraph == nil || *fc.CodeGraph) && graph.HasSources(reg.Root) {
		if project.AdoptedKey != "" { // the graph records its root path: rebuild rather than move
			_ = os.RemoveAll(filepath.Join(cacheDir, "graphs", "projects", project.AdoptedKey))
		}
		gs = graph.NewService(reg.Root, cacheDir, project.Key, func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
			return sb.Output(ctx, dir, env, argv...) // go list compiles repo code: sandboxed
		})
		gs.Start(ctx)                                    // the first thing ternly does in a codebase: load or build its graph
		ag.KnownSymbol = func(ref string) (bool, bool) { // answers' symbol references, against the graph as it stands
			if !factChecks {
				return false, false
			}
			g, _, err := gs.Graph(ctx, 2*time.Second) // not ready yet: not judged
			if err != nil || g == nil {
				return false, false
			}
			return g.Known(ref)
		}
		for _, t := range graph.Tools(gs) {
			reg.Add(t)
		}
		ag.AddInstructions(graph.Guidance)
	}
	var mem *memory.Memory
	if (fc.Memory == nil || *fc.Memory) && !*noMemory {
		userDir := filepath.Join(dataDir, "user")
		if inEval {
			userDir = filepath.Join(project.Dir, "eval-user") // the user's own notes would contaminate the measurement
		}
		if mem, err = memory.Open(project.Dir, userDir); err != nil {
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
		if fc.MemoryEnrich != enrichOff {
			remote := fc.MemoryEnrich == enrichRemote
			mem.SetEnricher(&memory.LLMEnricher{Model: "local, else the session's model", Complete: func(ctx context.Context, system, user string) (string, error) {
				select { // discovery runs in the background
				case <-router.Ready():
				case <-ctx.Done():
					return "", ctx.Err()
				}
				um := enrichModel(router, ag.Current(), remote)
				if um == nil {
					return "", errors.New("no model for enrichment yet") // retried at the next write
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
	var prt *plugins.Runtime
	if fc.Plugins == nil || *fc.Plugins {
		if st, err := plugins.OpenStore(filepath.Join(dataDir, "plugins")); err != nil {
			notes = append(notes, "plugins off: "+err.Error())
		} else {
			prt = &plugins.Runtime{Store: st, Reg: reg, Root: reg.Root, Home: home, Subagent: ag.Subagent, NoCode: noCodeReason(pol),
				Notify: func(s string) { ag.Emit(agent.Event{Kind: agent.EvStatus, Text: s}) }, Note: ag.SetNote, Mode: pol.Mode,
				SessionID: func() string {
					if s := mgr.Current(); s != nil {
						return s.ID
					}
					return ""
				}}
			reg.Hooks, ag.PromptHook = prt, prt.PromptHook
			notes = append(notes, prt.ApplyAsync(ctx)...) // MCP servers start in the background
			if ins := prt.Instructions(); ins != "" {
				ag.AddInstructions(ins)
			}
			defer prt.Close()
			go prt.Watch(ctx, 3*time.Second)
			go func() { // SessionStart hooks: their output reaches the model once, framed as untrusted
				if c := prt.SessionStart(ctx, "startup"); c != "" {
					framed, _ := reg.Frame.Wrap("hook", c)
					ag.SetNote("Context from plugin SessionStart hooks:\n" + framed)
				}
			}()
		}
	}
	var caps *capability.Service
	if prt != nil && (fc.Suggestions == nil || *fc.Suggestions) && !inEval {
		src := capability.DefaultSources
		if cs := fc.CatalogSources; cs != nil {
			src.Marketplaces = append(src.Marketplaces, cs.Marketplaces...)
			if cs.MCPRegistry != "" {
				src.MCPRegistry = cs.MCPRegistry
			}
			if cs.NPM != nil {
				src.NPMSearch = *cs.NPM
			}
		}
		cat := &capability.Catalog{Dir: filepath.Join(dataDir, "catalog"), Sources: src}
		caps = &capability.Service{Catalog: cat, Suggester: &capability.Suggester{File: filepath.Join(project.Dir, "capability.json")},
			Validator: &capability.Validator{}, Outcomes: &capability.Outcomes{File: filepath.Join(dataDir, "catalog", "outcomes.jsonl")},
			Detector: &capability.Detector{
				Covered: capability.CoveredBy(func() []string {
					var out []string
					for _, sp := range reg.Specs() {
						out = append(out, sp.Name+" "+sp.Description)
					}
					return out
				}),
				Classify: func(ctx context.Context, prompt string) (string, error) { // only for ambiguous mentions; a few tokens
					um := enrichModel(router, ag.Current(), false) // the prompt goes to a local model, else the session's own
					if um == nil {
						return "", errors.New("no model")
					}
					out, u, err := llm.Collect(llm.New(um.Provider.Endpoint()).Stream(ctx, llm.Request{Model: um.ID, System: capability.ClassifyPrompt,
						Messages: []llm.Message{{Role: "user", Content: prompt}}, MaxTokens: 4}))
					ag.Commit(agent.Record{T: "usage", Usage: &u, Cost: um.Cost(u)})
					return out, err
				}}}
		defer cat.Close()
		go func() { // the catalog refreshes daily, in the background (the first time takes a few minutes)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			if (fc.CatalogSources == nil || !fc.CatalogSources.Offline) && cat.Due(24*time.Hour) {
				_ = cat.Refresh(ctx, nil)
			}
		}()
	}
	for _, p := range func() []plugins.Installed {
		if prt == nil {
			return nil
		}
		return prt.Store.List()
	}() {
		for _, v := range p.Env { // plugin secrets are never shown to a model
			reg.Redact.Add(v)
		}
	}
	reg.Hold() // start-up tools are in; from here every change waits for a turn boundary and is announced
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
	// The background work is waited for when ternly exits, so no git process
	// outlives it: the drift check and snapshot finish; gc is cancelled.
	bgCtx, bgCancel := context.WithCancel(ctx)
	var bgWG sync.WaitGroup
	defer func() {
		bgCancel()
		done := make(chan struct{})
		go func() { bgWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}()
	background := func() {
		bgWG.Add(1)
		go func() {
			defer bgWG.Done()
			if cs := mgr.Drift(ctx, st); len(cs) > 0 {
				ag.Emit(agent.Event{Kind: agent.EvStatus, Text: fmt.Sprintf("%d file(s) changed outside this session since it paused; the model is told on your next prompt", len(cs))})
			} else if ag.CP != nil {
				_, _ = ag.CP.Snapshot(ctx)
			}
			if repo == nil {
				return
			}
			if bgCtx.Err() != nil {
				return
			}
			_ = repo.GC(bgCtx, project.Saved) // cancelled at exit: only collection is interrupted
			if msg, err := repo.EnforceCap(bgCtx, int64(capMB)<<20, pruneOrder(project, sess.ID)); msg != "" && err == nil {
				ag.Emit(agent.Event{Kind: agent.EvStatus, Text: msg})
			}
		}()
	}
	pin := pick(*model, restoredPin, fc.Model)

	if *prompt != "" {
		code := headless(ctx, ag, router, discoverFn, pin, *prompt, &emit, notes, background)
		if !inEval {
			mgr.AutoTitle(ctx)
		}
		return code
	}

	app := &tui.App{Agent: ag, Router: router, Reg: reg, Access: accessFrom(*accessible), Discover: func() ([]*discover.Model, []string) {
		ms, w := discoverFn()
		if pin != "" {
			router.SetModels(ms)
			if _, err := router.Pin(pin); err != nil {
				w = append(w, err.Error())
			}
		}
		return ms, w
	}, Notes: notes, Remote: remote, Version: version, Sessions: mgr, Banner: banner, Pick: *resumeID == "?", Memory: mem, Theme: os.Getenv("TERNLY_THEME"),
		Surface: stc, Actions: stc, // the ADR 021 producer (Phase B): meter, /why, trust, quota
		Plugins: prt, Capabilities: caps, ConfigPath: filepath.Join(cfgDir, "config.json"), Status: func() []string {
			lines := []string{"code graph on (Go): find_symbol, references, callers, … for the model"}
			if gs == nil {
				lines = []string{"code graph off (no go.mod, or code_graph: false)"}
			}
			return append(lines, status.Lines(stc.Snapshot().Connections, discover.FindCLIs(), time.Now())...)
		}}
	m := tui.New(app, darkTerminal()) // a first guess; the terminal's own answer arrives as a message
	p := tea.NewProgram(m, tea.WithContext(ctx))
	emit = func(e agent.Event) { stc.Observe(e); p.Send(tuiMsg(e)) }
	pol.Ask = tui.Asker(p)
	pol.Review = tui.Reviewer(p) // edits that ask are reviewed per hunk (ADR 021 amendment 1)
	if prt != nil {
		prt.Changed = func() { p.Send(tui.PluginsChanged()) }
	}
	background()
	if router.V2() && !inEval && os.Getenv("TERNLY_BACKGROUND_EVAL") != "1" && os.Getenv("TERNLY_HARNESS") != "1" {
		remeasure := func() { // a new measurement: discovery reads it, routing uses it
			o := dopts
			o.Measured = measurements(filepath.Join(dataDir, "capability"))
			if ms, _ := discover.Discover(ctx, o); len(ms) > 0 {
				router.SetModels(ms)
			}
		}
		sched := backgroundEvals(fc.Routing.BackgroundEval, *localOnly, dataDir, router, ag.Running, remeasure)
		router.SetEvalStatus(sched.Status)
		bgWG.Add(1) // waited for at exit: the evaluation's process group is stopped first
		go func() { defer bgWG.Done(); sched.Loop(bgCtx) }()
	}
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
	// Progress (dogfooding, ADR 014: a slow local model was silent for 25
	// minutes): after 30 s without output, a line says whether the model is
	// producing (tool arguments, reasoning) or still reading the prompt.
	var pmu sync.Mutex
	last, step, chunks := time.Now(), time.Now(), 0
	pctx, pstop := context.WithCancel(ctx)
	defer pstop()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
			}
			pmu.Lock()
			if quiet := time.Since(last); quiet >= 30*time.Second {
				if chunks > 0 {
					fmt.Fprintf(os.Stderr, "  … generating (%d chunks, %s)\n", chunks, time.Since(step).Round(time.Second))
				} else {
					fmt.Fprintf(os.Stderr, "  … waiting for the model (%s)\n", time.Since(step).Round(time.Second))
				}
				last = time.Now()
			}
			pmu.Unlock()
		}
	}()
	*emit = func(e agent.Event) {
		pmu.Lock()
		switch e.Kind {
		case agent.EvProgress:
			chunks = e.N
		case agent.EvUsage:
		default:
			last, step, chunks = time.Now(), time.Now(), 0
		}
		pmu.Unlock()
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
			if e.Tool == "verify" {
				switch e.Verdict {
				case agent.VerdictVerified:
					fmt.Fprintf(os.Stderr, "  ✓ verified in %s: %s\n", e.Elapsed.Round(time.Millisecond), e.Text)
				case agent.VerdictUnverified:
					fmt.Fprintf(os.Stderr, "  ? %s\n", strings.ReplaceAll(strings.TrimSpace(e.Text), "\n", "\n    "))
				default:
					fmt.Fprintf(os.Stderr, "  ✗ verification failed: %s\n", firstOutputLine(e.Text))
				}
				break
			}
			if !e.OK {
				fmt.Fprintf(os.Stderr, "  ✗ %s failed: %s\n", e.Tool, strings.SplitN(strings.TrimSpace(e.Text), "\n", 2)[0])
			}
		case agent.EvStatus:
			fmt.Fprintln(os.Stderr, "  ↻", e.Text)
		case agent.EvError:
			failed = true
			fmt.Fprintln(os.Stderr, "error:", e.Text)
		case agent.EvDone:
			fmt.Fprintf(os.Stderr, "\n$%.4f · %.0f%% cache hits · %d tokens in, %d out\n", e.Ledger.Cost, e.Ledger.CacheRate()*100, e.Ledger.Usage.In+e.Ledger.Usage.CacheRead, e.Ledger.Usage.Out)
		}
	}
	background()
	ictx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	t0 := time.Now()
	fmt.Fprintf(os.Stderr, "prompt %s\n", ag.PromptVersion())
	ag.RunTask(ictx, prompt) // Run, plus the levers that are on (ADR 029)
	if c := ag.Responses; c != nil {
		fmt.Fprintf(os.Stderr, "response cache: %d hit(s), %d miss(es)\n", c.Hits.Load(), c.Misses.Load())
	}
	if st := ag.Stats(); st.Plans > 0 || st.Retries > 0 {
		fmt.Fprintf(os.Stderr, "levers: %d plan(s), %d retry(ies) from scratch\n", st.Plans, st.Retries)
	}
	fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(t0).Round(time.Millisecond))
	if failed {
		return 1
	}
	return 0
}

// firstOutputLine is the first line of a check's output that isn't the
// "$ command" header.
func firstOutputLine(s string) string {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "$ ") {
			return l
		}
	}
	return strings.TrimSpace(s)
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

// enrichSetting is memory_enrich: false (off), true or "local" (default: a
// local model, else the model the session already sends its conversation
// to), or "remote" (also the cheapest remote model: an explicit opt-in).
type enrichSetting int

const (
	enrichLocal enrichSetting = iota
	enrichOff
	enrichRemote
)

func (e *enrichSetting) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "false", "off":
		*e = enrichOff
	case "true", "local", "null":
		*e = enrichLocal
	case "remote":
		*e = enrichRemote
	default:
		return fmt.Errorf("memory_enrich: want true, false, \"local\" or \"remote\", got %s", b)
	}
	return nil
}

// enrichModel picks who may see memory notes for enrichment: a local model
// (tier 2+ preferred: tiny models write poor wordings), else the session's
// own model (it already sees the conversation the notes come from), else —
// only with memory_enrich: "remote" — the cheapest model anywhere.
func enrichModel(r *discover.Router, session *discover.Model, remote bool) *discover.Model {
	var local *discover.Model
	for _, m := range r.Models() {
		if m.Local() && (local == nil || m.Tier >= 2 && local.Tier < 2) {
			local = m
		}
	}
	switch {
	case local != nil:
		return local
	case session != nil:
		return session
	case remote:
		return r.Utility(4000)
	}
	return nil
}

// mcpLoginCLI is ternly --mcp-login: the /mcp login flow, with each host the
// server's authorization needs approved on the terminal.
func mcpLoginCLI(ctx context.Context, m *mcpremote.Manager, remotes []tools.RemoteConfig, name string) int {
	m.Prepare(remotes)
	in := bufio.NewReader(os.Stdin)
	err := m.Login(ctx, name, func(host string) bool {
		fmt.Fprintf(os.Stderr, "%s's login uses %s — allow ternly to contact it for this server? [y/N] ", name, host)
		ans, _ := in.ReadString('\n')
		return strings.EqualFold(strings.TrimSpace(ans), "y")
	}, func(msg string) { fmt.Fprintln(os.Stderr, msg) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp login:", err)
		return 1
	}
	for _, st := range m.Statuses() {
		if st.Name == name {
			fmt.Fprintf(os.Stderr, "%s: logged in · %d tools · %s\n", name, st.Tools, st.Era)
		}
	}
	return 0
}

// unsandboxedNote is the start-up warning when commands can't be sandboxed.
func unsandboxedNote(optedOut bool) string {
	const what = "every shell command asks first (nothing is auto-approved, not even in yolo), and plugin hooks and MCP servers are disabled"
	switch {
	case optedOut:
		return "--no-sandbox: shell commands run unsandboxed, so " + what
	case runtime.GOOS == "darwin":
		return "macOS support is experimental: there is no sandbox yet (v0.2 adds one), so " + what
	}
	return "bubblewrap not found — shell commands would run unsandboxed, so " + what + " (install bubblewrap: e.g. sudo pacman -S bubblewrap, apt install bubblewrap)"
}

func noCodeReason(p *tools.Policy) string {
	if !p.Unsandboxed {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return "there is no sandbox on macOS yet (experimental; v0.2)"
	}
	return "shell commands run unsandboxed (bubblewrap missing or --no-sandbox)"
}

// accessFrom is the TUI's access settings: the environment, plus --accessible.
func accessFrom(accessible bool) tui.Access {
	a := tui.AccessFromEnv()
	if accessible {
		a.ScreenReader, a.ReducedMotion = true, true
	}
	return a
}
