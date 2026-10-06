package main

// Linux-only end-to-end tests: bubblewrap sandboxing and the pty-driven TUI.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/session"
	"github.com/rajasatyajit/ternly/internal/testutil"
)

func bwrapUsable() bool {
	return exec.Command("bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "true").Run() == nil
}

// M1.1(a): a model-driven shell command cannot read ternly's keys/config,
// cache (checkpoint repositories) or session data. The control shows the
// data is really there to be read: the probe, run on the host, sees it —
// and ternly with --no-sandbox refuses to run it unasked at all (ADR 016).
func TestSandboxedCommandsCannotReadPrivateData(t *testing.T) {
	testutil.Require(t, "bubblewrap", bwrapUsable())
	testutil.Require(t, "git", testutil.Have("git"))
	probe := `cat ~/.config/ternly/marker ~/.cache/ternly/marker ~/.local/share/ternly/marker; cat ~/.cache/ternly/checkpoints/*.git/info/exclude; echo inside=$TERNLY`
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
		if !sandboxed {
			if !strings.HasPrefix(res, "permission denied by policy") || !strings.Contains(res, "unsandboxed") {
				t.Fatalf("unsandboxed, the command ran unasked:\n%s", res)
			}
			host := exec.Command("bash", "-c", probe)
			host.Env = append(os.Environ(), "HOME="+home, "TERNLY=")
			hout, _ := host.CombinedOutput()
			if n := len(regexp.MustCompile(`CFG-MARKER|CACHE-MARKER|SESSION-MARKER|written by ternly`).FindAllString(string(hout), -1)); n != 4 {
				t.Fatalf("control: on the host the probe should see all 4 markers, saw %d:\n%s", n, hout)
			}
		}
		// the session (and its checkpoint store) persists, owner-only
		logs, _ := filepath.Glob(filepath.Join(home, ".local", "share", "ternly", "projects", "*", "sessions", "*", "events.log"))
		if len(logs) != 1 {
			t.Fatalf("expected one saved session, found %v", logs)
		}
		if fi, _ := os.Stat(logs[0]); fi.Mode().Perm() != 0o600 {
			t.Fatalf("session log mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

// ───────────── TUI driven through a pseudo-terminal (M1.1 d) ─────────────

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]`)

type screen struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	pos   int       // waitFor only matches text produced after the previous match
	reply io.Writer // the pty master: answers terminal status queries like a real terminal
	Mute  bool      // don't answer: behave like a terminal that ignores queries
}

// SetReply is called by startInPTY before the child starts.
func (s *screen) SetReply(w io.Writer) { s.mu.Lock(); s.reply = w; s.mu.Unlock() }

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reply != nil && !s.Mute {
		// termenv asks for the background colour (OSC 11) and cursor position
		// (DSR) at start-up; real terminals answer within milliseconds.
		if bytes.Contains(p, []byte("\x1b]11;?")) {
			_, _ = s.reply.Write([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\"))
		}
		if bytes.Contains(p, []byte("\x1b[6n")) {
			_, _ = s.reply.Write([]byte("\x1b[1;1R"))
		}
	}
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

// sessionsOf lists ws's saved sessions as the real binary stores them.
func sessionsOf(t *testing.T, home, ws string) (*session.Project, []session.Meta) {
	t.Helper()
	p, err := session.OpenProject(filepath.Join(home, ".local", "share", "ternly"), ws)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := p.List()
	return p, ms
}

func run1(t *testing.T, home string, args ...string) string {
	t.Helper()
	out, err := ternly(t, home, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ternly %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// validHistory: every tool call answered (providers reject anything else).
func validHistory(t *testing.T, st agent.State) {
	t.Helper()
	open := map[string]bool{}
	for _, m := range st.History {
		for _, tc := range m.ToolCalls {
			open[tc.ID] = true
		}
		if m.Role == "tool" {
			delete(open, m.ToolCallID)
		}
	}
	if len(open) > 0 {
		t.Fatalf("unanswered tool calls: %v", open)
	}
}

// Requirement 11: switching must never lose data, even if the process is
// killed mid-switch. The binary SIGKILLs itself between each pair of phases.
func TestKillMidSwitch(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	for _, ph := range []string{"persisted", "unlocked", "loaded", "swapped"} {
		t.Run(ph, func(t *testing.T) {
			f := newProvider(t, step{call: [2]string{"write_file", `{"path":"a.txt","content":"x\n"}`}}, step{text: "ok"})
			home := testHome(t, f.URL)
			ws := t.TempDir()
			run1(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws, "-p", "first session work")
			run1(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws, "-new", "-p", "second session")
			_, ms := sessionsOf(t, home, ws)
			if len(ms) != 2 {
				t.Fatalf("setup: %d sessions", len(ms))
			}
			from, to := ms[1], ms[0] // older (with the tool call), newer

			c := ternly(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws, "-resume", from.ID)
			c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark", "TERNLY_TEST_CRASH_PHASE="+ph)
			scr := &screen{}
			tty, err := startInPTY(c, scr, 140, 45)
			if err != nil {
				t.Fatal(err)
			}
			scr.waitFor(t, "resumed")
			_, _ = tty.Write([]byte("/switch " + to.ID + "\r"))
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "killed") {
					t.Fatalf("expected SIGKILL at phase %s, got %v", ph, err)
				}
			case <-time.After(15 * time.Second):
				_ = c.Process.Kill()
				t.Fatal("process did not die at phase " + ph)
			}
			p, after := sessionsOf(t, home, ws)
			for _, want := range []session.Meta{from, to} {
				s, st, err := p.Open(want.ID)
				if err != nil {
					t.Fatalf("after kill at %s: session %s won't open: %v", ph, want.ID, err)
				}
				if len(st.Turns) != 1 || len(st.History) < 2 {
					t.Fatalf("after kill at %s: session %s has %d turns / %d messages", ph, want.ID, len(st.Turns), len(st.History))
				}
				validHistory(t, st)
				_ = s.Close("")
			}
			if len(after) != 2 {
				t.Fatalf("after kill at %s: %d sessions", ph, len(after))
			}
			out := run1(t, home, "-model", "fake/m1", "-C", ws, "-c", "-p", "still works")
			if !strings.Contains(out, "ok") {
				t.Fatalf("-c after the kill: %s", out)
			}
		})
	}
}

func TestSessionFlows(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	f := newProvider(t, step{text: "ok"})
	home := testHome(t, f.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("v1\n"), 0o644)
	base := []string{"-model", "fake/m1", "-mode", "yolo", "-C", ws}

	run1(t, home, append(base, "-p", "first prompt")...)
	run1(t, home, append(base, "-c", "-p", "continued")...)
	run1(t, home, append(base, "-p", "unrelated one-shot")...) // headless never auto-resumes
	_, ms := sessionsOf(t, home, ws)
	if len(ms) != 2 {
		t.Fatalf("want 2 sessions, got %+v", ms)
	}
	var a session.Meta
	for _, m := range ms {
		if m.Turns == 2 {
			a = m
		}
	}
	if a.ID == "" || a.Title != "Scripted Title" || a.Status != "paused" {
		t.Fatalf("continued session: %+v", ms)
	}
	run1(t, home, append(base, "-resume", a.ID, "-p", "resumed by id")...)
	out, err := ternly(t, home, append(base, "-resume", "-p", "x")...).CombinedOutput()
	if err == nil || !strings.Contains(string(out), a.ID) || !strings.Contains(string(out), "Scripted Title") {
		t.Fatalf("bare --resume should list sessions and exit non-zero: %v\n%s", err, out)
	}

	// Drift: a file changes while no session runs; the TUI auto-resumes the latest session and tells the model.
	_ = os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("edited outside\n"), 0o644)
	c := ternly(t, home, base...)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 160, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(80 * time.Millisecond) }
	scr.waitFor(t, "resumed “Scripted Title”")
	scr.waitFor(t, "changed outside this session")

	// A second process can't take the open session.
	if out, err := ternly(t, home, append(base, "-c", "-p", "x")...).CombinedOutput(); err == nil || !strings.Contains(string(out), "open in another ternly process") {
		t.Fatalf("second process on a locked session: %v\n%s", err, out)
	}

	send("after drift\r")
	scr.waitFor(t, "ok")
	if _, users := f.seen(); !strings.Contains(users[len(users)-1], "changed outside it") || !strings.Contains(users[len(users)-1], "notes.txt") {
		t.Fatalf("drift note not given to the model: %q", users[len(users)-1])
	}

	send("/sessions\r")
	scr.waitFor(t, "Sessions")
	send("one-shot")                   // fuzzy filter by title words: the other session is titled too, so filter by its prompt-derived status
	send("\x1b")                       // esc closes
	time.Sleep(400 * time.Millisecond) // a lone Esc is held 50 ms in case a sequence follows: keep the next key out of that window, even on a loaded CI runner
	send("/rename Renamed Session\r")
	scr.waitFor(t, "renamed: Renamed Session")
	send("/new\r")
	scr.waitFor(t, "new session")
	send("/fork " + a.ID + "\r")
	scr.waitFor(t, "forked into")
	send("/export md out.md\r")
	scr.waitFor(t, "exported out.md")
	if b, _ := os.ReadFile(filepath.Join(ws, "out.md")); !strings.Contains(string(b), "resumed by id") {
		t.Fatalf("export lacks the forked history: %s", b)
	}
	send("/switch " + a.ID + "\r")
	scr.waitFor(t, "switched to “Renamed Session”")
	send("/stop\r")
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after /stop: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("/stop did not exit")
	}
	_, ms = sessionsOf(t, home, ws)
	status := map[string]string{}
	for _, m := range ms {
		status[m.ID] = m.Status
	}
	if len(ms) != 4 || status[a.ID] != "stopped" {
		t.Fatalf("after /stop: %+v", ms)
	}

	// The next start sees the stopped session: a new session, with a pointer to /resume.
	c2 := ternly(t, home, base...)
	c2.Env = append(c2.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr2 := &screen{}
	tty2, err := startInPTY(c2, scr2, 200, 50)
	if err != nil {
		t.Fatal(err)
	}
	scr2.waitFor(t, "was stopped")
	scr2.waitFor(t, "/resume "+a.ID+" reopens it")
	_, _ = tty2.Write([]byte("/exit\r"))
	_ = c2.Wait()
}

// Time from process start to an interactive TUI showing a resumed
// 1,000-turn session (target: under 100 ms beyond process start-up).
func TestResumeLargeSessionTimeToInteractive(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	f := newProvider(t, step{text: "ok"})
	home := testHome(t, f.URL)
	ws := t.TempDir()
	p, err := session.OpenProject(filepath.Join(home, ".local", "share", "ternly"), ws)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := p.Create()
	out := strings.Repeat("some tool output line\n", 90)
	for i := range 1000 {
		ts := time.Now().UnixMilli()
		s.Record(agent.Record{T: "turn", Prompt: fmt.Sprintf("turn %d", i), TS: ts})
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "user", Content: fmt.Sprintf("turn %d", i)}})
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: fmt.Sprint("c", i), Name: "read_file", Args: `{"path":"a.go"}`}}}})
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "tool", ToolCallID: fmt.Sprint("c", i), Content: out}})
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "assistant", Content: fmt.Sprintf("**Done** with turn %d; see `a.go`.", i)}})
	}
	s.Record(agent.Record{T: "title", Text: "Big Session", TS: time.Now().UnixMilli()})
	_ = s.Close("paused")

	var best, bestVersion time.Duration = time.Hour, time.Hour
	for range 3 {
		t0 := time.Now()
		if err := ternly(t, home, "-version").Run(); err != nil {
			t.Fatal(err)
		}
		bestVersion = min(bestVersion, time.Since(t0))

		c := ternly(t, home, "-model", "fake/m1", "-C", ws, "-c")
		c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
		scr := &screen{}
		t0 = time.Now()
		tty, err := startInPTY(c, scr, 160, 50)
		if err != nil {
			t.Fatal(err)
		}
		scr.waitFor(t, "with turn 999") // the transcript is drawn above the banner
		scr.waitFor(t, "resumed “Big Session”")
		best = min(best, time.Since(t0))
		_, _ = tty.Write([]byte("/exit\r"))
		_ = c.Wait()
	}
	t.Logf("1000-turn session: interactive with transcript in %v (process start-up alone, -version: %v; best of 3; terminal answers status queries)", best.Round(time.Millisecond), bestVersion.Round(time.Millisecond))

	// A terminal that never answers status queries must not delay start-up (v1 waited ~5 s).
	c := ternly(t, home, "-model", "fake/m1", "-C", ws, "-c")
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{Mute: true}
	t0 := time.Now()
	tty, err := startInPTY(c, scr, 160, 50)
	if err != nil {
		t.Fatal(err)
	}
	scr.waitFor(t, "resumed “Big Session”")
	t.Logf("same, in a terminal that ignores status queries: %v (v1 waited 5 s for its init-time OSC query; v2 asks without blocking)", time.Since(t0).Round(time.Millisecond))
	_, _ = tty.Write([]byte("/exit\r"))
	_ = c.Wait()
}
