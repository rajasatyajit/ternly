package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/surface"
)

// Per-hunk review of an edit (ADR 021 amendment 1, ADR 023): the proposed
// change, hunk by hunk; Space toggles the hunk under the cursor, y applies
// the selected ones, a also stops asking for edits this session (ignored for
// a model whose trust is lost), n or Esc declines.

type reviewMsg struct {
	p     surface.EditProposal
	reply chan surface.EditDecision
}

type reviewState struct {
	reviewMsg
	sel []bool
	cur int
}

// Reviewer bridges the core's edit review (agent goroutine) to the UI.
func Reviewer(p *tea.Program) surface.Reviewer {
	return func(ctx context.Context, pr surface.EditProposal) surface.EditDecision {
		reply := make(chan surface.EditDecision, 1)
		p.Send(reviewMsg{p: pr, reply: reply})
		select {
		case d := <-reply:
			return d
		case <-ctx.Done():
			return surface.EditDecision{Apply: make([]bool, len(pr.Hunks))}
		}
	}
}

func (m *Model) openReview(msg reviewMsg) {
	r := &reviewState{reviewMsg: msg, sel: make([]bool, len(msg.p.Hunks))}
	for i := range r.sel {
		r.sel[i] = true // everything proposed is selected; the person removes what they don't want
	}
	m.review = r
	m.layout()
	m.refresh(true)
}

func (m *Model) reviewKey(k tea.KeyPressMsg) tea.Cmd {
	r := m.review
	answer := func(apply []bool, always bool) {
		r.reply <- surface.EditDecision{Apply: apply, Always: always}
		n := 0
		for _, a := range apply {
			if a {
				n++
			}
		}
		verb := fmt.Sprintf("applied %d of %d hunks to %s", n, len(apply), untrusted(r.p.Path))
		if n == 0 {
			verb = "declined the edit to " + untrusted(r.p.Path)
		}
		m.review = nil
		m.addInfo(sDim.Render("  " + verb))
		m.layout()
	}
	switch k.String() {
	case "up", "k", "shift+tab":
		r.cur = (r.cur + len(r.sel) - 1) % len(r.sel)
	case "down", "j", "tab":
		r.cur = (r.cur + 1) % len(r.sel)
	case "space", " ":
		r.sel[r.cur] = !r.sel[r.cur]
	case "y", "enter":
		answer(append([]bool(nil), r.sel...), false)
	case "a", "A":
		answer(append([]bool(nil), r.sel...), true)
	case "n", "N", "esc", "ctrl+c":
		answer(make([]bool, len(r.sel)), false)
	}
	return nil
}

// reviewRows bounds the diff shown for the current hunk.
const reviewRows = 14

func (m *Model) reviewView() string {
	r := m.review
	p := r.p
	n := 0
	for _, s := range r.sel {
		if s {
			n++
		}
	}
	title := fmt.Sprintf("Edit %s?  hunk %d of %d · %d selected", untrusted(p.Path), r.cur+1, len(p.Hunks), n)
	if p.NewFile {
		title = fmt.Sprintf("Create %s?  %d selected of %d", untrusted(p.Path), n, len(p.Hunks))
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(title) + "\n")
	if p.Why != "" {
		b.WriteString(sWarn.Render(untrusted(p.Why)) + "\n")
	}
	h := p.Hunks[r.cur]
	mark := map[bool]string{true: "[x]", false: "[ ]"}[r.sel[r.cur]]
	b.WriteString(sAccent.Render(fmt.Sprintf("%s @@ -%d,%d +%d,%d @@", mark, h.OldStart, h.OldLines, h.NewStart, h.NewLines)) + "\n")
	for i, l := range h.Lines {
		if i == reviewRows {
			b.WriteString(sDim.Render(fmt.Sprintf("… %d more lines in this hunk", len(h.Lines)-reviewRows)) + "\n")
			break
		}
		l = truncate(untrusted(l), max(10, m.w-8))
		switch {
		case strings.HasPrefix(l, "+"):
			l = sOK.Render(l)
		case strings.HasPrefix(l, "-"):
			l = sErr.Render(l)
		default:
			l = sDim.Render(l)
		}
		b.WriteString(l + "\n")
	}
	var marks []string
	for i, s := range r.sel {
		c := map[bool]string{true: "x", false: " "}[s]
		if i == r.cur {
			marks = append(marks, sAccent.Render(fmt.Sprintf("›%d[%s]", i+1, c)))
		} else {
			marks = append(marks, fmt.Sprintf(" %d[%s]", i+1, c))
		}
	}
	if len(marks) > 1 {
		b.WriteString(strings.Join(marks, " ") + "\n")
	}
	b.WriteString(sOK.Render("[y]") + " apply selected   " + sAccent.Render("[a]") + " always   " + sErr.Render("[n]") + " decline   " +
		sDim.Render("space toggles · ↑↓ hunks"))
	border := cAmber
	if p.Why != "" {
		border = cRed
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(m.w - 2).Render(b.String())
}

// reviewHeight is the dialog's height, for the layout.
func (m *Model) reviewHeight() int {
	if m.review == nil {
		return 0
	}
	return strings.Count(m.reviewView(), "\n") + 1
}

// reviewViewPlain is the review dialog for the accessible mode: no borders,
// the whole question in words.
func (m *Model) reviewViewPlain() string {
	if m.review == nil {
		return ""
	}
	r := m.review
	var b strings.Builder
	fmt.Fprintf(&b, "Edit %s: hunk %d of %d, %s.", untrusted(r.p.Path), r.cur+1, len(r.p.Hunks), map[bool]string{true: "selected", false: "not selected"}[r.sel[r.cur]])
	if r.p.Why != "" {
		b.WriteString(" " + untrusted(r.p.Why) + ".")
	}
	b.WriteString("\n")
	for i, l := range r.p.Hunks[r.cur].Lines {
		if i == reviewRows {
			fmt.Fprintf(&b, "%d more lines in this hunk.\n", len(r.p.Hunks[r.cur].Lines)-reviewRows)
			break
		}
		word := map[byte]string{'+': "added: ", '-': "removed: ", ' ': "unchanged: "}[untrusted(l)[0]]
		b.WriteString(word + untrusted(l)[1:] + "\n")
	}
	b.WriteString("y applies the selected hunks, a also stops asking, n declines; Space toggles this hunk, arrows move between hunks.")
	return b.String()
}
