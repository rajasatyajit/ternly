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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/tools"
	"github.com/rajasatyajit/ternly/internal/tui"
)

var version = "0.1.0"

type fileConfig struct {
	Mode      string         `json:"mode"`
	Budget    float64        `json:"budget"`
	Verify    *string        `json:"verify"`
	NoLocal   bool           `json:"no_local"`
	Model     string         `json:"model"`
	Tiers     map[string]int `json:"tiers"`
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
		localOnly  = flag.Bool("local-only", false, "use only local models (zero cost, fully offline)")
		noLocal    = flag.Bool("no-local", false, "ignore local model servers")
		noSandbox  = flag.Bool("no-sandbox", false, "run shell commands without bubblewrap")
		noNet      = flag.Bool("no-net", false, "deny network to shell commands (bubblewrap)")
		projectMCP = flag.Bool("project-mcp", false, "also start MCP servers from ./.mcp.json (untrusted repo config)")
		listModels = flag.Bool("models", false, "list discovered models and exit")
		dir        = flag.String("C", ".", "workspace directory")
		showVer    = flag.Bool("version", false, "print version")
	)
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
	var notes []string
	for _, d := range [][2]string{{filepath.Join(home, ".config", "vane"), cfgDir}, {filepath.Join(home, ".cache", "vane"), cacheDir}} {
		switch moved, err := migrateLegacy(d[0], d[1]); { // pre-rename installs
		case err != nil:
			notes = append(notes, "migrating "+d[0]+": "+err.Error())
		case moved:
			notes = append(notes, "migrated "+d[0]+" → "+d[1])
		}
	}
	_ = os.MkdirAll(cacheDir, 0o700)

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
	ag.Verify = agent.DetectVerify(reg.Root)
	if fc.Verify != nil {
		ag.Verify = *fc.Verify
	}
	if *verify != "" {
		ag.Verify = *verify
	}
	if ag.Verify == "off" {
		ag.Verify = ""
	}
	ag.Budget = fc.Budget
	if *budget >= 0 {
		ag.Budget = *budget
	}
	pin := pick(*model, fc.Model, "")

	if *prompt != "" {
		return headless(ctx, ag, router, discoverFn, pin, *prompt, &emit, notes)
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
	}, Notes: notes, Version: version}
	dark := darkTerminal()
	lipgloss.SetHasDarkBackground(dark) // pre-seed: no blocking OSC query for adaptive colours
	m := tui.New(app, dark)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	emit = func(e agent.Event) { p.Send(tuiMsg(e)) }
	pol.Ask = tui.Asker(p)
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

// tuiMsg converts agent events into the TUI's message type.
func tuiMsg(e agent.Event) tea.Msg { return tui.AgentMsg(e) }

func headless(ctx context.Context, ag *agent.Agent, router *discover.Router, disc func() ([]*discover.Model, []string), pin, prompt string, emit *func(agent.Event), notes []string) int {
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
