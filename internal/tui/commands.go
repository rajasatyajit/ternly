package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/commands"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/mcpremote"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// command is a built-in slash command. docs/commands.md maps each one to
// the harnesses it comes from.
type command struct {
	name    string
	aliases []string
	args    string // argument hint
	section string
	desc    string
	run     func(m *Model, arg string) tea.Cmd // nil: handled by legacyCommand
}

var sections = []string{"Session", "Models & cost", "Setup", "Changes", "Planning & modes", "Files & shell", "Extensions"}

// builtins is filled in init: the table refers to methods of Model.
var builtins []*command

func init() {
	builtins = []*command{
		{name: "help", aliases: []string{"?"}, args: "[command]", section: "Session", desc: "list commands, or explain one", run: (*Model).cmdHelp},
		{name: "clear", aliases: []string{"reset"}, section: "Session", desc: "new conversation in this session (files untouched)"},
		{name: "new", section: "Session", desc: "start a new session in place"},
		{name: "resume", aliases: []string{"switch", "continue", "chat", "sessions"}, args: "[id]", section: "Session", desc: "pick a saved session, or switch to one"},
		{name: "fork", aliases: []string{"branch"}, args: "[n|id]", section: "Session", desc: "new session from this one (optionally before turn n)"},
		{name: "rename", args: "<title>", section: "Session", desc: "rename the session"},
		{name: "delete", args: "<id>", section: "Session", desc: "delete a session and its checkpoints"},
		{name: "export", args: "[md|json] [file]", section: "Session", desc: "export the conversation"},
		{name: "compact", aliases: []string{"compress", "summarize"}, args: "[instructions]", section: "Session", desc: "summarise older turns with the cheapest model", run: (*Model).cmdCompact},
		{name: "undo", section: "Session", desc: "revert the last turn (files and conversation)"},
		{name: "rewind", aliases: []string{"checkpoint"}, args: "[n] [both|code|chat]", section: "Session", desc: "list turns, or restore to before turn n"},
		{name: "btw", aliases: []string{"side"}, args: "<question>", section: "Session", desc: "side question, answered without adding to the conversation", run: (*Model).cmdBtw},
		{name: "copy", args: "[n]", section: "Session", desc: "copy the last (or nth-latest) answer to the clipboard", run: (*Model).cmdCopy},
		{name: "pause", section: "Session", desc: "stop at a safe point and save"},
		{name: "stop", section: "Session", desc: "save as stopped and exit"},
		{name: "exit", aliases: []string{"quit", "q"}, section: "Session", desc: "exit (the session is saved)"},

		{name: "model", args: "[id|auto]", section: "Models & cost", desc: "pin a model, or return to cost-aware routing"},
		{name: "models", args: "[filter]", section: "Models & cost", desc: "list models: tier, price, context"},
		{name: "cost", aliases: []string{"usage", "stats", "tokens"}, section: "Models & cost", desc: "tokens, cache hits, spend and guard counters"},
		{name: "context", section: "Models & cost", desc: "what the next request carries, against the window", run: (*Model).cmdContext},
		{name: "status", section: "Models & cost", desc: "version, model, mode, limits, sandbox, memory", run: (*Model).cmdStatus},
		{name: "budget", args: "<usd>", section: "Models & cost", desc: "hard spend cap for the session"},
		{name: "limits", args: "[steps N|time 45m|turn-usd X]", section: "Models & cost", desc: "per-turn step, time and spend limits"},
		{name: "refresh", section: "Models & cost", desc: "re-discover providers and models"},

		{name: "init", section: "Setup", desc: "analyse the repository and write AGENTS.md", run: (*Model).cmdInit},
		{name: "memory", aliases: []string{"memories"}, args: "[search|forget|edit|add|promote]", section: "Setup", desc: "view and edit what ternly remembers"},
		{name: "config", aliases: []string{"settings"}, args: "[key value]", section: "Setup", desc: "effective settings; set one for this session", run: (*Model).cmdConfig},
		{name: "permissions", aliases: []string{"allowed-tools"}, section: "Setup", desc: "permission mode and what's allowed this session", run: (*Model).cmdPermissions},
		{name: "mode", args: "ask|edits|yolo|plan", section: "Setup", desc: "permission mode"},
		{name: "verify", args: "<cmd|off>", section: "Setup", desc: "the check run after edits"},
		{name: "doctor", section: "Setup", desc: "check sandbox, tools, providers, memory", run: (*Model).cmdDoctor},
		{name: "theme", args: "[dark|light|auto]", section: "Setup", desc: "colour theme", run: (*Model).cmdTheme},
		{name: "about", section: "Setup", desc: "version and build", run: (*Model).cmdAbout},

		{name: "review", section: "Changes", desc: "review uncommitted changes with the strongest model"},
		{name: "diff", section: "Changes", desc: "working-tree changes (git diff + untracked)", run: (*Model).cmdDiff},
		{name: "commit", args: "[message]", section: "Changes", desc: "commit all changes (message written by the cheapest model if omitted)", run: (*Model).cmdCommit},
		{name: "git", args: "<args>", section: "Changes", desc: "run a git command (sandboxed)", run: (*Model).cmdGit},

		{name: "plan", args: "[prompt|off]", section: "Planning & modes", desc: "read-only mode: investigate and plan, no edits", run: (*Model).cmdPlan},
		{name: "ask", args: "[prompt]", section: "Planning & modes", desc: "a read-only turn (or switch to read-only mode)", run: (*Model).cmdAsk},
		{name: "code", args: "[prompt]", section: "Planning & modes", desc: "back to normal editing mode", run: (*Model).cmdCode},
		{name: "architect", args: "<prompt>", section: "Planning & modes", desc: "strongest model plans, cheapest capable model edits", run: (*Model).cmdArchitect},

		{name: "add", aliases: []string{"mention"}, args: "<files>", section: "Files & shell", desc: "pin files: their contents go with every prompt", run: (*Model).cmdAdd},
		{name: "drop", args: "[files]", section: "Files & shell", desc: "unpin files (all without arguments)", run: (*Model).cmdDrop},
		{name: "ls", section: "Files & shell", desc: "list pinned files", run: (*Model).cmdLs},
		{name: "run", args: "<command>", section: "Files & shell", desc: "run a shell command (also !cmd); output goes with the next prompt", run: (*Model).cmdRun},
		{name: "test", args: "[command]", section: "Files & shell", desc: "run the tests; on failure the model fixes them", run: (*Model).cmdTest},
		{name: "lint", args: "[command]", section: "Files & shell", desc: "run the linter; on failure the model fixes it", run: (*Model).cmdLint},
		{name: "web", args: "<url>", section: "Files & shell", desc: "fetch a page as text for the next prompt", run: (*Model).cmdWeb},
		{name: "editor", aliases: []string{"edit"}, section: "Files & shell", desc: "compose the prompt in $EDITOR", run: (*Model).cmdEditor},

		{name: "plugin", aliases: []string{"plugins", "extensions"}, args: "[add|update|remove|enable|disable|info|scope|import|marketplace]", section: "Extensions", desc: "install, review and manage plugins (pinned, sandboxed)", run: (*Model).cmdPlugin},
		{name: "skills", section: "Extensions", desc: "skills, agents and rules available to the model", run: (*Model).cmdSkills},
		{name: "agents", section: "Extensions", desc: "subagents the model can delegate to", run: (*Model).cmdSkills},
		{name: "mcp", section: "Extensions", desc: "MCP servers and their tools · login|logout <server>", run: (*Model).cmdMCP},
		{name: "tools", section: "Extensions", desc: "the tools the model can call", run: (*Model).cmdTools},
		{name: "commands", args: "[reload]", section: "Extensions", desc: "user-defined commands; reload them", run: (*Model).cmdCommands},
	}
}

