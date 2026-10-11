package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/surface"
)

func lane(id int, name, state string, step int) *agent.SubState {
	return &agent.SubState{ID: id, Name: name, State: state, Model: "ollama/gemma4:latest", Step: step, Doing: "grep Resize",
		In: 1200, Out: 80, Cost: 0, Started: time.Now().Add(-42 * time.Second), Took: 12 * time.Second}
}

// Lanes show each subagent's state above the input: a row per agent at
// 100 columns or more, one row for all below; within the width; the
// transcript gives up the rows.
func TestLanes(t *testing.T) {
	for _, s := range [][2]int{{200, 50}, {120, 40}, {80, 24}} {
		m := sized(t, s[0], s[1])
		h0 := m.vp.h
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(1, "lexer", "running", 3)})
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(2, "tests", "done", 5)})
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(1, "lexer", "running", 4)}) // an update, not a new lane
		v := strings.Join(fits(t, m.View().Content, s[0]), "\n")
		if len(m.lanes) != 2 {
			t.Fatalf("%dx%d: %d lanes, want 2", s[0], s[1], len(m.lanes))
		}
		if s[0] >= 100 {
			for _, want := range []string{"● lexer", "ollama/gemma4:latest", "step 4", "grep Resize", "42s", "✓ tests", "12s"} {
				if !strings.Contains(v, want) {
					t.Errorf("%dx%d: lanes lack %q:\n%s", s[0], s[1], want, v)
				}
			}
			if m.vp.h != h0-2 {
				t.Errorf("%dx%d: the transcript is %d rows, want %d (two lanes)", s[0], s[1], m.vp.h, h0-2)
			}
		} else {
			if !strings.Contains(v, "2 agents: ● lexer 4 · ✓ tests") {
				t.Errorf("%dx%d: the summary row:\n%s", s[0], s[1], v)
			}
			if m.vp.h != h0-1 {
				t.Errorf("%dx%d: the transcript is %d rows, want %d (one row)", s[0], s[1], m.vp.h, h0-1)
			}
		}
	}
}

// More than three: two rows and a count; ASCII glyphs; a new prompt clears
// them; names and activity are shown as text.
func TestLanesMoreASCIIAndCleared(t *testing.T) {
	m := sized(t, 120, 40)
	for i, st := range []string{"running", "running", "queued", "done", "failed"} {
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(i+1, "a"+string(rune('0'+i)), st, i)})
	}
	v := stripANSI(m.lanesView())
	if strings.Count(v, "\n")+1 != maxLaneRows || !strings.Contains(v, "+3 more: 1 queued, 1 done, 1 failed") {
		t.Errorf("five lanes:\n%s", v)
	}
	m.App.Access.ASCII = true
	for _, r := range stripANSI(m.lanesView()) {
		if r > 126 {
			t.Fatalf("ASCII lanes: %q", stripANSI(m.lanesView()))
		}
	}
	for _, w := range []int{120, 80} { // a row of its own, and the summary row
		h := sized(t, w, 40)
		h.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: &agent.SubState{ID: 9, Name: "evil\x1b[2J", State: "running", Doing: "x\x1b]0;t\x07"}})
		if v := h.lanesView(); strings.Contains(v, "\x1b[2J") || strings.Contains(v, "\x1b]0;") || !strings.Contains(stripANSI(v), `evil\x1b[2J`) {
			t.Errorf("%d columns: a lane passed a control sequence through: %q", w, v)
		}
	}
	_ = m.startWith("next", "") // not run: only the turn's start state
	if len(m.lanes) != 0 || m.laneRows() != 0 {
		t.Error("a new prompt should clear the last turn's lanes")
	}
}

// A pending approval says what and why it asks.
func TestApprovalSaysWhy(t *testing.T) {
	m := sized(t, 120, 40)
	m.perm = &permMsg{tool: "bash", summary: "rm -rf build"}
	m.layout()
	v := strings.Join(fits(t, m.View().Content, 120), "\n")
	for _, want := range []string{"Allow", "rm -rf build", "why: the permission mode is"} {
		if !strings.Contains(v, want) {
			t.Errorf("the approval lacks %q:\n%s", want, v)
		}
	}
	if got := strings.Count(m.View().Content, "\n") + 1; got > 40 {
		t.Errorf("the frame has %d rows with the approval open", got)
	}
}

// The per-hunk review of an edit says why it asks too, when the core gives
// no reason of its own; a reason the core gives is shown instead.
func TestReviewSaysWhy(t *testing.T) {
	m := sized(t, 120, 40)
	p := surface.EditProposal{Tool: "edit_file", Path: "lru.go", Hunks: []surface.Hunk{{OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, Lines: []string{"-a", "+b"}}}}
	m.review = &reviewState{reviewMsg: reviewMsg{p: p}, sel: []bool{true}}
	if v := stripANSI(m.reviewView()); !strings.Contains(v, "Edit lru.go?") || !strings.Contains(v, "why: the permission mode is") {
		t.Errorf("the review lacks what or why:\n%s", v)
	}
	p.Why = "the model's trust is lost: every edit is reviewed"
	m.review = &reviewState{reviewMsg: reviewMsg{p: p}, sel: []bool{true}}
	if v := stripANSI(m.reviewView()); !strings.Contains(v, "trust is lost") || strings.Contains(v, "why: the permission mode") {
		t.Errorf("the core's reason should replace the generic one:\n%s", v)
	}
}
