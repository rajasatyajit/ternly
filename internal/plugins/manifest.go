// Package plugins adapts other harnesses' extension formats (Claude Code
// plugins, skills, subagents, hooks and MCP servers; Gemini CLI extensions;
// Cursor rules; OpenCode agents) into ternly's model, installs them pinned
// by commit, and runs their code confined (ADR 011, docs/compat.md).
//
// Every plugin is untrusted code: nothing executable runs before the user
// approves exactly what it will run, and a change to that requires approval
// again with a diff.
package plugins

import (
	"github.com/rajasatyajit/ternly/internal/rootfs"

	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind is what a component is.
type Kind string

const (
	KCommand Kind = "command" // a slash command (prompt template)
	KSkill   Kind = "skill"   // instructions loaded on demand
	KAgent   Kind = "agent"   // a subagent: system prompt + tool subset
	KHook    Kind = "hook"    // code run on an event (executable)
	KMCP     Kind = "mcp"     // an MCP server (executable)
	KRule    Kind = "rule"    // instructions: always, or on demand
	KContext Kind = "context" // always-loaded context file (GEMINI.md)
)

// Component is one normalised piece of a plugin.
type Component struct {
	Kind        Kind
	Name        string // as the user and model see it, namespaced: "plugin:name"
	Description string
	Path        string // the file it came from (bodies load lazily from here)
	Origin      string // "claude", "gemini", "cursor", "opencode", "codex"

	ArgHint string   // commands, skills
	Tools   []string // agents: allowed tools (Claude Code names, mapped later)
	Model   string   // agents: requested model (advisory; routing decides)
	Always  bool     // rules/context: always in the instructions
	Globs   []string // rules: files the rule is about
	Invoke  bool     // skills: the model may load it (false: user-only)

	Hook *Hook
	MCP  *MCPServer

	base string // the directory Path must stay inside ("" = not confined: the user's own files)
}

// confine drops components whose file isn't inside m.Dir once symlinks are
// resolved (a manifest path with "..", or a repository file linked to
// ~/.ssh), and marks the rest so their bodies are re-checked when read.
func (m *Manifest) confine() {
	base, err := filepath.EvalSymlinks(m.Dir)
	if err != nil {
		base = filepath.Clean(m.Dir)
	}
	kept := m.Components[:0]
	for _, c := range m.Components {
		if c.Path != "" {
			if !inside(base, c.Path) {
				m.skip(c.Name, "its file is outside the plugin's directory (a path or symlink that leaves it)")
				continue
			}
			c.base = base
		}
		kept = append(kept, c)
	}
	m.Components = kept
}

// inside reports whether path, with symlinks resolved, is within base.
func inside(base, path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Hook is an executable hook handler.
type Hook struct {
	Event   string   // ternly's event: PreToolUse, PostToolUse, UserPromptSubmit, SessionStart
	Matcher string   // tool-name matcher (Claude Code syntax)
	Command string   // shell form (run with sh -c), or
	Args    []string // exec form
	Timeout time.Duration
}

// MCPServer is an MCP server a plugin starts.
type MCPServer struct {
	Server  string // name inside the plugin
	Command string
	Args    []string
	Env     map[string]string
	URL     string // remote (not supported yet: skipped)
}

// Skip is a feature that was found but not loaded, and why.
type Skip struct{ What, Why string }

// Manifest is a normalised plugin.
type Manifest struct {
	Name, Version, Description string
	Author, Homepage, Repo     string // as declared (never used for trust)
	License                    string
	Format                     string // claude-plugin, gemini-extension, skills, …
	Dir                        string
	Components                 []Component
	Skipped                    []Skip

	fs *rootfs.Dir // what files are read through while loading: confined to Dir
}

func (m *Manifest) readFile(p string) ([]byte, error) { return m.fs.ReadFile(p) }
func (m *Manifest) exists(p string) bool              { return m.fs.Exists(p) }
func (m *Manifest) frontmatter(p string) (front, string, error) {
	b, err := m.fs.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	return parseFrontmatter(p, b)
}

// Executable components run code: hooks and MCP servers.
func (m *Manifest) Executable() []Component {
	var out []Component
	for _, c := range m.Components {
		if c.Kind == KHook || c.Kind == KMCP {
			out = append(out, c)
		}
	}
	return out
}

func (m *Manifest) skip(what, why string) { m.Skipped = append(m.Skipped, Skip{what, why}) }

// Load reads a plugin directory in whichever format it is: a Claude Code
// plugin (.claude-plugin/plugin.json or the standard layout), a Gemini CLI
// extension (gemini-extension.json), or a bare skills/agents/rules tree.
func Load(dir, fallbackName string) (*Manifest, error) {
	fs, err := rootfs.Open(dir)
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	m := &Manifest{Dir: dir, Name: fallbackName, fs: fs}
	switch {
	case m.exists("gemini-extension.json"):
		if err := loadGemini(m); err != nil {
			return nil, err
		}
	default:
		if err := loadClaude(m); err != nil {
			return nil, err
		}
	}
	if m.Name == "" {
		m.Name = filepath.Base(dir)
	}
	if !validName.MatchString(m.Name) {
		return nil, fmt.Errorf("invalid plugin name %q", m.Name)
	}
	m.confine()
	sort.SliceStable(m.Components, func(i, j int) bool { return m.Components[i].Name < m.Components[j].Name })
	return m, nil
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ─────────────────────────── Claude Code ───────────────────────────

type claudeManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Author      json.RawMessage `json:"author"`
	Homepage    string          `json:"homepage"`
	Repository  json.RawMessage `json:"repository"`
	License     string          `json:"license"`
	Skills      json.RawMessage `json:"skills"`
	Commands    json.RawMessage `json:"commands"`
	Agents      json.RawMessage `json:"agents"`
	Hooks       json.RawMessage `json:"hooks"`
	MCPServers  json.RawMessage `json:"mcpServers"`
	LSPServers  json.RawMessage `json:"lspServers"`
	UserConfig  json.RawMessage `json:"userConfig"`
	Deps        json.RawMessage `json:"dependencies"`
	Channels    json.RawMessage `json:"channels"`
	Styles      json.RawMessage `json:"outputStyles"`
	Workflows   json.RawMessage `json:"workflows"`
	Settings    json.RawMessage `json:"settings"`
}

func loadClaude(m *Manifest) error {
	var pm claudeManifest
	m.Format = "claude-plugin"
	if b, err := m.readFile(filepath.Join(m.Dir, ".claude-plugin", "plugin.json")); err == nil {
		if err := json.Unmarshal(b, &pm); err != nil {
			return fmt.Errorf("plugin.json: %w", err)
		}
		m.Name, m.Version, m.Description, m.Homepage, m.License = orStr(pm.Name, m.Name), pm.Version, pm.Description, pm.Homepage, pm.License
		m.Author, m.Repo = authorName(pm.Author), jsonString(pm.Repository)
	} else if !m.exists(filepath.Join(m.Dir, "skills")) && !m.exists(filepath.Join(m.Dir, "SKILL.md")) && !m.exists(filepath.Join(m.Dir, "commands")) && !m.exists(filepath.Join(m.Dir, "agents")) {
		return fmt.Errorf("%s: no .claude-plugin/plugin.json, gemini-extension.json, skills/, commands/ or agents/", m.Dir)
	}
	for _, f := range []struct {
		raw  json.RawMessage
		what string
	}{{pm.LSPServers, "lspServers (language servers)"}, {pm.UserConfig, "userConfig (settings prompted at install)"}, {pm.Deps, "dependencies (other plugins)"},
		{pm.Channels, "channels"}, {pm.Styles, "outputStyles"}, {pm.Workflows, "workflows"}, {pm.Settings, "settings (agent, subagentStatusLine)"}} {
		if len(f.raw) > 0 && string(f.raw) != "null" {
			m.skip(f.what, "no ternly equivalent")
		}
	}
	for _, d := range []string{".lsp.json", "output-styles", "workflows", "themes", "monitors", "bin"} {
		if m.exists(filepath.Join(m.Dir, d)) {
			m.skip(d, "no ternly equivalent")
		}
	}

	// skills: skills/<name>/SKILL.md (+ manifest paths, which add), or a root SKILL.md
	skillDirs := []string{filepath.Join(m.Dir, "skills")}
	skillDirs = append(skillDirs, m.paths(pm.Skills)...)
	for _, d := range skillDirs {
		loadSkills(m, d, "claude")
	}
	if !m.exists(filepath.Join(m.Dir, "skills")) && m.exists(filepath.Join(m.Dir, "SKILL.md")) {
		loadSkill(m, filepath.Join(m.Dir, "SKILL.md"), "claude")
	}
	// commands: manifest paths replace the default scan
	if cmds := m.paths(pm.Commands); len(cmds) > 0 {
		for _, p := range cmds {
			loadCommands(m, p)
		}
	} else if len(pm.Commands) > 0 && string(pm.Commands) != "null" {
		m.skip("commands (inline object map)", "only file and directory paths are supported")
	} else {
		loadCommands(m, filepath.Join(m.Dir, "commands"))
	}
	if ags := m.paths(pm.Agents); len(ags) > 0 {
		for _, p := range ags {
			loadAgents(m, p, "claude")
		}
	} else {
		loadAgents(m, filepath.Join(m.Dir, "agents"), "claude")
	}
	// hooks: hooks/hooks.json merged with the manifest's (path or inline)
	if b, err := m.readFile(filepath.Join(m.Dir, "hooks", "hooks.json")); err == nil {
		loadHooks(m, b, true, "claude")
	}
	if len(pm.Hooks) > 0 && string(pm.Hooks) != "null" {
		if p := m.paths(pm.Hooks); len(p) > 0 {
			for _, f := range p {
				if b, err := m.readFile(f); err == nil {
					loadHooks(m, b, true, "claude")
				}
			}
		} else {
			loadHooks(m, pm.Hooks, false, "claude")
		}
	}
	// MCP: .mcp.json merged with the manifest's
	if b, err := m.readFile(filepath.Join(m.Dir, ".mcp.json")); err == nil {
		loadMCP(m, b, true, "claude", "${CLAUDE_PLUGIN_ROOT}")
	}
	if len(pm.MCPServers) > 0 && string(pm.MCPServers) != "null" {
		if p := m.paths(pm.MCPServers); len(p) > 0 {
			for _, f := range p {
				if b, err := m.readFile(f); err == nil {
					loadMCP(m, b, true, "claude", "${CLAUDE_PLUGIN_ROOT}")
				}
			}
		} else {
			loadMCP(m, pm.MCPServers, false, "claude", "${CLAUDE_PLUGIN_ROOT}")
		}
	}
	return nil
}

// paths resolves manifest component paths: "./…" strings (or arrays), which
// must stay inside the plugin. Bundles and URLs are not followed.
func (m *Manifest) paths(raw json.RawMessage) []string {
	var one string
	var many []string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		many = []string{one}
	} else {
		_ = json.Unmarshal(raw, &many)
	}
	var out []string
	for _, p := range many {
		if !strings.HasPrefix(p, "./") && p != "." {
			if strings.HasPrefix(p, "https://") || strings.HasSuffix(p, ".mcpb") || strings.HasSuffix(p, ".dxt") {
				m.skip(p, "bundles and remote bundles aren't supported")
			}
			continue
		}
		full := filepath.Join(m.Dir, filepath.Clean(p))
		if rel, err := filepath.Rel(m.Dir, full); err != nil || strings.HasPrefix(rel, "..") {
			m.skip(p, "path leaves the plugin directory")
			continue
		}
		out = append(out, full)
	}
	return out
}

func loadSkills(m *Manifest, dir, origin string) {
	entries, err := m.fs.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if f := filepath.Join(dir, e.Name(), "SKILL.md"); e.IsDir() && m.exists(f) {
			loadSkill(m, f, origin)
		}
	}
}