func lookup(name string) *command {
	for _, c := range builtins {
		if c.name == name || containsStr(c.aliases, name) {
			return c
		}
	}
	return nil
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// reserved: user commands can't take a built-in's name.
func reserved(name string) bool { return lookup(name) != nil }

// loadUserCommands (re)reads user-defined commands.
func (m *Model) loadUserCommands() []error {
	home, _ := os.UserHomeDir()
	cs, errs := commands.Load(commands.Dirs(m.App.Reg.Root, home), reserved)
	if m.App.Plugins != nil { // plugin commands and skills are namespaced (plugin:name), so they can't shadow these
		seen := map[string]bool{}
		for _, c := range cs {
			seen[c.Name] = true
		}
		for _, c := range m.App.Plugins.Commands() {
			if !seen[c.Name] && !reserved(c.Name) {
				seen[c.Name] = true
				cs = append(cs, c)
			}
		}
	}
	m.userCmds = cs
	return errs
}

func (m *Model) userCommand(name string) *commands.Command {
	for _, c := range m.userCmds {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// dispatch runs a slash command line.
func (m *Model) dispatch(v string) tea.Cmd {
	name, arg, _ := strings.Cut(strings.TrimPrefix(v, "/"), " ")
	arg = strings.TrimSpace(arg)
	if c := lookup(name); c != nil {
		if c.run != nil {
			return c.run(m, arg)
		}
		return m.legacyCommand("/"+c.name, arg, v)
	}
	if u := m.userCommand(name); u != nil {
		return m.runUser(u, arg)
	}
	m.addInfo(sErr.Render("  unknown command /" + name + " — /help"))
	return nil
}

// startMsg starts a turn from a background command (on the UI goroutine).
type startMsg struct{ show, prompt, extra string }

func (m *Model) runUser(u *commands.Command, arg string) tea.Cmd {
	if m.busy {
		m.addInfo(sErr.Render("  a turn is running — wait or press Esc"))
		return nil
	}
	show := "/" + u.Name
	if arg != "" {
		show += " " + arg
	}
	return func() tea.Msg {
		text, err := u.Expand(arg, commands.Hooks{Shell: m.shell})
		if err != nil {
			return infoMsg(sErr.Render("  /" + u.Name + ": " + err.Error()))
		}
		return startMsg{show: show, prompt: text}
	}
}

// shell runs a command through the registry's bash tool: permission policy,
// sandbox, redaction and output cap all apply. Returns the plain output.
func (m *Model) shell(cmd string) (string, error) {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	r := m.App.Reg.Call(context.Background(), llm.ToolCall{ID: "user", Name: "bash", Args: string(args)})
	out := tools.Unframe(r.Out)
	if r.Rejected {
		return "", errors.New(out)
	}
	return out, nil
}

// ─────────────────────────── help ───────────────────────────

func (m *Model) cmdHelp(arg string) tea.Cmd {
	if arg != "" {
		name := strings.TrimPrefix(arg, "/")
		if c := lookup(name); c != nil {
			s := fmt.Sprintf("  /%s %s — %s", c.name, c.args, c.desc)
			if len(c.aliases) > 0 {
				s += sDim.Render("\n  aliases: /" + strings.Join(c.aliases, " /"))
			}
			m.addInfo(s)
			return nil
		}
		if u := m.userCommand(name); u != nil {
			m.addInfo(fmt.Sprintf("  /%s %s — %s\n%s", u.Name, u.ArgHint, u.Description, sDim.Render("  "+u.Path)))
			return nil
		}
		m.addInfo(sErr.Render("  no command /" + name))
		return nil
	}
	var b strings.Builder
	for _, sec := range sections {
		b.WriteString("  " + sAccent.Render(sec) + "\n")
		for _, c := range builtins {
			if c.section != sec {
				continue
			}
			head := "/" + c.name
			if c.args != "" {
				head += " " + c.args
			}
			b.WriteString(fmt.Sprintf("    %-34s %s\n", head, sDim.Render(c.desc)))
		}
	}
	if len(m.userCmds) > 0 {
		b.WriteString("  " + sAccent.Render("Your commands") + sDim.Render(" (/commands for sources)") + "\n")
		for _, u := range m.userCmds {
			b.WriteString(fmt.Sprintf("    %-34s %s\n", "/"+u.Name+" "+u.ArgHint, sDim.Render(u.Description)))
		}
	}
	b.WriteString(sDim.Render("  @path includes a file · !cmd runs a command · Tab completes · Enter send · Shift/Alt+Enter newline · Esc interrupt · PgUp/PgDn scroll · ↑↓ history"))
	m.addInfo(b.String())
	return nil
}

// ─────────────────────────── session, cost, context ───────────────────────────

func (m *Model) cmdCompact(arg string) tea.Cmd {
	go func() {
		if err := m.App.Agent.CompactWith(context.Background(), arg); err != nil {
			m.App.Agent.Emit(agent.Event{Kind: agent.EvStatus, Text: err.Error()})
		}
	}()
	return nil
}

type btwMsg struct{ q, a string }

func (m *Model) cmdBtw(arg string) tea.Cmd {
	if arg == "" {
		m.addInfo(sErr.Render("  usage: /btw <question>"))
		return nil
	}
	m.addInfo(sDim.Render("  btw: ") + arg)
	return func() tea.Msg {
		a, err := m.App.Agent.Ask(context.Background(), arg)
		if err != nil {
			return infoMsg(sErr.Render("  " + err.Error()))
		}
		return btwMsg{q: arg, a: a}
	}
}

func (m *Model) cmdCopy(arg string) tea.Cmd {
	n := 1
	fmt.Sscan(arg, &n)
	var text string
	for i, seen := len(m.blocks)-1, 0; i >= 0; i-- {
		if m.blocks[i].kind == bAssistant {
			if seen++; seen == n {
				text = m.blocks[i].text
				break
			}
		}
	}
	if text == "" {
		m.addInfo(sErr.Render("  no answer to copy"))
		return nil
	}
	local := copyLocal(text)
	m.addInfo(sOK.Render(fmt.Sprintf("  copied %d characters", len(text))) + sDim.Render(" (terminal clipboard"+local+")"))
	return tea.SetClipboard(text) // OSC 52: works over SSH in most terminals
}

// copyLocal also hands text to a local clipboard tool when one exists.
func copyLocal(text string) string {
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}, {"pbcopy"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return " and " + c[0]
		}
	}
	return ""
}

func (m *Model) cmdContext(string) tea.Cmd {
	c := m.App.Agent.Context()
	total := c.System + c.Tools + c.Notes + c.User + c.Assistant + c.ToolResults
	rows := []struct {
		name string
		n    int
	}{{"system prompt", c.System}, {"tool definitions", c.Tools}, {"memory notes", c.Notes}, {"your messages", c.User}, {"answers", c.Assistant}, {"tool results", c.ToolResults}}
	var b strings.Builder
	win := c.Window
	head := fmt.Sprintf("  context: ~%s tokens", kfmt(total))
	if win > 0 {
		head += fmt.Sprintf(" of %s (%s, %.0f%%)", kfmt(win), c.Model, 100*float64(total)/float64(win))
	} else {
		head += sDim.Render(" (no model chosen yet)")
	}
	b.WriteString(head + "\n")
	for _, r := range rows {
		bar := ""
		if total > 0 {
			bar = strings.Repeat("█", r.n*30/max(total, 1))
		}
		b.WriteString(fmt.Sprintf("    %-17s %7s  %s\n", r.name, kfmt(r.n), sAccent.Render(bar)))
	}
	b.WriteString(sDim.Render("  estimates at ~3.6 characters per token · /compact summarises older turns"))
	m.addInfo(b.String())
	return nil
}

func (m *Model) cmdStatus(string) tea.Cmd {
	a := m.App.Agent
	lim, budget := a.Caps()
	model := "auto-routing"
	if p := m.App.Router.Pinned(); p != nil {
		model = "pinned " + p.Key()
	}
	if c := a.Current(); c != nil {
		model += " · last used " + c.Key()
	}
	sess := "(not saved)"
	if sm := m.App.Sessions; sm != nil && sm.Current() != nil {
		sess = sm.Current().ID
		if t := a.Title(); t != "" {
			sess += " “" + t + "”"
		}
	}
	lines := []string{
		"ternly " + m.App.Version,
		"session    " + sess,
		"workspace  " + m.App.Reg.Root,
		"model      " + model,
		"mode       " + m.App.Reg.Policy.Mode() + " · sandbox " + m.App.Reg.Sandbox.Mode(),
		"verify     " + verifyLabel(a.VerifyCmd()),
		fmt.Sprintf("limits     %d steps · %s · $%.2f per turn · budget $%.2f", lim.Steps, lim.Time, lim.TurnUSD, budget),
		"checkpoints " + map[bool]string{true: "on", false: "off"}[a.CP != nil],
	}
	if mem := m.App.Memory; mem != nil {
		st := "loading"
		if ok, err := mem.Ready(); ok {
			st = fmt.Sprintf("%d project · %d user items", len(mem.List("project"))+len(mem.List("session")), len(mem.List("user")))
			if err != nil {
				st = "failed: " + err.Error()
			}
		}
		vec := "lexical"
		if e := mem.Embedder(); e != nil {
			vec = e.Name()
		}
		lines = append(lines, fmt.Sprintf("memory     %s · %s · %d tokens/turn", st, vec, mem.Budget))
	} else {
		lines = append(lines, "memory     off")
	}
	if m.App.Status != nil {
		lines = append(lines, m.App.Status()...)
	}
	if len(m.pins) > 0 {
		lines = append(lines, "pinned     "+strings.Join(m.pins, ", "))
	}
	m.addInfo("  " + strings.Join(lines, "\n  "))
	return nil
}

func (m *Model) cmdAbout(string) tea.Cmd {
	m.addInfo(fmt.Sprintf("  ternly %s · %s %s/%s · Apache-2.0 · https://github.com/rajasatyajit/ternly", m.App.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH))
	return nil
}

// ─────────────────────────── setup ───────────────────────────

const initPrompt = `Create or update AGENTS.md at the workspace root: concise instructions for coding agents working in this repository. Investigate first (build files, CI config, directory layout, existing docs, test setup). Include: what the project is (one paragraph); how to build, test, lint and run (exact commands, verified by running the cheap ones); code layout (key directories and what lives where); conventions you can see in the code (naming, error handling, testing style); and anything non-obvious a newcomer would trip over. Keep it under ~120 lines, no filler, no generic advice. If AGENTS.md exists, improve it and keep what is still correct.`

func (m *Model) cmdInit(string) tea.Cmd {
	if m.busy {
		m.addInfo(sErr.Render("  a turn is running — wait or press Esc"))
		return nil
	}
	m.blocks = append(m.blocks, &block{kind: bUser, text: "/init"})
	return m.start(initPrompt)
}

func (m *Model) cmdConfig(arg string) tea.Cmd {
	if f := strings.Fields(arg); len(f) >= 1 {
		key, val := f[0], strings.TrimSpace(strings.TrimPrefix(arg, f[0]))
		key, val = strings.TrimSuffix(key, "="), strings.TrimPrefix(val, "=")
		if k, v, ok := strings.Cut(f[0], "="); ok {
			key, val = k, v
		}
		switch key {
		case "mode", "model", "verify", "budget", "limits":
			return m.legacyCommand("/"+key, strings.TrimSpace(val), "/"+key+" "+val)
		case "theme":
			return m.cmdTheme(val)
		case "memory_budget":
			if mem := m.App.Memory; mem != nil {
				fmt.Sscan(val, &mem.Budget)
				m.addInfo(sOK.Render(fmt.Sprintf("  memory budget: %d tokens per turn (this session)", mem.Budget)))
			}
			return nil
		default:
			m.addInfo(sErr.Render("  settable here: mode, model, verify, budget, limits, theme, memory_budget — others in " + m.App.ConfigPath))
			return nil
		}
	}
	a := m.App.Agent
	lim, budget := a.Caps()
	pin := "auto"
	if p := m.App.Router.Pinned(); p != nil {
		pin = p.Key()
	}
	lines := []string{
		sDim.Render("config file: " + orStr(m.App.ConfigPath, "(none)") + " · /config <key> <value> sets one for this session"),
		"mode           " + m.App.Reg.Policy.Mode(),
		"model          " + pin,
		"verify         " + verifyLabel(a.VerifyCmd()),
		fmt.Sprintf("budget         $%.2f", budget),
		fmt.Sprintf("limits         steps %d · time %s · turn-usd %.2f", lim.Steps, lim.Time, lim.TurnUSD),
		"theme          " + m.style + map[bool]string{true: " (fixed)", false: " (follows the terminal)"}[m.themeSet],
	}
	if mem := m.App.Memory; mem != nil {
		lines = append(lines, fmt.Sprintf("memory_budget  %d", mem.Budget))
	}
	m.addInfo("  " + strings.Join(lines, "\n  "))
	return nil
}

func (m *Model) cmdPermissions(string) tea.Cmd {
	p := m.App.Reg.Policy
	lines := []string{
		"mode     " + p.Mode() + sDim.Render("  (ask: confirm edits and unsafe commands · edits: auto-allow edits · yolo: also commands · plan: read-only)"),
		"sandbox  " + m.App.Reg.Sandbox.Mode() + map[bool]string{true: sWarn.Render("  — no sandbox: every shell command asks, plugin code is off (/doctor)")}[p.Unsandboxed],
		"always   " + orStr(strings.Join(p.Always(), ", "), "nothing yet (answer [a] to a prompt)"),
	}
	var trusted []string
	for s, ok := range p.Trusted {
		if ok {
			trusted = append(trusted, s)
		}
	}
	sort.Strings(trusted)
	lines = append(lines, "trusted MCP servers  "+orStr(strings.Join(trusted, ", "), "none"))
	lines = append(lines, sDim.Render("read-only tools never ask; destructive commands always ask; some are always refused"))
	m.addInfo("  " + strings.Join(lines, "\n  "))
	return nil
}

func (m *Model) cmdDoctor(string) tea.Cmd {
	return func() tea.Msg {
		ok, bad, warn := sOK.Render("✓"), sErr.Render("✗"), sWarn.Render("!")
		var b strings.Builder
		line := func(mark, what, detail string) {
			b.WriteString(fmt.Sprintf("  %s %-12s %s\n", mark, what, sDim.Render(detail)))
		}
		switch sm := m.App.Reg.Sandbox.Mode(); {
		case !m.App.Reg.Policy.Unsandboxed:
			line(ok, "sandbox", sm)
		case runtime.GOOS == "darwin":
			line(bad, "sandbox", "none — macOS support is EXPERIMENTAL (a sandbox comes in v0.2): every shell command asks first, and plugin hooks and MCP servers are disabled")
		default:
			line(bad, "sandbox", "none — every shell command asks first and plugin hooks and MCP servers are disabled; install bubblewrap (or drop --no-sandbox)")
		}
		for _, t := range []struct{ bin, why string }{{"git", "checkpoints, /diff, /commit"}, {"rg", "fast grep (falls back to Go)"}, {"go", "the Go code graph"}} {
			if p, err := exec.LookPath(t.bin); err == nil {
				line(ok, t.bin, p)
			} else {
				line(warn, t.bin, "not found — "+t.why)
			}
		}
		ms := m.App.Router.Models()
		provs, local, tools := map[string]int{}, 0, 0
		for _, x := range ms {
			provs[x.ProvID]++
			if x.Local() {
				local++
			}
			if x.Tools {
				tools++
			}
		}
		if len(ms) == 0 {
			line(bad, "models", "none found — set an API key or start Ollama / LM Studio, then /refresh")
		} else {
			line(ok, "models", fmt.Sprintf("%d from %d providers (%d local, %d with tools)", len(ms), len(provs), local, tools))
		}
		if m.App.Agent.CP != nil {
			line(ok, "checkpoints", "on (/undo, /rewind restore files)")
		} else {
			line(warn, "checkpoints", "off — /undo can only rewind the conversation")
		}
		if mem := m.App.Memory; mem == nil {
			line(warn, "memory", "off")
		} else if ready, err := mem.Ready(); err != nil {
			line(bad, "memory", err.Error())
		} else {
			vec := "lexical only (pull nomic-embed-text in Ollama for vectors)"
			if e := mem.Embedder(); e != nil {
				vec = "vectors: " + e.Name()
			}
			line(ok, "memory", map[bool]string{true: "loaded", false: "loading"}[ready]+" · "+vec)
		}
		if m.App.Status != nil {
			for _, s := range m.App.Status() {
				line(ok, "", s)
			}
		}
		nw := 0
		for _, u := range m.userCmds {
			for _, w := range u.Warnings() {
				line(warn, "command", w)
				nw++
			}
		}
		if len(m.userCmds) > 0 && nw == 0 {
			line(ok, "commands", fmt.Sprintf("%d user-defined, no problems found", len(m.userCmds)))
		}
		return infoMsg(strings.TrimRight(b.String(), "\n"))
	}
}

func (m *Model) cmdTheme(arg string) tea.Cmd {
	switch arg {
	case "dark", "light":
		m.themeSet = true
		m.setTheme(arg == "dark")
		m.addInfo(sOK.Render("  theme: " + arg))
		return nil
	case "auto", "":
		if arg == "" {
			m.addInfo(fmt.Sprintf("  theme: %s — /theme dark|light|auto", m.style))
			return nil
		}
		m.themeSet = false
		m.addInfo(sOK.Render("  theme: following the terminal"))
		return tea.RequestBackgroundColor
	}
	m.addInfo(sErr.Render("  usage: /theme dark|light|auto"))
	return nil
}

// ─────────────────────────── changes ───────────────────────────

// git runs git in the sandbox (repository config can't run code outside it).
func (m *Model) git(ctx context.Context, args ...string) (string, error) {
	argv := append([]string{"git", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "--no-pager"}, args...)
	out, err := m.App.Reg.Sandbox.Output(ctx, m.App.Reg.Root, nil, argv...)
	return string(out), err
}

func (m *Model) isGit(ctx context.Context) bool {
	out, err := m.git(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func colorDiff(d string, maxLines int) string {
	ls := strings.Split(strings.TrimRight(d, "\n"), "\n")
	more := 0
	if len(ls) > maxLines {
		more, ls = len(ls)-maxLines, ls[:maxLines]
	}
	for i, l := range ls {
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") || strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "index "):
			ls[i] = sDim.Render("  " + l)
		case strings.HasPrefix(l, "+"):
			ls[i] = sOK.Render("  " + l)
		case strings.HasPrefix(l, "-"):
			ls[i] = sErr.Render("  " + l)
		case strings.HasPrefix(l, "@@"):
			ls[i] = sAccent.Render("  " + l)
		default:
			ls[i] = "  " + l
		}
	}
	s := strings.Join(ls, "\n")
	if more > 0 {
		s += sDim.Render(fmt.Sprintf("\n  … %d more lines (/git diff for all)", more))
	}
	return s
}

func (m *Model) cmdDiff(string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if !m.isGit(ctx) {
			p, err := m.App.Agent.PlanRewind(ctx, 1, agent.RewindCode)
			if err != nil || len(p.Changes) == 0 {
				return infoMsg(sDim.Render("  not a git repository, and no checkpointed changes in this session"))
			}
			return infoMsg("  changed in this session (not a git repository; contents via /rewind):\n  " + checkpoint.Describe(p.Changes, 50))
		}
		stat, _ := m.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--stat", "HEAD")
		d, _ := m.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "HEAD")
		un, _ := m.git(ctx, "ls-files", "--others", "--exclude-standard")
		if strings.TrimSpace(d) == "" && strings.TrimSpace(un) == "" {
			return infoMsg(sDim.Render("  no changes"))
		}
		var b strings.Builder
		if s := strings.TrimSpace(stat); s != "" {
			b.WriteString(sDim.Render("  "+strings.ReplaceAll(s, "\n", "\n  ")) + "\n")
		}
		if u := strings.TrimSpace(un); u != "" {
			b.WriteString(sWarn.Render("  untracked: ") + strings.ReplaceAll(u, "\n", ", ") + "\n")
		}
		b.WriteString(colorDiff(d, 200))
		return infoMsg(b.String())
	}
}

