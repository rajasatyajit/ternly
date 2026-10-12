package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
)

// keyp sends a key by its name: "ctrl+x", "alt+a", "esc", "pgdown", "a".
func keyp(m *Model, s string) tea.Cmd {
	var k tea.KeyPressMsg
	switch {
	case s == "esc":
		k = tea.KeyPressMsg{Code: tea.KeyEscape}
	case s == "pgdown":
		k = tea.KeyPressMsg{Code: tea.KeyPgDown}
	case s == "pgup":
		k = tea.KeyPressMsg{Code: tea.KeyPgUp}
	case strings.HasPrefix(s, "ctrl+"):
		k = tea.KeyPressMsg{Code: rune(s[5]), Mod: tea.ModCtrl}
	case strings.HasPrefix(s, "alt+"):
		k = tea.KeyPressMsg{Code: rune(s[4]), Mod: tea.ModAlt}
	case strings.HasPrefix(s, "f") && len(s) > 1:
		var n int
		fmt.Sscan(s[1:], &n)
		k = tea.KeyPressMsg{Code: tea.KeyF1 + rune(n-1)}
	default:
		k = tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	if got := k.String(); got != s {
		panic(fmt.Sprintf("keyp(%q) built %q", s, got))
	}
	_, c := m.Update(k)
	return c
}

func TestParseLeader(t *testing.T) {
	for in, want := range map[string]string{"": DefaultLeader, "ctrl+y": "ctrl+y", " Ctrl+Y ": "ctrl+y", "alt+x": "alt+x", "f2": "f2", " F12": "f12"} {
		if got, note := ParseLeader(in); got != want || note != "" {
			t.Errorf("ParseLeader(%q) = %q, %q; want %q", in, got, note, want)
		}
	}
	// the shells' and terminals' keys, keys that type or edit, ternly's own
	for _, in := range []string{"ctrl+d", "ctrl+r", "ctrl+s", "ctrl+q", "ctrl+z", "ctrl+c", "ctrl+k", "ctrl+u", "ctrl+g", "alt+b", "alt+c",
		"enter", "esc", "tab", "x", "ctrl+", "ctrl+xx", "shift+x", "f13", "f0", "meta+x"} {
		if got, note := ParseLeader(in); got != DefaultLeader || note == "" {
			t.Errorf("ParseLeader(%q) = %q, %q; want the default and a note", in, got, note)
		}
	}
}

// The leader opens the which-key popup; a letter then opens its panel; Esc
// or another key closes the popup and does nothing else.
func TestLeaderWhichKey(t *testing.T) {
	m := sized(t, 120, 40)
	h0 := m.vp.h
	keyp(m, "ctrl+x")
	v := strings.Join(fits(t, m.View().Content, 120), "\n")
	for _, a := range keyActions {
		if !strings.Contains(v, a.key+" "+a.label) {
			t.Errorf("the popup lacks %q:\n%s", a.key+" "+a.label, v)
		}
	}
	if !strings.Contains(v, "Ctrl+X then a key") || m.vp.h >= h0 {
		t.Errorf("popup: title missing or the transcript didn't give it rows (%d → %d):\n%s", h0, m.vp.h, v)
	}
	keyp(m, "esc")
	if m.leaderWait || m.panel != nil || m.vp.h != h0 {
		t.Fatalf("Esc: wait %v, panel %v, rows %d (want %d)", m.leaderWait, m.panel, m.vp.h, h0)
	}
	keyp(m, "ctrl+x")
	keyp(m, "z") // no such letter
	if m.leaderWait || m.panel != nil || m.ta.Value() != "" {
		t.Fatalf("an unknown letter: wait %v, panel %v, typed %q", m.leaderWait, m.panel, m.ta.Value())
	}
	for _, a := range keyActions {
		if a.panel == "" {
			continue
		}
		keyp(m, "ctrl+x")
		keyp(m, a.key)
		if m.panel == nil || m.panel.kind != a.panel {
			t.Fatalf("ctrl+x %s: panel %v, want %s", a.key, m.panel, a.panel)
		}
		if v := strings.Join(fits(t, m.View().Content, 120), "\n"); !strings.Contains(v, panelTitles[a.panel]) {
			t.Errorf("ctrl+x %s: no %q on screen:\n%s", a.key, panelTitles[a.panel], v)
		}
		keyp(m, "ctrl+x")
		keyp(m, a.key) // the same again closes it
		if m.panel != nil {
			t.Fatalf("ctrl+x %s twice: still open", a.key)
		}
	}
}

// ADR 030 decision 2: ternly binds none of Ctrl+D, R, S, Q and Z.
func TestNoShellControlKeys(t *testing.T) {
	m := sized(t, 120, 40)
	for _, k := range []string{"ctrl+d", "ctrl+r", "ctrl+s", "ctrl+q", "ctrl+z"} {
		keyp(m, k)
		if m.leaderWait || m.panel != nil || m.side != nil || m.comp != nil || m.expanded {
			t.Errorf("%s did something: wait %v, panel %v, sidebar %v, palette %v, expanded %v", k, m.leaderWait, m.panel, m.side, m.comp, m.expanded)
		}
	}
}

// A configured leader replaces the default.
func TestConfiguredLeader(t *testing.T) {
	m := sized(t, 120, 40)
	m.leaderKey = "f2"
	keyp(m, "ctrl+x")
	if m.leaderWait {
		t.Fatal("ctrl+x still leads with f2 configured")
	}
	keyp(m, "f2")
	keyp(m, "e")
	if m.panel == nil || m.panel.kind != pError {
		t.Fatalf("f2 e: %v", m.panel)
	}
}

// Alt+letter goes straight to a panel only where the terminal tells it from
// Esc then the letter, and never for a letter the input edits words with.
func TestDirectKeys(t *testing.T) {
	m := sized(t, 120, 40)
	keyp(m, "alt+a")
	if m.panel != nil {
		t.Fatal("alt+a opened a panel on a legacy terminal")
	}
	m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	keyp(m, "alt+a")
	if m.panel == nil || m.panel.kind != pAgents {
		t.Fatalf("alt+a with key disambiguation: %v", m.panel)
	}
	keyp(m, "alt+a")
	keyp(m, "alt+c") // capitalise a word, in the input
	if m.panel != nil {
		t.Fatalf("alt+c opened %v", m.panel.kind)
	}
	if a := actionFor("c"); strings.Contains(m.actionKeys(a), "Alt") || !strings.Contains(m.actionKeys(actionFor("e")), "Alt+E") {
		t.Errorf("keys shown: c %q, e %q", m.actionKeys(a), m.actionKeys(actionFor("e")))
	}
}

// Below 160 columns a panel takes the transcript's place; from 160 it docks
// beside it. Either way every row fits, and Esc closes it.
func TestPanelPlacement(t *testing.T) {
	for _, s := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		m := sized(t, s[0], s[1])
		m.addInfo("TRANSCRIPT-MARKER")
		m.togglePanel(pPlan)
		v := strings.Join(fits(t, m.View().Content, s[0]), "\n")
		docked := s[0] >= panelRailMinW
		if !strings.Contains(v, "Plan") || !strings.Contains(v, "no plan data") {
			t.Errorf("%dx%d: no plan panel:\n%s", s[0], s[1], v)
		}
		if strings.Contains(v, "TRANSCRIPT-MARKER") != docked {
			t.Errorf("%dx%d: transcript shown %v, want %v (docked)", s[0], s[1], !docked, docked)
		}
		if docked && m.cw() != s[0]-panelRailW-1 {
			t.Errorf("%dx%d: transcript width %d", s[0], s[1], m.cw())
		}
		m.ta.SetValue("draft")
		keyp(m, "esc") // clears the input first
		if m.panel == nil || m.ta.Value() != "" {
			t.Fatalf("%dx%d: Esc with text typed: panel %v, input %q", s[0], s[1], m.panel, m.ta.Value())
		}
		keyp(m, "esc")
		if m.panel != nil || m.cw() != s[0] {
			t.Fatalf("%dx%d: Esc didn't close the panel (width %d)", s[0], s[1], m.cw())
		}
		if v := m.View().Content; !strings.Contains(v, "TRANSCRIPT-MARKER") {
			t.Errorf("%dx%d: the transcript didn't come back", s[0], s[1])
		}
	}
}

