package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/surface"
)

// The layout rule itself (ADR 030): fields in priority order; a line that
// doesn't fit shortens its least important fields first, then drops them;
// the first field stays; nothing passes the width.
func TestStatusLayoutPriority(t *testing.T) {
	fs := []statusField{
		{"MODEL-full-xxxxxxxx", "MODEL", 0},
		{"CTX-full-xxxxx", "CTX", 0},
		{"COST-full-xxx", "COST", 0},
		{"TURN-full-xxxxxxx", "TURN", 1},
		{"UNDO-full-xx", "UNDO", 1},
		{"CACHE-full", "", 1},
	}
	sep := " · "
	join := func(s ...string) string { return " " + strings.Join(s, sep) }
	all := join("MODEL-full-xxxxxxxx", "CTX-full-xxxxx", "COST-full-xxx", "TURN-full-xxxxxxx", "UNDO-full-xx", "CACHE-full")
	for _, c := range []struct {
		w, lines int
		want     []string
	}{
		{200, 2, []string{all}}, // everything fits on one line
		{200, 1, []string{all}},
		// two lines: the groups apart, each full
		{60, 2, []string{join("MODEL-full-xxxxxxxx", "CTX-full-xxxxx", "COST-full-xxx"), join("TURN-full-xxxxxxx", "UNDO-full-xx", "CACHE-full")}},
		// one line: shorten from the end (CACHE has no short form and stays until dropped)
		{60, 1, []string{join("MODEL-full-xxxxxxxx", "CTX", "COST", "TURN", "UNDO", "CACHE-full")}},
		// narrower: everything short, then the least important dropped
		{40, 1, []string{join("MODEL", "CTX", "COST", "TURN", "UNDO")}},
		{20, 1, []string{join("MODEL", "CTX", "COST")}},
		// the first field stays, cut to the width
		{4, 1, []string{" MO…"}},
	} {
		got := layoutStatus(fs, c.w, c.lines, sep)
		for i := range got {
			got[i] = stripANSI(got[i])
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("w=%d lines=%d:\n got %q\nwant %q", c.w, c.lines, got, c.want)
		}
		for _, l := range got {
			if lipgloss.Width(l) > c.w {
				t.Errorf("w=%d: %q is %d columns", c.w, l, lipgloss.Width(l))
			}
		}
	}
}

// statusModel is a model mid-session: a priced model, a finished turn with
// tokens, a verified change and two rewindable turns.
func statusModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := sized(t, w, h)
	md := &discover.Model{Provider: &discover.Provider{ID: "openrouter"}, ProvID: "openrouter", ID: "vendor/a-fairly-long-model-name", Tools: true, Priced: true, In: 1, Out: 2, Tier: 2}
	m.onAgent(agent.Event{Kind: agent.EvModel, Model: md, Reason: "T2 task → est. 9 s"})
	m.ledger = agent.Ledger{Usage: llm.Usage{In: 1000, Out: 50}, Cost: 0.0123}
	m.turnStart = time.Now().Add(-38 * time.Second)
	m.turnCost0, m.turnUsage0 = m.ledger.Cost, m.ledger.Usage
	m.ledger.Usage.In, m.ledger.Usage.Out = 5200, 430 // this turn: 4.2k in, 380 out
	m.turnTook = 38 * time.Second
	m.verify = agent.VerdictVerified
	m.App.Agent.Commit(agent.Record{T: "turn", Prompt: "one"})
	m.App.Agent.Commit(agent.Record{T: "turn", Prompt: "two"})
	m.layout()
	return m
}

// At every size the status fits, keeps the model, and shows this turn's
// facts while there's room; 80×24 takes two lines and keeps the transcript
// at 16 rows or more.
func TestStatusLine(t *testing.T) {
	for _, s := range [][2]int{{200, 50}, {120, 40}, {80, 24}, {60, 24}} {
		m := statusModel(t, s[0], s[1])
		bar := fits(t, m.statusBar(), s[0])
		text := strings.Join(bar, "\n")
		for _, want := range []string{"vendor/a-fairly-long-model-name", "cheapest", "$0.0", "turn 4.2k↑ 380↓", "38s", "verify ✓", "undo 2"} {
			if s[0] >= 120 && !strings.Contains(text, want) {
				t.Errorf("%dx%d: the status lacks %q:\n%s", s[0], s[1], want, text)
			}
		}
		if !strings.Contains(text, "vendor/a-fairly-long-model-name") {
			t.Errorf("%dx%d: the model went first:\n%s", s[0], s[1], text)
		}
		if s == [2]int{80, 24} {
			if len(bar) != 2 {
				t.Errorf("80x24: %d status lines, want 2:\n%s", len(bar), text)
			}
			for _, want := range []string{"$0.0", "4.2k↑", "38s", "✓", "2"} {
				if !strings.Contains(text, want) {
					t.Errorf("80x24: the status lacks %q:\n%s", want, text)
				}
			}
			if m.vp.h < 16 {
				t.Errorf("80x24: the transcript has %d rows, want ≥ 16", m.vp.h)
			}
		}
		fits(t, m.View().Content, s[0])
		if got := strings.Count(m.View().Content, "\n") + 1; got > s[1] {
			t.Errorf("%dx%d: the frame has %d rows", s[0], s[1], got)
		}
	}
}