func (m *Model) cmdGit(arg string) tea.Cmd {
	if arg == "" {
		m.addInfo(sErr.Render("  usage: /git <args>"))
		return nil
	}
	return func() tea.Msg {
		out, err := m.git(context.Background(), commands.SplitArgs(arg)...)
		s := strings.TrimRight(out, "\n")
		if err != nil {
			s += "\n" + err.Error()
		}
		if strings.HasPrefix(arg, "diff") || strings.HasPrefix(arg, "show") {
			return infoMsg(colorDiff(s, 400))
		}
		return infoMsg("  " + strings.ReplaceAll(tools.Cap(orStr(s, "(no output)"), 20000), "\n", "\n  "))
	}
}

func (m *Model) cmdCommit(arg string) tea.Cmd {
	if m.busy {
		m.addInfo(sErr.Render("  a turn is running — wait or press Esc"))
		return nil
	}
	ask := m.App.Reg.Policy.Ask
	return func() tea.Msg {
		ctx := context.Background()
		if !m.isGit(ctx) {
			return infoMsg(sErr.Render("  not a git repository"))
		}
		un, _ := m.git(ctx, "ls-files", "--others", "--exclude-standard")
		var add, skipped []string
		for _, f := range strings.Split(strings.TrimSpace(un), "\n") {
			switch {
			case f == "":
			case checkpoint.IsSecretPath(f):
				skipped = append(skipped, f)
			default:
				add = append(add, f)
			}
		}
		stat, _ := m.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--stat", "HEAD")
		if strings.TrimSpace(stat) == "" && len(add) == 0 {
			return infoMsg(sDim.Render("  nothing to commit"))
		}
		msg := strings.TrimSpace(arg)
		if msg == "" {
			d, _ := m.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "HEAD")
			var err error
			if msg, err = m.commitMessage(ctx, stat, d, add); err != nil {
				return infoMsg(sErr.Render("  couldn't write a message (" + err.Error() + ") — use /commit <message>"))
			}
		}
		desc := fmt.Sprintf("commit tracked changes%s with message: %s", map[bool]string{true: fmt.Sprintf(" + %d new file(s)", len(add)), false: ""}[len(add) > 0], truncate(msg, 120))
		if len(skipped) > 0 {
			desc += "; never committed (secret-like): " + strings.Join(skipped[:min(4, len(skipped))], ", ")
		}
		if ask == nil || ask(ctx, "commit", desc, false) == tools.Deny {
			return infoMsg(sDim.Render("  commit cancelled"))
		}
		if out, err := m.git(ctx, "add", "-u"); err != nil {
			return infoMsg(sErr.Render("  git add: " + err.Error() + " " + out))
		}
		if len(add) > 0 {
			if out, err := m.git(ctx, append([]string{"add", "--"}, add...)...); err != nil {
				return infoMsg(sErr.Render("  git add: " + err.Error() + " " + out))
			}
		}
		out, err := m.git(ctx, "commit", "-m", msg)
		if err != nil {
			return infoMsg(sErr.Render("  git commit: " + err.Error() + "\n  " + strings.TrimSpace(out)))
		}
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return infoMsg(sOK.Render("  " + first))
	}
}

