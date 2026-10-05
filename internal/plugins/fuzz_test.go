package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Plugin files are third-party input. Run: go test -fuzz FuzzLoadClaude
// ./internal/plugins (and the targets below).

// checkManifest is what any load must guarantee: a valid name; component
// files inside the plugin directory; hooks with something to run and a
// bounded timeout; MCP servers with a command.
func checkManifest(t *testing.T, dir string, m *Manifest) {
	if !validName.MatchString(m.Name) {
		t.Fatalf("invalid name %q accepted", m.Name)
	}
	for _, c := range m.Components {
		if c.Path != "" {
			if rel, err := filepath.Rel(dir, c.Path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("%s %s points outside the plugin: %s", c.Kind, c.Name, c.Path)
			}
		}
		if h := c.Hook; h != nil {
			if strings.TrimSpace(h.Command) == "" && len(h.Args) == 0 {
				t.Fatalf("hook %s has nothing to run", c.Name)
			}
			if h.Timeout <= 0 || h.Timeout > 60*time.Second {
				t.Fatalf("hook %s timeout %v", c.Name, h.Timeout)
			}
		}
		if s := c.MCP; s != nil && s.URL == "" && strings.TrimSpace(s.Command) == "" {
			t.Fatalf("MCP server %s has no command", c.Name)
		}
	}
}

func writeAll(t *testing.T, dir string, files map[string][]byte) {
	for p, b := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzLoadClaude(f *testing.F) {
	f.Add([]byte(`{"name":"p","skills":"./skills","commands":["./cmd/a.md","../../etc/passwd"],"hooks":"./h.json","mcpServers":{"db":{"command":"npx","args":["-y","x@1"]}}}`),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./check.sh","timeout":1e300}]}],"Stop":[{"hooks":[{"command":"x"}]}]}}`),
		[]byte(`{"mcpServers":{"a":{"url":"https://x"},"b":{"command":""},"c":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/s.js"]}}}`),
		[]byte("---\nname: s\ndescription: \"d\"\nallowed-tools: [Bash(*), Read]\nnested:\n  a: b\n---\nbody"))
	f.Add([]byte(`{"name":"../x"}`), []byte(`{"PreToolUse":[{"hooks":[{"type":"command"}]}]}`), []byte(`[]`), []byte("---\n---\n"))
	f.Fuzz(func(t *testing.T, plugin, hooks, mcp, skill []byte) {
		dir := t.TempDir()
		writeAll(t, dir, map[string][]byte{
			".claude-plugin/plugin.json": plugin,
			"hooks/hooks.json":           hooks,
			".mcp.json":                  mcp,
			"skills/s/SKILL.md":          skill,
			"agents/a.md":                skill,
			"commands/c.md":              skill,
		})
		m, err := Load(dir, "fuzz")
		if err != nil {
			return
		}
		checkManifest(t, dir, m)
	})
}

func FuzzLoadGemini(f *testing.F) {
	f.Add([]byte(`{"name":"g","version":"1","contextFileName":"../../../etc/hosts","mcpServers":{"s":{"command":"${extensionPath}/bin/s","args":["${workspacePath}"]}}}`),
		[]byte("description = \"d\"\nprompt = \"\"\"{{args}} !{ls}\"\"\"\n"),
		[]byte(`{"hooks":{"BeforeTool":[{"hooks":[{"type":"command","command":"x","timeout":-5}]}]}}`))
	f.Add([]byte(`{"name":"","contextFileName":["a","b"]}`), []byte("prompt = 1"), []byte(`null`))
	f.Fuzz(func(t *testing.T, ext, toml, hooks []byte) {
		dir := t.TempDir()
		writeAll(t, dir, map[string][]byte{
			"gemini-extension.json": ext,
			"commands/x/y.toml":     toml,
			"hooks/hooks.json":      hooks,
			"GEMINI.md":             []byte("context"),
		})
		m, err := Load(dir, "fuzz")
		if err != nil {
			return
		}
		checkManifest(t, dir, m)
	})
}

// FuzzFrontmatter: the YAML-subset reader behind skills, agents, commands
// and rules never panics, and a file without a closing fence is all body.
func FuzzFrontmatter(f *testing.F) {
	f.Add([]byte("---\nname: x\ntools: [a, \"b\"]\nlist:\n  - one\n  - two\n---\nbody"))
	f.Add([]byte("\uFEFF---\r\nk: v\r\n---\r\n"))
	f.Add([]byte("---\nk: [\n---"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p := filepath.Join(t.TempDir(), "x.md")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		fm, body, err := readFrontmatter("", p)
		if err != nil {
			return
		}
		if fm == nil {
			t.Fatal("nil frontmatter map")
		}
		if len(body) > len(b) {
			t.Fatalf("body (%d bytes) longer than the file (%d)", len(body), len(b))
		}
		_ = fm.str("name")
		_ = fm.list("tools")
	})
}
