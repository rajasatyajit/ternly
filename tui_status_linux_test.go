package main

import (
	"regexp"
	"testing"
)

// TestTUIMeterFromCore: the TUI reads the real ADR 021 producer
// (internal/status): after a turn, the status bar shows the context used
// against the model's window (the fake catalog says 100k).
func TestTUIMeterFromCore(t *testing.T) {
	f := newProvider(t, step{text: "meter-turn-done"})
	home := testHome(t, f.URL)
	c := ternly(t, home, "-model", "fake/m1", "-C", t.TempDir())
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 140, 40)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait(); _ = tty.Close() }()
	scr.waitFor(t, "fake")
	scr.mu.Lock()
	from := scr.pos
	scr.mu.Unlock()
	_, _ = tty.Write([]byte("hello\r"))
	scr.waitFor(t, "meter-turn-done")
	// the meter may be drawn before the answer's text; later frames rewrite
	// only the digits that change, so search from the turn's start
	scr.mu.Lock()
	scr.pos = from
	scr.mu.Unlock()
	scr.waitForRe(t, regexp.MustCompile(`ctx [0-9.]+k?/100\.0k`))
}
