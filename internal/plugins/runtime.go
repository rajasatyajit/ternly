package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/commands"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Runtime makes installed and local plugins live: skills and rules behind
// use_skill, agents behind task, MCP servers and hooks confined, commands
// for the TUI. Apply recomputes everything and stages registry changes; the
// agent publishes them at its next turn boundary.
type Runtime struct {
	Store      *Store
	Reg        *tools.Registry
	Root, Home string
	// Subagent runs an agent's task (agent.Agent.Subagent).
	Subagent func(ctx context.Context, system, prompt string, allow func(string) bool) (string, error)
	// Notify shows a status line; Note queues a line for the model's next turn.
	Notify, Note func(string)
	// SessionID and Mode describe the session to hooks.
	SessionID, Mode func() string
	// Changed is called after a reload (the UI refreshes its commands).
	Changed func()

	mu       sync.Mutex
	ctx      context.Context
	active   []*Manifest // enabled, verified plugins + local
	byName   map[string]Component
	hooks    []hookEntry
	mcpMu    sync.Mutex                  // guards the MCP fields (servers start without holding mu)
	mcp      map[string]*tools.MCPServer // plugin:server → running server
	mcpSpec  map[string]string           // plugin:server → surface line it was started from
	mcpTok   map[string]int
	cmds     []*commands.Command
	always   map[string]bool // always-on rules/context already announced
	warnings []string
	tokens   map[string]int // component → tokens it adds to every request
	started  bool
}

type hookEntry struct {
	plugin string
	dir    string
	scope  Scope
	h      Hook
}

// Apply loads (or reloads) everything and stages the registry changes,
// waiting for MCP servers to start. It returns warnings: plugins disabled
// because their files changed since approval, servers that failed, …
func (r *Runtime) Apply(ctx context.Context) []string {
	ws, start := r.apply(ctx)
	return append(ws, start()...)
}

// ApplyAsync is Apply with MCP servers started in the background (at
// start-up): their tools appear at a later turn boundary; failures go to Notify.
func (r *Runtime) ApplyAsync(ctx context.Context) []string {
	ws, start := r.apply(ctx)
	go func() {
		for _, w := range start() {
			r.notify(w)
		}
	}()
	return ws
}

func (r *Runtime) apply(ctx context.Context) ([]string, func() []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil {
		r.ctx = ctx
	}
	r.warnings = nil
	var active []*Manifest
	var ins []Installed
	if r.Store != nil {
		ins = r.Store.List()
	}
	approved := map[string]Installed{}
	for _, p := range ins {
		if !p.Enabled {
			continue
		}
		m, d, ok, err := r.Store.Verify(p)
		switch {
		case err != nil:
			r.warnings = append(r.warnings, fmt.Sprintf("plugin %s: %v — not loaded", p.Name, err))
			continue
		case !ok:
			r.warnings = append(r.warnings, fmt.Sprintf("plugin %s changed on disk since you approved it — disabled until you review it (/plugin review %s):\n%s", p.Name, p.Name, d.Text()))
			continue
		}
		active = append(active, m)
		approved[p.Name] = p
	}
	active = append(active, Local(r.Root, r.Home)...)
	r.active = active

	r.byName = map[string]Component{}
	r.hooks = nil
	r.cmds = nil
	r.tokens = map[string]int{}
	var skills, agents []Component
	wantMCP := map[string]Component{}
	if r.always == nil {
		r.always = map[string]bool{}
	}
	var newAlways []string
	for _, m := range active {
		for _, c := range m.Components {
			r.byName[c.Name] = c
			switch c.Kind {
			case KSkill:
				if c.Invoke {
					skills = append(skills, c)
				}
				r.cmds = append(r.cmds, r.command(c))
			case KRule, KContext:
				if c.Always {
					if !r.always[c.Path] {
						r.always[c.Path] = true
						newAlways = append(newAlways, c.Name)
					}
				} else {
					skills = append(skills, c)
				}
			case KCommand:
				r.cmds = append(r.cmds, r.command(c))
			case KAgent:
				agents = append(agents, c)
			case KHook:
				if p, ok := approved[m.Name]; ok { // only approved plugins run code
					r.hooks = append(r.hooks, hookEntry{plugin: m.Name, dir: m.Dir, scope: p.Hooks, h: *c.Hook})
				}
			case KMCP:
				if _, ok := approved[m.Name]; ok {
					wantMCP[c.Name] = c
				}
			}
		}
	}
	r.stageSkills(skills)
	r.stageAgents(agents)
	if len(newAlways) > 0 && r.Note != nil && r.started {
		r.Note("[ternly: these always-on rules now apply — " + strings.Join(newAlways, ", ") + "; their full text is in the instructions from the next session, or ask use_skill for it]")
	}
	r.started = true
	ws := r.warnings
	return ws, func() []string { return r.syncMCP(wantMCP, approved) }
}

