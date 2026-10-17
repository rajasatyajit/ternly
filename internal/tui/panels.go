package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Panels (ADR 030, level 2): what the always-on lines summarise, in full,
// one key chord away (keys.go). At 160 columns or more a panel docks as a
// rail beside the transcript; narrower, it takes the transcript's place
// until it closes. One panel at a time, and not with the session sidebar
// (Ctrl+B): opening one closes the other. Esc closes a panel once the input
// is empty; PgUp/PgDn scroll it. A panel's lines are built when it opens and
// when what it shows changes (an event, a resize), never per frame. In the
// accessible (linear) mode a panel is printed as text instead.

type panelKind string

const (
	pAgents  panelKind = "agents"
	pChanges panelKind = "changes"
	pError   panelKind = "error"
	pPlan    panelKind = "plan"
	pRouting panelKind = "routing"
)

var panelTitles = map[panelKind]string{
	pAgents: "Agents", pChanges: "Changes", pError: "Last error", pPlan: "Plan", pRouting: "Routing",
}

// panelDescs are the palette's descriptions.
var panelDescs = map[panelKind]string{
	pAgents:  "each subagent: model, step, last call, usage, time",
	pChanges: "files ternly changed, and the working tree's diff",
	pError:   "the last error in full: cause, message, retries, failover",
	pPlan:    "every step of the plan with its state (plan data: Phase C)",
	pRouting: "why this model, and how routing ranks them all",
}

const (
	panelRailW    = 64  // the docked rail's width
	panelRailMinW = 160 // the window width from which a panel docks
)

type panel struct {
	kind  panelKind
	lines []string // built for width w
	w     int
	top   int    // the first line shown
	diff  string // changes: the working tree's diff, read off the UI goroutine
	busy  bool   // changes: the diff is being read
}

// panelDiffMsg brings the diff the changes panel asked for.
type panelDiffMsg struct{ text string }

// errRecord is the last error and what led to it.
type errRecord struct {
	text  string
	at    time.Time
	model string
	trail []string // the turn's status lines before it (retries, failover)
}

// panelRail: a panel is open and docks beside the transcript.
func (m *Model) panelRail() bool {
	return m.panel != nil && m.w >= panelRailMinW && !m.linear()
}

// panelOverlay: a panel is open and takes the transcript's place.
func (m *Model) panelOverlay() bool {
	return m.panel != nil && m.w < panelRailMinW && !m.linear()
}

// togglePanel opens a panel, or closes it if it's the one open.
func (m *Model) togglePanel(k panelKind) tea.Cmd {
	if m.linear() { // printed, not drawn
		p := &panel{kind: k}
		m.panel = p
		m.buildPanel()
		m.panel = nil
		m.addInfo(strings.Join(p.lines, "\n"))
		if k == pChanges {
			return m.fetchDiff()
		}
		return nil
	}
	if m.panel != nil && m.panel.kind == k {
		m.closePanel()
		return nil
	}
	wasRail := m.panelRail()
	m.side = nil // one beside the transcript at a time
	m.panel = &panel{kind: k}
	m.buildPanel()
	if m.panelRail() != wasRail || m.panelRail() {
		m.rewrap() // the transcript's width changed
	} else {
		m.layout()
	}
	if k == pChanges {
		return m.fetchDiff()
	}
	return nil
}

func (m *Model) closePanel() {
	rail := m.panelRail()
	m.panel = nil
	if rail {
		m.rewrap()
	} else {
		m.layout()
		m.refresh(false)
	}
}

// fetchDiff reads the diff for the changes panel off the UI goroutine.
func (m *Model) fetchDiff() tea.Cmd {
	if m.panel != nil {
		m.panel.busy = true
		m.buildPanel()
	}
	return func() tea.Msg { return panelDiffMsg{m.diffText(400)} }
}

func (m *Model) onPanelDiff(msg panelDiffMsg) {
	if m.panel == nil || m.panel.kind != pChanges {
		if m.linear() {
			m.addInfo(msg.text)
		}
		return
	}
	m.panel.diff, m.panel.busy = msg.text, false
	m.buildPanel()
}

// panelWidth is the width a panel's lines are built for.
func (m *Model) panelWidth() int {
	if m.panelRail() {
		return panelRailW
	}
	return max(20, m.w)
}