// commitMessage asks the cheapest model for a message from the diff.
func (m *Model) commitMessage(ctx context.Context, stat, diff string, untracked []string) (string, error) {
	um := m.App.Router.Utility(8000)
	if um == nil {
		return "", errors.New("no model")
	}
	in := "Files:\n" + stat
	if len(untracked) > 0 {
		in += "\nNew files: " + strings.Join(untracked, ", ")
	}
	in += "\n\nDiff:\n" + tools.Cap(m.App.Reg.Redact.Apply(diff), 24000)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, u, err := llm.Collect(llm.New(um.Provider.Endpoint()).Stream(ctx, llm.Request{Model: um.ID, MaxTokens: 300,
		System:   "Write a git commit message for this change: an imperative subject line of at most 72 characters, then (only if useful) a blank line and a short body. Output only the message.",
		Messages: []llm.Message{{Role: "user", Content: in}}}))
	m.App.Agent.Commit(agent.Record{T: "usage", Usage: &u, Cost: um.Cost(u)})
	out = strings.Trim(strings.TrimSpace(out), "`")
	if err == nil && out == "" {
		err = errors.New("empty reply")
	}
	return out, err
}

// ─────────────────────────── planning and modes ───────────────────────────

const planNote = "[Plan mode — read-only. Investigate with read-only tools and safe commands, then present a concrete plan: files, changes, order, risks and how to verify. Do not edit files or run mutating commands; the user switches back with /code.]"