func loadSkill(m *Manifest, path, origin string) {
	fm, body, err := m.frontmatter(path)
	if err != nil {
		m.skip(path, err.Error())
		return
	}
	name := orStr(fm.str("name"), filepath.Base(filepath.Dir(path)))
	desc := fm.str("description")
	if w := fm.str("when_to_use"); w != "" {
		desc = strings.TrimSpace(desc + " " + w)
	}
	if desc == "" {
		desc = firstLine(body)
	}
	c := Component{Kind: KSkill, Name: m.Name + ":" + name, Description: clip(desc, 1024), Path: path, Origin: origin,
		ArgHint: fm.str("argument-hint"), Invoke: fm.str("disable-model-invocation") != "true"}
	for _, k := range []string{"allowed-tools", "hooks", "context", "agent", "model", "effort", "shell"} {
		if fm.has(k) {
			why := "not honoured"
			switch k {
			case "allowed-tools":
				why = "not honoured: a skill can't grant permissions; ternly's permission policy decides"
			case "hooks":
				why = "skill-scoped hooks aren't loaded"
			case "model", "effort":
				why = "routing chooses the model"
			}
			m.skip(c.Name+" "+k, why)
		}
	}
	m.Components = append(m.Components, c)
}

func loadCommands(m *Manifest, dir string) {
	_ = m.fs.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		fm, body, err := m.frontmatter(p)
		if err != nil {
			m.skip(p, err.Error())
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(rel), ".md"), "/", ":")
		m.Components = append(m.Components, Component{Kind: KCommand, Name: m.Name + ":" + name, Description: orStr(fm.str("description"), firstLine(body)),
			Path: p, Origin: "claude", ArgHint: fm.str("argument-hint")})
		if fm.has("allowed-tools") {
			m.skip(m.Name+":"+name+" allowed-tools", "not honoured: a command can't grant permissions")
		}
		return nil
	})
}