// One panel or the session sidebar beside the transcript, never both.
func TestPanelAndSidebar(t *testing.T) {
	m := sized(t, 200, 50)
	m.App.Sessions = nil // toggleSidebar needs sessions: give it none, then check the panel side
	m.togglePanel(pRouting)
	m.side = &sidebar{}
	m.togglePanel(pAgents)
	if m.side != nil || m.panel == nil {
		t.Fatalf("a panel didn't replace the sidebar: side %v panel %v", m.side, m.panel)
	}
}

// What each panel shows, from the events it follows.
func TestPanelContents(t *testing.T) {
	m := sized(t, 200, 50)
	view := func() string { return strings.Join(fits(t, m.View().Content, 200), "\n") }

	m.togglePanel(pAgents)
	if v := view(); !strings.Contains(v, "no subagents in this session yet") {
		t.Errorf("agents, none:\n%s", v)
	}
	m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(1, "lexer", "running", 3)})
	if v := view(); !strings.Contains(v, "lexer") || !strings.Contains(v, "last   grep Resize") || !strings.Contains(v, "1.2k in") {
		t.Errorf("agents, one running:\n%s", v)
	}

	m.togglePanel(pError)
	if v := view(); !strings.Contains(v, "no errors in this session") {
		t.Errorf("error, none:\n%s", v)
	}
	m.onAgent(agent.Event{Kind: agent.EvStatus, Text: "ollama/m1 failed (HTTP 503) — failing over"})
	m.onAgent(agent.Event{Kind: agent.EvError, Text: "HTTP 429: rate limit reached for requests"})
	if v := stripANSI(strings.Join(m.panel.lines, "\n")); !strings.Contains(v, "rate limit or quota ran out") || !strings.Contains(v, "HTTP 429: rate limit reached") ||
		!strings.Contains(v, "failing over") || !strings.Contains(v, "next: wait for the reset") {
		t.Errorf("error, after a 429:\n%s", v)
	}

	m.togglePanel(pRouting)
	if v := view(); !strings.Contains(v, "Routing") || !strings.Contains(v, "routing explanations need") {
		t.Errorf("routing without the status interface:\n%s", v)
	}

	m.togglePanel(pChanges)
	if v := view(); !strings.Contains(v, "no edits by ternly in this session") || !strings.Contains(v, "reading the diff") {
		t.Errorf("changes, none yet:\n%s", v)
	}
	m.onAgent(agent.Event{Kind: agent.EvToolStart, Tool: "edit_file", ToolID: "e1", Args: `{"path":"lru.go","old_string":"a","new_string":"b"}`})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, Tool: "edit_file", ToolID: "e1", OK: true})
	m.Update(panelDiffMsg{"  diff --git a/lru.go b/lru.go\n  +c.evictUntilFits()"})
	if v := view(); !strings.Contains(v, "1 file changed by ternly") || !strings.Contains(v, "  lru.go") || !strings.Contains(v, "+c.evictUntilFits()") {
		t.Errorf("changes, one edit:\n%s", v)
	}
}