func (m *Model) enterPlan() {
	if mode := m.App.Reg.Policy.Mode(); mode != "plan" {
		m.prevMode = mode
		m.App.Reg.Policy.SetMode("plan")
	}
}

func (m *Model) leavePlan() {
	if m.App.Reg.Policy.Mode() == "plan" {
		m.App.Reg.Policy.SetMode(orStr(m.prevMode, "ask"))
	}
}

func (m *Model) cmdPlan(arg string) tea.Cmd {
	if arg == "off" {
		m.leavePlan()
		m.addInfo(sOK.Render("  left plan mode: " + m.App.Reg.Policy.Mode()))
		return nil
	}
	m.enterPlan()
	m.addInfo(sOK.Render("  plan mode: read-only (/code to leave)"))
	if arg != "" {
		return m.userTurn(arg)
	}
	return nil
}

func (m *Model) cmdAsk(arg string) tea.Cmd {
	if arg == "" {
		return m.cmdPlan("")
	}
	wasPlan := m.App.Reg.Policy.Mode() == "plan"
	m.enterPlan()
	if !wasPlan {
		m.afterTurn = append(m.afterTurn, func() tea.Cmd { m.leavePlan(); return nil })
	}
	return m.userTurn(arg)
}

func (m *Model) cmdCode(arg string) tea.Cmd {
	m.leavePlan()
	m.addInfo(sOK.Render("  editing mode: " + m.App.Reg.Policy.Mode()))
	if arg != "" {
		return m.userTurn(arg)
	}
	return nil
}

func (m *Model) cmdArchitect(arg string) tea.Cmd {
	if arg == "" {
		m.addInfo(sErr.Render("  usage: /architect <what to build or change>"))
		return nil
	}
	if m.busy {
		m.addInfo(sErr.Render("  a turn is running — wait or press Esc"))
		return nil
	}
	top := m.strongest()
	if top == nil {
		m.addInfo(sErr.Render("  no models yet — /refresh"))
		return nil
	}
	prevPin := m.App.Router.Pinned()
	_, _ = m.App.Router.Pin(top.Key())
	m.enterPlan()
	m.addInfo(sDim.Render("  architect: " + top.Key() + " plans (read-only); then routing picks the cheapest capable model to implement"))
	m.afterTurn = append(m.afterTurn, func() tea.Cmd {
		m.leavePlan()
		if prevPin != nil {
			_, _ = m.App.Router.Pin(prevPin.Key())
		} else {
			_, _ = m.App.Router.Pin("auto")
		}
		if m.lastFailed {
			m.addInfo(sWarn.Render("  architect: the planning turn didn't finish — not implementing"))
			return nil
		}
		m.blocks = append(m.blocks, &block{kind: bUser, text: "(architect) implement the plan"})
		return m.start("Implement the plan above exactly, as the editor. Make the edits, then verify they build and the tests pass.")
	})
	m.blocks = append(m.blocks, &block{kind: bUser, text: "/architect " + arg})
	m.App.Agent.SetNextEffort("high") // the plan is where reasoning pays (ADR 015)
	return m.start("As the architect, plan this change for an implementer who will follow your plan exactly: name the files, functions and concrete edits, in order, and how to verify. Do not edit.\n\nTask: " + arg)
}