// loadAgents reads Claude Code / Gemini / OpenCode agent markdown files.
func loadAgents(m *Manifest, dir, origin string) {
	_ = m.fs.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		fm, body, err := m.frontmatter(p)
		if err != nil {
			m.skip(p, err.Error())
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := fm.str("name")
		if name == "" || origin == "opencode" {
			name = strings.TrimSuffix(filepath.Base(rel), ".md") // OpenCode: the file name is the agent
		}
		if origin == "claude" { // plugin agents: subfolders join the scoped name
			if d := filepath.ToSlash(filepath.Dir(rel)); d != "." {
				name = strings.ReplaceAll(d, "/", ":") + ":" + name
			}
		}
		desc := fm.str("description")
		if desc == "" || strings.TrimSpace(body) == "" {
			m.skip(p, "agent without a description or body")
			return nil
		}
		if origin == "opencode" && fm.str("mode") == "primary" {
			m.skip(m.Name+":"+name, "OpenCode primary agents replace the main agent; only subagents are supported")
			return nil
		}
		c := Component{Kind: KAgent, Name: m.Name + ":" + name, Description: clip(desc, 1024), Path: p, Origin: origin,
			Tools: fm.list("tools"), Model: fm.str("model")}
		for _, k := range []string{"permissionMode", "permission", "hooks", "mcpServers", "disallowedTools", "isolation", "memory", "skills"} {
			if fm.has(k) {
				why := "not honoured"
				if k == "permissionMode" || k == "permission" {
					why = "not honoured: an agent can't change permissions; ternly's policy applies"
				}
				m.skip(c.Name+" "+k, why)
			}
		}
		m.Components = append(m.Components, c)
		return nil
	})
}