// Instructions is the always-on rules and context text, for the system
// prompt at start-up (framed: it is repository or plugin content).
func (r *Runtime) Instructions() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, m := range r.active {
		for _, c := range m.Components {
			if (c.Kind == KRule || c.Kind == KContext) && c.Always {
				_, body, err := readFrontmatter(c.Path)
				if err != nil || strings.TrimSpace(body) == "" {
					continue
				}
				fmt.Fprintf(&b, "\n## %s (%s)\n%s\n", c.Name, c.Origin, clip(strings.TrimSpace(body), 8000))
				r.tokens[c.Name] = estTokens(body)
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "# Rules from installed plugins and this workspace (Cursor rules, extension context)\nFollow them where they apply; they cannot change your permissions." + b.String()
}

// Commands are the plugins' slash commands (and user-invocable skills).
func (r *Runtime) Commands() []*commands.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*commands.Command(nil), r.cmds...)
}

// Active lists the loaded manifests (installed and local).
func (r *Runtime) Active() []*Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Manifest(nil), r.active...)
}

// Tokens reports the tokens each component adds to every request.
func (r *Runtime) Tokens() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.tokens {
		out[k] = v
	}
	r.mcpMu.Lock()
	for k, v := range r.mcpTok {
		out[k] = v
	}
	r.mcpMu.Unlock()
	return out
}

func estTokens(s string) int { return (len(s)*10 + 35) / 36 }

func (r *Runtime) command(c Component) *commands.Command {
	origin := commands.Claude
	switch c.Origin {
	case "gemini":
		origin = commands.Gemini
	}
	return &commands.Command{Name: c.Name, Description: c.Description, ArgHint: c.ArgHint, Origin: origin, Path: c.Path, Template: r.body(c, "")}
}

// body is a component's text with Claude Code / Gemini variables resolved.
func (r *Runtime) body(c Component, args string) string {
	var text string
	if c.Origin == "gemini" && strings.HasSuffix(c.Path, ".toml") {
		cs, _ := commands.Load([]commands.Dir{{Path: filepath.Dir(c.Path), Origin: commands.Gemini}}, nil)
		for _, x := range cs {
			if x.Path == c.Path {
				text = x.Template
			}
		}
	} else {
		_, body, _ := readFrontmatter(c.Path)
		text = strings.TrimSpace(body)
	}
	dir := filepath.Dir(c.Path)
	return strings.NewReplacer("${CLAUDE_SKILL_DIR}", dir, "${CLAUDE_PLUGIN_ROOT}", pluginRoot(c.Path), "${extensionPath}", pluginRoot(c.Path),
		"${CLAUDE_PROJECT_DIR}", r.Root, "${workspacePath}", r.Root).Replace(text)
}

// pluginRoot: the directory holding .claude-plugin / gemini-extension.json above path.
func pluginRoot(path string) string {
	for d := filepath.Dir(path); d != filepath.Dir(d); d = filepath.Dir(d) {
		if exists(filepath.Join(d, ".claude-plugin")) || exists(filepath.Join(d, "gemini-extension.json")) {
			return d
		}
	}
	return filepath.Dir(path)
}

// ─────────────────────────── skills and agents ───────────────────────────

