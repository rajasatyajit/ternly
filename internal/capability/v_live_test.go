package capability

import (
	"context"
	"os"
	"testing"
)

func TestLiveValidation(t *testing.T) {
	if os.Getenv("TERNLY_CAP_LIVE") == "" {
		t.Skip()
	}
	v := &Validator{}
	for _, e := range []Entry{
		{ID: "a", Kind: "npm", Install: "npm:@j0hanz/fetch-url-mcp@3.0.1"},
		{ID: "b", Kind: "npm", Install: "npm:@rog0x/mcp-time-tools@1.0.2"},
		{ID: "c", Kind: "npm", Install: "npm:@infoinlet/mcp-time@0.1.1"},
		{ID: "d", Kind: "mcp", Install: "pypi:mcp-server-fetch==2025.4.7"},
		{ID: "e", Kind: "plugin", Install: "figma@claude-plugins-official anthropics/claude-plugins-official"},
		{ID: "f", Kind: "extension", Install: "https://github.com/no-such-owner-ternly/no-such-repo"},
	} {
		t.Logf("%-70s → %v", e.Install, v.Check(context.Background(), e))
	}
}