// It collapses to one line below 24 rows and when the input grows.
func TestStatusCollapses(t *testing.T) {
	if m := statusModel(t, 80, 24); m.statusLines() != 2 {
		t.Fatal("80x24 should allow two status lines")
	}
	if m := statusModel(t, 80, 23); m.statusLines() != 1 || len(strings.Split(m.statusBar(), "\n")) != 1 {
		t.Error("80x23: one status line")
	}
	m := statusModel(t, 80, 24)
	m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	m.layout()
	if m.ta.Height() < 2 || m.statusLines() != 1 {
		t.Errorf("a grown input (%d lines): %d status lines, want 1", m.ta.Height(), m.statusLines())
	}
	// an open panel: two status lines would leave the transcript < 16 rows
	m = statusModel(t, 80, 30)
	if m.statusLines() != 2 {
		t.Fatal("80x30, nothing open: two status lines")
	}
	key(m, "ctrl+k")
	m.layout()
	if m.statusLines() != 1 || m.vp.h < 3 {
		t.Errorf("80x30 with the palette open (%d rows): %d status lines, transcript %d", m.panelHeight(), m.statusLines(), m.vp.h)
	}
}

// A model with no price shows "cost unknown", never a made-up $0.0000.
func TestStatusCostUnknown(t *testing.T) {
	m := sized(t, 120, 40)
	m.onAgent(agent.Event{Kind: agent.EvModel, Model: &discover.Model{Provider: &discover.Provider{ID: "x"}, ProvID: "x", ID: "m1"}, Reason: "T1"})
	if bar := stripANSI(m.statusBar()); !strings.Contains(bar, "cost unknown") || strings.Contains(bar, "$0.0000") {
		t.Errorf("unpriced model: %q", bar)
	}
}

// Verdicts, routing reasons and the ASCII set.
func TestStatusGlyphs(t *testing.T) {
	m := statusModel(t, 200, 50)
	for v, want := range map[string]string{agent.VerdictFailed: "verify ✗", agent.VerdictUnverified: "verify ?"} {
		m.verify = v
		if bar := stripANSI(m.statusBar()); !strings.Contains(bar, want) {
			t.Errorf("verdict %s: %q", v, bar)
		}
	}
	for r, want := range map[string]string{"pinned": "◆ pinned", "failover": "⇄ failover", "escalated: too slow": "↑ escalated"} {
		m.reason = r
		if bar := stripANSI(m.statusBar()); !strings.Contains(bar, want) {
			t.Errorf("reason %q: %q", r, bar)
		}
	}
	m.reason = ""
	m.App.Access.ASCII = true
	for _, s := range [][2]int{{200, 50}, {80, 24}} {
		m.Update(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
		bar := stripANSI(m.statusBar())
		for _, r := range bar {
			if r > 126 {
				t.Fatalf("%dx%d ASCII: %q has %q", s[0], s[1], bar, r)
			}
		}
		if s[0] == 200 && !strings.Contains(bar, "turn 4.2k^ 380v") {
			t.Errorf("ASCII: %q", bar)
		}
	}
}

// The locale decides the glyph set; TERNLY_ASCII overrides it.
func TestAccessASCII(t *testing.T) {
	for _, c := range []struct {
		all, ctype, lang, force string
		want                    bool
	}{
		{"", "", "en_US.UTF-8", "", false},
		{"", "", "C", "", true},
		{"", "", "", "", true},
		{"C", "", "en_US.UTF-8", "", true}, // LC_ALL wins
		{"", "en_US.utf8", "C", "", false}, // then LC_CTYPE
		{"", "", "C", "0", false},
		{"", "", "en_US.UTF-8", "1", true},
	} {
		t.Setenv("LC_ALL", c.all)
		t.Setenv("LC_CTYPE", c.ctype)
		t.Setenv("LANG", c.lang)
		if c.force != "" {
			t.Setenv("TERNLY_ASCII", c.force)
		} else {
			t.Setenv("TERNLY_ASCII", "")
			_ = unsetenv("TERNLY_ASCII")
		}
		if got := AccessFromEnv().ASCII; got != c.want {
			t.Errorf("%+v: ASCII %v", c, got)
		}
	}
}

func unsetenv(k string) error { return os.Unsetenv(k) }

// The facts come from the agent's events: the verification's verdict, and the
// turn's time when it ends.
func TestStatusFromEvents(t *testing.T) {
	m := sized(t, 200, 50)
	m.turnStart, m.busy = time.Now().Add(-5*time.Second), true
	m.onAgent(agent.Event{Kind: agent.EvVerify, Text: "go build ./..."})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, ToolID: "verify", Tool: "verify", OK: true, Verdict: agent.VerdictVerified})
	m.onAgent(agent.Event{Kind: agent.EvDone})
	bar := stripANSI(m.statusBar())
	if !strings.Contains(bar, "verify ✓") {
		t.Errorf("the verify event didn't reach the status: %q", bar)
	}
	if m.turnTook < 5*time.Second || !strings.Contains(bar, "5.0s") {
		t.Errorf("the finished turn's time: %v, %q", m.turnTook, bar)
	}
}

