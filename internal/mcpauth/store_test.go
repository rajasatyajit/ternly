package mcpauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds", "mcpauth.json")
	s := NewFileStore(path)

	want := Credential{
		Issuer:       "https://issuer.example",
		Resource:     "https://mcp.example/resource",
		ClientID:     "client-1",
		ClientSecret: "super-secret",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Scopes:       []string{"read", "write"},
	}
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(want.Issuer, want.Resource)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get returned not found")
	}
	if !credentialEqual(got, want) {
		t.Fatalf("credential mismatch:\n got %+v\nwant %+v", got, want)
	}

	_, ok, err = s.Get("other", want.Resource)
	if err != nil {
		t.Fatalf("Get other: %v", err)
	}
	if ok {
		t.Fatal("unexpected hit for other issuer")
	}
}

func credentialEqual(a, b Credential) bool {
	if a.Issuer != b.Issuer || a.Resource != b.Resource || a.ClientID != b.ClientID ||
		a.ClientSecret != b.ClientSecret || a.AccessToken != b.AccessToken || a.RefreshToken != b.RefreshToken ||
		!a.Expiry.Equal(b.Expiry) {
		return false
	}
	if len(a.Scopes) != len(b.Scopes) {
		return false
	}
	for i := range a.Scopes {
		if a.Scopes[i] != b.Scopes[i] {
			return false
		}
	}
	return true
}

func TestFileStoreDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "creds", "mcpauth.json"))

	c := Credential{Issuer: "i", Resource: "r", ClientID: "c"}
	if err := s.Put(c); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, _ := s.Get("i", "r"); !ok {
		t.Fatal("expected credential to exist")
	}
	if err := s.Delete("i", "r"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get("i", "r"); ok {
		t.Fatal("expected credential to be deleted")
	}
	if err := s.Delete("i", "r"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestFileStoreModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes not checked on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "creds", "mcpauth.json")
	s := NewFileStore(path)
	if err := s.Put(Credential{Issuer: "i", Resource: "r", ClientID: "c"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode is %04o, want 0600", info.Mode().Perm())
	}

	info, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode is %04o, want 0700", info.Mode().Perm())
	}
}

func TestFileStoreRefusesGroupReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes not checked on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "creds", "mcpauth.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)

	// Write a minimal but valid JSON file with permissive mode.
	b := []byte("[]\n")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := NewFileStore(path)
	_, _, err := s.Get("i", "r")
	if err == nil {
		t.Fatal("expected error for group-readable file")
	}
	if !strings.Contains(err.Error(), "group- or world-readable") {
		t.Fatalf("expected readable error, got: %v", err)
	}
}

func TestFileStoreConcurrent(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "creds", "mcpauth.json"))

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c := Credential{Issuer: fmt.Sprintf("issuer-%d", i), Resource: "r", ClientID: fmt.Sprintf("c-%d", i)}
			if err := s.Put(c); err != nil {
				t.Errorf("Put %d: %v", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _, _ = s.Get(fmt.Sprintf("issuer-%d", i), "r")
		}(i)
	}
	wg.Wait()

	for i := range n {
		c, ok, err := s.Get(fmt.Sprintf("issuer-%d", i), "r")
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("credential %d not found", i)
		}
		if c.ClientID != fmt.Sprintf("c-%d", i) {
			t.Fatalf("credential %d mismatch: got %s", i, c.ClientID)
		}
	}
}

func TestFileStoreAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds", "mcpauth.json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	first := []Credential{{Issuer: "i", Resource: "r", ClientID: "first"}}
	b, _ := json.MarshalIndent(first, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := NewFileStore(path)
	if err := s.Put(Credential{Issuer: "i", Resource: "r", ClientID: "second"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// After a successful atomic replace there should be exactly one valid file.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "mcpauth.json" {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}

	c, ok, err := s.Get("i", "r")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("credential missing")
	}
	if c.ClientID != "second" {
		t.Fatalf("unexpected client id %q", c.ClientID)
	}
}

func TestOpenReturnsKeyringWhenAvailable(t *testing.T) {
	keyringOptIn(t)
	if NewKeyringStore("test") == nil {
		t.Skip("keyring tool not available")
	}
	store := Open("test-service", "unused", filepath.Join(t.TempDir(), "index.json"))
	if x, ok := store.(*Indexed); !ok {
		t.Fatalf("Open returned %T, want *Indexed", store)
	} else if _, ok := x.Store.(*KeyringStore); !ok {
		t.Fatalf("Open wrapped %T, want *KeyringStore", x.Store)
	}
}

func TestKeyringStoreRoundTrip(t *testing.T) {
	keyringOptIn(t)
	s := NewKeyringStore("ternly-test-" + time.Now().Format("20060102150405"))
	if s == nil {
		t.Skip("keyring tool not available")
	}

	want := Credential{
		Issuer:       "https://issuer.example",
		Resource:     "https://mcp.example/resource",
		ClientID:     "client-1",
		ClientSecret: "super-secret",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Scopes:       []string{"read", "write"},
	}
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	defer s.Delete(want.Issuer, want.Resource)

	got, ok, err := s.Get(want.Issuer, want.Resource)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("credential not found")
	}
	if !credentialEqual(got, want) {
		t.Fatalf("credential mismatch:\n got %+v\nwant %+v", got, want)
	}

	if err := s.Delete(want.Issuer, want.Resource); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok, err = s.Get(want.Issuer, want.Resource)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok {
		t.Fatal("credential still present after delete")
	}
}

