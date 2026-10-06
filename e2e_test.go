package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/session"
)

// End-to-end tests run the real ternly (run()) in a child process: the test
// binary re-executes itself with TERNLY_TEST_ARGS set.
func TestMain(m *testing.M) {
	if a := os.Getenv("TERNLY_TEST_ARGS"); a != "" {
		var args []string
		_ = json.Unmarshal([]byte(a), &args)
		if ph := os.Getenv("TERNLY_TEST_CRASH_PHASE"); ph != "" { // die like a crash between switch phases
			session.CrashHook = func(p string) {
				if p == ph {
					_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				}
			}
		}
		os.Args = append([]string{"ternly"}, args...)
		os.Exit(run())
	}
	sweepHomes()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { // a killed run (Ctrl+C, timeout) removes its homes first
		<-sig
		stopping.Store(true) // no new homes from tests still running
		stopChildren()
		removeOurHomes()
		os.Exit(2)
	}()
	os.Exit(m.Run())
}

const homePrefix = "ternly-e2e-home-"

var stopping atomic.Bool // set by the signal handler

// stopChildren ends this process's children (the ternly runs): SIGTERM first
// (ternly then cancels its background git and exits), SIGKILL after 5 s.
// They're found with pgrep -P, not from the tests' exec.Cmds (which other
// goroutines are still starting).
func stopChildren() {
	kids := func() []int {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
		var pids []int
		for _, f := range strings.Fields(string(out)) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
		return pids
	}
	for _, pid := range kids() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(kids()) > 0; {
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range kids() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// removeOurHomes removes every home named for this process, including any
// created while the handler ran.
func removeOurHomes() {
	ents, _ := os.ReadDir(homeRoot())
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), fmt.Sprintf("%s%d-", homePrefix, os.Getpid())) {
			_ = os.RemoveAll(filepath.Join(homeRoot(), e.Name()))
		}
	}
}

// homeRoot is where test homes go: $TERNLY_TEST_TMP, else /var/tmp (the OS
// temp dir the sandbox doesn't replace), else os.TempDir().
func homeRoot() string {
	if d := os.Getenv("TERNLY_TEST_TMP"); d != "" {
		return d
	}
	if fi, err := os.Stat("/var/tmp"); err == nil && fi.IsDir() {
		if f, err := os.CreateTemp("/var/tmp", ".ternly-probe-"); err == nil {
			f.Close()
			os.Remove(f.Name())
			return "/var/tmp"
		}
	}
	return os.TempDir()
}

// sweepHomes removes test homes whose test process is gone (killed with
// SIGKILL, which no handler sees).
func sweepHomes() {
	ents, _ := os.ReadDir(homeRoot())
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), homePrefix)
		if !ok {
			continue
		}
		pidStr, _, _ := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid == os.Getpid() || syscall.Kill(pid, 0) == nil {
			continue // alive (or ours)
		}
		_ = os.RemoveAll(filepath.Join(homeRoot(), e.Name()))
	}
}

type step struct {
	text string
	call [2]string // tool name, JSON args
}

// fakeProvider is an OpenAI-compatible server: /models lists one model,
// /chat/completions plays steps in order (repeating the last) and records the
// tool results it was sent.
type fakeProvider struct {
	*httptest.Server
	mu    sync.Mutex
	steps []step
	n     int
	tools []string
	users []string
	specs []string // the tool listing of each request, raw JSON
	raw   []string // every request body
}

func newProvider(t *testing.T, steps ...step) *fakeProvider {
	f := &fakeProvider{steps: steps}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		rawBody, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(rawBody))
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools json.RawMessage `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Messages[0].Content, "Title this coding-session") { // auto-title: not part of the script
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Scripted Title"}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
			return
		}
		f.mu.Lock()
		f.specs = append(f.specs, string(body.Tools))
		f.raw = append(f.raw, string(rawBody))
		if last := body.Messages[len(body.Messages)-1]; last.Role == "tool" {
			f.tools = append(f.tools, last.Content)
		} else if last.Role == "user" {
			f.users = append(f.users, last.Content)
		}
		s := f.steps[min(f.n, len(f.steps)-1)]
		f.n++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"content": s.text}
		fin := "stop"
		if s.call[0] != "" {
			chunk = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("c%d", f.n),
				"function": map[string]any{"name": s.call[0], "arguments": s.call[1]}}}}
			fin = "tool_calls"
		}
		for _, c := range []any{
			map[string]any{"choices": []any{map[string]any{"delta": chunk}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": fin}}},
		} {
			b, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeProvider) seen() (tools, users []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tools...), append([]string(nil), f.users...)
}

// testHome makes a HOME with ternly config pointing at the fake provider.
// It lives in the OS's /var/tmp, not /tmp: the sandbox mounts a fresh /tmp,
// which would hide anything there and make masking tests pass for the wrong
// reason. Homes are named for the test process, removed on cleanup, on
// SIGINT/SIGTERM, and — if the process was killed outright — by the next
// run (sweepHomes).
func testHome(t *testing.T, provider string) string {
	t.Helper()
	if stopping.Load() {
		t.Skip("the run is being stopped")
	}
	home, err := os.MkdirTemp(homeRoot(), fmt.Sprintf("%s%d-", homePrefix, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if stopping.Load() { // the handler may have swept already
		_ = os.RemoveAll(home)
		t.Skip("the run is being stopped")
	}
	cfg := filepath.Join(home, ".config", "ternly")
	cache := filepath.Join(home, ".cache", "ternly")
	data := filepath.Join(home, ".local", "share", "ternly")
	for _, d := range []string{cfg, cache, data} {
		_ = os.MkdirAll(d, 0o700)
	}
	conf := fmt.Sprintf(`{"no_local":true,"suggestions":false,"providers":[{"id":"fake","kind":"openai","base_url":%q,"key_env":"FAKE_KEY"}]}`, provider)
	_ = os.WriteFile(filepath.Join(cfg, "config.json"), []byte(conf), 0o600)
	_ = os.WriteFile(filepath.Join(cache, "catalog.json"), []byte(`{"m1":{"Tools":true,"Ctx":100000}}`), 0o600) // no network fetch
	for d, mark := range map[string]string{cfg: "CFG-MARKER", cache: "CACHE-MARKER", data: "SESSION-MARKER"} {
		_ = os.WriteFile(filepath.Join(d, "marker"), []byte(mark), 0o600)
	}
	return home
}

func childEnv(home string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasSuffix(k, "_API_KEY") || k == "HOME" || k == "TERNLY_TEST_ARGS" {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "HOME="+home, "FAKE_KEY=k"), extra...)
}

func ternly(t *testing.T, home string, args ...string) *exec.Cmd {
	if stopping.Load() {
		t.Skip("the run is being stopped")
	}
	b, _ := json.Marshal(args)
	c := exec.Command(os.Args[0])
	c.Env = childEnv(home, "TERNLY_TEST_ARGS="+string(b))
	return c
}