// The status line is cached between frames; every input it shows must
// refresh it.
func TestStatusCacheFollowsInputs(t *testing.T) {
	m := statusModel(t, 200, 50)
	bar := func() string { return stripANSI(m.statusBar()) }
	_ = bar()
	for _, c := range []struct {
		name   string
		change func()
		want   string
	}{
		{"an error", func() { m.onAgent(agent.Event{Kind: agent.EvError, Text: "HTTP 500"}) }, "1 error"},
		{"a turn", func() { m.App.Agent.Commit(agent.Record{T: "turn", Prompt: "three"}) }, "undo 3"},
		{"spending", func() { m.ledger.Cost = 1.5 }, "$1.5000"},
		{"time passing", func() { m.busy, m.turnStart = true, time.Now().Add(-47*time.Second) }, "47s"},
		{"ASCII", func() { m.App.Access.ASCII = true }, "turn 4.2k^"},
	} {
		c.change()
		if got := bar(); !strings.Contains(got, c.want) {
			t.Errorf("after %s the status shows %q, want %q", c.name, got, c.want)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	if got := m.statusBar(); lipgloss.Width(strings.Split(got, "\n")[0]) > 70 {
		t.Errorf("after a resize the status is still %d columns", lipgloss.Width(got))
	}
}

// A quota that ran out reads "quota out until …" in full, quota✗ short, and
// quota:x short in ASCII (not "quotax").
func TestStatusQuotaOut(t *testing.T) {
	for _, c := range []struct {
		w     int
		ascii bool
		want  string
	}{{200, false, "quota out until"}, {76, false, "quota✗"}, {76, true, "quota:x"}} {
		m := statusModel(t, c.w, 30)
		m.App.Access.ASCII = c.ascii
		key := m.model.Key()
		m.snap = surface.Snapshot{Models: []surface.Model{{Key: key, Connection: "cloud"}},
			Connections: []surface.Connection{{ID: "cloud", Quota: &surface.Quota{ExhaustedUntil: time.Now().Add(time.Hour)}}}}
		m.snapGen++
		if bar := stripANSI(m.statusBar()); !strings.Contains(bar, c.want) {
			t.Errorf("w=%d ascii=%v: %q lacks %q", c.w, c.ascii, bar, c.want)
		}
	}
}

// ternly's own estimate (no Retry-After) never reads as a clock time; it
// counts down, and the status line re-renders as it does.
func TestStatusQuotaEstimated(t *testing.T) {
	m := statusModel(t, 200, 30)
	key := m.model.Key()
	set := func(left time.Duration, estimated bool) string {
		m.snap = surface.Snapshot{Models: []surface.Model{{Key: key, Connection: "cloud"}},
			Connections: []surface.Connection{{ID: "cloud", Quota: &surface.Quota{ExhaustedUntil: time.Now().Add(left), UntilEstimated: estimated}}}}
		m.snapGen++
		return stripANSI(m.statusBar())
	}
	clock := regexp.MustCompile(`quota out until \d\d:\d\d`)
	if bar := set(time.Hour, true); !strings.Contains(bar, "quota out, retry in about 1h (estimated)") || clock.MatchString(bar) {
		t.Errorf("estimated hold: %q", bar)
	}
	if bar := set(time.Hour, false); !clock.MatchString(bar) || strings.Contains(bar, "estimated") {
		t.Errorf("the provider's time: %q", bar)
	}
	k1 := func() statusKey { set(time.Hour, true); return m.statusKey() }()
	k2 := func() statusKey { set(20*time.Minute, true); return m.statusKey() }()
	if k1.quotaOut == k2.quotaOut {
		t.Errorf("the countdown doesn't change the status key: %q", k1.quotaOut)
	}
}
