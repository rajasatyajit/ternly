package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/tools"
)

// TestLiveInstall (TERNLY_PLUGIN_LIVE=1, network): confirmation → usable
// time and tokens added, for a skills plugin from Anthropic's skills
// marketplace and for an MCP server from the MCP registry (npx, confined).
func TestLiveInstall(t *testing.T) {
	if os.Getenv("TERNLY_PLUGIN_LIVE") == "" {
		t.Skip("TERNLY_PLUGIN_LIVE not set")
	}
	ctx := context.Background()
	ws := t.TempDir()
	reg, err := tools.NewRegistry(ws, tools.NewPolicy("ask", nil), tools.NewSandbox(true, false, nil), tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := OpenStore(t.TempDir())
	rt := &Runtime{Store: st, Reg: reg, Root: reg.Root, Home: t.TempDir()}
	defer rt.Close()

	t0 := time.Now()
	if _, err := st.AddMarketplace(ctx, "anthropics/skills"); err != nil {
		t.Fatal(err)
	}
	src, _, err := st.Resolve("document-skills", "anthropic-agent-skills")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Fetch(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	fetch := time.Since(t0)
	t.Logf("review for document-skills:\n%s", Review(p))
	t1 := time.Now() // the user approves here
	if _, err := st.Accept(p); err != nil {
		t.Fatal(err)
	}
	rt.Apply(ctx)
	reg.Commit()
	usable := time.Since(t1)
	tok := 0
	for k, v := range rt.Tokens() {
		if strings.HasPrefix(k, "document-skills:") {
			tok += v
		}
	}
	t.Logf("tokens: %v", rt.Tokens())
	t.Logf("document-skills: fetch+review %v; confirmation → usable %v; %d skills; ~%d tokens per request", fetch.Round(time.Millisecond), usable.Round(time.Millisecond), len(p.Manifest.Components), tok)

	// An MCP server from the registry, as a suggestion would install it.
	gen := filepath.Join(t.TempDir(), "time")
	_ = os.MkdirAll(filepath.Join(gen, ".claude-plugin"), 0o700)
	_ = os.WriteFile(filepath.Join(gen, ".claude-plugin", "plugin.json"), []byte(`{"name":"time","version":"1.0.2"}`), 0o600)
	_ = os.WriteFile(filepath.Join(gen, ".mcp.json"), []byte(`{"mcpServers":{"time":{"command":"npx","args":["-y","@infoinlet/mcp-time@0.1.1"]}}}`), 0o600)
	p2, err := st.Fetch(ctx, Source{Kind: "local", URL: gen})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("review for time:\n%s", Review(p2))
	t2 := time.Now()
	if _, err := st.Accept(p2); err != nil {
		t.Fatal(err)
	}
	ws2 := rt.Apply(ctx) // waits for the server: initialize + tools/list
	reg.Commit()
	var names []string
	for _, sp := range reg.Specs() {
		if strings.HasPrefix(sp.Name, "mcp__plugin_time") {
			names = append(names, sp.Name)
		}
	}
	t.Logf("time MCP server: confirmation → usable %v (npx download + start, cold); tools %v; ~%d tokens per request; warnings %v", time.Since(t2).Round(time.Millisecond), names, rt.Tokens()["time:time"], ws2)
	rt.Close()
	rt.mcp = nil
	t3 := time.Now()
	ws3 := rt.Apply(ctx)
	reg.Commit()
	names = nil
	for _, sp := range reg.Specs() {
		if strings.HasPrefix(sp.Name, "mcp__plugin_time") {
			names = append(names, sp.Name)
		}
	}
	t.Logf("time MCP server, warm npm cache: start → usable %v; tools %v; ~%d tokens; warnings %v", time.Since(t3).Round(time.Millisecond), names, rt.Tokens()["time:time"], ws3)
}