// PgDn and PgUp scroll a long panel; its title stays.
func TestPanelScroll(t *testing.T) {
	m := sized(t, 120, 30)
	var d []string
	for i := range 100 {
		d = append(d, fmt.Sprintf("  +line %03d", i))
	}
	m.togglePanel(pChanges)
	m.Update(panelDiffMsg{strings.Join(d, "\n")})
	v := m.View().Content
	if !strings.Contains(v, "line 000") || strings.Contains(v, "line 090") || !strings.Contains(v, "more lines (pgdn)") {
		t.Fatalf("top of the panel:\n%s", stripANSI(v))
	}
	for range 10 {
		keyp(m, "pgdown")
	}
	v = m.View().Content
	if !strings.Contains(v, "Changes") || !strings.Contains(v, "line 099") || strings.Contains(v, "line 000") {
		t.Fatalf("after PgDn:\n%s", stripANSI(v))
	}
	for range 10 {
		keyp(m, "pgup")
	}
	if v = m.View().Content; !strings.Contains(v, "line 000") {
		t.Fatalf("after PgUp:\n%s", stripANSI(v))
	}
}

// The palette lists every panel with its keys, and /panel opens one.
func TestPalettePanels(t *testing.T) {
	m := sized(t, 120, 40)
	key(m, "ctrl+k")
	typeText(m, "rout")
	found := false
	for _, it := range m.comp.items {
		if it.name == "panel routing" && it.keys == "Ctrl+X r" {
			found = true
		}
	}
	if !found {
		t.Fatalf("palette: no routing panel with its keys: %+v", m.comp.items)
	}
	m.ta.Reset()
	m.comp = nil
	m.dispatch("/panel why")
	if m.panel == nil || m.panel.kind != pRouting {
		t.Fatalf("/panel why: %v", m.panel)
	}
}