// userTurn starts a turn the user typed (shown as their message).
func (m *Model) userTurn(prompt string) tea.Cmd {
	if m.busy {
		m.queue = append(m.queue, prompt)
		m.addInfo(sDim.Render("  queued: ") + prompt)
		return nil
	}
	m.blocks = append(m.blocks, &block{kind: bUser, text: prompt})
	return m.start(prompt)
}

// ─────────────────────────── files and shell ───────────────────────────

const (
	maxPinBytes   = 40 << 10
	maxAttachSize = 120 << 10
)

// readable checks that rel is a regular workspace file that may be sent to a model.
func (m *Model) readable(rel string) (string, []byte, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if checkpoint.IsSecretPath(rel) {
		return rel, nil, fmt.Errorf("%s looks like a secret; not sent to a model", rel)
	}
	b, err := m.App.Reg.ReadFile(rel)
	return rel, b, err
}

func (m *Model) cmdAdd(arg string) tea.Cmd {
	if arg == "" {
		m.addInfo(sErr.Render("  usage: /add <files>  (globs work: /add internal/tui/*.go)"))
		return nil
	}
	var added, errs []string
	for _, a := range commands.SplitArgs(arg) {
		matches := []string{a}
		if strings.ContainsAny(a, "*?[") {
			ms, _ := filepath.Glob(filepath.Join(m.App.Reg.Root, a))
			matches = matches[:0]
			for _, p := range ms {
				matches = append(matches, m.App.Reg.Rel(p))
			}
		}
		for _, f := range matches {
			rel, _, err := m.readable(f)
			switch {
			case err != nil:
				errs = append(errs, err.Error())
			case containsStr(m.pins, rel):
			default:
				m.pins = append(m.pins, rel)
				added = append(added, rel)
			}
		}
	}
	s := ""
	if len(added) > 0 {
		s = sOK.Render("  pinned: "+strings.Join(added, ", ")) + sDim.Render(" — sent with every prompt until /drop")
	}
	for _, e := range errs {
		s += "\n" + sErr.Render("  "+e)
	}
	m.addInfo(strings.TrimPrefix(s, "\n"))
	return nil
}

func (m *Model) cmdDrop(arg string) tea.Cmd {
	if arg == "" {
		m.pins = nil
		m.addInfo(sOK.Render("  unpinned all files"))
		return nil
	}
	drop := commands.SplitArgs(arg)
	kept := m.pins[:0]
	for _, p := range m.pins {
		if !containsStr(drop, p) {
			kept = append(kept, p)
		}
	}
	m.pins = kept
	return m.cmdLs("")
}

func (m *Model) cmdLs(string) tea.Cmd {
	if len(m.pins) == 0 {
		m.addInfo(sDim.Render("  no pinned files — /add <files>"))
		return nil
	}
	var b strings.Builder
	for _, p := range m.pins {
		_, data, err := m.readable(p)
		if err != nil {
			b.WriteString(fmt.Sprintf("  %s %s\n", sErr.Render("✗"), p+" — "+err.Error()))
			continue
		}
		b.WriteString(fmt.Sprintf("  %s %-50s %s\n", sOK.Render("●"), p, sDim.Render(kfmt(len(data)*10/36)+" tokens")))
	}
	m.addInfo(strings.TrimRight(b.String(), "\n"))
	return nil
}

var reMention = regexp.MustCompile(`(?:^|\s)@([\w./~-]+[\w])`)

// attachments is the context sent with a prompt: plan-mode instructions,
// output the user collected (/run, /web), pinned files and @mentioned files.
func (m *Model) attachments(prompt string) string {
	var parts []string
	if m.App.Reg.Policy.Mode() == "plan" {
		parts = append(parts, planNote)
	}
	parts = append(parts, m.pending...)
	m.pending = nil
	files := append([]string(nil), m.pins...)
	for _, mm := range reMention.FindAllStringSubmatch(prompt, -1) {
		if !containsStr(files, mm[1]) {
			if _, err := os.Stat(filepath.Join(m.App.Reg.Root, mm[1])); err == nil {
				files = append(files, mm[1])
			}
		}
	}
	total := 0
	for _, f := range files {
		rel, data, err := m.readable(f)
		if err != nil {
			parts = append(parts, fmt.Sprintf("(%s: %v)", f, err))
			continue
		}
		note := ""
		if len(data) > maxPinBytes {
			data, note = data[:maxPinBytes], fmt.Sprintf("\n… truncated at %d KB; read_file for the rest", maxPinBytes>>10)
		}
		if total += len(data); total > maxAttachSize {
			parts = append(parts, fmt.Sprintf("(%s not included: attachments over %d KB)", rel, maxAttachSize>>10))
			continue
		}
		framed, _ := m.App.Reg.Frame.Wrap("file:"+rel, m.App.Reg.Redact.Apply(string(data))+note)
		parts = append(parts, "Current contents of "+rel+":\n"+framed)
	}
	return strings.Join(parts, "\n\n")
}

type ranMsg struct {
	cmd, out string
	failed   bool
	fix      string // non-empty: start a fixing turn with this prompt
}

// runCmd runs a shell command for the user: shown, and either added as
// context for the next prompt or (fix != "") handed to the model on failure.
func (m *Model) runCmd(cmd, fix string) tea.Cmd {
	m.blocks = append(m.blocks, &block{kind: bTool, tool: "bash", id: "user-run", text: cmd})
	m.refresh(true)
	return func() tea.Msg {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		r := m.App.Reg.Call(context.Background(), llm.ToolCall{ID: "user-run", Name: "bash", Args: string(args)})
		return ranMsg{cmd: cmd, out: r.Out, failed: r.IsErr, fix: fix}
	}
}

func (m *Model) onRan(r ranMsg) tea.Cmd {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if b := m.blocks[i]; b.kind == bTool && b.id == "user-run" && b.state == 0 {
			b.state, b.detail, b.rendered = 1, firstLines(tools.Unframe(r.out), 6), ""
			if r.failed {
				b.state = 2
			}
			break
		}
	}
	ctx := "Output of `" + r.cmd + "`, which the user ran:\n" + r.out
	switch {
	case r.fix != "" && r.failed:
		if m.busy {
			m.pending = append(m.pending, ctx)
			return nil
		}
		m.blocks = append(m.blocks, &block{kind: bUser, text: r.fix})
		return m.startWith(r.fix, ctx)
	case r.fix != "":
		m.addInfo(sOK.Render("  ✓ passed: " + r.cmd))
	default:
		m.pending = append(m.pending, ctx)
		m.addInfo(sDim.Render("  output added to your next prompt"))
	}
	return nil
}

func (m *Model) cmdRun(arg string) tea.Cmd {
	if arg == "" {
		m.addInfo(sErr.Render("  usage: /run <command>  (or !command)"))
		return nil
	}
	return m.runCmd(arg, "")
}

func (m *Model) cmdTest(arg string) tea.Cmd {
	v := m.App.Agent.VerifyCmd()
	if v == "off" {
		v = ""
	}
	cmd := orStr(arg, orStr(v, agent.DetectVerify(m.App.Reg.Root)))
	if cmd == "" {
		m.addInfo(sErr.Render("  no test command detected — /test <command> or /verify <command>"))
		return nil
	}
	return m.runCmd(cmd, "The tests fail (`"+cmd+"`, output below). Find and fix the root cause, then run them again.")
}

