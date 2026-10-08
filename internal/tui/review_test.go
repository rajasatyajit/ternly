package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/surface"
)

func proposal() surface.EditProposal {
	return surface.EditProposal{Tool: "write_file", Path: "cart.go", Hunks: []surface.Hunk{
		{OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2, Lines: []string{" package cart", "-old one", "+new one"}},
		{OldStart: 20, OldLines: 1, NewStart: 20, NewLines: 1, Lines: []string{"-old two \x1b]52;c;eA==\x07", "+new two"}},
		{OldStart: 40, OldLines: 1, NewStart: 40, NewLines: 1, Lines: []string{"-old three", "+new three"}},
	}}
}

func openReviewFor(t *testing.T, p surface.EditProposal) (*Model, chan surface.EditDecision) {
	t.Helper()
	m := testModel(t)
	reply := make(chan surface.EditDecision, 1)
	m.Update(reviewMsg{p: p, reply: reply})
	return m, reply
}

func press(m *Model, code rune, text string) {
	m.Update(tea.KeyPressMsg{Code: code, Text: text})
}

// The dialog shows the hunk under the cursor; Space toggles it; y sends the
// selection; the transcript says what happened.
func TestReviewDialog(t *testing.T) {
	m, reply := openReviewFor(t, proposal())
	v := m.View().Content
	if !strings.Contains(v, "Edit cart.go?") || !strings.Contains(v, "hunk 1 of 3") || !strings.Contains(v, "-old one") || !strings.Contains(v, "+new one") {
		t.Fatalf("dialog:\n%s", v)
	}
	press(m, tea.KeyDown, "")
	if v := m.View().Content; !strings.Contains(v, "hunk 2 of 3") || strings.Contains(v, "\x1b]52") || !strings.Contains(v, `\x1b]52`) {
		t.Fatalf("second hunk (escaped):\n%s", v)
	}
	if raw := m.reviewView(); strings.Contains(raw, "\x1b]52") { // the dialog's own layer, before the frame filter
		t.Fatal("a diff line reached the dialog unescaped")
	}
	press(m, tea.KeySpace, " ")
	press(m, 'y', "y")
	d := <-reply
	if len(d.Apply) != 3 || !d.Apply[0] || d.Apply[1] || !d.Apply[2] || d.Always {
		t.Fatalf("decision %+v", d)
	}
	if m.review != nil || !strings.Contains(m.View().Content, "applied 2 of 3 hunks to cart.go") {
		t.Fatal("dialog not closed or not reported")
	}
}

// n declines everything; a also asks to stop asking.
func TestReviewDeclineAndAlwaysKeys(t *testing.T) {
	m, reply := openReviewFor(t, proposal())
	press(m, 'n', "n")
	if d := <-reply; d.Apply[0] || d.Apply[1] || d.Apply[2] {
		t.Fatalf("n: %+v", d)
	}
	m.Update(reviewMsg{p: proposal(), reply: reply})
	press(m, 'a', "a")
	if d := <-reply; !d.Always || !d.Apply[1] {
		t.Fatalf("a: %+v", d)
	}
}

// A restricted model's edit names the reason; while the dialog waits,
// nothing animates and the cursor isn't drawn in the input box.
func TestReviewWaitsStill(t *testing.T) {
	p := proposal()
	p.Why = "ollama/qwen3.6 is measured as easily baited"
	m := testModel(t)
	m.busy, m.discovering = true, false // the turn is running when a review opens
	m.Update(reviewMsg{p: p, reply: make(chan surface.EditDecision, 1)})
	m.ticking = false
	m.Update(tickMsg(time.Now()))
	v := m.View()
	if m.ticking || !strings.Contains(v.Content, "easily baited") || !strings.Contains(v.Content, "Waiting for your answer") || v.Cursor != nil {
		t.Fatalf("waiting: ticking=%v cursor=%v\n%s", m.ticking, v.Cursor, v.Content)
	}
}
