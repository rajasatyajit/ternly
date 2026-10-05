package mcpauth

// Credential storage for remote MCP servers (ADR 014). First written by
// ternly itself (kimi-k2.7-code via Ollama Cloud) during M8's dogfooding,
// then reviewed and fixed: exit statuses read correctly, no secret on a
// command line, a keyring probe with a file fallback, and a resource index.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Credential is what is stored for one authorization server (issuer) and resource.
type Credential struct {
	Issuer       string    `json:"issuer"`
	Resource     string    `json:"resource"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	RedirectURI  string    `json:"redirect_uri,omitempty"` // the loopback URI the client was registered with
}

// Store keeps credentials. Implementations must be safe for concurrent use.
type Store interface {
	Get(issuer, resource string) (Credential, bool, error)
	Put(c Credential) error
	Delete(issuer, resource string) error
}

func key(issuer, resource string) string { return issuer + "\x00" + resource }

// FileStore keeps all credentials in one JSON file, mode 0600, in a 0700 directory.
type FileStore struct {
	path string
	mu   sync.RWMutex
}

// NewFileStore returns a file-backed credential store.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) load() (map[string]Credential, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Credential{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential file is not a regular file (a link?); refusing to read it")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("credential file is group- or world-readable; fix permissions (chmod 600) and try again")
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}

	var creds []Credential
	if err := json.Unmarshal(b, &creds); err != nil {
		return nil, fmt.Errorf("decode credential file: %w", err)
	}
	m := make(map[string]Credential, len(creds))
	for _, c := range creds {
		m[key(c.Issuer, c.Resource)] = c
	}
	return m, nil
}

func (s *FileStore) save(m map[string]Credential) error {
	creds := make([]Credential, 0, len(m))
	for _, c := range m {
		creds = append(creds, c)
	}
	sort.Slice(creds, func(i, j int) bool {
		a := key(creds[i].Issuer, creds[i].Resource)
		b := key(creds[j].Issuer, creds[j].Resource)
		return a < b
	})

	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	return writeAtomic(s.path, b, 0o600)
}

// Get returns the credential for the given issuer and resource.
func (s *FileStore) Get(issuer, resource string) (Credential, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := s.load()
	if err != nil {
		return Credential{}, false, err
	}
	c, ok := m[key(issuer, resource)]
	return c, ok, nil
}

// Put stores the credential.
func (s *FileStore) Put(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	m[key(c.Issuer, c.Resource)] = c
	return s.save(m)
}

// Delete removes the credential for the given issuer and resource.
func (s *FileStore) Delete(issuer, resource string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, key(issuer, resource))
	return s.save(m)
}

func writeAtomic(path string, b []byte, perm os.FileMode) error {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate temp name: %w", err)
	}
	tmp := path + ".tmp" + hex.EncodeToString(raw)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create temp credential file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp credential file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp credential file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp credential file: %w", err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("chmod temp credential file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace credential file: %w", err)
	}
	return nil
}

// KeyringStore keeps each credential in the OS keyring via its command-line tool.
// It uses secret-tool (libsecret) on Linux and security (Keychain) on macOS.
type KeyringStore struct {
	service string
	tool    string
}

// NewKeyringStore returns a keyring-backed store, or nil if the OS tool isn't available.
func NewKeyringStore(service string) *KeyringStore {
	var tool string
	switch runtime.GOOS {
	case "darwin":
		tool = "security"
	default:
		tool = "secret-tool"
	}
	if _, err := exec.LookPath(tool); err != nil {
		return nil
	}
	return &KeyringStore{service: service, tool: tool}
}

// run runs the keyring tool with stdin; stderr is captured, never shown
// with secrets (it doesn't carry them: secrets go on stdin).
func run(name string, stdin []byte, args ...string) (stdout []byte, code int, stderr string, err error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), ee.ExitCode(), strings.TrimSpace(errb.String()), nil
	}
	return out.Bytes(), 0, strings.TrimSpace(errb.String()), err
}

// account is the macOS keychain account for a credential.
func account(issuer, resource string) string { return issuer + " " + resource }

// quote makes an argument safe inside a `security -i` command line.
func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

// Get returns the credential from the OS keyring.
func (s *KeyringStore) Get(issuer, resource string) (Credential, bool, error) {
	var out []byte
	var code int
	var stderr string
	var err error
	switch s.tool {
	case "security":
		out, code, stderr, err = run("security", nil, "find-generic-password", "-s", s.service, "-a", account(issuer, resource), "-w")
		if code == 44 { // errSecItemNotFound
			return Credential{}, false, nil
		}
	default:
		out, code, stderr, err = run("secret-tool", nil, "lookup", "service", s.service, "issuer", issuer, "resource", resource)
		if code == 1 && stderr == "" { // no such item
			return Credential{}, false, nil
		}
	}
	if err != nil || code != 0 {
		return Credential{}, false, fmt.Errorf("keyring lookup failed (exit %d): %s", code, firstLine(stderr))
	}
	b := bytes.TrimSpace(out)
	if len(b) == 0 {
		return Credential{}, false, nil
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return Credential{}, false, errors.New("keyring secret malformed")
	}
	return c, true, nil
}

// Put stores the credential in the OS keyring. The secret is passed on
// stdin, never on a command line (visible in ps).
func (s *KeyringStore) Put(c Credential) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}
	var code int
	var stderr string
	switch s.tool {
	case "security": // security -i reads commands from stdin, so -w's value stays off the command line
		line := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", quote(s.service), quote(account(c.Issuer, c.Resource)), quote(string(b)))
		_, code, stderr, err = run("security", []byte(line), "-i")
	default:
		_, code, stderr, err = run("secret-tool", b, "store", "--label=ternly MCP: "+c.Resource, "service", s.service, "issuer", c.Issuer, "resource", c.Resource)
	}
	if err != nil || code != 0 {
		return fmt.Errorf("keyring store failed (exit %d): %s", code, firstLine(stderr))
	}
	return nil
}

// Delete removes the credential from the OS keyring.
func (s *KeyringStore) Delete(issuer, resource string) error {
	var code int
	var stderr string
	var err error
	switch s.tool {
	case "security":
		_, code, stderr, err = run("security", nil, "delete-generic-password", "-s", s.service, "-a", account(issuer, resource))
		if code == 44 {
			return nil
		}
	default:
		_, code, stderr, err = run("secret-tool", nil, "clear", "service", s.service, "issuer", issuer, "resource", resource)
		if code == 1 && stderr == "" {
			return nil
		}
	}
	if err != nil || code != 0 {
		return fmt.Errorf("keyring clear failed (exit %d): %s", code, firstLine(stderr))
	}
	return nil
}

// usable probes the keyring: the tool can be installed while no keyring
// service runs (a headless Linux box), and then every store would fail.
func (s *KeyringStore) usable() bool {
	_, _, err := s.Get("ternly-probe", "ternly-probe")
	return err == nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// Open returns the keyring store when it works, else the file store; either
// way wrapped with a resource index (indexPath, not secret) so a server's
// credential can be found before its issuer is known.
func Open(service, path, indexPath string) Store {
	var st Store = NewFileStore(path)
	if ks := NewKeyringStore(service); ks != nil && ks.usable() {
		st = ks
	}
	return &Indexed{Store: st, Path: indexPath}
}

// Indexed adds ByResource to a Store with a small index file mapping each
// resource to its issuer (no secrets in it).
type Indexed struct {
	Store
	Path string
	mu   sync.Mutex
}

func (x *Indexed) index() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(x.Path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// Put stores the credential and records its issuer for the resource.
func (x *Indexed) Put(c Credential) error {
	if err := x.Store.Put(c); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	m := x.index()
	m[c.Resource] = c.Issuer
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(x.Path), 0o700); err != nil {
		return err
	}
	return writeAtomic(x.Path, b, 0o600)
}

// Delete removes the credential and its index entry.
func (x *Indexed) Delete(issuer, resource string) error {
	if err := x.Store.Delete(issuer, resource); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	m := x.index()
	delete(m, resource)
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeAtomic(x.Path, b, 0o600)
}

// ByResource returns the credential stored for resource, whichever issuer.
func (x *Indexed) ByResource(resource string) (Credential, bool, error) {
	x.mu.Lock()
	issuer, ok := x.index()[resource]
	x.mu.Unlock()
	if !ok {
		return Credential{}, false, nil
	}
	return x.Store.Get(issuer, resource)
}
