package plugins

import (
	"github.com/rajasatyajit/ternly/internal/rootfs"

	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Local returns what is already on disk in other harnesses' standard
// locations, as two pseudo-plugins: "user" (your home) and "project" (the
// workspace). Only prompt text is taken from them (skills, agents, rules):
// executable parts never start from these locations.
func Local(root, home string) []*Manifest {
	uf, uerr := rootfs.Open(home) // personal files: links may lead anywhere inside home
	pf, perr := rootfs.Open(root) // repository files: nothing may lead outside the workspace
	if uerr != nil || perr != nil {
		return nil
	}
	defer uf.Close()
	defer pf.Close()
	user := &Manifest{Name: "user", Format: "local", Dir: home, fs: uf}
	for _, d := range []string{".claude/skills", ".agents/skills", ".config/opencode/skills"} {
		loadSkills(user, filepath.Join(home, d), "claude")
	}
	loadAgents(user, filepath.Join(home, ".claude/agents"), "claude")
	loadAgents(user, filepath.Join(home, ".gemini/agents"), "gemini")
	for _, d := range []string{".config/opencode/agents", ".config/opencode/agent"} {
		loadAgents(user, filepath.Join(home, d), "opencode")
	}
	loadRules(user, filepath.Join(home, ".cursor/rules"))

	proj := &Manifest{Name: "project", Format: "local", Dir: root, fs: pf}
	for _, d := range []string{".claude/skills", ".agents/skills", ".opencode/skills"} {
		loadSkills(proj, filepath.Join(root, d), "claude")
	}
	loadAgents(proj, filepath.Join(root, ".claude/agents"), "claude")
	loadAgents(proj, filepath.Join(root, ".gemini/agents"), "gemini")
	for _, d := range []string{".opencode/agents", ".opencode/agent"} {
		loadAgents(proj, filepath.Join(root, d), "opencode")
	}
	loadRules(proj, filepath.Join(root, ".cursor/rules"))
	if b, err := proj.readFile(".cursorrules"); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		proj.Components = append(proj.Components, Component{Kind: KRule, Name: "project:cursorrules", Description: "legacy .cursorrules", Path: filepath.Join(root, ".cursorrules"), Origin: "cursor", Always: true})
	}
	for _, f := range []string{".claude/settings.json", ".gemini/settings.json"} {
		if b, err := proj.readFile(f); err == nil && strings.Contains(string(b), `"hooks"`) {
			proj.skip(f+" hooks", "repository hooks never run automatically; package them as a plugin and /plugin add it to review and approve them")
		}
	}

	user.confine() // records each file's base, so bodies are read back confined
	proj.confine() // repository files: a skill linked to ~/.ssh stays out
	var out []*Manifest
	for _, m := range []*Manifest{user, proj} {
		for i := range m.Components { // personal and project items keep their bare names, as in Claude Code
			m.Components[i].Name = strings.TrimPrefix(m.Components[i].Name, m.Name+":")
		}
		sort.SliceStable(m.Components, func(i, j int) bool { return m.Components[i].Name < m.Components[j].Name })
		if len(m.Components) > 0 || len(m.Skipped) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// loadRules reads Cursor rules (.cursor/rules/**/*.mdc). alwaysApply rules
// join the instructions; others are offered to the model by description
// (globs are shown as what the rule is about).
func loadRules(m *Manifest, dir string) {
	_ = m.fs.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".mdc") {
			return nil
		}
		fm, body, err := m.frontmatter(p)
		if err != nil || strings.TrimSpace(body) == "" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := "rule:" + strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(rel), ".mdc"), "/", ":")
		c := Component{Kind: KRule, Name: m.Name + ":" + name, Path: p, Origin: "cursor", Always: fm.str("alwaysApply") == "true",
			Description: fm.str("description"), Globs: fm.list("globs")}
		switch {
		case c.Always:
		case c.Description == "" && len(c.Globs) > 0:
			c.Description = "rules for " + strings.Join(c.Globs, ", ")
		case c.Description == "":
			c.Description = firstLine(body) + " (manual rule)"
		}
		m.Components = append(m.Components, c)
		return nil
	})
}

// Found is a plugin installed for another harness, which can be imported.
type Found struct {
	Name, Dir, Harness, Version string
}

// FoundElsewhere lists plugins installed for Claude Code (its plugin cache) and
// Gemini CLI (its extensions directory). Importing one goes through the same
// review and approval as installing it.
func FoundElsewhere(home string) []Found {
	var out []Found
	cache := filepath.Join(home, ".claude", "plugins", "cache")
	mkts, _ := os.ReadDir(cache)
	for _, mk := range mkts {
		plugs, _ := os.ReadDir(filepath.Join(cache, mk.Name()))
		for _, pl := range plugs {
			vers, _ := os.ReadDir(filepath.Join(cache, mk.Name(), pl.Name()))
			var newest string
			for _, v := range vers {
				if v.IsDir() && !exists(filepath.Join(cache, mk.Name(), pl.Name(), v.Name(), ".orphaned_at")) && v.Name() > newest {
					newest = v.Name()
				}
			}
			if newest != "" {
				out = append(out, Found{Name: pl.Name(), Dir: filepath.Join(cache, mk.Name(), pl.Name(), newest), Harness: "Claude Code (" + mk.Name() + ")", Version: newest})
			}
		}
	}
	exts, _ := os.ReadDir(filepath.Join(home, ".gemini", "extensions"))
	for _, e := range exts {
		if d := filepath.Join(home, ".gemini", "extensions", e.Name()); exists(filepath.Join(d, "gemini-extension.json")) {
			out = append(out, Found{Name: e.Name(), Dir: d, Harness: "Gemini CLI"})
		}
	}
	return out
}

// CodexMCP reads MCP servers from Codex's config.toml ([mcp_servers.NAME]
// with command, args, env), as a plugin manifest to review and approve.
func CodexMCP(path string) *Manifest {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m := &Manifest{Name: "codex-mcp", Format: "codex-config", Dir: filepath.Dir(path)}
	servers := map[string]*MCPServer{}
	var cur *MCPServer
	inEnv := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			sec := strings.Trim(line, "[] ")
			name, rest, _ := strings.Cut(strings.TrimPrefix(sec, "mcp_servers."), ".")
			cur, inEnv = nil, false
			if strings.HasPrefix(sec, "mcp_servers.") {
				name = strings.Trim(name, `"`)
				if servers[name] == nil {
					servers[name] = &MCPServer{Server: name, Env: map[string]string{}}
				}
				cur, inEnv = servers[name], rest == "env"
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || cur == nil {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case inEnv:
			cur.Env[k] = unquote(v)
		case k == "command":
			cur.Command = unquote(v)
		case k == "args":
			for _, a := range strings.Split(strings.Trim(v, "[]"), ",") {
				if a = strings.TrimSpace(a); a != "" {
					cur.Args = append(cur.Args, unquote(a))
				}
			}
		case k == "url":
			cur.URL = unquote(v)
		}
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := servers[n]
		switch {
		case s.URL != "":
			m.skip("MCP server "+n, "remote (http) MCP servers aren't supported yet")
		case s.Command != "":
			m.Components = append(m.Components, Component{Kind: KMCP, Name: m.Name + ":" + n, Description: "MCP server " + n, Origin: "codex", MCP: s})
		}
	}
	return m
}

func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `'"`)
}
