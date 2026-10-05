package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/session"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// transcriptTurns is how many of a resumed session's turns are rendered;
// earlier ones stay in the model's context and in /export.
const transcriptTurns = 40

type (
	openPickerMsg struct{}
	switchedMsg   struct {
		st   agent.State
		verb string
	}
)

// picker is the /sessions fuzzy chooser.
type picker struct {
	all    []session.Meta
	filter string
	sel    int
}

// items returns the sessions matching the filter, best match first (recency breaks ties).
func (p *picker) items() []session.Meta {
	type scored struct {
		m     session.Meta
		score int
	}
	var out []scored
	for _, m := range p.all {
		if s, ok := fuzzy(p.filter, m.Title+" "+m.ID+" "+m.Status); ok {
			out = append(out, scored{m, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score < out[j].score })
	ms := make([]session.Meta, len(out))
	for i, s := range out {
		ms[i] = s.m
	}
	return ms
}

// fuzzy reports whether query's letters appear in order in text; lower scores
// mean tighter matches (fewer gaps, earlier start).
func fuzzy(query, text string) (int, bool) {
	q, t := []rune(strings.ToLower(query)), []rune(strings.ToLower(text))
	if len(q) == 0 {
		return 0, true
	}
	score, ti, first := 0, 0, -1
	for _, c := range q {
		for ti < len(t) && t[ti] != c {
			ti++
			if first >= 0 {
				score++
			}
		}
		if ti == len(t) {
			return 0, false
		}
		if first < 0 {
			first = ti
		}
		ti++
	}
	return score*4 + first, true
}

func (m *Model) openPicker() {
	list, err := m.App.Sessions.Project.List()
	if err != nil {
		m.addInfo(sErr.Render("  " + err.Error()))
		return
	}
	m.picker = &picker{all: list}
	m.layout()
	m.refresh(true)
}

func (m *Model) pickerKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.picker
	items := p.items()
	switch k.String() {
	case "esc", "ctrl+c":
		m.picker = nil
	case "up", "ctrl+p":
		p.sel = max(0, p.sel-1)
	case "down", "ctrl+n", "tab":
		p.sel = min(len(items)-1, p.sel+1)
	case "enter":
		m.picker = nil
		if p.sel < len(items) {
			if cur := m.App.Sessions.Current(); cur != nil && items[p.sel].ID == cur.ID {
				m.addInfo(sDim.Render("  already in that session"))
				break
			}
			m.layout()
			return m.switchTo(items[p.sel].ID)
		}
	case "backspace":
		if r := []rune(p.filter); len(r) > 0 {
			p.filter, p.sel = string(r[:len(r)-1]), 0
		}
	default:
		if k.Text != "" && k.Mod&^tea.ModShift == 0 {
			p.filter, p.sel = p.filter+k.Text, 0
		}
	}
	m.layout()
	return nil
}

func (m *Model) pickerView() string {
	p := m.picker
	items := p.items()
	cur := ""
	if c := m.App.Sessions.Current(); c != nil {
		cur = c.ID
	}
	rows := []string{lipgloss.NewStyle().Bold(true).Render("Sessions") + sDim.Render("  type to filter · ↑↓ · enter switch · esc close") + "\n" + sAccent.Render("› ") + p.filter}
	for i, s := range items {
		if i == 8 {
			rows = append(rows, sDim.Render(fmt.Sprintf("  … %d more", len(items)-8)))
			break
		}
		tags := ""
		if s.ID == cur {
			tags += sOK.Render(" current")
		}
		if s.Locked && s.ID != cur {
			tags += sWarn.Render(" open elsewhere")
		}
		line := fmt.Sprintf("%-38s %s · %d turns · $%.4f · %s", truncate(orStr(s.Title, "(untitled)"), 38), ago(s.Active), s.Turns, s.Cost, orStr(s.Status, "?"))
		mark := "  "
		if i == p.sel {
			mark, line = sAccent.Render("▸ "), lipgloss.NewStyle().Bold(true).Render(line)
		}
		rows = append(rows, mark+line+tags)
	}
	if len(items) == 0 {
		rows = append(rows, sDim.Render("  no match"))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(m.w - 2).Render(strings.Join(rows, "\n"))
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// switchTo moves to session id in place; mid-turn it pauses at the next safe point first.
func (m *Model) switchTo(id string) tea.Cmd {
	if m.busy {
		m.App.Agent.Pause()
		m.pendingSwitch = id
		m.addInfo(sDim.Render("  pausing at the next safe point, then switching"))
		return nil
	}
	return func() tea.Msg {
		st, err := m.App.Sessions.Switch(context.Background(), id)
		if err != nil {
			return infoMsg(sErr.Render("  switch failed (still in the current session): " + err.Error()))
		}
		return switchedMsg{st: st, verb: "switched to"}
	}
}

// sessionCommand handles session slash commands; ok=false means "not mine".
func (m *Model) sessionCommand(name, arg string) (tea.Cmd, bool) {
	sm := m.App.Sessions
	if sm == nil {
		return nil, false
	}
	ctx := context.Background()
	switch name {
	case "/sessions":
		m.openPicker()
	case "/switch", "/resume":
		if arg == "" {
			m.openPicker()
			return nil, true
		}
		return m.switchTo(arg), true
	case "/new":
		if m.busy {
			m.addInfo(sErr.Render("  a turn is running — press Esc or /pause first"))
			return nil, true
		}
		return func() tea.Msg {
			if err := sm.New(); err != nil {
				return infoMsg(sErr.Render("  " + err.Error()))
			}
			return switchedMsg{verb: "new session"}
		}, true
	case "/fork":
		if m.busy {
			m.addInfo(sErr.Render("  a turn is running — press Esc or /pause first"))
			return nil, true
		}
		from, n := "", 0
		if v, err := strconv.Atoi(arg); err == nil {
			n = v
		} else {
			from = arg
		}
		return func() tea.Msg {
			id, err := sm.Fork(ctx, from, n)
			if err != nil {
				return infoMsg(sErr.Render("  fork failed: " + err.Error()))
			}
			return switchedMsg{st: m.App.Agent.Export(), verb: "forked into " + id + ":"}
		}, true
	case "/delete":
		if arg == "" {
			m.addInfo(sErr.Render("  usage: /delete <session id>  (see /sessions)"))
			return nil, true
		}
		ask := m.App.Reg.Policy.Ask
		return func() tea.Msg {
			if ask == nil || ask(ctx, "delete session", "permanently delete session "+arg+" and its checkpoints", true) == tools.Deny {
				return infoMsg(sDim.Render("  delete cancelled"))
			}
			if err := sm.Delete(ctx, arg); err != nil {
				return infoMsg(sErr.Render("  delete failed: " + err.Error()))
			}
			return infoMsg(sOK.Render("  deleted session " + arg))
		}, true
	case "/rename":
		if arg == "" {
			m.addInfo(sErr.Render("  usage: /rename <title>"))
			return nil, true
		}
		sm.Rename(arg)
		m.addInfo(sOK.Render("  renamed: " + arg))
	case "/export":
		f := strings.Fields(arg)
		format, path := "md", ""
		if len(f) > 0 {
			format = f[0]
		}
		if len(f) > 1 {
			path = f[1]
		}
		m.addInfo(m.export(format, path))
	case "/pause":
		if m.busy {
			m.App.Agent.Pause()
			m.addInfo(sDim.Render("  pausing at the next safe point…"))
			return nil, true
		}
		m.App.Agent.Commit(agent.Record{T: "status", Text: "paused"})
		if c := sm.Current(); c != nil {
			_ = c.Flush()
		}
		m.paused = true
		m.addInfo(sOK.Render("  ⏸ paused — session saved; send a prompt to continue"))
	case "/stop":
		if m.busy {
			m.App.Agent.Pause()
			m.pendingStop = true
			m.addInfo(sDim.Render("  stopping at the next safe point…"))
			return nil, true
		}
		_ = sm.Close("stopped")
		return tea.Quit, true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) export(format, path string) string {
	b, err := m.App.Sessions.Export(format)
	if err != nil {
		return sErr.Render("  " + err.Error())
	}
	if path == "" {
		ext := map[string]string{"json": "json"}[format]
		path = "ternly-" + m.App.Sessions.Current().ID + "." + orStr(ext, "md")
	}
	if !filepath.IsLocal(path) { // stays inside the workspace
		return sErr.Render("  export path must be relative and inside the workspace")
	}
	full := filepath.Join(m.App.Reg.Root, path)
	if err := os.WriteFile(full, b, 0o600); err != nil {
		return sErr.Render("  " + err.Error())
	}
	return sOK.Render(fmt.Sprintf("  exported %s (%d bytes)", path, len(b)))
}

// loadTranscript re-renders the conversation of a restored session (its last turns).
func (m *Model) loadTranscript(st agent.State) {
	m.blocks = m.blocks[:1]
	h := st.History
	start := 0
	if n := len(st.Turns); n > transcriptTurns {
		start = st.Turns[n-transcriptTurns].Hist
		m.blocks = append(m.blocks, &block{kind: bInfo, text: sDim.Render(fmt.Sprintf("  … %d earlier turns not shown (still in the model's context; /export md for all)", n-transcriptTurns))})
	}
	results := map[string]string{}
	for _, msg := range h {
		if msg.Role == "tool" {
			results[msg.ToolCallID] = tools.Unframe(msg.Content)
		}
	}
	for _, msg := range h[start:] {
		switch msg.Role {
		case "user":
			m.blocks = append(m.blocks, &block{kind: bUser, text: msg.Content})
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.blocks = append(m.blocks, &block{kind: bAssistant, text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				text := tc.Name
				if t := m.App.Reg.Get(tc.Name); t != nil {
					text = t.Summary([]byte(tc.Args))
				}
				state := 1
				if r := results[tc.ID]; strings.HasPrefix(r, "error:") || strings.HasPrefix(r, "permission denied") || strings.HasPrefix(r, "cancelled") {
					state = 2
				}
				m.blocks = append(m.blocks, &block{kind: bTool, tool: tc.Name, id: tc.ID, text: text, state: state})
			}
		}
	}
	m.ledger = st.Ledger
	m.model = nil
	m.refresh(true)
}
