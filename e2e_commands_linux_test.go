package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/testutil"
)

// Slash commands in the real TUI (M5): a user-defined command in Claude
// Code's format, completion, /add, !cmd, plan mode and /commit.
func TestTUICommands(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	f := newProvider(t,
		step{text: "greeted"},
		step{text: "explained"},
		step{text: "noted"},
		step{call: [2]string{"edit_file", `{"path":"a.go","old_string":"package a","new_string":"package b"}`}},
		step{text: "planned"},
	)
	home := testHome(t, f.URL)
	cfg := filepath.Join(home, ".config", "ternly", "config.json")
	b, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(b), "{", `{"memory":false,`, 1)), 0o600) // no background model calls
	ws := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", ws, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "t@t") // the test HOME has no global identity
	git("config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a // PIN-MARKER\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(ws, ".claude", "commands"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, ".claude", "commands", "greet.md"), []byte("---\ndescription: greet someone\n---\nSay hello to $0 and $1."), 0o644)
	git("add", ".")
	git("commit", "-qm", "base")

	c := ternly(t, home, "-model", "fake/m1", "-mode", "ask", "-C", ws)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark", "COLORTERM=")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 160, 50)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(80 * time.Millisecond) }
	lastUser := func() string { _, u := f.seen(); return u[len(u)-1] }
	scr.waitFor(t, "Code, ternly.")
	scr.waitFor(t, "fake")

	// Completion: typing a prefix lists matches; Tab completes; Esc closes.
	send("/rewi")
	scr.waitFor(t, "list turns, or restore")
	send("\x1b") // esc
	send("\x03") // ctrl+c clears the input

	// User-defined command, Claude Code format: $0 is the first argument.
	send("/greet Ada Lovelace\r")
	scr.waitFor(t, "greeted")
	if got := lastUser(); got != "Say hello to Ada and Lovelace." {
		t.Fatalf("expanded command sent as %q", got)
	}

	// /add pins a file: its contents go with the prompt (framed as data).
	send("/add a.go\r")
	scr.waitFor(t, "pinned: a.go")
	send("explain a.go\r")
	scr.waitFor(t, "explained")
	if got := lastUser(); !strings.Contains(got, "Current contents of a.go") || !strings.Contains(got, "PIN-MARKER") || !strings.Contains(got, "<<<UNTRUSTED") {
		t.Fatalf("pinned file not attached: %q", got)
	}
	send("/drop\r")
	scr.waitFor(t, "unpinned all files")

	// !cmd: output goes with the next prompt.
	send("!echo RUN-MARKER\r")
	scr.waitFor(t, "output added to your next prompt")
	send("what did it print\r")
	scr.waitFor(t, "noted")
	if got := lastUser(); !strings.Contains(got, "RUN-MARKER") || !strings.HasPrefix(got, "what did it print") {
		t.Fatalf("command output not attached: %q", got)
	}

	// Plan mode: an edit is refused even though the model asks for it.
	send("/plan rename the package\r")
	scr.waitFor(t, "planned")
	tools, _ := f.seen()
	if len(tools) == 0 || !strings.Contains(tools[len(tools)-1], "plan mode is read-only") {
		t.Fatalf("edit not refused in plan mode: %q", tools)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "a.go")); !strings.HasPrefix(string(b), "package a") {
		t.Fatal("file edited in plan mode")
	}
	send("/code\r")
	scr.waitFor(t, "editing mode: ask")

	// /commit with a message: confirmation dialog, then a real commit.
	_ = os.WriteFile(filepath.Join(ws, "b.txt"), []byte("new\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, ".env"), []byte("SECRET=x\n"), 0o644)
	send("/commit add b\r")
	scr.waitFor(t, "never committed (secret-like): .env")
	send("y")
	scr.waitFor(t, "add b")
	if got := git("log", "-1", "--format=%s"); got != "add b" {
		t.Fatalf("last commit %q", got)
	}
	if files := git("show", "--name-only", "--format=", "HEAD"); files != "b.txt" {
		t.Fatalf("committed %q, want only b.txt", files)
	}

	send("/exit\r")
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal(fmt.Sprint("ternly did not exit"))
	}
}

// /architect runs a read-only planning turn, then an implementing turn;
// /test hands a failure to the model; @path attaches a file.
func TestTUIArchitectTestMention(t *testing.T) {
	f := newProvider(t,
		step{text: "PLAN-DONE"},
		step{text: "IMPL-DONE"},
		step{text: "FIXED"},
		step{text: "SEEN"},
	)
	home := testHome(t, f.URL)
	cfg := filepath.Join(home, ".config", "ternly", "config.json")
	b, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(b), "{", `{"memory":false,`, 1)), 0o600)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a // MENTION-MARKER\n"), 0o644)
	c := ternly(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark", "COLORTERM=")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 160, 50)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(80 * time.Millisecond) }
	scr.waitFor(t, "Code, ternly.")
	scr.waitFor(t, "fake")

	send("/architect add a ping endpoint\r")
	scr.waitFor(t, "PLAN-DONE")
	scr.waitFor(t, "IMPL-DONE")
	_, users := f.seen()
	if len(users) < 2 || !strings.Contains(users[0], "As the architect") || !strings.Contains(users[0], "Plan mode") || !strings.HasPrefix(users[1], "Implement the plan above") || strings.Contains(users[1], "Plan mode") {
		t.Fatalf("architect turns: %q", users)
	}

	send("/test sh -c 'echo TEST-FAIL-MARKER; exit 3'\r")
	scr.waitFor(t, "FIXED")
	_, users = f.seen()
	if got := users[len(users)-1]; !strings.HasPrefix(got, "The tests fail") || !strings.Contains(got, "TEST-FAIL-MARKER") {
		t.Fatalf("fix turn: %q", got)
	}

	send("what is in @a.go\r")
	scr.waitFor(t, "SEEN")
	_, users = f.seen()
	if got := users[len(users)-1]; !strings.Contains(got, "MENTION-MARKER") {
		t.Fatalf("@a.go not attached: %q", got)
	}
	send("/exit\r")
	_ = c.Wait()
}