// refreshPanel rebuilds the open panel after what it shows changed.
func (m *Model) refreshPanel(kinds ...panelKind) {
	if m.panel == nil {
		return
	}
	for _, k := range kinds {
		if k == m.panel.kind {
			m.buildPanel()
			return
		}
	}
	if len(kinds) == 0 {
		m.buildPanel()
	}
}

func (m *Model) buildPanel() {
	p := m.panel
	p.w = m.panelWidth()
	var body []string
	switch p.kind {
	case pAgents:
		body = m.agentsPanel()
	case pChanges:
		body = m.changesPanel()
	case pError:
		body = m.errorPanel()
	case pPlan:
		body = m.planPanel()
	case pRouting:
		body = m.routingPanel()
	}
	a := actionForPanel(p.kind)
	title := lipgloss.NewStyle().Bold(true).Render(panelTitles[p.kind])
	if a != nil {
		title += sDim.Render("  " + m.actionKeys(a) + " · esc · pgup/pgdn")
	}
	lines := []string{title}
	for _, l := range body {
		for _, r := range strings.Split(l, "\n") {
			lines = append(lines, ansi.Truncate(r, p.w-1, "…"))
		}
	}
	p.lines = lines
	p.top = min(p.top, max(0, len(lines)-1))
}

func actionForPanel(k panelKind) *keyAction {
	for i := range keyActions {
		if keyActions[i].panel == k {
			return &keyActions[i]
		}
	}
	return nil
}

// panelView is the panel, h rows of its width.
func (m *Model) panelView(h int) []string {
	p := m.panel
	if p.w != m.panelWidth() {
		m.buildPanel()
	}
	rows := make([]string, 0, h)
	rows = append(rows, p.lines[0]) // the title stays
	body := p.lines[1:]
	top := min(p.top, max(0, len(body)-(h-1)))
	for i := top; i < len(body) && len(rows) < h; i++ {
		rows = append(rows, body[i])
	}
	if more := len(body) - top - (h - 1); more > 0 && h > 2 {
		rows[h-1] = sDim.Render(fmt.Sprintf("… %d more lines (pgdn)", more+1))
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	for i, r := range rows {
		if pad := p.w - lipgloss.Width(r); pad > 0 {
			rows[i] = r + strings.Repeat(" ", pad)
		}
	}
	return rows
}

// besidePanel joins the transcript and the docked panel, row by row.
func (m *Model) besidePanel(transcript string) string {
	rows := strings.Split(transcript, "\n")
	side := m.panelView(len(rows))
	sep := sDim.Render("│")
	cw := m.cw()
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r)
		if pad := cw - lipgloss.Width(r); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(sep)
		b.WriteString(side[i])
	}
	return b.String()
}

// panelKey: Esc (with nothing typed) closes the panel, PgUp/PgDn scroll it.
func (m *Model) panelKey(k tea.KeyPressMsg) bool {
	p := m.panel
	page := max(1, m.vp.h-2)
	switch k.String() {
	case "esc":
		if m.ta.Value() != "" {
			return false
		}
		m.closePanel()
	case "pgup":
		p.top = max(0, p.top-page)
	case "pgdown":
		p.top = min(max(0, len(p.lines)-2), p.top+page)
	default:
		return false
	}
	return true
}

// ─────────────────────────── the panels ───────────────────────────

func (m *Model) agentsPanel() []string {
	lanes, out := m.lanes, []string(nil)
	if len(lanes) == 0 {
		if len(m.prevLanes) == 0 {
			return []string{sDim.Render("no subagents in this session yet"), sDim.Render("they show here, and as lanes above the input, while the model delegates (the task tool)")}
		}
		lanes = m.prevLanes
		out = append(out, sDim.Render(fmt.Sprintf("none in this turn; the last ones, in turn %d:", m.prevLanesTurn)), "")
	}
	for _, s := range lanes {
		name := untrusted(orStr(s.Name, fmt.Sprint("agent ", s.ID)))
		head := m.laneGlyph(s.State) + " " + lipgloss.NewStyle().Bold(true).Render(name) + sDim.Render(" · "+s.State)
		out = append(out, head)
		if s.Model != "" {
			out = append(out, "  model  "+untrusted(s.Model))
		}
		out = append(out, fmt.Sprintf("  step   %d", s.Step))
		if s.Doing != "" {
			out = append(out, "  last   "+untrusted(s.Doing))
		}
		out = append(out, fmt.Sprintf("  usage  %s in · %s out · %s", kfmt(s.In), kfmt(s.Out), usd(s.Cost)))
		if d := laneTime(s); d > 0 {
			out = append(out, "  time   "+durShort(d))
		}
		out = append(out, "")
	}
	return out
}