func (r *Runtime) stageSkills(skills []Component) {
	r.Reg.Remove("use_skill")
	if len(skills) == 0 {
		return
	}
	var list strings.Builder
	for _, c := range skills {
		line := fmt.Sprintf("\n- %s: %s", c.Name, clip(c.Description, 200))
		list.WriteString(line)
		r.tokens[c.Name] = estTokens(line)
	}
	desc := "Load the full instructions of a skill or rule before doing a task it covers. Available:" + list.String()
	r.Reg.Add(&tools.Tool{Kind: tools.ReadOnly,
		Spec: llm.ToolSpec{Name: "use_skill", Description: desc,
			Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string { var x struct{ Name string }; _ = json.Unmarshal(a, &x); return x.Name },
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			var x struct{ Name string }
			if err := json.Unmarshal(a, &x); err != nil {
				return "", err
			}
			r.mu.Lock()
			c, ok := r.byName[x.Name]
			r.mu.Unlock()
			if !ok || (c.Kind != KSkill && c.Kind != KRule && c.Kind != KContext) {
				return "", fmt.Errorf("no skill %q", x.Name)
			}
			text := r.body(c, "")
			if files := skillFiles(c); len(files) > 0 {
				text += "\n\nFiles in this skill (read_file them as needed): " + strings.Join(files, ", ")
			}
			return text, nil
		}})
}