// The linear (screen reader) mode prints the keys and the panels as text.
func TestPanelsLinear(t *testing.T) {
	m := sized(t, 120, 40)
	m.App.Access.ScreenReader = true
	keyp(m, "ctrl+x")
	if !strings.Contains(stripANSI(m.blocks[len(m.blocks)-1].text), "Ctrl+X, then: a agents, c changes") {
		t.Fatalf("linear leader: %q", m.blocks[len(m.blocks)-1].text)
	}
	keyp(m, "p")
	if m.panel != nil || !strings.Contains(stripANSI(m.blocks[len(m.blocks)-1].text), "no plan data") {
		t.Fatalf("linear panel: open %v, printed %q", m.panel, m.blocks[len(m.blocks)-1].text)
	}
}

// ASCII mode draws the popup in ASCII.
func TestWhichKeyASCII(t *testing.T) {
	m := sized(t, 80, 24)
	m.App.Access.ASCII = true
	keyp(m, "ctrl+x")
	v := strings.Join(fits(t, m.whichKeyView(), 80), "\n")
	if strings.ContainsAny(v, "╭╮╰╯─│") || !strings.Contains(v, "+-") {
		t.Fatalf("ASCII popup:\n%s", v)
	}
}

// A turn that ends with the changes panel open reads the diff again.
func TestChangesPanelRefreshesAfterTurn(t *testing.T) {
	m := sized(t, 120, 40)
	m.togglePanel(pChanges)
	m.Update(panelDiffMsg{"  old diff"})
	m.busy = true
	if c := m.onAgent(agent.Event{Kind: agent.EvDone}); c == nil || !m.panel.busy {
		t.Fatalf("EvDone with the changes panel open: cmd %v, reading %v", c, m.panel.busy)
	}
}

// The error panel's trail is the failing turn's, not an earlier turn's.
func TestErrorTrailIsThisTurns(t *testing.T) {
	m := sized(t, 120, 40)
	m.onAgent(agent.Event{Kind: agent.EvStatus, Text: "EARLIER-TURN failing over"})
	_ = m.startWith("next", "") // a new turn (its command, which runs the agent, isn't called)
	m.onAgent(agent.Event{Kind: agent.EvError, Text: "HTTP 500"})
	if len(m.lastErr.trail) != 0 {
		t.Fatalf("trail carried over from an earlier turn: %q", m.lastErr.trail)
	}
}

// After the turn that ran them, the agents panel still shows the last
// subagents, saying which turn they were in.
func TestAgentsPanelKeepsLastSubagents(t *testing.T) {
	m := sized(t, 120, 40)
	m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(1, "lexer", "done", 5)})
	_ = m.startWith("next", "")
	if len(m.lanes) != 0 {
		t.Fatal("lanes kept into the next turn")
	}
	m.togglePanel(pAgents)
	v := stripANSI(strings.Join(m.panel.lines, "\n"))
	if !strings.Contains(v, "none in this turn; the last ones, in turn") || !strings.Contains(v, "lexer") {
		t.Fatalf("agents panel in the next turn:\n%s", v)
	}
}