// Claude Code event → ternly event. Gemini's BeforeTool/AfterTool map too.
var hookEvents = map[string]string{
	"PreToolUse": "PreToolUse", "PostToolUse": "PostToolUse", "UserPromptSubmit": "UserPromptSubmit", "SessionStart": "SessionStart",
	"BeforeTool": "PreToolUse", "AfterTool": "PostToolUse",
}

func loadHooks(m *Manifest, raw []byte, wrapped bool, origin string) {
	var events map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Timeout float64  `json:"timeout"`
			Async   bool     `json:"async"`
		} `json:"hooks"`
	}
	if wrapped {
		var w struct {
			Hooks json.RawMessage `json:"hooks"`
		}
		if json.Unmarshal(raw, &w) != nil {
			m.skip("hooks", "unreadable hooks file")
			return
		}
		raw = w.Hooks
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		m.skip("hooks", "unreadable hooks: "+err.Error())
		return
	}
	names := make([]string, 0, len(events))
	for ev := range events {
		names = append(names, ev)
	}
	sort.Strings(names)
	n := 0
	for _, ev := range names {
		tev, ok := hookEvents[ev]
		if !ok {
			m.skip("hook "+ev, "event not supported (ternly runs PreToolUse, PostToolUse, UserPromptSubmit and SessionStart)")
			continue
		}
		for _, g := range events[ev] {
			for _, h := range g.Hooks {
				if h.Type != "command" && h.Type != "" {
					m.skip(fmt.Sprintf("hook %s (%s)", ev, h.Type), "only command hooks are supported")
					continue
				}
				if strings.TrimSpace(h.Command) == "" && len(h.Args) == 0 {
					m.skip("hook "+ev, "no command to run")
					continue
				}
				if h.Async {
					m.skip("hook "+ev+" async", "runs synchronously in ternly")
				}
				to := time.Duration(h.Timeout * float64(time.Second))
				if origin == "gemini" { // Gemini's timeout is in milliseconds
					to = time.Duration(h.Timeout * float64(time.Millisecond))
				}
				if to <= 0 || to > 60*time.Second {
					to = 60 * time.Second
				}
				n++
				m.Components = append(m.Components, Component{Kind: KHook, Name: fmt.Sprintf("%s:hook-%d", m.Name, n), Origin: origin,
					Description: tev + " " + orStr(g.Matcher, "*"),
					Hook:        &Hook{Event: tev, Matcher: g.Matcher, Command: h.Command, Args: h.Args, Timeout: to}})
			}
		}
	}
}

