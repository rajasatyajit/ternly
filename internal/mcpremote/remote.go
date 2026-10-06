// Package mcpremote runs ternly's remote MCP servers (ADR 014): each server
// gets its own network grant (netguard), its OAuth flow (mcpauth) and its
// stored credentials, and its tools are registered like any MCP server's.
// A host the server's authorization needs beyond its own is never contacted
// until the user approves it with /mcp login (or ternly --mcp-login).
package mcpremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/mcpauth"
	"github.com/rajasatyajit/ternly/internal/netguard"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Manager owns the remote servers of one ternly process.
type Manager struct {
	Reg     *tools.Registry
	Dir     string // <data>/mcp: grants, credentials, index
	Version string
	Redact  func(value string)     // add a secret to the redactor
	Store   mcpauth.Store          // nil: the keyring if usable, else a 0600 file in Dir
	Open    func(url string) error // shows the login page (nil: the system browser)

	mu      sync.Mutex
	store   mcpauth.Store
	servers map[string]*state
}

type state struct {
	cfg     tools.RemoteConfig
	srv     *tools.RemoteServer
	httpc   *http.Client // the server's netguarded client
	flow    *mcpauth.Flow
	err     error
	pending []string // hosts discovery found that aren't granted yet
}

// Grant is what one server may reach, as approved.
type Grant struct {
	URL   string    `json:"url"`
	Hosts []string  `json:"hosts"` // beyond the server's own host
	At    time.Time `json:"approved"`
}

// Status is one server's state, for /mcp.
type Status struct {
	Name, URL, Era string
	Tools          int
	Hosts          []string // granted, the server's own first
	Auth           string   // "no login needed", "logged in", "needs login"
	Err            string
	Pending        []string
}

// ErrNeedsApproval is returned when a server's authorization needs hosts the
// user hasn't approved; the error names them.
var ErrNeedsApproval = errors.New("needs approval")

func (m *Manager) init() {
	if m.servers == nil {
		m.servers = map[string]*state{}
		m.store = m.Store
		if m.Open == nil {
			m.Open = openBrowser
		}
		if m.store == nil {
			m.store = mcpauth.Open("ternly-mcp", filepath.Join(m.Dir, "credentials.json"), filepath.Join(m.Dir, "credentials-index.json"))
		}
	}
}

// Prepare records the configured servers without connecting.
func (m *Manager) Prepare(cfgs []tools.RemoteConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	for _, c := range cfgs {
		m.servers[c.Name] = &state{cfg: c}
	}
}

// Start connects every configured remote server (concurrently) and
// registers its tools. Failures are returned as notes, never fatal.
func (m *Manager) Start(ctx context.Context, cfgs []tools.RemoteConfig) []string {
	m.Prepare(cfgs)
	var notes []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range cfgs {
		wg.Add(1)
		go func(c tools.RemoteConfig) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second) // a black-holed server doesn't hold up start-up
			defer cancel()
			if err := m.connect(cctx, c.Name, nil, nil); err != nil {
				mu.Lock()
				notes = append(notes, fmt.Sprintf("mcp %s: %v", c.Name, err))
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	sort.Strings(notes)
	return notes
}

// connect starts one server. approve, when set, is called for each host the
// server's authorization needs beyond its grant, and allows a browser login
// (nil: stored or refreshed credentials only; ungranted hosts are recorded).
func (m *Manager) connect(ctx context.Context, name string, approve func(host string) error, notify func(string)) error {
	m.mu.Lock()
	st := m.servers[name]
	m.mu.Unlock()
	if st == nil {
		return fmt.Errorf("no remote server %q", name)
	}
	u, err := url.Parse(st.cfg.URL)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return fmt.Errorf("%s: remote servers need https (http only on this machine)", st.cfg.URL)
	}
	grant := m.grant(name)
	hosts := append([]string{u.Host}, grant.Hosts...)
	httpc := netguard.Client(netguard.Grant{Hosts: hosts, AllowPrivate: isLoopback(u.Hostname())}, 5*time.Minute) // dial, TLS, response headers: a long tools/call answers late
	m.mu.Lock()
	st.httpc = httpc
	m.mu.Unlock()
	var pending []string
	flow := &mcpauth.Flow{Resource: canonical(u), HTTP: httpc, Store: m.store, ClientName: "ternly", Notify: notify, Open: m.Open, Interactive: approve != nil,
		Approve: func(host string) error {
			if slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, host) }) {
				return nil
			}
			if approve != nil {
				return approve(host)
			}
			pending = append(pending, host)
			return fmt.Errorf("%w: its authorization uses %s, which isn't granted — /mcp login %s to review it", ErrNeedsApproval, host, name)
		}}
	var auth tools.RemoteAuth = &redacting{flow, m.Redact}
	m.mu.Lock()
	if st.srv != nil {
		st.srv.Close()
		st.srv = nil
	}
	m.mu.Unlock()
	srv, err := tools.StartRemote(ctx, m.Reg, name, st.cfg.URL, st.cfg.Headers, httpc, auth, m.Version)
	if errors.Is(err, mcpauth.ErrLoginRequired) || errors.Is(err, ErrNeedsApproval) {
		err = fmt.Errorf("%w — /mcp login %s", err, name)
	}
	m.mu.Lock()
	st.srv, st.flow, st.err, st.pending = srv, flow, err, pending
	m.mu.Unlock()
	return err
}

