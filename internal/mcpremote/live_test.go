package mcpremote

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Live servers through the whole path (manager, netguard, client, OAuth
// discovery). TERNLY_MCP_LIVE=1. Linear and Notion: discovery only — start-up
// never registers a client or opens a browser.
func TestLive(t *testing.T) {
	if os.Getenv("TERNLY_MCP_LIVE") != "1" {
		t.Skip("TERNLY_MCP_LIVE=1 runs against live servers")
	}
	reg, err := tools.NewRegistry(t.TempDir(), tools.NewPolicy("yolo", nil), nil, tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	m := &Manager{Reg: reg, Dir: t.TempDir(), Version: "live-test", Store: &memStore{}, Open: func(string) error { opened++; return nil }}
	cfg := func(name, url string) tools.RemoteConfig {
		return tools.RemoteConfig{Name: name, MCPServerConfig: tools.MCPServerConfig{URL: url}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t0 := time.Now()
	notes := m.Start(ctx, []tools.RemoteConfig{
		cfg("deepwiki", "https://mcp.deepwiki.com/mcp"), cfg("context7", "https://mcp.context7.com/mcp"),
		cfg("linear", "https://mcp.linear.app/mcp"), cfg("notion", "https://mcp.notion.com/mcp"),
	})
	t.Logf("start (4 servers, concurrent): %s", time.Since(t0).Round(time.Millisecond))
	for _, n := range notes {
		t.Log("note:", n)
	}
	reg.Commit()
	for _, st := range m.Statuses() {
		t.Logf("%-9s era=%-10s tools=%-2d auth=%-11s hosts=%v pending=%v", st.Name, st.Era, st.Tools, st.Auth, st.Hosts, st.Pending)
		switch st.Name {
		case "deepwiki", "context7":
			if st.Err != "" || st.Tools == 0 {
				t.Errorf("%s: %s", st.Name, st.Err)
			}
		case "linear", "notion":
			if st.Auth != "needs login" {
				t.Errorf("%s: want needs login, got %+v", st.Name, st)
			}
		}
	}
	if opened != 0 {
		t.Error("start-up opened a browser")
	}
	for _, c := range []struct{ tool, args string }{
		{"mcp__deepwiki__read_wiki_structure", `{"repoName":"golang/go"}`},
		{"mcp__context7__resolve-library-id", `{"libraryName":"cobra","query":"cobra cli"}`},
	} {
		t1 := time.Now()
		res := reg.Call(ctx, llm.ToolCall{ID: "1", Name: c.tool, Args: c.args})
		t.Logf("%s: %s, %d bytes, err=%v: %.120q", c.tool, time.Since(t1).Round(time.Millisecond), len(res.Out), res.IsErr, tools.Unframe(res.Out))
		if res.IsErr || !strings.Contains(res.Out, "UNTRUSTED") {
			t.Errorf("%s: %s", c.tool, res.Out)
		}
	}
	m.Close()
}
