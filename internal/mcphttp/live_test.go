package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive (TERNLY_MCP_LIVE=1, network): real public servers, no auth —
// DeepWiki speaks the 2025 revision (the fallback path), Context7 the
// 2026-07-28 one.
func TestLive(t *testing.T) {
	if os.Getenv("TERNLY_MCP_LIVE") == "" {
		t.Skip("TERNLY_MCP_LIVE not set")
	}
	for _, s := range []struct {
		url, tool string
		args      map[string]any
		era       Era
	}{
		{"https://mcp.deepwiki.com/mcp", "read_wiki_structure", map[string]any{"repoName": "golang/go"}, EraLegacy},
		{"https://mcp.context7.com/mcp", "resolve-library-id", map[string]any{"libraryName": "bubbletea", "query": "terminal UI framework for Go"}, EraModern},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		c := &Client{URL: s.url, HTTP: &http.Client{Timeout: 60 * time.Second}, Name: "ternly", Version: "test"}
		t0 := time.Now()
		if err := c.Connect(ctx); err != nil {
			t.Errorf("%s: connect: %v", s.url, err)
			cancel()
			continue
		}
		tConnect := time.Since(t0)
		res, err := c.Call(ctx, "tools/list", map[string]any{})
		var tl struct {
			Tools []struct{ Name string } `json:"tools"`
		}
		_ = json.Unmarshal(res, &tl)
		var names []string
		for _, x := range tl.Tools {
			names = append(names, x.Name)
		}
		t1 := time.Now()
		out, cerr := c.Call(ctx, "tools/call", map[string]any{"name": s.tool, "arguments": s.args})
		t.Logf("%s: era %s (want %s), connect %v; tools %v (%v); %s → %d bytes in %v, err %v: %.160s",
			s.url, c.Era(), s.era, tConnect.Round(time.Millisecond), names, err, s.tool, len(out), time.Since(t1).Round(time.Millisecond), cerr, strings.ReplaceAll(string(out), "\n", " "))
		if c.Era() != s.era || err != nil || cerr != nil || len(out) == 0 {
			t.Errorf("%s failed", s.url)
		}
		c.Close()
		cancel()
	}
}