func (m *Model) changesPanel() []string {
	var out []string
	if len(m.sessFiles) == 0 {
		out = append(out, sDim.Render("no edits by ternly in this session"))
	} else {
		files := make([]string, 0, len(m.sessFiles))
		for f := range m.sessFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		word := "files"
		if len(files) == 1 {
			word = "file"
		}
		out = append(out, fmt.Sprintf("%d %s changed by ternly this session:", len(files), word))
		for _, f := range files {
			out = append(out, "  "+untrusted(f))
		}
	}
	out = append(out, "", sAccent.Render("working tree"))
	switch {
	case m.panel.busy:
		out = append(out, sDim.Render("reading the diff…"))
	case m.panel.diff != "":
		out = append(out, strings.Split(m.panel.diff, "\n")...)
	}
	return out
}

func (m *Model) errorPanel() []string {
	e := m.lastErr
	if e == nil {
		return []string{sDim.Render("no errors in this session")}
	}
	g := m.glyph()
	cause, next := errorCause(e.text)
	out := []string{sErr.Bold(true).Render(g.fail + " " + cause)}
	head := "at " + e.at.Format("15:04:05")
	if e.model != "" {
		head += " · " + untrusted(e.model)
	}
	if m.errors > 1 {
		head += fmt.Sprintf(" · %d errors this session", m.errors)
	}
	out = append(out, sDim.Render(head), "")
	w := max(16, m.panelWidth()-2)
	out = append(out, lipgloss.NewStyle().Width(w).Render(untrusted(e.text)), "")
	if len(e.trail) > 0 {
		out = append(out, sAccent.Render("before it, this turn"))
		for _, t := range e.trail {
			out = append(out, "  "+untrusted(t))
		}
		out = append(out, "")
	}
	out = append(out, sDim.Render("next: "+next))
	return out
}

func (m *Model) planPanel() []string {
	p := m.snap.Plan
	if len(p) == 0 {
		return []string{
			sDim.Render("no plan in this session"),
			sDim.Render("a plan shows here when the core plans first (the plan_first lever, ADR 029);"),
			sDim.Render("/plan is a read-only mode, whose plan is in the answer"),
		}
	}
	done := 0
	for _, it := range p {
		if it.State == "done" {
			done++
		}
	}
	out := []string{fmt.Sprintf("%d of %d steps done", done, len(p)), ""}
	for i, it := range p {
		glyph, word := "queued", "pending" // the lanes' glyphs: ○ pending, ● running, ✓ done
		switch it.State {
		case "done":
			glyph, word = "done", "done"
		case "active":
			glyph, word = "running", "running"
		}
		out = append(out, fmt.Sprintf("%s %d. %s", m.laneGlyph(glyph), i+1, untrusted(it.Text))+sDim.Render("  "+word))
	}
	return out
}

func (m *Model) routingPanel() []string {
	var out []string
	if m.model != nil {
		out = append(out, "  this turn: "+untrusted(m.model.Key())+sDim.Render(" — "+untrusted(orStr(m.reason, "routed"))), "")
	}
	return append(out, strings.Split(m.whyText(), "\n")...)
}

// cmdPanel is /panel <name>: the palette's way to every panel.
func (m *Model) cmdPanel(arg string) tea.Cmd {
	alias := map[string]panelKind{"diff": pChanges, "errors": pError, "why": pRouting, "subagents": pAgents}
	k := panelKind(strings.ToLower(strings.TrimSpace(arg)))
	if a, ok := alias[string(k)]; ok {
		k = a
	}
	if _, ok := panelTitles[k]; !ok {
		var b []string
		for i := range keyActions {
			if a := &keyActions[i]; a.panel != "" {
				b = append(b, fmt.Sprintf("  %-8s %s", a.panel, sDim.Render(a.label+" · "+m.actionKeys(a))))
			}
		}
		m.addInfo("  /panel <name>:\n" + strings.Join(b, "\n"))
		return nil
	}
	return m.togglePanel(k)
}
