package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSuite is a suite with one task whose oracle runs `sh -c <script>` in ws.
func fakeSuite(t *testing.T, script string) (*suite, task, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("probe"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &suite{dir: dir}
	tk := task{ID: "probe", Oracle: map[string]string{"probe.txt": "probe.txt"}, Cmd: []string{"sh", "-c", script}}
	return s, tk, t.TempDir()
}

// Without bubblewrap the oracle refuses, and the task's command never runs
// (the security review of c6442e0: it used to run unsandboxed).
func TestOracleRefusesWithoutBwrap(t *testing.T) {
	old := lookBwrap
	lookBwrap = func() (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookBwrap = old })
	s, tk, ws := fakeSuite(t, "touch ran")
	pass, _, err := s.oracle(tk, ws)
	if !errors.Is(err, errNoSandbox) || pass {
		t.Fatalf("oracle without bwrap: pass=%v err=%v, want errNoSandbox", pass, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "ran")); err == nil {
		t.Fatal("the untrusted command ran without a sandbox")
	}
	// run refuses before spending any quota
	if err := s.runCmd([]string{"-out", t.TempDir(), "-bin", "/bin/false"}); !errors.Is(err, errNoSandbox) {
		t.Fatalf("run without bwrap: %v, want errNoSandbox", err)
	}
}

// The oracle's environment carries no keys, tokens or git environment.
func TestOracleEnvScrubbed(t *testing.T) {
	base := []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-ant-x", "OPENAI_API_KEY=sk-x", "GITHUB_TOKEN=ghp_x", "GH_TOKEN=gh",
		"AWS_SECRET=x", "DB_PASSWORD=x", "GIT_DIR=/real/.git", "GIT_INDEX_FILE=/i", "TERNLY_HARNESS=1", "HOME=/home/u", "SSH_AUTH_SOCK=/s"}
	env := oracleEnv(base)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.Contains(kv, "sk-") || strings.HasSuffix(k, "_TOKEN") || strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_PASSWORD") ||
			k == "GIT_DIR" || k == "GIT_INDEX_FILE" || k == "HOME" || k == "SSH_AUTH_SOCK" || strings.HasPrefix(k, "TERNLY_") {
			t.Errorf("oracle env carries %s", kv)
		}
	}
	if !slices.Contains(env, "PATH=/bin") || !slices.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("oracle env lost PATH or the git isolation: %q", env)
	}
}

// End to end, when bubblewrap is available: the oracle's process sees no key.
func TestOracleProcessHasNoKeys(t *testing.T) {
	if _, err := lookBwrap(); err != nil {
		t.Skip("no bwrap")
	}
	t.Setenv("SUITE_CACHE", t.TempDir())
	t.Setenv("FAKE_API_KEY", "sk-should-not-leak")
	t.Setenv("SOME_TOKEN", "tok-should-not-leak")
	s, tk, ws := fakeSuite(t, "env > env.txt")
	if _, _, err := s.oracle(tk, ws); err != nil {
		t.Skipf("bwrap unusable here: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(ws, "env.txt"))
	if err != nil {
		t.Skipf("the sandboxed oracle didn't run: %v", err)
	}
	if strings.Contains(string(b), "should-not-leak") {
		t.Fatalf("a secret reached the oracle:\n%s", b)
	}
}

// The sandbox hides the user's runtime directory (the SSH agent's socket).
func TestOracleNoAgentSocket(t *testing.T) {
	if _, err := lookBwrap(); err != nil {
		t.Skip("no bwrap")
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		t.Skip("no runtime dir")
	}
	t.Setenv("SUITE_CACHE", t.TempDir())
	s, tk, ws := fakeSuite(t, "ls -A "+rt+" > rt.txt 2>&1; true")
	if _, _, err := s.oracle(tk, ws); err != nil {
		t.Skipf("bwrap unusable here: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "rt.txt"))
	if strings.TrimSpace(string(b)) != "" {
		t.Fatalf("the runtime directory is visible in the sandbox:\n%s", b)
	}
}
