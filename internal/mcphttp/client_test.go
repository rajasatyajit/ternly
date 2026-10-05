package mcphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type rpcReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
}

// fakeServer answers like a modern (2026-07-28) or a 2025 server.
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	modern   bool
	sse      bool   // answer requests as SSE, with a notification first
	bare     bool   // modern responses without jsonrpc and id (as Context7)
	token    string // required bearer token ("" = none)
	sessions map[string]bool
	seen     []*http.Request
	deleted  bool
}

func newFake(t *testing.T, modern bool) *fakeServer {
	f := &fakeServer{modern: modern, sessions: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Clone(context.Background()))
	f.mu.Unlock()
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://x/.well-known/oauth-protected-resource", scope="read write"`)
		w.WriteHeader(401)
		return
	}
	if r.Method == "DELETE" {
		f.mu.Lock()
		f.deleted = true
		f.mu.Unlock()
		return
	}
	var q rpcReq
	_ = json.NewDecoder(r.Body).Decode(&q)
	reply := func(result any) {
		var b []byte
		if f.bare {
			b, _ = json.Marshal(map[string]any{"result": result})
		} else {
			b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(q.ID), "result": result})
		}
		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, ": comment\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\nevent: message\ndata: %s\n\n", b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
	if f.modern {
		if r.Header.Get("MCP-Protocol-Version") != Modern || r.Header.Get("Mcp-Method") != q.Method {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32020,"message":"header mismatch"}}`)
			return
		}
		switch q.Method {
		case "server/discover":
			reply(map[string]any{"supportedVersions": []string{Modern}})
		case "tools/list":
			reply(map[string]any{"tools": []any{map[string]any{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": "hit " + r.Header.Get("Mcp-Name") + " " + r.Header.Get("Mcp-Param-Region")}}})
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32601,"message":"no method"}}`)
		}
		return
	}
	// 2025 server: modern requests get a non-modern 400 (as DeepWiki).
	if r.Header.Get("MCP-Protocol-Version") == Modern {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Bad Request: Unsupported protocol version: 2026-07-28"}}`)
		return
	}
	sess := r.Header.Get("Mcp-Session-Id")
	switch {
	case q.Method == "initialize":
		w.Header().Set("Mcp-Session-Id", fmt.Sprintf("s%d", len(f.sessions)+1))
		f.mu.Lock()
		f.sessions[fmt.Sprintf("s%d", len(f.sessions)+1)] = true
		f.mu.Unlock()
		reply(map[string]any{"protocolVersion": Legacy, "capabilities": map[string]any{}})
		return
	case !f.sessions[sess]:
		w.WriteHeader(404) // unknown or expired session
		return
	case q.Method == "notifications/initialized":
		w.WriteHeader(202)
	case q.Method == "tools/list":
		reply(map[string]any{"tools": []any{map[string]any{"name": "ask"}}})
	default:
		reply(map[string]any{"ok": true})
	}
}

func client(f *fakeServer) *Client {
	return &Client{URL: f.URL, HTTP: f.Client(), Name: "ternly", Version: "test"}
}

func TestModern(t *testing.T) {
	for _, variant := range []struct{ sse, bare bool }{{false, false}, {true, false}, {false, true}} {
		f := newFake(t, true)
		f.sse, f.bare = variant.sse, variant.bare
		c := client(f)
		ctx := context.Background()
		if err := c.Connect(ctx); err != nil || c.Era() != EraModern {
			t.Fatalf("%+v: connect: %v (%s)", variant, err, c.Era())
		}
		c.SetToolHeaders("lookup", json.RawMessage(`{"properties":{"region":{"type":"string","x-mcp-header":"Region"}}}`))
		res, err := c.Call(ctx, "tools/call", map[string]any{"name": "lookup", "arguments": map[string]any{"region": "eu-west"}})
		if err != nil || !strings.Contains(string(res), "hit lookup eu-west") {
			t.Fatalf("%+v: call: %s %v", variant, res, err)
		}
		f.mu.Lock()
		last := f.seen[len(f.seen)-1]
		f.mu.Unlock()
		if last.Header.Get("Mcp-Session-Id") != "" || last.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Errorf("%+v: headers %v", variant, last.Header)
		}
	}
}

// A 2025 server answers the modern probe with a non-modern 400: the client
// falls back to initialize, keeps the session, and re-initializes when the
// server forgets it.
func TestLegacyFallbackAndSessionExpiry(t *testing.T) {
	f := newFake(t, false)
	f.sse = true
	c := client(f)
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil || c.Era() != EraLegacy {
		t.Fatalf("connect: %v (%s)", err, c.Era())
	}
	if res, err := c.Call(ctx, "tools/list", nil); err != nil || !strings.Contains(string(res), "ask") {
		t.Fatalf("list: %s %v", res, err)
	}
	f.mu.Lock()
	f.sessions = map[string]bool{} // the server restarts
	f.mu.Unlock()
	if _, err := c.Call(ctx, "tools/list", nil); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	c.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deleted {
		t.Error("session not deleted on close")
	}
	inits := 0
	for _, r := range f.seen {
		if r.Header.Get("MCP-Protocol-Version") == Legacy && r.Header.Get("Mcp-Session-Id") == "" && r.Method == "POST" {
			inits++
		}
	}
	if inits != 2 {
		t.Errorf("%d initializes, want 2 (start and after expiry)", inits)
	}
}

// 401 → Authorize → the request is retried with the token.
func TestChallengeRetry(t *testing.T) {
	f := newFake(t, true)
	f.token = "tok"
	var got Challenge
	token := ""
	c := client(f)
	c.Token = func(context.Context) (string, error) { return token, nil }
	c.Authorize = func(_ context.Context, ch Challenge) error { got, token = ch, "tok"; return nil }
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Status != 401 || got.ResourceMetadata != "https://x/.well-known/oauth-protected-resource" || got.Scope != "read write" {
		t.Fatalf("challenge %+v", got)
	}
}

func TestParseChallenge(t *testing.T) {
	ch := ParseChallenge(`Bearer realm="OAuth", error="insufficient_scope", scope="files:read, files:write", resource_metadata="https://m/.well-known/oauth-protected-resource/mcp"`)
	if ch.Error != "insufficient_scope" || ch.Scope != "files:read, files:write" || !strings.HasSuffix(ch.ResourceMetadata, "/mcp") {
		t.Fatalf("%+v", ch)
	}
	if ParseChallenge(`Basic realm="x"`).ResourceMetadata != "" {
		t.Fatal("non-bearer challenge parsed")
	}
	if headerValue("ünïcode") != "=?base64?w7xuw69jb2Rl?=" || headerValue("plain") != "plain" {
		t.Fatal("header encoding")
	}
	_ = io.Discard
}