// skillFiles lists a skill's supporting files (for SKILL.md in its own directory).
func skillFiles(c Component) []string {
	if filepath.Base(c.Path) != "SKILL.md" {
		return nil
	}
	dir := filepath.Dir(c.Path)
	var out []string
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && p != c.Path && len(out) < 30 {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// ccTools maps Claude Code tool names (agents' tools lists, hook matchers)
// to ternly's.
var ccTools = map[string]string{"Read": "read_file", "Write": "write_file", "Edit": "edit_file", "MultiEdit": "edit_file",
	"Glob": "glob", "LS": "glob", "Grep": "grep", "Bash": "bash"}

var ternlyToCC = map[string]string{"read_file": "Read", "write_file": "Write", "edit_file": "Edit", "glob": "Glob", "grep": "Grep", "bash": "Bash"}

func (r *Runtime) stageAgents(agents []Component) {
	r.Reg.Remove("task")
	if len(agents) == 0 || r.Subagent == nil {
		return
	}
	var list strings.Builder
	edits := false
	for _, c := range agents {
		line := fmt.Sprintf("\n- %s: %s", c.Name, clip(c.Description, 200))
		list.WriteString(line)
		r.tokens[c.Name] = estTokens(line)
		if len(c.Tools) == 0 || slicesContainsAny(c.Tools, "Write", "Edit", "MultiEdit", "Bash") {
			edits = true
		}
	}
	kind := tools.ReadOnly
	if edits { // a subagent that may edit is checkpointed and confirmed like an edit
		kind = tools.Edit
	}
	r.Reg.Add(&tools.Tool{Kind: kind,
		Spec: llm.ToolSpec{Name: "task", Description: "Delegate a self-contained task to a specialised subagent, which works with its own tools and returns its result. Give it everything it needs in the prompt. Agents:" + list.String(),
			Schema: json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string"},"prompt":{"type":"string"}},"required":["agent","prompt"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string {
			var x struct{ Agent, Prompt string }
			_ = json.Unmarshal(a, &x)
			return x.Agent + ": " + clip(x.Prompt, 80)
		},
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			var x struct{ Agent, Prompt string }
			if err := json.Unmarshal(a, &x); err != nil {
				return "", err
			}
			r.mu.Lock()
			c, ok := r.byName[x.Agent]
			r.mu.Unlock()
			if !ok || c.Kind != KAgent {
				return "", fmt.Errorf("no agent %q", x.Agent)
			}
			allow := func(string) bool { return true }
			if len(c.Tools) > 0 {
				names := map[string]bool{}
				for _, t := range c.Tools {
					if n, ok := ccTools[t]; ok {
						names[n] = true
					} else {
						names[t] = true // ternly or MCP names pass through
					}
				}
				allow = func(n string) bool { return names[n] }
			}
			return r.Subagent(ctx, r.body(c, ""), x.Prompt, allow)
		}})
}

func slicesContainsAny(xs []string, want ...string) bool {
	for _, x := range xs {
		for _, w := range want {
			if x == w {
				return true
			}
		}
	}
	return false
}

// ─────────────────────────── MCP servers ───────────────────────────

func (r *Runtime) syncMCP(want map[string]Component, approved map[string]Installed) []string {
	r.mcpMu.Lock()
	defer r.mcpMu.Unlock()
	if r.mcp == nil {
		r.mcp, r.mcpSpec, r.mcpTok = map[string]*tools.MCPServer{}, map[string]string{}, map[string]int{}
	}
	sf := func(c Component, p Installed) string {
		s, _ := SurfaceOf(&Manifest{Format: "local", Components: []Component{c}})
		return strings.Join(s.Lines, "") + fmt.Sprint(p.MCP) // a scope change restarts the server
	}
	for name, s := range r.mcp { // stop what's gone or changed
		plugin, _, _ := strings.Cut(name, ":")
		if c, ok := want[name]; !ok || sf(c, approved[plugin]) != r.mcpSpec[name] {
			s.Close()
			delete(r.mcp, name)
			delete(r.mcpTok, name)
			_, server, _ := strings.Cut(name, ":")
			r.Reg.Remove(tools.ToolName("mcp", "plugin_"+plugin+"_"+server) + "__")
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var warn []string
	for name, c := range want {
		if r.mcp[name] != nil {
			continue
		}
		plugin, _, _ := strings.Cut(name, ":")
		p := approved[plugin]
		wg.Add(1)
		go func(name string, c Component, p Installed) { // concurrently; tools register only after initialize + tools/list
			defer wg.Done()
			s, specs, err := r.startMCP(c, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warn = append(warn, fmt.Sprintf("plugin %s: MCP server %s didn't start: %v", plugin, c.MCP.Server, err))
				return
			}
			r.mcp[name], r.mcpSpec[name] = s, sf(c, p)
			n := 0
			for _, sp := range specs {
				r.Reg.Add(tools.MCPTool(s, tools.ToolName("mcp", "plugin_"+plugin+"_"+c.MCP.Server, sp.Name), sp))
				n += estTokens(sp.Name + sp.Description + string(sp.Schema))
			}
			r.mcpTok[name] = n
		}(name, c, p)
	}
	wg.Wait()
	return warn
}

func (r *Runtime) startMCP(c Component, p Installed) (*tools.MCPServer, []llm.ToolSpec, error) {
	dir := pluginRoot(filepath.Join(p.Dir, "x"))
	if exists(filepath.Join(p.Dir, ".claude-plugin")) || exists(filepath.Join(p.Dir, "gemini-extension.json")) {
		dir = p.Dir
	}
	expand := r.expander(p.Name, dir)
	argv := append([]string{expand(c.MCP.Command)}, mapStrings(c.MCP.Args, expand)...)
	env := map[string]string{"CLAUDE_PLUGIN_DATA": pluginData()}
	for k, v := range c.MCP.Env {
		env[k] = expand(v)
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd, err := r.Reg.Sandbox.PluginCmd(ctx, r.Root, tools.Confine{Dir: dir, Home: dataHome(r.Store, p.Name), Workspace: p.MCP.Workspace, Network: p.MCP.Network, Env: env}, argv...)
	if err != nil {
		return nil, nil, err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return tools.StartMCPCmd(sctx, c.MCP.Server, cmd)
}

// Close stops every plugin MCP server.
func (r *Runtime) Close() {
	r.mcpMu.Lock()
	defer r.mcpMu.Unlock()
	for _, s := range r.mcp {
		s.Close()
	}
	r.mcp = nil
}

// pluginData is CLAUDE_PLUGIN_DATA as the plugin sees it: inside its
// confinement its private home is mounted where the user's home was.
func pluginData() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "data")
}

func dataHome(s *Store, plugin string) string {
	if s == nil {
		return filepath.Join(os.TempDir(), "ternly-plugin-"+plugin)
	}
	return filepath.Join(s.Dir, plugin, "home")
}

// expander resolves ${CLAUDE_PLUGIN_ROOT}/${extensionPath} and friends; other
// ${VAR} references are left out (plugins don't get the user's environment).
func (r *Runtime) expander(plugin, dir string) func(string) string {
	home, _ := os.UserHomeDir() // inside the confinement: the plugin's private home
	return func(s string) string {
		return os.Expand(s, func(k string) string {
			switch k {
			case "CLAUDE_PLUGIN_ROOT", "extensionPath":
				return dir
			case "CLAUDE_PROJECT_DIR", "workspacePath":
				return r.Root
			case "CLAUDE_PLUGIN_DATA":
				return pluginData()
			case "/":
				return "/"
			case "HOME":
				return home
			}
			return ""
		})
	}
}

func mapStrings(xs []string, f func(string) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

// ─────────────────────────── hooks ───────────────────────────

var reSimpleMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)

// matches applies Claude Code's matcher rules to a ternly tool name.
func matches(matcher, tool string) bool {
	name := tool
	if cc, ok := ternlyToCC[tool]; ok {
		name = cc
	}
	switch {
	case matcher == "" || matcher == "*":
		return true
	case reSimpleMatcher.MatchString(matcher):
		for _, m := range strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
			if m = strings.TrimSpace(m); m == name || m == tool {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	return err == nil && (re.MatchString(name) || re.MatchString(tool))
}

// toolInput renames ternly's arguments to Claude Code's (path → file_path).
func toolInput(tool string, args json.RawMessage) any {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return json.RawMessage(args)
	}
	if p, ok := m["path"]; ok && (tool == "read_file" || tool == "write_file" || tool == "edit_file") {
		m["file_path"] = p
		delete(m, "path")
	}
	return m
}

type hookResult struct {
	code   int
	stdout string
	stderr string
	err    error
}

func (r *Runtime) runHook(ctx context.Context, e hookEntry, input map[string]any) hookResult {
	expand := r.expander(e.plugin, e.dir)
	var argv []string
	if len(e.h.Args) > 0 {
		argv = append([]string{expand(e.h.Command)}, mapStrings(e.h.Args, expand)...)
	} else {
		argv = []string{"sh", "-c", expand(e.h.Command)}
	}
	ctx, cancel := context.WithTimeout(ctx, e.h.Timeout)
	defer cancel()
	cmd, err := r.Reg.Sandbox.PluginCmd(ctx, r.Root, tools.Confine{Dir: e.dir, Home: dataHome(r.Store, e.plugin), Workspace: e.scope.Workspace, Network: e.scope.Network,
		Env: map[string]string{"CLAUDE_PLUGIN_DATA": pluginData()}}, argv...)
	if err != nil {
		return hookResult{err: err}
	}
	if r.SessionID != nil {
		input["session_id"] = r.SessionID()
	}
	if r.Mode != nil {
		input["permission_mode"] = r.Mode()
	}
	input["cwd"] = r.Root
	b, _ := json.Marshal(input)
	cmd.Stdin = bytes.NewReader(b)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{&out, 64 << 10}, &limited{&errb, 16 << 10}
	err = cmd.Run()
	res := hookResult{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(interface{ ExitCode() int }); ok && err != nil {
		res.code = ee.ExitCode()
	} else if err != nil {
		res.err = err
	}
	return res
}

type limited struct {
	b *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// PreTool implements tools.HookRunner. A hook denies with exit code 2 or a
// "deny" decision; "allow" and "ask" are ignored: plugins never grant.
func (r *Runtime) PreTool(ctx context.Context, tool string, args json.RawMessage) (bool, string) {
	for _, e := range r.hooksFor("PreToolUse", tool) {
		cc := tool
		if n, ok := ternlyToCC[tool]; ok {
			cc = n
		}
		res := r.runHook(ctx, e, map[string]any{"hook_event_name": "PreToolUse", "tool_name": cc, "tool_input": toolInput(tool, args)})
		if res.err != nil {
			r.notify(fmt.Sprintf("plugin %s hook failed to run: %v", e.plugin, res.err))
			continue
		}
		if res.code == 2 {
			return true, e.plugin + ": " + orStr(strings.TrimSpace(res.stderr), "denied")
		}
		var out struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
			Specific struct {
				PermissionDecision string `json:"permissionDecision"`
				Reason             string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if s := strings.TrimSpace(res.stdout); strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &out) == nil {
			if out.Specific.PermissionDecision == "deny" || out.Decision == "block" {
				return true, e.plugin + ": " + orStr(out.Specific.Reason, orStr(out.Reason, "denied"))
			}
		}
	}
	return false, ""
}

// PostTool implements tools.HookRunner (hooks run; their output is not used).
func (r *Runtime) PostTool(ctx context.Context, tool string, args json.RawMessage, out string, failed bool) {
	for _, e := range r.hooksFor("PostToolUse", tool) {
		cc := tool
		if n, ok := ternlyToCC[tool]; ok {
			cc = n
		}
		r.runHook(ctx, e, map[string]any{"hook_event_name": "PostToolUse", "tool_name": cc, "tool_input": toolInput(tool, args), "tool_response": clip(out, 20000)})
	}
}

// PromptHook runs UserPromptSubmit hooks: exit 2 (or a "block" decision)
// blocks the prompt; plain output or additionalContext is returned as context.
func (r *Runtime) PromptHook(ctx context.Context, prompt string) (bool, string, string) {
	var add []string
	for _, e := range r.hooksFor("UserPromptSubmit", "") {
		res := r.runHook(ctx, e, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": prompt})
		if res.err != nil {
			r.notify(fmt.Sprintf("plugin %s hook failed to run: %v", e.plugin, res.err))
			continue
		}
		if res.code == 2 {
			return true, e.plugin + ": " + strings.TrimSpace(res.stderr), ""
		}
		s := strings.TrimSpace(res.stdout)
		var out struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
			Specific struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &out) == nil {
			if out.Decision == "block" {
				return true, e.plugin + ": " + out.Reason, ""
			}
			s = out.Specific.AdditionalContext
		}
		if s != "" && res.code == 0 {
			add = append(add, clip(s, 10000))
		}
	}
	return false, "", strings.Join(add, "\n\n")
}

// SessionStart runs SessionStart hooks and returns their combined output
// (context for the model, to be framed as untrusted by the caller).
func (r *Runtime) SessionStart(ctx context.Context, source string) string {
	var add []string
	for _, e := range r.hooksFor("SessionStart", "") {
		res := r.runHook(ctx, e, map[string]any{"hook_event_name": "SessionStart", "source": source})
		if res.err != nil || res.code != 0 {
			continue
		}
		s := strings.TrimSpace(res.stdout)
		var out struct {
			Specific struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &out) == nil {
			s = out.Specific.AdditionalContext
		}
		if s != "" {
			add = append(add, e.plugin+": "+clip(s, 10000))
		}
	}
	return strings.Join(add, "\n\n")
}

func (r *Runtime) hooksFor(event, tool string) []hookEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []hookEntry
	for _, e := range r.hooks {
		if e.h.Event == event && (event == "UserPromptSubmit" || event == "SessionStart" || matches(e.h.Matcher, tool)) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].plugin < out[j].plugin })
	return out
}

func (r *Runtime) notify(s string) {
	if r.Notify != nil {
		r.Notify(s)
	}
}

// Watch reloads when files change in installed plugins or in the standard
// skill, agent and rule directories (manual edits), every interval.
func (r *Runtime) Watch(ctx context.Context, interval time.Duration) {
	last := r.fingerprint()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if fp := r.fingerprint(); fp != last {
			last = fp
			for _, w := range r.ApplyAsync(ctx) {
				r.notify(w)
			}
			r.notify("plugins, skills or rules changed on disk — reloaded (applies from your next prompt)")
			if r.Changed != nil {
				r.Changed()
			}
		}
	}
}

// fingerprint summarises names, sizes and times of every watched file.
func (r *Runtime) fingerprint() string {
	var dirs []string
	if r.Store != nil {
		for _, p := range r.Store.List() {
			dirs = append(dirs, p.Dir)
		}
	}
	for _, d := range []string{".claude/skills", ".agents/skills", ".config/opencode/skills", ".claude/agents", ".gemini/agents", ".config/opencode/agents", ".config/opencode/agent", ".cursor/rules"} {
		dirs = append(dirs, filepath.Join(r.Home, d))
	}
	for _, d := range []string{".claude/skills", ".agents/skills", ".opencode/skills", ".claude/agents", ".gemini/agents", ".opencode/agents", ".opencode/agent", ".cursor/rules", ".cursorrules"} {
		dirs = append(dirs, filepath.Join(r.Root, d))
	}
	h := sha256.New()
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if fi, err := e.Info(); err == nil {
				fmt.Fprintf(h, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
			}
			return nil
		})
	}
	return string(h.Sum(nil))
}