// Login authorizes a server: approve is asked about each host its
// authorization needs beyond its grant (an approved host is saved to the
// grant), then the browser login runs. New tools are announced at the next
// turn boundary, like any other tool change.
func (m *Manager) Login(ctx context.Context, name string, approve func(host string) bool, notify func(string)) error {
	m.mu.Lock()
	m.init()
	st := m.servers[name]
	m.mu.Unlock()
	if st == nil {
		return fmt.Errorf("no remote server %q (configure it in ~/.config/ternly/mcp.json with a \"url\")", name)
	}
	return m.connect(ctx, name, func(host string) error {
		if approve == nil || !approve(host) {
			return fmt.Errorf("%w: %s was not approved", ErrNeedsApproval, host)
		}
		if err := m.addGrant(name, st.cfg.URL, host); err != nil {
			return err
		}
		return netguard.Allow(st.httpc, host)
	}, notify)
}

// Logout deletes a server's stored credentials.
func (m *Manager) Logout(name string) error {
	m.mu.Lock()
	m.init()
	st := m.servers[name]
	m.mu.Unlock()
	if st == nil {
		return fmt.Errorf("no remote server %q", name)
	}
	u, _ := url.Parse(st.cfg.URL)
	if x, ok := m.store.(interface {
		ByResource(string) (mcpauth.Credential, bool, error)
	}); ok {
		if c, found, err := x.ByResource(canonical(u)); err == nil && found {
			return m.store.Delete(c.Issuer, c.Resource)
		}
	}
	return nil
}

// Statuses lists the remote servers.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for name, st := range m.servers {
		u, _ := url.Parse(st.cfg.URL)
		s := Status{Name: name, URL: st.cfg.URL, Hosts: append([]string{u.Host}, m.grantLocked(name).Hosts...), Auth: "no login needed", Pending: st.pending}
		if st.srv != nil {
			s.Era, s.Tools = st.srv.Era(), st.srv.Tools
		}
		if x, ok := m.store.(interface {
			ByResource(string) (mcpauth.Credential, bool, error)
		}); ok {
			if _, found, _ := x.ByResource(canonical(u)); found {
				s.Auth = "logged in"
			}
		}
		if st.err != nil {
			s.Err = st.err.Error()
			if errors.Is(st.err, ErrNeedsApproval) || errors.Is(st.err, mcpauth.ErrLoginRequired) {
				s.Auth = "needs login"
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close ends every server's session.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.servers {
		if st.srv != nil {
			st.srv.Close()
		}
	}
}

func (m *Manager) grantsPath() string { return filepath.Join(m.Dir, "grants.json") }

func (m *Manager) grants() map[string]Grant {
	gs := map[string]Grant{}
	if b, err := os.ReadFile(m.grantsPath()); err == nil {
		_ = json.Unmarshal(b, &gs)
	}
	return gs
}

func (m *Manager) grant(name string) Grant {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.grantLocked(name)
}

func (m *Manager) grantLocked(name string) Grant {
	g := m.grants()[name]
	if st := m.servers[name]; st != nil && g.URL != st.cfg.URL {
		return Grant{} // granted for another URL: void
	}
	return g
}

func (m *Manager) addGrant(name, serverURL, host string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gs := m.grants()
	g := gs[name]
	if g.URL != serverURL {
		g = Grant{URL: serverURL}
	}
	if !slices.Contains(g.Hosts, host) {
		g.Hosts = append(g.Hosts, host)
	}
	g.At = time.Now().UTC()
	gs[name] = g
	b, _ := json.MarshalIndent(gs, "", "  ")
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.grantsPath(), b, 0o600)
}

// redacting adds every token it hands out to the redactor.
type redacting struct {
	*mcpauth.Flow
	redact func(string)
}

func (r *redacting) Token(ctx context.Context) (string, error) {
	t, err := r.Flow.Token(ctx)
	if t != "" && r.redact != nil {
		r.redact(t)
	}
	return t, err
}

// canonical is the server's URI as the RFC 8707 resource: no fragment, no
// trailing slash.
func canonical(u *url.URL) string {
	c := *u
	c.Fragment = ""
	c.Scheme, c.Host = strings.ToLower(c.Scheme), strings.ToLower(c.Host)
	return strings.TrimSuffix(c.String(), "/")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openBrowser opens the login page; the URL is also shown, so a failure
// here only means the user opens it themselves.
func openBrowser(u string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if err := mcpauth.WebURL(u); err != nil {
		return err
	}
	if _, err := exec.LookPath(name); err != nil {
		return err
	}
	return exec.Command(name, u).Start()
}
