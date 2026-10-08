package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/tools"
)

func TestUntrusted(t *testing.T) {
	for in, want := range map[string]string{
		"plain text\n\twith tab":     "plain text\n\twith tab",
		"héllo — ünïcode ✓":          "héllo — ünïcode ✓",
		"a\x1b[2Kb":                  `a\x1b[2Kb`,
		"\x1b]52;c;cm0gLXJmIH4=\x07": `\x1b]52;c;cm0gLXJmIH4=\x07`,
		"line\r\nnext":               "line\nnext",
		"over\rwrite":                `over\x0dwrite`,
		"del\x7f":                    `del\x7f`,
		"c1\u009b31m":                `c1\x9b31m`,
		"bad\xffutf8":                "bad�utf8",
		"rm -rf ~ \u202e# safe":      "rm -rf ~ \\u202e# safe",
		"isolate\u2066x\u2069":       "isolate\\u2066x\\u2069",
	} {
		if got := untrusted(in); got != want {
			t.Errorf("untrusted(%q) = %q, want %q", in, got, want)
		}
	}
}

// Hostile sequences from the model, a tool, a provider error or a command
// awaiting permission never reach the terminal as sequences.
func TestNoInjectedSequencesOnScreen(t *testing.T) {
	m := testModel(t)
	payloads := []string{
		"\x1b]52;c;cm0gLXJmIH4=\x07", // OSC 52: write the clipboard
		"\x1b]0;pwned\x07",           // set the window title
		"\x1b[2K\x1b[1A",             // erase and move up: overwrite the dialog
		"\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", // hidden hyperlink
		"\u009b2J", // C1 CSI: clear the screen
	}
	all := strings.Join(payloads, " ")
	m.Update(agentMsg(agent.Event{Kind: agent.EvText, Text: "answer " + all}))
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "bash", ToolID: "1", Text: "ls " + all}))
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolEnd, Tool: "bash", ToolID: "1", Text: "out " + all}))
	m.Update(agentMsg(agent.Event{Kind: agent.EvError, Text: "provider said " + all}))
	m.Update(agentMsg(agent.Event{Kind: agent.EvStatus, Text: "status " + all}))
	m.Update(permMsg{tool: "bash", summary: "rm -rf ~ " + all, reply: make(chan tools.Decision, 1)})
	screen := m.View().Content
	for _, p := range payloads {
		raw := strings.Split(p, ";")[0] // the introducer alone: ESC] / ESC[ / C1 CSI
		if len(raw) > 4 {
			raw = raw[:4]
		}
		if strings.Contains(screen, raw) {
			t.Errorf("injected sequence %q reached the screen", raw)
		}
	}
	if !strings.Contains(screen, `\x1b]52`) {
		t.Error("the escaped form should be visible, so a person sees something odd")
	}
}

func testModel(t *testing.T) *Model {
	t.Helper()
	m := benchModel(t, 0)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	return m
}