func lintCommand(root string) string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(root, f)); return err == nil }
	switch {
	case has("go.mod"):
		return "go vet ./..."
	case has("Cargo.toml"):
		return "cargo clippy"
	case has("package.json"):
		return "npm run lint"
	case has("pyproject.toml") || has("setup.py") || has("requirements.txt"):
		return "ruff check ."
	}
	return ""
}

func (m *Model) cmdLint(arg string) tea.Cmd {
	cmd := orStr(arg, lintCommand(m.App.Reg.Root))
	if cmd == "" {
		m.addInfo(sErr.Render("  no linter detected — /lint <command>"))
		return nil
	}
	return m.runCmd(cmd, "The linter reports problems (`"+cmd+"`, output below). Fix them, then run it again.")
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript|svg)[^>]*>.*?</(script|style|noscript|svg)>`)
	reBlock  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/pre)[^>]*>`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reBlank  = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// htmlText is a page's readable text: scripts and styles dropped, block
// ends kept as line breaks, entities decoded.
func htmlText(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = html.UnescapeString(reTag.ReplaceAllString(s, " "))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

type webMsg struct {
	url, text string
	err       error
}

func (m *Model) cmdWeb(arg string) tea.Cmd {
	if !strings.HasPrefix(arg, "http://") && !strings.HasPrefix(arg, "https://") {
		m.addInfo(sErr.Render("  usage: /web <http(s) URL>"))
		return nil
	}
	if m.App.Reg.Sandbox.NoNet {
		m.addInfo(sErr.Render("  /web is off: this session runs with --no-net"))
		return nil
	}
	if m.App.Reg.Policy.Mode() == "plan" {
		m.addInfo(sErr.Render("  /web is off in plan mode (read-only, no outside data) — /code to leave"))
		return nil
	}
	m.addInfo(sDim.Render("  fetching " + arg + "…"))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", arg, nil)
		if err != nil {
			return webMsg{url: arg, err: err}
		}
		req.Header.Set("User-Agent", "ternly/"+m.App.Version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return webMsg{url: arg, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return webMsg{url: arg, err: fmt.Errorf("HTTP %d", resp.StatusCode)}
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		text := string(b)
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") || strings.Contains(strings.ToLower(text[:min(len(text), 512)]), "<html") {
			text = htmlText(text)
		}
		return webMsg{url: arg, text: tools.Cap(text, 60000)}
	}
}

func (m *Model) onWeb(w webMsg) {
	if w.err != nil {
		m.addInfo(sErr.Render("  /web " + w.url + ": " + w.err.Error()))
		return
	}
	framed, flagged := m.App.Reg.Frame.Wrap("web", m.App.Reg.Redact.Apply(w.text))
	m.pending = append(m.pending, "Page "+w.url+" (fetched by the user):\n"+framed)
	s := sOK.Render(fmt.Sprintf("  fetched %s (~%s tokens) — added to your next prompt", w.url, kfmt(len(w.text)*10/36)))
	if flagged {
		s += "\n" + sWarn.Render("  ⚠ the page contains text that looks like instructions — it is passed to the model as untrusted data")
	}
	m.addInfo(s)
}

type editedMsg struct {
	text string
	err  error
}

func (m *Model) cmdEditor(string) tea.Cmd {
	ed := orStr(os.Getenv("VISUAL"), orStr(os.Getenv("EDITOR"), "vi"))
	f, err := os.CreateTemp("", "ternly-prompt-*.md")
	if err != nil {
		m.addInfo(sErr.Render("  " + err.Error()))
		return nil
	}
	_, _ = f.WriteString(m.ta.Value())
	f.Close()
	argv := commands.SplitArgs(ed)
	c := exec.Command(argv[0], append(argv[1:], f.Name())...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		b, rerr := os.ReadFile(f.Name())
		if err == nil {
			err = rerr
		}
		return editedMsg{text: strings.TrimRight(string(b), "\n"), err: err}
	})
}

// ─────────────────────────── extensions ───────────────────────────

func (m *Model) cmdTools(string) tea.Cmd {
	var b strings.Builder
	for _, sp := range m.App.Reg.Specs() {
		d, _, _ := strings.Cut(sp.Description, ". ")
		b.WriteString(fmt.Sprintf("  %-20s %s\n", sp.Name, sDim.Render(truncate(d, m.w-26))))
	}
	m.addInfo(strings.TrimRight(b.String(), "\n"))
	return nil
}

type mcpMsg struct{ text string }

// verifyLabel describes the verify setting: a command, coverage checks
// only, or off.
func verifyLabel(v string) string {
	switch v {
	case "off":
		return "off"
	case "":
		return "coverage checks of changed files (no project command)"
	}
	return v + " + coverage checks of changed files"
}

func (m *Model) cmdMCP(arg string) tea.Cmd {
	if f := strings.Fields(arg); len(f) > 0 {
		return m.mcpAccount(f)
	}
	servers := map[string][]string{}
	for _, sp := range m.App.Reg.Specs() {
		if rest, ok := strings.CutPrefix(sp.Name, "mcp__"); ok {
			srv, tool, _ := strings.Cut(rest, "__")
			servers[srv] = append(servers[srv], tool)
		}
	}
	remote := map[string]mcpremote.Status{}
	if m.App.Remote != nil {
		for _, st := range m.App.Remote.Statuses() {
			remote[st.Name] = st
			if _, ok := servers[st.Name]; !ok {
				servers[st.Name] = nil
			}
		}
	}
	if len(servers) == 0 {
		m.addInfo(sDim.Render("  no MCP servers — configure them in ~/.config/ternly/mcp.json (./.mcp.json with --project-mcp)"))
		return nil
	}
	var names []string
	for s := range servers {
		names = append(names, s)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, s := range names {
		trusted := ""
		if m.App.Reg.Policy.Trusted[s] {
			trusted = sOK.Render(" trusted")
		}
		b.WriteString(fmt.Sprintf("  %s%s %s\n", sAccent.Render(s), trusted, sDim.Render(fmt.Sprintf("(%d tools)", len(servers[s])))))
		if st, ok := remote[s]; ok {
			auth := st.Auth
			if auth == "needs login" {
				auth = sWarn.Render(auth + " — /mcp login " + s)
			}
			b.WriteString(sDim.Render(fmt.Sprintf("    %s · %s · hosts %s · ", st.URL, orStr(st.Era, "not connected"), strings.Join(st.Hosts, ", "))) + auth + "\n")
			if st.Err != "" && st.Auth != "needs login" {
				b.WriteString("    " + sErr.Render(st.Err) + "\n")
			}
		}
		if len(servers[s]) > 0 {
			b.WriteString("    " + strings.Join(servers[s], ", ") + "\n")
		}
	}
	m.addInfo(strings.TrimRight(b.String(), "\n"))
	return nil
}

// mcpAccount is /mcp login|logout <server>. A login asks before ternly
// contacts any host the server's authorization uses beyond the server
// itself, then opens the browser; the tools arrive at the next prompt.
func (m *Model) mcpAccount(f []string) tea.Cmd {
	if m.App.Remote == nil || len(f) != 2 || (f[0] != "login" && f[0] != "logout") {
		m.addInfo(sDim.Render("  /mcp · /mcp login <server> · /mcp logout <server>  (remote servers: \"url\" in ~/.config/ternly/mcp.json)"))
		return nil
	}
	rm, name := m.App.Remote, f[1]
	if f[0] == "logout" {
		if err := rm.Logout(name); err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sOK.Render("  " + name + ": credentials deleted (its tools stay until ternly restarts or the server refuses them)"))
		}
		return nil
	}
	ask := m.App.Reg.Policy.Ask
	m.addInfo(sDim.Render("  " + name + ": logging in…"))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var lines []string
		err := rm.Login(ctx, name, func(host string) bool {
			return ask != nil && ask(ctx, "allow network host", fmt.Sprintf("%s's login uses %s — let ternly contact it for this server (saved to its grant)", name, host), false) != tools.Deny
		}, func(msg string) { lines = append(lines, msg) })
		text := ""
		for _, l := range lines {
			text += sDim.Render("  "+l) + "\n"
		}
		if err != nil {
			return mcpMsg{text + sErr.Render("  "+name+": "+err.Error())}
		}
		return mcpMsg{text + sOK.Render("  "+name+": logged in — its tools are usable from your next prompt")}
	}
}

