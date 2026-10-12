package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Live times are whole seconds: the status line changes at most once a
// second while a turn runs (ADR 030, F2.1).
func TestLiveElapsedWholeSeconds(t *testing.T) {
	for d, want := range map[time.Duration]string{1200 * time.Millisecond: "1s", 9900 * time.Millisecond: "9s", 75 * time.Second: "1m15s"} {
		if got := durLive(d); got != want {
			t.Errorf("durLive(%v) = %q, want %q", d, got, want)
		}
	}
	m := sized(t, 120, 40)
	m.busy = true
	m.turnStart = time.Now().Add(-1200 * time.Millisecond)
	k1 := m.statusKey()
	m.turnStart = time.Now().Add(-1700 * time.Millisecond)
	if k2 := m.statusKey(); k1.elapsed != k2.elapsed {
		t.Fatalf("a running turn's status changed within the second: %q → %q", k1.elapsed, k2.elapsed)
	}
	m.busy = false
	m.turnTook = 1700 * time.Millisecond
	if got := m.elapsedText(m.turnTook); got != "1.7s" {
		t.Errorf("a finished turn: %q, want the tenth", got)
	}
}

// No glyph on the status line is wide: a line with a wide character is
// re-sent whole on every frame by the renderer (ultraviolet 006e29f), and
// the status line changes often.
func TestStatusGlyphsNarrow(t *testing.T) {
	g := unicodeGlyphs
	for _, s := range []string{g.model, g.routed, g.pinned, g.escalated, g.failover, g.in, g.out, g.ok, g.fail, g.unknown, g.cache, g.sep} {
		for _, r := range s {
			if w := ansi.StringWidth(string(r)); w > 1 {
				t.Errorf("status glyph %q is %d columns wide", r, w)
			}
		}
	}
}

// Work in progress is a spinner cell and static text: between spinner steps
// the line doesn't change, and the text never does.
func TestMovingIsStatic(t *testing.T) {
	m := sized(t, 120, 40)
	m.frame = 0
	a := m.moving("Thinking…")
	m.frame = spinEvery - 1
	if b := m.moving("Thinking…"); a != b {
		t.Fatalf("changed within one spinner step: %q → %q", a, b)
	}
	m.frame = spinEvery
	c := m.moving("Thinking…")
	if c == a || !strings.HasSuffix(stripANSI(c), " Thinking…") || stripANSI(a)[len(stripANSI(a))-len("Thinking…"):] != "Thinking…" {
		t.Fatalf("spinner step: %q → %q", stripANSI(a), stripANSI(c))
	}
	m.App.Access.ASCII = true
	if s := stripANSI(m.moving("x")); !strings.HasPrefix(s, "|") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "\\") {
		t.Errorf("ASCII spinner: %q", s)
	}
}

// A running lane counts in whole seconds; a finished one keeps the tenth.
func TestLaneDurWholeSecondsWhileRunning(t *testing.T) {
	s := lane(1, "lexer", "running", 1)
	s.Started = time.Now().Add(-1500 * time.Millisecond)
	if got := laneDur(s); got != "1s" {
		t.Errorf("running: %q, want 1s", got)
	}
	s.State, s.Took = "done", 1500*time.Millisecond
	if got := laneDur(s); got != "1.5s" {
		t.Errorf("done: %q, want 1.5s", got)
	}
}
