package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/session"
)

// The session sidebar (ADR 023): Ctrl+B shows the project's sessions beside
// the transcript and focuses it; ↑↓ select, Enter switches, Esc returns to
// the input; Ctrl+B while it's focused hides it. It needs sidebarMinW
// columns; the list is re-read when it opens and when a turn or a switch
// ends, never per frame.

const (
	sidebarW    = 32
	sidebarMinW = 100
)

type sidebar struct {
	list    []session.Meta
	sel     int
	focused bool
	err     string
}

// sideShown: the sidebar takes room this frame.
func (m *Model) sideShown() bool {
	return m.side != nil && m.w >= sidebarMinW && !m.linear()
}

// cw is the transcript's width: the window, less the sidebar when shown.
func (m *Model) cw() int {
	if m.sideShown() {
		return m.w - sidebarW - 1
	}
	return m.w
}

func (m *Model) reloadSidebar() {
	if m.side == nil || m.App.Sessions == nil {
		return
	}
	list, err := m.App.Sessions.Project.List()
	m.side.err = ""
	if err != nil {
		m.side.err = err.Error()
	}
	m.side.list = list
	m.side.sel = min(m.side.sel, max(0, len(list)-1))
}

// toggleSidebar is Ctrl+B.
func (m *Model) toggleSidebar() {
	switch {
	case m.App.Sessions == nil:
		m.addInfo(sDim.Render("  sessions aren't saved here"))
		return
	case m.linear():
		m.addInfo("  the session list: /resume")
		return
	case m.side == nil:
		m.side = &sidebar{focused: true}
		m.reloadSidebar()
		if cur := m.App.Sessions.Current(); cur != nil {
			for i, s := range m.side.list {
				if s.ID == cur.ID {
					m.side.sel = i
				}
			}
		}
		if m.w < sidebarMinW {
			m.addInfo(sDim.Render(fmt.Sprintf("  the sidebar needs %d columns (this window has %d); /resume lists sessions", sidebarMinW, m.w)))
		}
	case m.side.focused:
		m.side = nil
	default:
		m.side.focused = true
	}
	m.rewrap()
}

// rewrap re-renders the transcript for a changed content width.
func (m *Model) rewrap() {
	if m.w > 0 {
		m.newMarkdown()
	}
	for _, b := range m.blocks {
		b.rendered = ""
	}
	m.layout()
	m.refresh(true)
}

func (m *Model) sidebarKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	s := m.side
	switch k.String() {
	case "up", "k":
		s.sel = max(0, s.sel-1)
	case "down", "j":
		s.sel = min(len(s.list)-1, s.sel+1)
	case "esc":
		s.focused = false
	case "enter":
		s.focused = false
		if s.sel < len(s.list) {
			if cur := m.App.Sessions.Current(); cur != nil && s.list[s.sel].ID == cur.ID {
				return nil, true
			}
			return m.switchTo(s.list[s.sel].ID), true
		}
	case "ctrl+b":
		return nil, false // handled by onKey
	default:
		return nil, true // keys don't reach the input while the sidebar has focus
	}
	return nil, true
}

func (m *Model) sidebarView(h int) string {
	s := m.side
	cur := ""
	if c := m.App.Sessions.Current(); c != nil {
		cur = c.ID
	}
	title := "Sessions"
	if s.focused {
		title += sDim.Render("  ↑↓ enter esc")
	} else {
		title += sDim.Render("  ctrl+b")
	}
	rows := []string{lipgloss.NewStyle().Bold(true).Render(title)}
	if s.err != "" {
		rows = append(rows, sErr.Render(truncate(s.err, sidebarW-2)))
	}
	per := 2
	first := max(0, min(s.sel-(h-2)/per/2, len(s.list)-(h-2)/per))
	for i := first; i < len(s.list) && len(rows)+per <= h; i++ {
		e := s.list[i]
		cursor, here := " ", " " // two columns: the cursor, and the current session
		if e.ID == cur {
			here = sOK.Render("●")
		}
		name := truncate(untrusted(orStr(e.Title, "(untitled)")), sidebarW-4)
		if i == s.sel && s.focused {
			cursor, name = sAccent.Render("›"), sAccent.Render(name)
		}
		rows = append(rows, cursor+here+" "+name, sDim.Render(fmt.Sprintf("   %s · %d turns", ago(e.Active), e.Turns)))
	}
	if len(s.list) == 0 && s.err == "" {
		rows = append(rows, sDim.Render("no sessions yet"))
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return lipgloss.NewStyle().Width(sidebarW).Render(strings.Join(rows[:h], "\n"))
}

// besideSidebar joins the transcript and the sidebar, row by row.
func (m *Model) besideSidebar(transcript string) string {
	h := strings.Count(transcript, "\n") + 1
	sep := sDim.Render(strings.TrimRight(strings.Repeat("│\n", h), "\n"))
	left := lipgloss.NewStyle().Width(m.cw()).Render(transcript)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, sep, m.sidebarView(h))
}
