package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// End-to-end tests run the real ternly (run()) in a child process: the test
// binary re-executes itself with TERNLY_TEST_ARGS set.
func TestMain(m *testing.M) {
	if a := os.Getenv("TERNLY_TEST_ARGS"); a != "" {
		var args []string
		_ = json.Unmarshal([]byte(a), &args)
		os.Args = append([]string{"ternly"}, args...)
		os.Exit(run())
	}
	os.Exit(m.Run())
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
}

func newProvider(t *testing.T, steps ...step) *fakeProvider {
	f := &fakeProvider{steps: steps}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
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

// testHome makes a HOME with ternly config pointing at the fake provider. It
// lives outside /tmp: the sandbox mounts a fresh /tmp, which would hide
// anything there and make masking tests pass for the wrong reason.
func testHome(t *testing.T, provider string) string {
	t.Helper()
	wd, _ := os.Getwd()
	home, err := os.MkdirTemp(wd, ".e2e-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	cfg := filepath.Join(home, ".config", "ternly")
	cache := filepath.Join(home, ".cache", "ternly")
	data := filepath.Join(home, ".local", "share", "ternly")
	for _, d := range []string{cfg, cache, data} {
		_ = os.MkdirAll(d, 0o700)
	}
	conf := fmt.Sprintf(`{"no_local":true,"providers":[{"id":"fake","kind":"openai","base_url":%q,"key_env":"FAKE_KEY"}]}`, provider)
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
	b, _ := json.Marshal(args)
	c := exec.Command(os.Args[0])
	c.Env = childEnv(home, "TERNLY_TEST_ARGS="+string(b))
	return c
}

func bwrapUsable() bool {
	return exec.Command("bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "true").Run() == nil
}

// M1.1(a): a model-driven shell command cannot read ternly's keys/config,
// cache (checkpoint repositories) or session data. The --no-sandbox control
// shows the data is really there to be read.
func TestSandboxedCommandsCannotReadPrivateData(t *testing.T) {
	if !bwrapUsable() {
		t.Skip("bubblewrap not usable here")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	probe := `cat ~/.config/ternly/marker ~/.cache/ternly/marker ~/.local/share/ternly/marker; cat ~/.cache/ternly/checkpoints/*/*.git/info/exclude; echo inside=$TERNLY`
	for _, sandboxed := range []bool{true, false} {
		f := newProvider(t, step{call: [2]string{"bash", fmt.Sprintf(`{"command":%q}`, probe)}}, step{text: "done"})
		home := testHome(t, f.URL)
		ws := t.TempDir()
		_ = os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x"), 0o644)
		args := []string{"-model", "fake/m1", "-mode", "yolo", "-C", ws, "-p", "probe"}
		if !sandboxed {
			args = append(args, "-no-sandbox")
		}
		out, err := ternly(t, home, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ternly: %v\n%s", err, out)
		}
		tools, _ := f.seen()
		if len(tools) != 1 {
			t.Fatalf("expected one tool result, got %d\n%s", len(tools), out)
		}
		res := tools[0]
		leaked := regexp.MustCompile(`CFG-MARKER|CACHE-MARKER|SESSION-MARKER|written by ternly`).FindAllString(res, -1)
		if sandboxed && (len(leaked) > 0 || !strings.Contains(res, "inside=1")) {
			t.Fatalf("sandboxed command read private data %v:\n%s", leaked, res)
		}
		if !sandboxed && len(leaked) != 4 {
			t.Fatalf("control run should see all 4 markers, saw %v:\n%s", leaked, res)
		}
		// retention follows the session: the checkpoint repository is gone after exit
		repos, _ := filepath.Glob(filepath.Join(home, ".cache", "ternly", "checkpoints", "*", "*.git"))
		if len(repos) != 0 {
			t.Fatalf("checkpoint repository outlived the session: %v", repos)
		}
	}
}

// ───────────── TUI driven through a pseudo-terminal (M1.1 d) ─────────────

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

type screen struct {
	mu  sync.Mutex
	buf bytes.Buffer
	pos int // waitFor only matches text produced after the previous match
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *screen) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		text := reANSI.ReplaceAllString(s.buf.String(), "")
		if i := strings.Index(text[min(s.pos, len(text)):], want); i >= 0 {
			s.pos += i + len(want)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	tail := reANSI.ReplaceAllString(s.buf.String(), "")
	s.mu.Unlock()
	t.Fatalf("timed out waiting for %q; screen tail:\n%s", want, tail[max(0, len(tail)-1500):])
}

func TestTUIUndoRewindLimits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newProvider(t,
		step{call: [2]string{"write_file", `{"path":"a.txt","content":"v1\n"}`}}, step{text: "first-turn-done"},
		step{call: [2]string{"write_file", `{"path":"a.txt","content":"v1 again\n"}`}}, step{text: "second-turn-done"},
	)
	home := testHome(t, f.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "keep.txt"), []byte("mine\n"), 0o644)
	fileIs := func(want string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(ws, "a.txt"))
		if got := string(b); (err != nil && want != "") || got != want {
			t.Fatalf("a.txt = %q (err %v), want %q", got, err, want)
		}
	}

	c := ternly(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark", "COLORTERM=")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 140, 45)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(60 * time.Millisecond) }

	scr.waitFor(t, "Code, ternly.")
	scr.waitFor(t, "fake")
	send("write v1\r")
	scr.waitFor(t, "first-turn-done")
	fileIs("v1\n")

	send("/limits steps 7\r")
	scr.waitFor(t, "per-turn limits: 7 steps")

	send("/rewind\r")
	scr.waitFor(t, "restores to before turn n")
	scr.waitFor(t, "write v1")

	send("/undo\r")
	scr.waitFor(t, "allow Rewind?")
	send("y")
	scr.waitFor(t, "rewound to before turn 1 (both)")
	fileIs("")
	if b, _ := os.ReadFile(filepath.Join(ws, "keep.txt")); string(b) != "mine\n" {
		t.Fatal("user file touched by /undo")
	}

	send("\r") // /undo put the prompt back in the input box: resend it
	scr.waitFor(t, "second-turn-done")
	fileIs("v1 again\n")
	if _, users := f.seen(); len(users) != 2 || users[1] != "write v1" {
		t.Fatalf("prompts sent: %q", users)
	}

	send("/rewind 1 code\r")
	scr.waitFor(t, "allow Rewind?")
	send("y")
	scr.waitFor(t, "rewound to before turn 1 (code)")
	fileIs("")

	send("/rewind\r") // code-only rewind keeps the conversation
	scr.waitFor(t, "restores to before turn n")
	scr.waitFor(t, " 1 ")

	send("/exit\r")
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ternly exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ternly did not exit")
	}
}
