package main

// Linux-only end-to-end tests: bubblewrap sandboxing and the pty-driven TUI.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/testutil"
)

func bwrapUsable() bool {
	return exec.Command("bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "true").Run() == nil
}

// M1.1(a): a model-driven shell command cannot read ternly's keys/config,
// cache (checkpoint repositories) or session data. The --no-sandbox control
// shows the data is really there to be read.
func TestSandboxedCommandsCannotReadPrivateData(t *testing.T) {
	testutil.Require(t, "bubblewrap", bwrapUsable())
	testutil.Require(t, "git", testutil.Have("git"))
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
	testutil.Require(t, "git", testutil.Have("git"))
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
		t.Fatalf("pseudo-terminal: %v", err)
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