func loadMCP(m *Manifest, raw []byte, wrapped bool, origin, rootVar string) {
	type server struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		HTTPURL string            `json:"httpUrl"`
	}
	var servers map[string]server
	if wrapped {
		var w struct {
			M map[string]server `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			m.skip("mcpServers", "unreadable: "+err.Error())
			return
		}
		servers = w.M
	} else if err := json.Unmarshal(raw, &servers); err != nil {
		m.skip("mcpServers", "unreadable: "+err.Error())
		return
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := servers[n]
		c := Component{Kind: KMCP, Name: m.Name + ":" + n, Origin: origin, Description: "MCP server " + n}
		switch {
		case s.URL != "" || s.HTTPURL != "" || s.Type == "http" || s.Type == "sse" || s.Type == "ws":
			m.skip("MCP server "+n, "remote (http/sse/ws) MCP servers aren't supported yet")
			continue
		case s.Command == "":
			m.skip("MCP server "+n, "no command")
			continue
		}
		c.MCP = &MCPServer{Server: n, Command: s.Command, Args: s.Args, Env: s.Env}
		m.Components = append(m.Components, c)
	}
}

// ─────────────────────────── Gemini CLI ───────────────────────────

func loadGemini(m *Manifest) error {
	m.Format = "gemini-extension"
	var gm struct {
		Name            string          `json:"name"`
		Version         string          `json:"version"`
		Description     string          `json:"description"`
		MCPServers      json.RawMessage `json:"mcpServers"`
		ContextFileName string          `json:"contextFileName"`
		ExcludeTools    []string        `json:"excludeTools"`
		Settings        json.RawMessage `json:"settings"`
		Themes          json.RawMessage `json:"themes"`
		Plan            json.RawMessage `json:"plan"`
		MigratedTo      string          `json:"migratedTo"`
	}
	b, err := m.readFile(filepath.Join(m.Dir, "gemini-extension.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &gm); err != nil {
		return fmt.Errorf("gemini-extension.json: %w", err)
	}
	m.Name, m.Version, m.Description = orStr(gm.Name, m.Name), gm.Version, gm.Description
	if len(gm.ExcludeTools) > 0 {
		m.skip("excludeTools", "ternly's tool set isn't trimmed per extension")
	}
	for _, f := range []struct {
		raw  json.RawMessage
		what string
	}{{gm.Settings, "settings (values prompted at install)"}, {gm.Themes, "themes"}, {gm.Plan, "plan"}} {
		if len(f.raw) > 0 && string(f.raw) != "null" {
			m.skip(f.what, "no ternly equivalent")
		}
	}
	if gm.MigratedTo != "" {
		m.skip("migratedTo", "this extension moved to "+gm.MigratedTo)
	}
	ctx := orStr(gm.ContextFileName, "GEMINI.md")
	if f := filepath.Join(m.Dir, filepath.Clean(ctx)); m.exists(f) {
		m.Components = append(m.Components, Component{Kind: KContext, Name: m.Name + ":context", Description: "context file " + ctx, Path: f, Origin: "gemini", Always: true})
	}
	_ = m.fs.WalkDir(filepath.Join(m.Dir, "commands"), func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".toml") {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(m.Dir, "commands"), p)
		name := strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(rel), ".toml"), "/", ":")
		m.Components = append(m.Components, Component{Kind: KCommand, Name: m.Name + ":" + name, Description: m.tomlDescription(p), Path: p, Origin: "gemini"})
		return nil
	})
	loadSkills(m, filepath.Join(m.Dir, "skills"), "gemini")
	loadAgents(m, filepath.Join(m.Dir, "agents"), "gemini")
	if b, err := m.readFile(filepath.Join(m.Dir, "hooks", "hooks.json")); err == nil {
		loadHooks(m, b, true, "gemini")
	}
	if len(gm.MCPServers) > 0 && string(gm.MCPServers) != "null" {
		loadMCP(m, gm.MCPServers, false, "gemini", "${extensionPath}")
	}
	if m.exists(filepath.Join(m.Dir, "policies")) {
		m.skip("policies", "Gemini policy files aren't loaded; ternly's permission policy applies")
	}
	return nil
}

func (m *Manifest) tomlDescription(p string) string {
	b, err := m.readFile(p)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "description" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return "custom command"
}

// ─────────────────────────── frontmatter ───────────────────────────

// front is a parsed YAML frontmatter block: the subset these formats use
// (scalars, inline [a, b] and block "- a" lists, and nested maps, which are
// kept as present but not interpreted).
type front map[string]any

func (f front) has(k string) bool { _, ok := f[k]; return ok }
func (f front) str(k string) string {
	s, _ := f[k].(string)
	return s
}

// list reads "a, b", "[a, b]" or a block list.
func (f front) list(k string) []string {
	switch v := f[k].(type) {
	case []string:
		return v
	case string:
		var out []string
		for _, p := range strings.Split(strings.Trim(v, "[]"), ",") {
			if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// readFrontmatter reads path confined to base (the directory that owns it;
// "" = the file's own directory) and parses it.
func readFrontmatter(base, path string) (front, string, error) {
	if base == "" {
		base = filepath.Dir(path)
	}
	b, err := rootfs.ReadFile(base, path)
	if err != nil {
		return nil, "", err
	}
	return parseFrontmatter(path, b)
}

// parseFrontmatter splits a YAML-subset header from the body.
func parseFrontmatter(path string, b []byte) (front, string, error) {
	if len(b) > 1<<20 {
		return nil, "", fmt.Errorf("%s is over 1 MB", filepath.Base(path))
	}
	s := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\uFEFF")
	fm := front{}
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return fm, s, nil
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return fm, s, nil
	}
	body = strings.TrimPrefix(body, "\n")
	var key string
	sc := bufio.NewScanner(strings.NewReader(head))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if indent := len(line) - len(strings.TrimLeft(line, " ")); indent > 0 && key != "" {
			if item, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
				l, _ := fm[key].([]string)
				fm[key] = append(l, strings.Trim(item, `"'`))
			} else if _, isStr := fm[key].(string); !isStr || fm[key] == "" {
				fm[key] = map[string]any{} // nested map: present, not interpreted
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			fm[key] = front{key: v}.list(key)
			continue
		}
		fm[key] = strings.Trim(v, `"'`)
	}
	return fm, body, nil
}

// ─────────────────────────── helpers ───────────────────────────

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func firstLine(s string) string {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(l, "#")); l != "" {
			return clip(l, 120)
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func authorName(raw json.RawMessage) string {
	var a struct{ Name string }
	if json.Unmarshal(raw, &a) == nil && a.Name != "" {
		return a.Name
	}
	return jsonString(raw)
}

func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	if s == "" {
		var r struct{ URL string }
		_ = json.Unmarshal(raw, &r)
		s = r.URL
	}
	return s
}