func (m *Model) cmdCommands(arg string) tea.Cmd {
	if arg == "reload" {
		errs := m.loadUserCommands()
		s := sOK.Render(fmt.Sprintf("  reloaded: %d user commands", len(m.userCmds)))
		for _, e := range errs {
			s += "\n" + sWarn.Render("  ! "+e.Error())
		}
		m.addInfo(s)
		return nil
	}
	if len(m.userCmds) == 0 {
		m.addInfo(sDim.Render("  no user commands — put markdown files in .ternly/commands/ or ~/.config/ternly/commands/ (Claude Code, OpenCode, Gemini CLI and Codex locations work too; see docs/commands.md)"))
		return nil
	}
	var b strings.Builder
	for _, u := range m.userCmds {
		where := "user"
		if u.Project {
			where = "project"
		}
		b.WriteString(fmt.Sprintf("  /%-24s %s %s\n", u.Name+" "+u.ArgHint, u.Description, sDim.Render("("+where+", "+string(u.Origin)+")")))
	}
	m.addInfo(strings.TrimRight(b.String(), "\n"))
	return nil
}

// ─────────────────────────── completion ───────────────────────────

type compItem struct {
	name, hint, desc string
	project          bool
}

type completion struct {
	items []compItem
	sel   int
}

// fuzzy scores how well q matches s as a subsequence (-1: no match):
// a prefix beats a word-start match beats a scattered one; shorter wins ties.
func fuzzyScore(q, s string) int {
	if q == "" {
		return 1
	}
	if strings.HasPrefix(s, q) {
		return 1000 - len(s)
	}
	score, j, run := 0, 0, 0
	for i := 0; i < len(s) && j < len(q); i++ {
		if s[i] == q[j] {
			run++
			score += 10 * run
			if i == 0 || s[i-1] == '-' || s[i-1] == ':' || s[i-1] == '_' {
				score += 30
			}
			j++
		} else {
			run = 0
		}
	}
	if j < len(q) {
		return -1
	}
	return score - len(s)
}

// updateCompletion shows matching commands while a command name is typed.
func (m *Model) updateCompletion() {
	v := m.ta.Value()
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") {
		if m.comp != nil {
			m.comp = nil
			m.layout()
		}
		return
	}
	q := strings.ToLower(v[1:])
	type scored struct {
		compItem
		s int
	}
	var all []scored
	for _, c := range builtins {
		best := fuzzyScore(q, c.name)
		for _, a := range c.aliases {
			best = max(best, fuzzyScore(q, a)-5)
		}
		if best >= 0 {
			all = append(all, scored{compItem{name: c.name, hint: c.args, desc: c.desc}, best})
		}
	}
	for _, u := range m.userCmds {
		if s := fuzzyScore(q, strings.ToLower(u.Name)); s >= 0 {
			all = append(all, scored{compItem{name: u.Name, hint: u.ArgHint, desc: u.Description, project: u.Project}, s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s > all[j].s })
	if len(all) == 0 {
		m.comp = nil
		m.layout()
		return
	}
	prev := m.comp
	m.comp = &completion{}
	for _, a := range all[:min(len(all), 8)] {
		m.comp.items = append(m.comp.items, a.compItem)
	}
	if prev != nil && prev.sel < len(m.comp.items) {
		m.comp.sel = prev.sel
	}
	m.layout()
}

// compKey handles keys while the completion list is open.
func (m *Model) compKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	c := m.comp
	switch k.String() {
	case "tab":
		m.ta.SetValue("/" + c.items[c.sel].name + " ")
		m.ta.CursorEnd()
		m.comp = nil
		m.layout()
		return nil, true
	case "up", "ctrl+p":
		c.sel = (c.sel + len(c.items) - 1) % len(c.items)
		return nil, true
	case "down", "ctrl+n":
		c.sel = (c.sel + 1) % len(c.items)
		return nil, true
	case "esc":
		m.comp = nil
		m.layout()
		return nil, true
	case "enter": // run the highlighted command unless the typed name is already exact
		typed := strings.TrimPrefix(strings.TrimSpace(m.ta.Value()), "/")
		if lookup(typed) == nil && m.userCommand(typed) == nil {
			m.ta.SetValue("/" + c.items[c.sel].name)
		}
		m.comp = nil
		m.layout()
	}
	return nil, false
}

func (m *Model) compView() string {
	var b strings.Builder
	for i, it := range m.comp.items {
		name := "/" + it.name
		if it.hint != "" {
			name += " " + sDim.Render(it.hint)
		}
		desc := it.desc
		if it.project {
			desc += " (project)"
		}
		line := fmt.Sprintf(" %-40s %s", name, sDim.Render(truncate(desc, max(10, m.w-48))))
		if i == m.comp.sel {
			line = sAccent.Render("›") + line
		} else {
			line = " " + line
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) cmdSkills(string) tea.Cmd {
	rt := m.App.Plugins
	if rt == nil {
		m.addInfo(sDim.Render("  plugins are off"))
		return nil
	}
	tokens := rt.Tokens()
	var b strings.Builder
	total := 0
	for _, mf := range rt.Active() {
		for _, c := range mf.Components {
			switch c.Kind {
			case "skill", "agent", "rule", "context":
				t := tokens[c.Name]
				total += t
				b.WriteString(fmt.Sprintf("  %-7s %-32s %s\n", c.Kind, c.Name, sDim.Render(truncate(c.Description, max(20, m.w-52))+map[bool]string{true: fmt.Sprintf(" (~%d tok)", t), false: ""}[t > 0])))
			}
		}
	}
	if b.Len() == 0 {
		m.addInfo(sDim.Render("  none — skills from ~/.claude/skills, .claude/skills, agents from .claude/agents, rules from .cursor/rules, or /plugin add"))
		return nil
	}
	b.WriteString(sDim.Render(fmt.Sprintf("  listings cost ~%d tokens per request; bodies load only when used (use_skill, task)", total)))
	m.addInfo(b.String())
	return nil
}
