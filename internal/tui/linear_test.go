package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/surface"
	"github.com/rajasatyajit/ternly/internal/tools"
)

func linearModel(t *testing.T) *Model {
	t.Helper()
	m := testModel(t)
	m.App.Access = Access{ScreenReader: true, ReducedMotion: true}
	m.discovering = false
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

// printed collects what a command printed to the scrollback (tea.Println).
func printed(c tea.Cmd) string {
	if c == nil {
		return ""
	}
	switch msg := c().(type) {
	case tea.BatchMsg:
		var out []string
		for _, cc := range msg {
			out = append(out, printed(cc))
		}
		return strings.Join(out, "")
	default:
		s := fmt.Sprintf("%+v", msg)
		if strings.Contains(s, "messageBody") {
			return s
		}
	}
	return ""
}

// The accessible mode is linear: no alternate screen, no box drawing;
// finished blocks go to the scrollback once and leave the live frame; an
// answer still streaming stays live until the turn ends.
func TestLinearMode(t *testing.T) {
	m := linearModel(t)
	v := m.View()
	if v.AltScreen || strings.ContainsAny(v.Content, "╭╮╰╯│─") || !strings.Contains(v.Content, "> ") {
		t.Fatalf("linear frame (alt=%v):\n%s", v.AltScreen, v.Content)
	}
	m.busy = true
	var out string
	_, c := m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "read_file", ToolID: "1", Text: "cart.go"}))
	out += printed(c)
	if !strings.Contains(m.View().Content, "[running]") {
		t.Fatal("a running tool should be live")
	}
	_, c = m.Update(agentMsg(agent.Event{Kind: agent.EvToolEnd, Tool: "read_file", ToolID: "1", OK: true}))
	out += printed(c)
	_, c = m.Update(agentMsg(agent.Event{Kind: agent.EvText, Text: "The answer \x1b]0;pwned\x07 is here."}))
	out += printed(c)
	if strings.Contains(out, "The answer") || !strings.Contains(m.View().Content, "The answer") {
		t.Fatal("a streaming answer must stay live, not go to the scrollback")
	}
	_, c = m.Update(agentMsg(agent.Event{Kind: agent.EvDone}))
	out += printed(c)
	if strings.Count(out, "The answer") != 1 || strings.Count(out, "cart.go") != 1 {
		t.Fatalf("scrollback should hold each finished block once:\n%s", out)
	}
	if strings.Contains(out, "\x1b]0") {
		t.Fatal("text printed to the scrollback wasn't escaped")
	}
	// an info line quoting outside text (ternly styles these, so renderBlock
	// doesn't escape them): the scrollback filter must
	m.addInfo("  git says: \x1b]0;pwned\x07")
	_, c = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if p := printed(c); !strings.Contains(p, "git says") || strings.Contains(p, "\x1b]0") {
		t.Fatalf("info line in the scrollback: %q", p)
	}
	if strings.Contains(m.View().Content, "The answer") {
		t.Fatal("a printed block is still in the live frame")
	}
}

// Dialogs are sentences: the permission question and the edit review.
func TestLinearDialogs(t *testing.T) {
	m := linearModel(t)
	m.Update(permMsg{tool: "bash", summary: "go test ./...", reply: make(chan tools.Decision, 1)})
	if v := m.View().Content; !strings.Contains(v, "Allow Bash? go test ./...") || !strings.Contains(v, "y yes, a always, n no.") || strings.ContainsAny(v, "╭│") {
		t.Fatalf("permission:\n%s", v)
	}
	m.perm = nil
	m.Update(reviewMsg{p: surface.EditProposal{Path: "a.go", Hunks: []surface.Hunk{{Lines: []string{"-old", "+new", " same"}}}}, reply: make(chan surface.EditDecision, 1)})
	v := m.View().Content
	for _, want := range []string{"Edit a.go: hunk 1 of 1, selected.", "removed: old", "added: new", "unchanged: same", "Space toggles"} {
		if !strings.Contains(v, want) {
			t.Errorf("review lacks %q:\n%s", want, v)
		}
	}
}