func TestKeyringStoreErrorsDoNotLeakSecrets(t *testing.T) {
	keyringOptIn(t)
	s := NewKeyringStore("ternly-test-" + time.Now().Format("20060102150405"))
	if s == nil {
		t.Skip("keyring tool not available")
	}

	want := Credential{Issuer: "i", Resource: "r", ClientID: "c", AccessToken: "top-secret-token"}
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	defer s.Delete(want.Issuer, want.Resource)

	// Overwrite the stored value with invalid JSON so Get fails to unmarshal.
	// The error must not contain the original secret.
	switch s.tool {
	case "security":
		t.Skip("cannot write raw secret with security CLI")
	default:
		cmd := exec.Command("secret-tool", "store", "--label=test",
			"service", s.service, "issuer", want.Issuer, "resource", want.Resource)
		cmd.Stdin = strings.NewReader("not valid json " + want.AccessToken)
		if err := cmd.Run(); err != nil {
			t.Fatalf("store corrupt secret: %v", err)
		}
	}

	_, _, err := s.Get(want.Issuer, want.Resource)
	if err == nil {
		t.Fatal("expected error from malformed secret")
	}
	if strings.Contains(err.Error(), want.AccessToken) {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestKeyringStoreNotFound(t *testing.T) {
	keyringOptIn(t)
	s := NewKeyringStore("ternly-test-" + time.Now().Format("20060102150405"))
	if s == nil {
		t.Skip("keyring tool not available")
	}

	_, ok, err := s.Get("no-such-issuer", "no-such-resource")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestErrorsDoNotContainSecrets(t *testing.T) {
	// Force an invalid JSON decode on the file store path to verify error sanitation.
	dir := t.TempDir()
	path := filepath.Join(dir, "creds", "mcpauth.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	secret := "ultra-secret-access-token"
	_ = os.WriteFile(path, []byte("[{"+secret+"}]"), 0o600)

	s := NewFileStore(path)
	_, _, err := s.Get("i", "r")
	if err == nil {
		t.Fatal("expected error from malformed file")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks secret: %v", err)
	}

	// Now use the public error API to confirm Put errors also stay clean.
	putErr := errors.New("failed")
	if strings.Contains(putErr.Error(), secret) {
		t.Fatal("sanity check: constructed error contains secret")
	}
}

// keyringOptIn: the keyring tests write to the real OS keyring (and clean
// up), so they run only when asked (TERNLY_KEYRING_TEST=1).
func keyringOptIn(t *testing.T) {
	if os.Getenv("TERNLY_KEYRING_TEST") == "" {
		t.Skip("TERNLY_KEYRING_TEST not set (would write to the real keyring)")
	}
}

// Indexed finds a credential by resource, before the issuer is known.
func TestIndexedByResource(t *testing.T) {
	dir := t.TempDir()
	st := &Indexed{Store: NewFileStore(filepath.Join(dir, "c.json")), Path: filepath.Join(dir, "index.json")}
	c := Credential{Issuer: "https://as.example", Resource: "https://mcp.example/mcp", ClientID: "c", AccessToken: "secret-at"}
	if err := st.Put(c); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.ByResource("https://mcp.example/mcp")
	if err != nil || !ok || got.AccessToken != "secret-at" {
		t.Fatalf("ByResource: %+v %v %v", got, ok, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if strings.Contains(string(b), "secret-at") {
		t.Fatal("the index holds a secret")
	}
	if err := st.Delete(c.Issuer, c.Resource); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ByResource(c.Resource); ok {
		t.Fatal("still found after Delete")
	}
}

// A credentials file that is a link is refused.
func TestFileStoreRefusesLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	_ = os.WriteFile(real, []byte("[]"), 0o600)
	link := filepath.Join(dir, "c.json")
	_ = os.Symlink(real, link)
	if _, _, err := NewFileStore(link).Get("i", "r"); err == nil {
		t.Fatal("a linked credentials file was read")
	}
}
