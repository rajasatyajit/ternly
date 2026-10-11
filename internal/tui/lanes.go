package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
)

// Agent lanes (ADR 030, level 0): while subagents run, and after, until the
// next prompt, each one's state is on screen above the input: its name, its
// model, how far it got, what it's doing, its tokens and cost, its time. At
// 100 columns or more: a row per agent (three at most; the rest summed up in
// the last). Narrower: one row for all of them. A lane's name, model and
// activity are outside text (the agent definition, a tool summary).

const maxLaneRows = 3

// onSubagent keeps the latest state of each subagent of this turn.
func (m *Model) onSubagent(s *agent.SubState) {
	if s == nil {
		return
	}
	for i, l := range m.lanes {
		if l.ID == s.ID {
			m.lanes[i] = s
			return
		}
	}
	m.lanes = append(m.lanes, s)
	sort.Slice(m.lanes, func(i, j int) bool { return m.lanes[i].ID < m.lanes[j].ID })
	m.layout() // one more row above the input
}

// laneRows is how many rows the lanes take at the current width.
func (m *Model) laneRows() int {
	switch n := len(m.lanes); {
	case n == 0:
		return 0
	case m.w < 100:
		return 1
	default:
		return min(n, maxLaneRows)
	}
}

func (m *Model) laneGlyph(state string) string {
	g := map[string][2]string{"queued": {"○", "o"}, "running": {"●", "*"}, "done": {"✓", "ok"}, "failed": {"✗", "x"}}[state]
	s := g[0]
	if m.App.Access.ASCII {
		s = g[1]
	}
	switch state {
	case "done":
		return sOK.Render(s)
	case "failed":
		return sErr.Render(s)
	case "running":
		return sAccent.Render(s)
	}
	return sDim.Render(s)
}

func laneTime(s *agent.SubState) time.Duration {
	switch {
	case s.State == "running" && !s.Started.IsZero():
		return time.Since(s.Started)
	case s.Took > 0:
		return s.Took
	}
	return 0
}

// lanesView renders the lanes, laneRows() rows, each within the width.
func (m *Model) lanesView() string {
	n := len(m.lanes)
	if n == 0 {
		return ""
	}
	w := max(10, m.w-2)
	if m.w < 100 { // one row for all
		parts := make([]string, 0, n)
		for _, s := range m.lanes {
			p := m.laneGlyph(s.State) + " " + untrusted(s.Name)
			if s.State == "running" || s.State == "queued" {
				p += sDim.Render(fmt.Sprintf(" %d", s.Step))
			}
			parts = append(parts, p)
		}
		head := sDim.Render(fmt.Sprintf("%d agents: ", n))
		return " " + fitLines(head+strings.Join(parts, sDim.Render(" · ")), w)
	}
	rows := make([]string, 0, maxLaneRows)
	show := n
	if n > maxLaneRows {
		show = maxLaneRows - 1
	}
	g := m.glyph()
	for _, s := range m.lanes[:show] {
		model := s.Model
		if model == "" {
			model = "routing…"
		}
		fields := []string{
			m.laneGlyph(s.State) + " " + lipgloss.NewStyle().Bold(true).Render(untrusted(s.Name)),
			sDim.Render(untrusted(model)),
			sDim.Render(fmt.Sprintf("step %d", s.Step)),
		}
		if s.Doing != "" && s.State == "running" {
			fields = append(fields, untrusted(s.Doing))
		}
		fields = append(fields, sDim.Render(fmt.Sprintf("%s%s %s%s", kfmt(s.In), g.in, kfmt(s.Out), g.out)),
			sDim.Render(fmt.Sprintf("$%.4f", s.Cost)), sDim.Render(durShort(laneTime(s))))
		rows = append(rows, " "+fitLines(strings.Join(fields, sDim.Render(g.sep)), w))
	}
	if rest := m.lanes[show:]; len(rest) > 0 {
		count := map[string]int{}
		for _, s := range rest {
			count[s.State]++
		}
		var parts []string
		for _, st := range []string{"running", "queued", "done", "failed"} {
			if count[st] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", count[st], st))
			}
		}
		rows = append(rows, " "+sDim.Render(fitLines(fmt.Sprintf("+%d more: %s", len(rest), strings.Join(parts, ", ")), w)))
	}
	return strings.Join(rows, "\n")
}
