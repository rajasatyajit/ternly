package mcpremote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rajasatyajit/ternly/internal/mcpauth"
	"github.com/rajasatyajit/ternly/internal/tools"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]mcpauth.Credential
}

func (s *memStore) Get(iss, res string) (mcpauth.Credential, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[iss+" "+res]
	return c, ok, nil
}

func (s *memStore) Put(c mcpauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]mcpauth.Credential{}
	}
	s.m[c.Issuer+" "+c.Resource] = c
	return nil
}

func (s *memStore) Delete(iss, res string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, iss+" "+res)
	return nil
}

func (s *memStore) ByResource(res string) (mcpauth.Credential, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.m {
		if c.Resource == res {
			return c, true, nil
		}
	}
	return mcpauth.Credential{}, false, nil
}

// world is an OAuth-protected modern MCP server whose authorization server
// lives on another host (another port of 127.0.0.1), as Linear's and
// Notion's do.
type world struct {
	mcp, as   *httptest.Server
	challenge string // the S256 challenge of the pending login
	opened    int    // login pages "opened"
}

func newWorld(t *testing.T) *world {
	w := &world{}
	w.as = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(rw).Encode(map[string]any{"issuer": w.as.URL, "authorization_endpoint": w.as.URL + "/authorize", "token_endpoint": w.as.URL + "/token",
				"registration_endpoint": w.as.URL + "/register", "code_challenge_methods_supported": []string{"S256"}})
		case "/register":
			json.NewEncoder(rw).Encode(map[string]any{"client_id": "c1"})
		case "/authorize":
			q := r.URL.Query()
			w.challenge = q.Get("code_challenge")
			http.Redirect(rw, r, q.Get("redirect_uri")+"?"+url.Values{"code": {"k"}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
		case "/token":
			r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != w.challenge {
				rw.WriteHeader(400)
				json.NewEncoder(rw).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(rw).Encode(map[string]any{"access_token": "secret-token-123", "token_type": "Bearer", "expires_in": 3600})
		}
	}))
	t.Cleanup(w.as.Close)
	w.mcp = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			json.NewEncoder(rw).Encode(map[string]any{"resource": w.mcp.URL + "/mcp", "authorization_servers": []string{w.as.URL}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret-token-123" {
			rw.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+w.mcp.URL+`/.well-known/oauth-protected-resource/mcp"`)
			rw.WriteHeader(401)
			return
		}
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		var res any
		switch q.Method {
		case "server/discover":
			res = map[string]any{"supportedVersions": []string{"2026-07-28"}}
		case "tools/list":
			res = map[string]any{"tools": []any{map[string]any{"name": "issues", "description": "list issues", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			res = map[string]any{"content": []any{map[string]any{"type": "text", "text": "3 open issues; your token is secret-token-123"}}}
		}
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": res})
	}))
	t.Cleanup(w.mcp.Close)
	return w
}

// browser follows the login page's redirect back to ternly's listener.
func (w *world) browser(u string) error {
	w.opened++
	go func() {
		if resp, err := http.Get(u); err == nil {
			resp.Body.Close()
		}
	}()
	return nil
}

func TestStartLoginGrant(t *testing.T) {
	w := newWorld(t)
	dir := t.TempDir()
	reg, err := tools.NewRegistry(t.TempDir(), tools.NewPolicy("edits", nil), nil, tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	red := reg.Redact
	m := &Manager{Reg: reg, Dir: dir, Version: "test", Redact: red.Add, Store: &memStore{}, Open: w.browser}
	cfg := []tools.RemoteConfig{{Name: "tracker", MCPServerConfig: tools.MCPServerConfig{URL: w.mcp.URL + "/mcp"}}}
	ctx := context.Background()

	// Start-up never logs in: the server needs a login, no page is opened.
	notes := m.Start(ctx, cfg)
	if len(notes) != 1 || !strings.Contains(notes[0], "/mcp login tracker") || w.opened != 0 {
		t.Fatalf("start: %v (opened %d)", notes, w.opened)
	}
	if st := m.Statuses()[0]; st.Auth != "needs login" {
		t.Fatalf("status %+v", st)
	}

	// The authorization server is another host: declined, nothing granted,
	// and it was never contacted (the grant refused the dial).
	err = m.Login(ctx, "tracker", func(string) bool { return false }, nil)
	if !errors.Is(err, ErrNeedsApproval) || w.opened != 0 {
		t.Fatalf("declined login: %v (opened %d)", err, w.opened)
	}
	if _, err := os.Stat(filepath.Join(dir, "grants.json")); err == nil {
		t.Fatal("a declined host was granted")
	}

	// Approved: saved to the grant, the browser login runs, tools register.
	var asked []string
	if err := m.Login(ctx, "tracker", func(h string) bool { asked = append(asked, h); return true }, nil); err != nil {
		t.Fatal(err)
	}
	asHost := strings.TrimPrefix(w.as.URL, "http://")
	if len(asked) != 1 || asked[0] != asHost || w.opened != 1 {
		t.Fatalf("asked %v, opened %d", asked, w.opened)
	}
	reg.Commit()
	if reg.Get("mcp__tracker__issues") == nil {
		t.Fatal("tools not registered")
	}
	st := m.Statuses()[0]
	if st.Auth != "logged in" || st.Era != "2026-07-28" || len(st.Hosts) != 2 || st.Hosts[1] != asHost {
		t.Fatalf("status %+v", st)
	}

	// The token never reaches the model: the redactor knows it.
	if out := red.Apply("3 open issues; your token is secret-token-123"); strings.Contains(out, "secret-token-123") {
		t.Fatalf("token not redacted: %s", out)
	}

	// A new process: the stored token and the saved grant suffice, no login.
	m2 := &Manager{Reg: reg, Dir: dir, Version: "test", Store: m.store, Open: w.browser}
	if notes := m2.Start(ctx, cfg); len(notes) != 0 || w.opened != 1 {
		t.Fatalf("restart: %v (opened %d)", notes, w.opened)
	}

	// The grant is for this URL: pointing the name elsewhere voids it.
	moved := []tools.RemoteConfig{{Name: "tracker", MCPServerConfig: tools.MCPServerConfig{URL: w.mcp.URL + "/other"}}}
	m3 := &Manager{Reg: reg, Dir: dir, Version: "test", Store: &memStore{}, Open: w.browser}
	m3.Prepare(moved)
	if hosts := m3.Statuses()[0].Hosts; len(hosts) != 1 {
		t.Fatalf("grant survived a URL change: %v", hosts)
	}

	if err := m.Logout("tracker"); err != nil {
		t.Fatal(err)
	}
	if m.Statuses()[0].Auth == "logged in" {
		t.Fatal("still logged in after logout")
	}
}

func TestRefusesPlainHTTP(t *testing.T) {
	m := &Manager{Dir: t.TempDir(), Store: &memStore{}}
	notes := m.Start(context.Background(), []tools.RemoteConfig{{Name: "x", MCPServerConfig: tools.MCPServerConfig{URL: "http://mcp.example.com/mcp"}}})
	if len(notes) != 1 || !strings.Contains(notes[0], "https") {
		t.Fatalf("%v", notes)
	}
}
