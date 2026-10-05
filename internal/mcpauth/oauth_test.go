package mcpauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/mcphttp"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]Credential
}

func (s *memStore) Get(issuer, resource string) (Credential, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[issuer+"\x00"+resource]
	return c, ok, nil
}

func (s *memStore) Put(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Credential{}
	}
	s.m[c.Issuer+"\x00"+c.Resource] = c
	return nil
}

func (s *memStore) Delete(issuer, resource string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, issuer+"\x00"+resource)
	return nil
}

func (s *memStore) ByResource(resource string) (Credential, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.m {
		if c.Resource == resource {
			return c, true, nil
		}
	}
	return Credential{}, false, nil
}

// fakeAS is an authorization server (and the resource's metadata), with
// knobs for the failure cases.
type fakeAS struct {
	*httptest.Server
	mu          sync.Mutex
	issuer      string // as advertised (default: its own URL)
	noS256      bool
	badISS      bool
	expiresIn   int
	registered  int
	refreshed   int
	lastScope   string
	lastRes     string
	refreshNext string            // the refresh token it will accept next
	codes       map[string]string // code → challenge
}

func newAS(t *testing.T) *fakeAS {
	a := &fakeAS{expiresIn: 3600, codes: map[string]string{}, refreshNext: "r1"}
	a.Server = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.Close)
	return a
}

func (a *fakeAS) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	iss := a.issuer
	if iss == "" {
		iss = a.URL
	}
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource/mcp":
		json.NewEncoder(w).Encode(map[string]any{"resource": a.URL + "/mcp", "authorization_servers": []string{a.URL}, "scopes_supported": []string{"read", "write"}})
	case "/.well-known/oauth-authorization-server":
		methods := []string{"S256"}
		if a.noS256 {
			methods = []string{"plain"}
		}
		json.NewEncoder(w).Encode(map[string]any{"issuer": iss, "authorization_endpoint": a.URL + "/authorize", "token_endpoint": a.URL + "/token",
			"registration_endpoint": a.URL + "/register", "code_challenge_methods_supported": methods, "authorization_response_iss_parameter_supported": true})
	case "/register":
		var reg struct {
			RedirectURIs    []string `json:"redirect_uris"`
			ApplicationType string   `json:"application_type"`
		}
		json.NewDecoder(r.Body).Decode(&reg)
		if reg.ApplicationType != "native" || len(reg.RedirectURIs) != 1 || !strings.HasPrefix(reg.RedirectURIs[0], "http://127.0.0.1:") {
			http.Error(w, "bad registration", 400)
			return
		}
		a.registered++
		json.NewEncoder(w).Encode(map[string]any{"client_id": fmt.Sprintf("client-%d", a.registered)})
	case "/authorize":
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("resource") != a.URL+"/mcp" {
			http.Error(w, "bad authorize", 400)
			return
		}
		a.lastScope = q.Get("scope")
		code := fmt.Sprintf("code-%d", len(a.codes))
		a.codes[code] = q.Get("code_challenge")
		back := url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {iss}}
		if a.badISS {
			back.Set("iss", "https://evil.example")
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	case "/token":
		r.ParseForm()
		a.lastRes = r.Form.Get("resource")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if a.codes[r.Form.Get("code")] != base64.RawURLEncoding.EncodeToString(sum[:]) {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
		case "refresh_token":
			if r.Form.Get("refresh_token") != a.refreshNext {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			a.refreshed++
		}
		a.refreshNext = fmt.Sprintf("r%d", a.refreshed+2) // rotated every time
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at-%d", a.refreshed), "refresh_token": a.refreshNext, "expires_in": a.expiresIn, "token_type": "Bearer"})
	default:
		http.NotFound(w, r)
	}
}

// browser plays the user: open the authorization URL and follow its
// redirect back to ternly's loopback listener.
func browser(t *testing.T) func(string) error {
	return func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func flow(t *testing.T, a *fakeAS, st Store) *Flow {
	return &Flow{Resource: a.URL + "/mcp", HTTP: a.Client(), Store: st, Open: browser(t), Timeout: 10 * time.Second, Interactive: true,
		Approve: func(host string) error { return fmt.Errorf("%s not granted", host) }}
}

func TestLoginRefreshStepUp(t *testing.T) {
	a := newAS(t)
	st := &memStore{}
	f := flow(t, a, st)
	ctx := context.Background()
	ch := mcphttp.Challenge{Status: 401, ResourceMetadata: a.URL + "/.well-known/oauth-protected-resource/mcp", Scope: "read"}
	if err := f.Authorize(ctx, ch); err != nil {
		t.Fatal(err)
	}
	tok, err := f.Token(ctx)
	if err != nil || tok != "at-0" || a.registered != 1 || a.lastScope != "read" || a.lastRes != a.URL+"/mcp" {
		t.Fatalf("login: tok %q err %v registered %d scope %q resource %q", tok, err, a.registered, a.lastScope, a.lastRes)
	}
	// Expired: refreshed with the rotated token, not a new login.
	c, _, _ := st.Get(a.URL, a.URL+"/mcp")
	c.Expiry = time.Now().Add(-time.Minute)
	st.Put(c)
	if tok, err := f.Token(ctx); err != nil || tok != "at-1" || a.refreshed != 1 {
		t.Fatalf("refresh: %q %v (refreshed %d)", tok, err, a.refreshed)
	}
	// Step-up: the union of scopes, a new login, same registration.
	if err := f.Authorize(ctx, mcphttp.Challenge{Status: 403, Error: "insufficient_scope", Scope: "write"}); err != nil {
		t.Fatal(err)
	}
	if a.lastScope != "read write" || a.registered != 1 {
		t.Fatalf("step-up: scope %q, registrations %d", a.lastScope, a.registered)
	}
}

func TestRefusals(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(a *fakeAS){
		"no S256":         func(a *fakeAS) { a.noS256 = true },
		"issuer mismatch": func(a *fakeAS) { a.issuer = "https://other.example" },
		"iss mismatch":    func(a *fakeAS) { a.badISS = true },
	} {
		a := newAS(t)
		setup(a)
		err := flow(t, a, &memStore{}).Authorize(ctx, mcphttp.Challenge{Status: 401, ResourceMetadata: a.URL + "/.well-known/oauth-protected-resource/mcp"})
		if err == nil {
			t.Errorf("%s: logged in", name)
			continue
		}
		t.Logf("%s: %v", name, err)
		if strings.Contains(err.Error(), "at-") {
			t.Errorf("%s: a token in the error", name)
		}
	}
	// An authorization server on another host needs approval.
	a, other := newAS(t), newAS(t)
	f := flow(t, a, &memStore{})
	f.Resource = a.URL + "/mcp"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": a.URL + "/mcp", "authorization_servers": []string{"http://localhost:" + strings.Split(other.URL, ":")[2]}})
	}))
	defer srv.Close()
	if err := f.Authorize(ctx, mcphttp.Challenge{Status: 401, ResourceMetadata: srv.URL}); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Errorf("another host wasn't refused: %v", err)
	}
}
