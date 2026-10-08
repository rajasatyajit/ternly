package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/session"
)

// sessionsModel has two saved sessions, "alpha" (current) and "beta".
func sessionsModel(t *testing.T, w int) *Model {
	t.Helper()
	m := testModel(t)
	p, err := session.OpenProject(t.TempDir(), m.App.Reg.Root)
	if err != nil {
		t.Fatal(err)
	}
	mgr := &session.Manager{Project: p, Agent: m.App.Agent, Policy: m.App.Reg.Policy, Router: m.App.Router}
	for _, title := range []string{"beta", "alpha"} {
		s, err := p.Create()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.Attach(s, agent.State{}, false); err != nil {
			t.Fatal(err)
		}
		mgr.Rename(title)
		_ = s.Flush() // the listing reads the saved metadata
		if title == "beta" {
			_ = s.Close("paused") // left, as when switching away
		}
	}
	m.App.Sessions = mgr
	m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
	return m
}

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

// Ctrl+B shows the sessions beside the transcript, focused; ↑↓ and Enter
// switch; the transcript narrows to make room; Ctrl+B again hides it.
func TestSidebar(t *testing.T) {
	m := sessionsModel(t, 140)
	m.addInfo("a transcript line that is long enough to show where the transcript wraps at the content width")
	m.Update(ctrl('b'))
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "Sessions") || !strings.Contains(v, "● alpha") || !strings.Contains(v, "beta") || m.cw() != 140-sidebarW-1 {
		t.Fatalf("sidebar (cw %d):\n%s", m.cw(), v)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := len([]rune(l)); w > 140 {
			t.Fatalf("a row is %d columns wide, window 140: %q", w, l)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // keys stay in the sidebar while it's focused
	if m.ta.Value() != "" {
		t.Fatal("a key reached the input while the sidebar had focus")
	}
	sel := m.side.list[m.side.sel].Title
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.side.list[m.side.sel].Title == sel {
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	target := m.side.list[m.side.sel]
	if target.Title != "beta" {
		t.Fatalf("selected %q", target.Title)
	}
	// a session started elsewhere meanwhile shows up after the switch
	g, err := m.App.Sessions.Project.Create()
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Close("paused")
	_, c := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if c == nil {
		t.Fatal("Enter didn't switch")
	}
	msg := c()
	if info, ok := msg.(infoMsg); ok {
		t.Fatalf("switch failed: %s", stripANSI(string(info)))
	}
	m.Update(msg)
	if cur := m.App.Sessions.Current(); cur == nil || cur.Meta().Title != "beta" {
		t.Fatalf("current after switch: %+v", cur)
	}
	if v := stripANSI(m.View().Content); !strings.Contains(v, "● beta") || strings.Count(v, "(untitled)") != 1 {
		t.Fatalf("the sidebar didn't follow the switch (or the new session is missing):\n%s", v)
	}
	// a turn's end refreshes titles
	m.App.Sessions.Rename("beta renamed")
	_ = m.App.Sessions.Current().Flush()
	m.Update(agentMsg(agent.Event{Kind: agent.EvDone}))
	if !strings.Contains(stripANSI(m.sidebarView(20)), "beta renamed") { // the header shows the title too: look in the sidebar itself
		t.Fatal("the sidebar didn't refresh after a turn")
	}
	m.Update(ctrl('b')) // focus again
	m.Update(ctrl('b')) // hide
	if m.side != nil || m.cw() != 140 {
		t.Fatalf("hide: side=%v cw=%d", m.side, m.cw())
	}
}

// Too narrow: it says how wide it needs to be and takes no room; the
// accessible mode points to /resume instead.
func TestSidebarNarrowAndLinear(t *testing.T) {
	m := sessionsModel(t, 80)
	m.Update(ctrl('b'))
	if v := stripANSI(m.View().Content); !strings.Contains(v, "needs 100 columns") || m.cw() != 80 {
		t.Fatalf("narrow:\n%s", v)
	}
	l := sessionsModel(t, 140)
	l.App.Access.ScreenReader = true
	l.Update(ctrl('b'))
	if l.side != nil || !strings.Contains(stripANSI(l.View().Content), "/resume") {
		t.Fatal("accessible mode: the sidebar should point to /resume")
	}
}

// With the sidebar open over a 10k-line transcript, a keystroke still
// renders well inside the frame budget (ADR 023).
func TestSidebarFrameBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing: not under the race detector or -short")
	}
	m := sessionsModel(t, 160)
	for i := range 1000 {
		m.blocks = append(m.blocks, &block{kind: bUser, text: fmt.Sprintf("turn %d", i)},
			&block{kind: bAssistant, text: "an answer\n\n- one\n- two"})
	}
	m.Update(ctrl('b'))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc}) // back to the input, sidebar shown
	m.refresh(true)
	var ds []time.Duration
	for range 100 {
		t0 := time.Now()
		m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		_ = m.View()
		ds = append(ds, time.Since(t0))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	t.Logf("keystroke to frame with the sidebar: median %v, p95 %v", ds[50], ds[95])
	if ds[95] > 16*time.Millisecond {
		t.Fatalf("p95 %v, budget 16 ms", ds[95])
	}
}
