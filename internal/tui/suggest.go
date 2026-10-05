package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/capability"
)

type suggestMsg struct{ s *capability.Suggestion }

type suggestion struct {
	s   *capability.Suggestion
	sel int
}

// detectAfterTurn runs capability detection off the UI goroutine.
func (m *Model) detectAfterTurn() tea.Cmd {
	caps := m.App.Capabilities
	if caps == nil {
		return nil
	}
	prompt, answer, cmds, outs := m.App.Agent.LastTurn()
	if prompt == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return suggestMsg{caps.AfterTurn(ctx, capability.Turn{Prompt: prompt, Answer: answer, Commands: cmds, ToolOutputs: outs})}
	}
}

// onSuggest shows a suggestion at a turn boundary (later, if a turn is running).
func (m *Model) onSuggest(s *capability.Suggestion) {
	if s == nil {
		return
	}
	if m.busy || m.perm != nil || m.picker != nil {
		m.laterSuggest = s
		return
	}
	m.suggest = &suggestion{s: s}
	m.layout()
	m.refresh(true)
}

func (m *Model) suggestKey(k tea.KeyPressMsg) tea.Cmd {
	sg := m.suggest
	n := len(sg.s.Candidates)
	switch key := k.String(); key {
	case "up", "ctrl+p":
		sg.sel = (sg.sel + n - 1) % n
	case "down", "ctrl+n", "tab":
		sg.sel = (sg.sel + 1) % n
	case "1", "2", "3":
		if i := int(key[0] - '1'); i < n {
			sg.sel = i
		}
	case "enter", "y":
		c := sg.s.Candidates[sg.sel]
		m.suggest = nil
		m.layout()
		m.addInfo(sDim.Render("  fetching " + c.Name + " for review…"))
		st := m.App.Plugins.Store
		return func() tea.Msg {
			t0 := time.Now()
			p, err := capability.Prepare(context.Background(), st, c.Entry)
			return pluginReviewMsg{p: p, t0: t0, err: err}
		}
	case "n", "esc":
		m.suggest = nil
		m.addInfo(sDim.Render("  not now — ternly won't suggest this again in this session"))
	case "d":
		need := sg.s.Need
		m.suggest = nil
		if err := m.App.Capabilities.Suggester.Dismiss(need.Key); err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sDim.Render("  ok — no more suggestions for " + need.Label + " in this project"))
		}
	}
	m.layout()
	return nil
}

func (m *Model) suggestView() string {
	sg := m.suggest
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("ternly can add "+sg.s.Need.Label) + sDim.Render(" — "+sg.s.Need.Why) + "\n")
	for i, c := range sg.s.Candidates {
		mark := "  "
		if i == sg.sel {
			mark = sAccent.Render("› ")
		}
		ver := c.Version
		if ver == "" {
			ver = "pinned at install"
		}
		b.WriteString(fmt.Sprintf("%s%d %s %s\n", mark, i+1, sAccent.Render(c.Name), sDim.Render(fmt.Sprintf("%s · by %s · %s · %s", c.Kind, orStr(c.Publisher, "?"), ver, c.Reason))))
		b.WriteString(sDim.Render(fmt.Sprintf("     %s · runs: %s", truncate(c.Description, max(20, m.w-60)), c.Runs)) + "\n")
	}
	b.WriteString(sOK.Render("[enter]") + " review & install   " + sAccent.Render("[1-3]") + " choose   " + sDim.Render("[n] not now   [d] never for "+sg.s.Need.Label+" here"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(m.w - 2).Render(b.String())
}

// cmdPluginSearch searches the capability catalog by hand.
func (m *Model) pluginSearch(q string) tea.Cmd {
	caps := m.App.Capabilities
	if caps == nil || caps.Catalog == nil {
		m.addInfo(sDim.Render("  the capability catalog is off"))
		return nil
	}
	return func() tea.Msg {
		t0 := time.Now()
		cs := caps.Catalog.Search(capability.Need{Key: q, Label: q, Query: q}, 8)
		if len(cs) == 0 {
			return pluginDoneMsg{text: sDim.Render(fmt.Sprintf("  nothing for %q in %d catalog entries (/plugin catalog refresh)", q, caps.Catalog.Len()))}
		}
		var b strings.Builder
		for i, c := range cs {
			fmt.Fprintf(&b, "  %d %s %s\n     %s\n", i+1, sAccent.Render(c.Name), sDim.Render(fmt.Sprintf("%s · %s · %s", c.Kind, c.Reason, c.Runs)), truncate(c.Description, max(20, m.w-8)))
		}
		fmt.Fprintf(&b, "%s", sDim.Render(fmt.Sprintf("  %d entries searched in %s · install: /plugin add <name>@<marketplace>, a git URL, or accept a suggestion", caps.Catalog.Len(), time.Since(t0).Round(time.Microsecond))))
		return pluginDoneMsg{text: b.String()}
	}
}
