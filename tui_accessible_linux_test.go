package main

import (
	"strings"
	"testing"
)

// TestTUIAccessibleMode: --accessible runs inline (no alternate screen), with
// no box drawing, and the answer reaches the terminal as text.
func TestTUIAccessibleMode(t *testing.T) {
	f := newProvider(t, step{text: "accessible-answer-text"})
	home := testHome(t, f.URL)
	c := ternly(t, home, "-model", "fake/m1", "-accessible", "-C", t.TempDir())
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 100, 40)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait(); _ = tty.Close() }()
	scr.waitFor(t, "fake")
	_, _ = tty.Write([]byte("hello\r"))
	scr.waitFor(t, "accessible-answer-text")
	scr.mu.Lock()
	raw := scr.buf.String()
	scr.mu.Unlock()
	if strings.Contains(raw, "\x1b[?1049h") {
		t.Fatal("the accessible mode switched to the alternate screen")
	}
	if text := reANSI.ReplaceAllString(raw, ""); strings.ContainsAny(text, "╭╮╰╯│") {
		t.Fatal("box drawing in the accessible mode")
	}
}
