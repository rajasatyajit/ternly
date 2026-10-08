// Package tui is the interactive front-end: streaming markdown, animated
// status, tool cards, inline permission dialogs and slash commands.
package tui

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/capability"
	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/commands"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/mcpremote"
	"github.com/rajasatyajit/ternly/internal/memory"
	"github.com/rajasatyajit/ternly/internal/plugins"
	"github.com/rajasatyajit/ternly/internal/session"
	"github.com/rajasatyajit/ternly/internal/surface"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// ─────────────────────────── palette ───────────────────────────

var (
	cAccent = lipgloss.Color("#8B5CF6")
	cCyan   = lipgloss.Color("#22D3EE")
	cGreen  = lipgloss.Color("#34D399")
	cRed    = lipgloss.Color("#F87171")
	cAmber  = lipgloss.Color("#FBBF24")
	cDim    = lipgloss.Color("#6B7280")
	cFaint  = lipgloss.Color("#374151") // setTheme: lighter on a light background

	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sAccent = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sUser   = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	sOK     = lipgloss.NewStyle().Foreground(cGreen)
	sErr    = lipgloss.NewStyle().Foreground(cRed)
	sWarn   = lipgloss.NewStyle().Foreground(cAmber)
	sTool   = lipgloss.NewStyle().Bold(true)
	sBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
	sBoxOn  = sBox.BorderForeground(cAccent)

	spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	shimmer    = gradient("#5B21B6", "#C4B5FD", 12)
)

func gradient(a, b string, n int) []lipgloss.Style {
	var ar, ag, ab, br, bg, bb int
	fmt.Sscanf(a, "#%02x%02x%02x", &ar, &ag, &ab)
	fmt.Sscanf(b, "#%02x%02x%02x", &br, &bg, &bb)
	out := make([]lipgloss.Style, n)
	for i := range out {
		t := float64(i) / float64(n-1)
		c := fmt.Sprintf("#%02x%02x%02x", int(float64(ar)+t*float64(br-ar)), int(float64(ag)+t*float64(bg-ag)), int(float64(ab)+t*float64(bb-ab)))
		out[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return out
}

// shine renders text with a highlight sweeping across it.
func shine(text string, phase int) string {
	rs := []rune(text)
	var sb strings.Builder
	n := len(shimmer)
	center := float64(phase%(len(rs)+12)) - 6
	for i, r := range rs {
		d := math.Abs(float64(i) - center)
		idx := n - 1 - int(math.Min(d*2, float64(n-1)))
		sb.WriteString(shimmer[idx].Render(string(r)))
	}
	return sb.String()
}

// ─────────────────────────── messages ───────────────────────────

type (
	agentMsg      agent.Event
	tickMsg       time.Time
	discoveredMsg struct {
		models []*discover.Model
		warn   []string
	}
	permMsg struct {
		tool, summary string
		danger        bool
		reply         chan tools.Decision
	}
	infoMsg    string
	rewoundMsg struct {
		plan agent.Plan
		done []checkpoint.Change
	}
)

type blockKind int

const (
	bUser blockKind = iota
	bAssistant
	bTool
	bInfo
	bError
)

type block struct {
	kind     blockKind
	text     string
	rendered string
	tool, id string
	state    int // 0 running, 1 ok, 2 fail, 3 unverified
	detail   string
	elapsed  time.Duration
	full     string   // a tool's whole output (Ctrl+O shows it)
	lines    []string // rendered, split (the virtualised transcript)
	linesFor string   // the rendering lines was split from
}

// ─────────────────────────── model ───────────────────────────

type Model struct {
	App           *App
	vp            transcript
	ta            textarea.Model
	md            *glamour.TermRenderer
	style         string
	dark          bool
	themeSet      bool // the user chose a theme (/theme or TERNLY_THEME): ignore the terminal's answer
	w, h          int
	blocks        []*block
	busy          bool
	cancel        context.CancelFunc
	frame         int
	dirty         bool
	activity      string
	model         *discover.Model
	reason        string
	ledger        agent.Ledger
	turnCost0     float64 // session cost when the current turn started
	perm          *permMsg
	queue         []string
	hist          []string
	histIx        int
	quitAt        time.Time
	ready         bool
	discovering   bool
	picker        *picker
	pendingSwitch string // switch to this session when the running turn has paused
	pendingStop   bool   // /stop issued mid-turn
	paused        bool   // /pause while idle: the next prompt re-activates the session
	comp          *completion
	userCmds      []*commands.Command
	pins          []string         // files sent with every prompt (/add)
	pending       []string         // context for the next prompt (/run, /web output)
	prevMode      string           // permission mode before plan mode
	afterTurn     []func() tea.Cmd // run when the current turn ends (/ask, /architect)
	suggest       *suggestion      // a capability suggestion on screen
	laterSuggest  *capability.Suggestion
	lastFailed    bool   // the last turn ended with an error
	ticking       bool   // the animation ticker is scheduled (only while something animates)
	expanded      bool   // tool output shown in full (Ctrl+O)
	answered      bool   // the current turn has shown answer text
	fileQ         string // the @ query the picker shows or awaits
	fileReq       bool   // a file listing for fileQ is wanted
	snap          surface.Snapshot
	surfaceCh     <-chan struct{}
}

// App bundles the long-lived services the UI drives.
type App struct {
	Agent        *agent.Agent
	Router       *discover.Router
	Reg          *tools.Registry
	Discover     func() ([]*discover.Model, []string)
	Notes        []string
	Version      string
	Sessions     *session.Manager    // nil: no persistence (tests)
	Banner       string              // shown at start (resumed session, fork offer)
	Pick         bool                // open the session picker at start (bare --resume)
	Memory       *memory.Memory      // nil: memory off
	Theme        string              // "dark" or "light" fixes the theme (TERNLY_THEME); "": follow the terminal
	ConfigPath   string              // the config file (for /config)
	Status       func() []string     // extra /status and /doctor lines (code graph, …)
	Plugins      *plugins.Runtime    // nil: plugins off
	Remote       *mcpremote.Manager  // remote MCP servers (ADR 014); nil: none
	Capabilities *capability.Service // nil: no suggestions
	Access       Access              // reduced motion, screen reader, mouse (AccessFromEnv)
	Surface      surface.Status      // status from the core (ADR 021); nil until its adapter exists
	Actions      surface.Actions     // nil: /why falls back to /models why
}

func New(app *App, dark bool) *Model {
	ta := textarea.New()
	ta.Placeholder = "Ask ternly to build, fix or explain…  (/help)"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	m := &Model{App: app, ta: ta, vp: transcript{h: 20, follow: true}, discovering: true}
	switch app.Theme {
	case "dark", "light":
		dark, m.themeSet = app.Theme == "dark", true
	}
	m.setTheme(dark)
	m.ta.SetVirtualCursor(false) // the terminal draws (and blinks) the cursor: no redraws while idle
	m.ta.Focus()
	m.blocks = append(m.blocks, &block{kind: bInfo, text: m.welcome()})
	if app.Sessions != nil {
		if st := app.Agent.Export(); len(st.History) > 0 {
			m.loadTranscript(st)
		}
	}
	if app.Banner != "" {
		m.blocks = append(m.blocks, &block{kind: bInfo, text: sAccent.Render("  ") + app.Banner})
	}
	for _, n := range app.Notes {
		m.blocks = append(m.blocks, &block{kind: bInfo, text: sWarn.Render("! ") + n})
	}
	for _, err := range m.loadUserCommands() {
		m.blocks = append(m.blocks, &block{kind: bInfo, text: sWarn.Render("! ") + err.Error()})
	}
	return m
}

// setTheme picks the palette and markdown style for a dark or light
// background (v2: the terminal's answer arrives as a message; nothing blocks).
func (m *Model) setTheme(dark bool) {
	m.dark = dark
	m.style = "dark"
	cFaint = lipgloss.Color("#374151")
	if !dark {
		m.style = "light"
		cFaint = lipgloss.Color("#D1D5DB")
	}
	sBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
	sBoxOn = sBox.BorderForeground(cAccent)
	st := textarea.DefaultStyles(dark)
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Focused.Placeholder = sDim
	m.ta.SetStyles(st)
	if m.w > 0 {
		m.md, _ = glamour.NewTermRenderer(glamour.WithStandardStyle(m.style), glamour.WithWordWrap(max(20, m.w-6)), glamour.WithEmoji())
	}
	for _, b := range m.blocks {
		b.rendered = ""
	}
}

func (m *Model) Init() tea.Cmd {
	m.ticking = true
	cmds := []tea.Cmd{tick(), tea.RequestBackgroundColor, func() tea.Msg {
		ms, w := m.App.Discover()
		return discoveredMsg{ms, w}
	}}
	if m.App.Pick && m.App.Sessions != nil {
		cmds = append(cmds, func() tea.Msg { return openPickerMsg{} })
	}
	if c := m.startSurface(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(70*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Asker bridges tool permission checks (agent goroutine) to the UI.
func Asker(p *tea.Program) tools.Asker {
	return func(ctx context.Context, tool, summary string, danger bool) tools.Decision {
		reply := make(chan tools.Decision, 1)
		p.Send(permMsg{tool: tool, summary: summary, danger: danger, reply: reply})
		select {
		case d := <-reply:
			return d
		case <-ctx.Done():
			return tools.Deny
		}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.md, _ = glamour.NewTermRenderer(glamour.WithStandardStyle(m.style), glamour.WithWordWrap(max(20, m.w-6)), glamour.WithEmoji())
		for _, b := range m.blocks {
			b.rendered = ""
		}
		m.ta.SetWidth(m.w - 4)
		m.ready = true
		m.layout()
		m.refresh(true)

	case tickMsg:
		m.frame++
		if m.dirty {
			m.refresh(false)
		}
		m.ticking = false // rescheduled below only if something still animates

	case discoveredMsg:
		m.discovering = false
		m.App.Router.SetModels(msg.models)
		m.blocks[0].text, m.blocks[0].rendered = m.welcome(), ""
		for _, w := range msg.warn {
			m.addInfo(sWarn.Render("! ") + untrusted(w))
		}
		m.refresh(true)

	case permMsg:
		msg.tool, msg.summary = untrusted(msg.tool), untrusted(msg.summary)
		m.perm = &msg
		m.layout()
		m.refresh(true)

	case infoMsg:
		m.addInfo(string(msg))

	case rewoundMsg:
		p := msg.plan
		txt := fmt.Sprintf("  ⏪ rewound to before turn %d (%s)", p.N, p.Mode)
		if len(msg.done) > 0 {
			txt += " · files: " + checkpoint.Describe(msg.done, 6)
		}
		if p.Mode != agent.RewindCode {
			txt += fmt.Sprintf(" · %d messages dropped — the prompt is back in the input box", p.Drop)
			m.ta.SetValue(p.Prompt)
		}
		m.addInfo(sOK.Render(txt))

	case agentMsg:
		cmds = append(cmds, m.onAgent(cleanEvent(agent.Event(msg))))

	case startMsg:
		if m.busy {
			m.queue = append(m.queue, msg.prompt)
			break
		}
		m.blocks = append(m.blocks, &block{kind: bUser, text: msg.show})
		cmds = append(cmds, m.startWith(msg.prompt, msg.extra))

	case btwMsg:
		m.blocks = append(m.blocks, &block{kind: bAssistant, text: msg.a})
		m.addInfo(sDim.Render("  (side answer — not added to the conversation)"))

	case ranMsg:
		cmds = append(cmds, m.onRan(msg))

	case webMsg:
		m.onWeb(msg)

	case suggestMsg:
		m.onSuggest(msg.s)

	case pluginReviewMsg:
		cmds = append(cmds, m.onPluginReview(msg))

	case mcpMsg:
		m.addInfo(msg.text)

	case pluginDoneMsg:
		if msg.reload {
			m.loadUserCommands()
		}
		if msg.text != "" {
			m.addInfo(msg.text)
		}

	case editedMsg:
		if msg.err != nil {
			m.addInfo(sErr.Render("  editor: " + msg.err.Error()))
		} else {
			m.ta.SetValue(msg.text)
			m.ta.SetHeight(min(8, max(1, m.ta.LineCount())))
			m.layout()
		}

	case filesMsg:
		m.onFiles(msg)

	case surfaceMsg:
		cmds = append(cmds, m.onSurface())

	case openPickerMsg:
		m.openPicker()

	case switchedMsg:
		m.queue, m.paused = nil, false
		m.loadTranscript(msg.st)
		title := orStr(m.App.Agent.Title(), m.App.Sessions.Current().ID)
		if p := msg.st.Settings; p != nil && p.Pin != "" {
			if mod, err := m.App.Router.Pin(p.Pin); err == nil && mod != nil {
				m.model, m.reason = mod, "pinned"
			}
		}
		m.addInfo(sOK.Render(fmt.Sprintf("  ⇄ %s “%s” · %d turns · $%.4f", msg.verb, title, msg.st.Ledger.Turns, msg.st.Ledger.Cost)))
		st := msg.st
		cmds = append(cmds, func() tea.Msg { // drift snapshots the workspace: off the UI goroutine
			if cs := m.App.Sessions.Drift(context.Background(), st); len(cs) > 0 {
				return infoMsg(sWarn.Render(fmt.Sprintf("  %d file(s) changed outside this session since it paused; the model is told on your next prompt", len(cs))))
			}
			return nil
		})

	case tea.FocusMsg:
		if !m.themeSet { // the system theme may have changed while away (GNOME light/dark)
			cmds = append(cmds, tea.RequestBackgroundColor)
		}

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.vp.scroll(-3)
		case tea.MouseWheelDown:
			m.vp.scroll(3)
		}

	case tea.BackgroundColorMsg:
		if !m.themeSet && msg.IsDark() != m.dark {
			m.setTheme(msg.IsDark())
			m.refresh(true)
		}

	case tea.KeyPressMsg:
		if c, handled := m.onKey(msg); handled {
			return m, c
		}
	}
	var c tea.Cmd
	m.ta, c = m.ta.Update(msg)
	cmds = append(cmds, c)
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.updateCompletion()
		if m.fileReq {
			m.fileReq = false
			cmds = append(cmds, m.globFiles(m.fileQ))
		}
	}
	if lc := min(8, max(1, m.ta.LineCount())); lc != m.ta.Height() {
		m.ta.SetHeight(lc)
		m.layout()
	}
	if !m.ticking && m.animating() { // idle costs ~0% CPU: no ticker unless something moves
		m.ticking = true
		cmds = append(cmds, tick())
	}
	return m, tea.Batch(cmds...)
}

// animating: something on screen changes with time (the splash, discovery,
// a turn's activity line, a running tool's spinner).
func (m *Model) animating() bool {
	if m.App.Access.ReducedMotion { // nothing moves; events redraw by themselves
		return !m.ready
	}
	if m.perm != nil { // waiting for the user, not working: nothing should look busy
		return false
	}
	if !m.ready || m.discovering || m.busy || m.dirty {
		return true
	}
	for i := len(m.blocks) - 1; i >= 0 && i >= len(m.blocks)-50; i-- { // running tools are recent
		if b := m.blocks[i]; b.kind == bTool && b.state == 0 {
			return true
		}
	}
	return false
}

func (m *Model) onKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.picker != nil && m.perm == nil {
		return m.pickerKey(k), true
	}
	if m.suggest != nil && m.perm == nil && m.picker == nil {
		return m.suggestKey(k), true
	}
	if m.comp != nil && m.perm == nil {
		if c, handled := m.compKey(k); handled {
			return c, true
		}
	}
	if m.perm != nil {
		var d tools.Decision
		switch k.String() {
		case "y", "Y", "enter":
			d = tools.Allow
		case "a", "A":
			d = tools.AllowAlways
		case "n", "N", "esc", "ctrl+c":
			d = tools.Deny
		default:
			return nil, true
		}
		m.perm.reply <- d
		verb := map[tools.Decision]string{tools.Allow: "allowed", tools.AllowAlways: "always allowed", tools.Deny: "denied"}[d]
		m.addInfo(sDim.Render(fmt.Sprintf("  %s %s", verb, m.perm.tool)))
		m.perm = nil
		m.layout()
		return nil, true
	}
	switch k.String() {
	case "ctrl+c":
		if m.busy {
			m.interrupt()
			return nil, true
		}
		if m.ta.Value() != "" {
			m.ta.Reset()
			return nil, true
		}
		if time.Since(m.quitAt) < 1500*time.Millisecond {
			return tea.Quit, true
		}
		m.quitAt = time.Now()
		m.addInfo(sDim.Render("  press Ctrl+C again to exit"))
		return nil, true
	case "esc":
		if m.busy {
			m.interrupt()
		}
		return nil, true
	case "enter":
		v := strings.TrimSpace(m.ta.Value())
		if v == "" {
			return nil, true
		}
		m.ta.Reset()
		m.ta.SetHeight(1)
		m.hist = append(m.hist, v)
		m.histIx = len(m.hist)
		if m.busy && !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "!") {
			m.queue = append(m.queue, v)
			m.addInfo(sDim.Render("  queued: ") + v)
			return nil, true
		}
		return m.submit(v), true
	case "up":
		if m.ta.LineCount() <= 1 && len(m.hist) > 0 && m.histIx > 0 {
			m.histIx--
			m.ta.SetValue(m.hist[m.histIx])
			return nil, true
		}
	case "down":
		if m.ta.LineCount() <= 1 && m.histIx < len(m.hist) {
			m.histIx++
			if m.histIx == len(m.hist) {
				m.ta.Reset()
			} else {
				m.ta.SetValue(m.hist[m.histIx])
			}
			return nil, true
		}
	case "pgup", "ctrl+u":
		m.vp.scroll(-max(1, m.vp.h/2))
		return nil, true
	case "pgdown", "ctrl+d":
		m.vp.scroll(max(1, m.vp.h/2))
		return nil, true
	case "ctrl+l":
		m.blocks = m.blocks[:1]
		m.refresh(true)
		return nil, true
	case "ctrl+o": // collapsible tool output
		m.expanded = !m.expanded
		for _, b := range m.blocks {
			if b.kind == bTool {
				b.rendered = ""
			}
		}
		m.refresh(false)
		return nil, true
	case "ctrl+k": // the command palette: every command, fuzzy-filtered as you type
		if m.ta.Value() == "" {
			m.ta.SetValue("/")
			m.ta.CursorEnd()
			m.updateCompletion()
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) interrupt() {
	if m.cancel != nil {
		m.cancel()
	}
	m.queue = nil
	m.addInfo(sWarn.Render("  ⏹ interrupted"))
}

func (m *Model) submit(v string) tea.Cmd {
	if strings.HasPrefix(v, "/") {
		return m.dispatch(v)
	}
	if cmd, ok := strings.CutPrefix(v, "!"); ok && strings.TrimSpace(cmd) != "" {
		return m.runCmd(strings.TrimSpace(cmd), "")
	}
	m.blocks = append(m.blocks, &block{kind: bUser, text: v})
	return m.start(v)
}

func (m *Model) start(prompt string) tea.Cmd { return m.startWith(prompt, "") }

// startWith starts a turn; extra context (pins, @files, collected output,
// plan-mode note) goes with the message but not into the turn's record.
func (m *Model) startWith(prompt, extra string) tea.Cmd {
	if a := m.attachments(prompt); a != "" {
		extra = strings.TrimSpace(a + "\n\n" + extra)
	}
	m.lastFailed, m.answered = false, false
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.busy, m.activity = cancel, true, "Routing"
	if m.paused { // a prompt after /pause continues the session
		m.App.Agent.Commit(agent.Record{T: "status", Text: "active"})
		m.paused = false
	}
	m.turnCost0 = m.ledger.Cost
	if m.discovering {
		m.activity = "Discovering models"
	}
	m.refresh(true)
	return func() tea.Msg {
		m.App.Agent.RunWith(ctx, prompt, extra)
		return nil
	}
}

func (m *Model) onAgent(e agent.Event) tea.Cmd {
	switch e.Kind {
	case agent.EvText:
		last := m.blocks[len(m.blocks)-1]
		if last.kind != bAssistant {
			last = &block{kind: bAssistant}
			m.blocks = append(m.blocks, last)
		}
		last.text += e.Text
		last.rendered = ""
		if strings.TrimSpace(e.Text) != "" {
			m.answered = true
		}
		m.activity = "Writing"
		m.dirty = true
		return nil
	case agent.EvModel:
		m.model, m.reason = e.Model, e.Reason
		m.activity = "Thinking"
		if e.Reason != "pinned" {
			m.addInfo(sDim.Render(fmt.Sprintf("  ◆ %s  %s · %s · %s", untrusted(e.Model.Key()), tierBadge(e.Model.Tier), discover.Price(e.Model), untrusted(e.Reason))))
		}
	case agent.EvToolStart:
		m.blocks = append(m.blocks, &block{kind: bTool, tool: e.Tool, id: e.ToolID, text: e.Text})
		m.activity = "Running " + prettyTool(e.Tool)
	case agent.EvVerify:
		m.blocks = append(m.blocks, &block{kind: bTool, tool: "verify", id: "verify", text: e.Text})
		m.activity = "Verifying"
	case agent.EvToolEnd:
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if b := m.blocks[i]; b.kind == bTool && b.id == e.ToolID && b.state == 0 {
				b.state, b.elapsed, b.detail, b.rendered, b.full = 2, e.Elapsed, firstLines(e.Text, 3), "", e.Text
				if e.OK {
					b.state = 1
					b.detail = ""
					if e.Tool == "bash" || e.Tool == "verify" {
						b.detail = firstLines(e.Text, 2)
					}
				}
				if e.Verdict == agent.VerdictUnverified {
					b.state, b.detail = 3, firstLines(e.Text, 4)
				}
				break
			}
		}
		m.activity = "Thinking"
	case agent.EvStatus:
		m.addInfo(sDim.Render("  ↻ " + e.Text))
	case agent.EvUsage:
		m.ledger = e.Ledger
	case agent.EvError:
		m.lastFailed = true
		m.blocks = append(m.blocks, &block{kind: bError, text: e.Text})
	case agent.EvDone:
		m.ledger = e.Ledger
		if m.busy && !m.answered && !m.lastFailed { // a turn that ends in silence looks like a hang
			m.blocks = append(m.blocks, &block{kind: bInfo, text: sWarn.Render("  the model ended the turn without an answer") +
				sDim.Render(" — ask again, or /model to pick another (/why shows the ranking)")})
		}
		m.busy, m.cancel = false, nil
		for _, b := range m.blocks {
			if b.kind == bTool && b.state == 0 {
				b.state, b.rendered = 2, ""
			}
		}
		if sm := m.App.Sessions; sm != nil {
			go sm.AutoTitle(context.Background())
			switch {
			case m.pendingStop:
				_ = sm.Close("stopped")
				return tea.Quit
			case m.pendingSwitch != "":
				id := m.pendingSwitch
				m.pendingSwitch, m.queue = "", nil
				return m.switchTo(id)
			}
		}
		var detect tea.Cmd
		if m.laterSuggest != nil { // a suggestion waited for this boundary
			s := m.laterSuggest
			m.laterSuggest = nil
			defer m.onSuggest(s)
		} else if !m.lastFailed {
			detect = m.detectAfterTurn()
		}
		if len(m.afterTurn) > 0 { // /ask restores the mode, /architect starts the editor turn
			after := m.afterTurn
			m.afterTurn = nil
			var out []tea.Cmd
			for _, f := range after {
				out = append(out, f())
			}
			if m.busy { // an after-turn hook started a turn: it runs before the queue
				m.refresh(true)
				return tea.Batch(append(out, detect)...)
			}
		}
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.blocks = append(m.blocks, &block{kind: bUser, text: next})
			cmd := m.start(next)
			go func() { cmd() }()
		}
		m.refresh(true)
		return detect
	}
	m.refresh(true)
	return nil
}

func (m *Model) addInfo(s string) {
	m.blocks = append(m.blocks, &block{kind: bInfo, text: s})
	m.refresh(true)
}

// ─────────────────────────── slash commands ───────────────────────────

// legacyCommand runs the built-ins implemented before the command table
// (name is canonical, e.g. "/resume"; v is the full line as typed).
func (m *Model) legacyCommand(name, arg, v string) tea.Cmd {
	f := []string{name}
	if c, ok := m.sessionCommand(f[0], arg); ok {
		return c
	}
	if c, ok := m.memoryCommand(f[0], arg); ok {
		return c
	}
	switch f[0] {
	case "/mode", "/model", "/verify", "/budget", "/limits": // settings are saved with the session
		defer m.saveSettings()
	}
	switch f[0] {
	case "/exit":
		return tea.Quit
	case "/clear":
		m.App.Agent.Reset()
		m.blocks = m.blocks[:1]
		m.refresh(true)
	case "/models":
		m.addInfo(m.modelsTable(arg))
	case "/model":
		mod, err := m.App.Router.Pin(arg)
		switch {
		case err != nil:
			m.addInfo(sErr.Render("  " + err.Error()))
		case mod == nil:
			m.addInfo(sOK.Render("  ◆ auto-routing enabled"))
		default:
			m.model, m.reason = mod, "pinned"
			m.addInfo(sOK.Render("  ◆ pinned " + untrusted(mod.Key())))
		}
	case "/cost":
		l, g := m.App.Agent.Ledger(), m.App.Agent.Stats()
		m.addInfo(fmt.Sprintf("  session: %s  in %s · out %s · cache read %s / write %s (%.0f%% hit) · %d turns",
			sAccent.Render(fmt.Sprintf("$%.4f", l.Cost)), kfmt(l.Usage.In), kfmt(l.Usage.Out), kfmt(l.Usage.CacheRead), kfmt(l.Usage.CacheWrite), l.CacheRate()*100, l.Turns) +
			sDim.Render(fmt.Sprintf("\n  guards: %d invalid calls rejected · %d denied · %d injection flags · %d loops stopped · %d claims challenged (%d unbacked) · %d checkpoints · %d failovers · %d empty answers asked again",
				g.Invalid, g.Denied, g.Flagged, g.Loops, g.Challenged, g.Unbacked, g.Checkpoints, g.Failovers, g.EmptyRetry)))
	case "/limits":
		m.addInfo(m.limits(arg))
	case "/undo":
		return m.rewind(0, agent.RewindBoth)
	case "/rewind":
		if arg == "" {
			m.addInfo(m.turnList())
			return nil
		}
		var n int
		mode := agent.RewindBoth
		if _, err := fmt.Sscanf(arg, "%d", &n); err != nil || n < 1 {
			m.addInfo(sErr.Render("  usage: /rewind [n] [both|code|chat]"))
			return nil
		}
		if f := strings.Fields(arg); len(f) > 1 {
			mode = f[1]
		}
		return m.rewind(n, mode)
	case "/compact":
		go func() {
			if err := m.App.Agent.Compact(context.Background()); err != nil {
				m.App.Agent.Emit(agent.Event{Kind: agent.EvStatus, Text: err.Error()})
			}
		}()
	case "/mode":
		switch arg {
		case "ask", "edits", "yolo":
			m.App.Reg.Policy.SetMode(arg)
			m.addInfo(sOK.Render("  permission mode: " + arg))
		case "plan":
			return m.cmdPlan("")
		default:
			m.addInfo(sErr.Render("  usage: /mode ask|edits|yolo|plan"))
		}
	case "/verify":
		if arg != "" {
			m.App.Agent.SetVerify(arg) // "off" disables verification
		}
		m.addInfo("  verify: " + verifyLabel(m.App.Agent.VerifyCmd()))
	case "/budget":
		lim, b := m.App.Agent.Caps()
		if _, err := fmt.Sscanf(arg, "%f", &b); err == nil {
			m.App.Agent.SetCaps(lim, b)
		}
		m.addInfo(fmt.Sprintf("  budget: $%.2f (0 = unlimited)", b))
	case "/refresh":
		m.discovering = true
		m.addInfo(sDim.Render("  re-discovering providers…"))
		return func() tea.Msg { ms, w := m.App.Discover(); return discoveredMsg{ms, w} }
	case "/review":
		p := "Review the uncommitted changes (`git diff` and `git status`) as a senior engineer: correctness, edge cases (errors, empty input, cancellation, concurrency safety), security, error handling, tests, readability. Also check minimality: speculative abstractions, unused options or parameters, dead code, TODO stubs, helpers that duplicate existing project code, and rewrites where a small diff would do. List concrete issues by severity with file:line, then propose fixes. Do not edit files."
		m.blocks = append(m.blocks, &block{kind: bUser, text: "/review"})
		prev := m.App.Router.Pinned()
		if prev == nil {
			if top := m.strongest(); top != nil {
				_, _ = m.App.Router.Pin(top.Key())
				return tea.Sequence(m.start(p), func() tea.Msg { _, _ = m.App.Router.Pin("auto"); return nil })
			}
		}
		return m.start(p)
	default:
		m.addInfo(sErr.Render("  unknown command " + f[0] + " — /help"))
	}
	return nil
}

func (m *Model) saveSettings() {
	if m.App.Sessions != nil {
		m.App.Sessions.SaveSettings()
	}
}

// rewind plans, confirms (via the permission dialog) and applies a rewind off the UI goroutine.
func (m *Model) rewind(n int, mode string) tea.Cmd {
	if m.busy {
		m.addInfo(sErr.Render("  a turn is running — press Esc first"))
		return nil
	}
	ask := m.App.Reg.Policy.Ask
	return func() tea.Msg {
		ctx := context.Background()
		p, err := m.App.Agent.PlanRewind(ctx, n, mode)
		if err != nil {
			return infoMsg(sErr.Render("  " + err.Error()))
		}
		files := "files unchanged"
		if len(p.Changes) > 0 {
			files = checkpoint.Describe(p.Changes, 6)
		}
		desc := fmt.Sprintf("rewind to before turn %d (%s): %s; %d conversation messages dropped", p.N, p.Mode, files, p.Drop)
		if len(p.Secrets) > 0 {
			desc += fmt.Sprintf("; never restored (secret-like): %s", strings.Join(p.Secrets[:min(len(p.Secrets), 4)], ", "))
		}
		if ask == nil || ask(ctx, "rewind", desc, len(p.Changes) > 0) == tools.Deny {
			return infoMsg(sDim.Render("  rewind cancelled"))
		}
		done, err := m.App.Agent.Rewind(ctx, p)
		if err != nil {
			return infoMsg(sErr.Render("  rewind failed: " + err.Error()))
		}
		return rewoundMsg{plan: p, done: done}
	}
}

func (m *Model) turnList() string {
	ts := m.App.Agent.Turns()
	if len(ts) == 0 {
		return sDim.Render("  no turns yet")
	}
	rows := []string{sDim.Render("  /rewind <n> [both|code|chat] restores to before turn n (● = changed files)")}
	for _, t := range ts {
		mark := sDim.Render("○")
		if t.Changed {
			mark = sWarn.Render("●")
		}
		rows = append(rows, fmt.Sprintf("  %2d %s %s  %s", t.N, sDim.Render(t.At.Format("15:04")), mark, truncate(t.Prompt, m.w-20)))
	}
	if m.App.Agent.CP == nil {
		rows = append(rows, sWarn.Render("  checkpoints are off — only the conversation can be rewound"))
	}
	return strings.Join(rows, "\n")
}

func (m *Model) limits(arg string) string {
	lim, budget := m.App.Agent.Caps()
	l := &lim
	if f := strings.Fields(arg); len(f) == 2 {
		var err error
		switch f[0] {
		case "steps":
			_, err = fmt.Sscanf(f[1], "%d", &l.Steps)
		case "time":
			l.Time, err = time.ParseDuration(f[1])
		case "turn-usd":
			_, err = fmt.Sscanf(f[1], "%g", &l.TurnUSD)
		default:
			err = fmt.Errorf("unknown limit %q", f[0])
		}
		if err != nil {
			return sErr.Render("  " + err.Error() + " — usage: /limits steps 80 | time 45m | turn-usd 5  (0 = off)")
		}
		m.App.Agent.SetCaps(lim, budget)
	} else if arg != "" {
		return sErr.Render("  usage: /limits steps 80 | time 45m | turn-usd 5  (0 = off)")
	}
	return fmt.Sprintf("  per-turn limits: %d steps · %s · $%.2f   session budget: $%.2f  (0 = off; changes apply from the next turn)", l.Steps, l.Time, l.TurnUSD, budget)
}

func (m *Model) strongest() *discover.Model {
	var best *discover.Model
	for _, x := range m.App.Router.Models() {
		if x.Tools && (best == nil || x.Tier > best.Tier || (x.Tier == best.Tier && x.Blended() < best.Blended())) {
			best = x
		}
	}
	return best
}

func (m *Model) modelsTable(filter string) string {
	ms := m.App.Router.Models()
	cu := m.App.Agent.Context()
	why := m.App.Router.Explanations(max(cu.System+cu.Tools+cu.Notes+cu.User+cu.Assistant+cu.ToolResults, 2000))
	if name, ok := strings.CutPrefix(filter, "why"); ok && why != nil { // /models why <model>: every term, with its source
		name = strings.ToLower(strings.TrimSpace(name))
		var out []string
		for _, x := range ms {
			if w := why[x]; w != nil && (name == "" && w.Rank[2] > 0 && w.Rank[2] <= 3 || name != "" && strings.Contains(strings.ToLower(x.Key()), name)) {
				out = append(out, sAccent.Render("  "+untrusted(x.Key()))+"\n  "+strings.ReplaceAll(untrusted(w.Long()), "\n", "\n  "))
			}
		}
		if len(out) == 0 {
			return "  no model matches " + name
		}
		return strings.Join(out, "\n")
	}
	var rows []string
	shown := 0
	byProv := map[string]int{}
	for _, x := range ms {
		byProv[x.ProvID]++
		if filter != "" && !strings.Contains(strings.ToLower(x.Key()), strings.ToLower(filter)) {
			continue
		}
		if shown >= 60 {
			continue
		}
		shown++
		tl := sOK.Render("✓")
		if !x.Tools {
			tl = sDim.Render("·")
		}
		row := fmt.Sprintf("  %s %s %-12s %6s  %s", tierBadge(x.Tier), tl, discover.Price(x), kfmt(x.Ctx), untrusted(x.Key()))
		if w := why[x]; w != nil {
			row += sDim.Render("  " + w.Short())
		}
		rows = append(rows, row)
	}
	var provs []string
	for p, n := range byProv {
		provs = append(provs, fmt.Sprintf("%s(%d)", p, n))
	}
	sort.Strings(provs)
	head := sDim.Render(fmt.Sprintf("  %d models · %s", len(ms), strings.Join(provs, " ")))
	if len(rows) == 0 {
		return head + "\n  no match"
	}
	if shown == 60 {
		rows = append(rows, sDim.Render("  … use /models <filter>"))
	}
	if why != nil {
		rows = append(rows, sDim.Render("  routing v2: rank T1/T2/T3 · p(success) · a T2 task's time and cost at this context · /models why [model] explains"))
	}
	return head + "\n" + strings.Join(rows, "\n")
}

// ─────────────────────────── rendering ───────────────────────────

func (m *Model) layout() {
	inputH := m.ta.Height() + 2
	permH := 0
	if m.perm != nil {
		permH = 5
	}
	if m.picker != nil {
		permH += min(len(m.picker.items()), 9) + 4
	}
	if m.comp != nil {
		permH += min(len(m.comp.items), compRows)
		if len(m.comp.items) > compRows {
			permH++
		}
	}
	if m.suggest != nil {
		permH += 2*len(m.suggest.s.Candidates) + 4
	}
	m.vp.h = max(3, m.h-1-inputH-1-permH)
	m.vp.clamp()
}

func (m *Model) refresh(force bool) {
	if !m.ready {
		return
	}
	if force {
		m.vp.follow = true
	}
	anim := m.busy
	for _, b := range m.blocks {
		if b.kind == bTool && b.state == 0 {
			anim = true
			b.rendered = m.renderBlock(b) // spinner frame changes every tick
		} else if b.rendered == "" {
			b.rendered = m.renderBlock(b)
		}
	}
	tail := ""
	switch {
	case m.busy && m.perm != nil:
		tail = "\n  " + sWarn.Render("Waiting for your answer") + sDim.Render("  y yes · a always · n no") + "\n"
	case m.busy:
		tail = "\n  " + m.moving(m.activity+"…") + sDim.Render("  esc to interrupt") + "\n"
	}
	m.vp.set(m.blocks, tail)
	m.dirty = anim
}

func (m *Model) renderBlock(b *block) string {
	if b.kind != bInfo { // ternly styles only info lines; every other block holds outside text
		b.text, b.detail, b.tool = untrusted(b.text), untrusted(b.detail), untrusted(b.tool)
	}
	switch b.kind {
	case bUser:
		return "\n" + sUser.Render("❯ ") + lipgloss.NewStyle().Width(m.w-4).Render(b.text)
	case bAssistant:
		if m.md == nil {
			return b.text
		}
		out, err := m.md.Render(b.text)
		if err != nil {
			return b.text
		}
		return strings.TrimRight(out, "\n ")
	case bTool:
		icon := m.toolIcon(b.state)
		line := fmt.Sprintf("  %s %s %s", icon, sTool.Render(prettyTool(b.tool)), truncate(b.text, m.w-30))
		if b.elapsed > 0 {
			line += sDim.Render("  " + dur(b.elapsed))
		}
		detail := b.detail
		if m.expanded && b.full != "" {
			detail = firstLines(untrusted(b.full), maxExpanded)
		}
		if detail != "" {
			ls := strings.Split(detail, "\n")
			for i, l := range ls {
				if i == len(ls)-1 && b.full != "" && strings.HasPrefix(l, "… +") { // firstLines' "… +N lines"
					l += " · ctrl+o expands"
				}
				line += "\n" + sDim.Render("    ⎿ "+truncate(l, m.w-10))
			}
			if m.expanded && b.full != "" {
				line += "\n" + sDim.Render("    ⎿ ctrl+o collapses")
			}
		}
		return line
	case bError:
		return sErr.Render("  ✗ ") + lipgloss.NewStyle().Width(m.w-6).Render(b.text)
	}
	return b.text
}

func (m *Model) View() tea.View {
	s, boxTop := m.render()
	v := tea.NewView(safeFrame(s))
	v.AltScreen = true   // basic key disambiguation (shift+enter) is requested by default
	v.ReportFocus = true // regaining focus re-reads the terminal's background (a GNOME light/dark switch)
	if m.App.Access.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if c := m.ta.Cursor(); c != nil && boxTop >= 0 && m.perm == nil {
		c.Y += boxTop + 1 // the box's top border
		c.X += 2          // its left border and padding
		c.Blink = !m.App.Access.ReducedMotion
		v.Cursor = c
	}
	return v
}

// render returns the frame and the row the input box starts on (-1: none).
func (m *Model) render() (string, int) {
	if !m.ready {
		return "\n  " + m.moving("ternly"), -1
	}
	var sb strings.Builder
	sb.WriteString(m.header() + "\n")
	sb.WriteString(m.vp.view() + "\n")
	if m.perm != nil {
		sb.WriteString(m.permView() + "\n")
	}
	if m.picker != nil {
		sb.WriteString(m.pickerView() + "\n")
	}
	if m.comp != nil {
		sb.WriteString(m.compView() + "\n")
	}
	if m.suggest != nil {
		sb.WriteString(m.suggestView() + "\n")
	}
	box := sBox
	if m.ta.Focused() {
		box = sBoxOn
	}
	boxTop := strings.Count(sb.String(), "\n")
	sb.WriteString(box.Width(m.w-2).Render(m.ta.View()) + "\n")
	sb.WriteString(m.statusBar())
	return sb.String(), boxTop
}

func (m *Model) header() string {
	logo := shimmer[len(shimmer)-1].Bold(true).Render("◆ ternly")
	cwd := m.App.Reg.Root
	if home, _ := os.UserHomeDir(); home != "" {
		cwd = strings.Replace(cwd, home, "~", 1)
	}
	right := sDim.Render(m.App.Reg.Sandbox.Mode() + " · " + m.App.Reg.Policy.Mode())
	if t := m.App.Agent.Title(); t != "" {
		right = sAccent.Render(truncate(t, 32)) + sDim.Render(" · ") + right
	}
	mid := sDim.Render("  " + cwd)
	gap := max(1, m.w-lipgloss.Width(logo)-lipgloss.Width(mid)-lipgloss.Width(right))
	return logo + mid + strings.Repeat(" ", gap) + right
}

func (m *Model) permView() string {
	p := m.perm
	border := cAmber
	title := "Allow " + prettyTool(p.tool) + "?"
	if p.danger {
		border = cRed
		title = "⚠ Potentially destructive — allow " + prettyTool(p.tool) + "?"
	}
	body := lipgloss.NewStyle().Bold(true).Render(title) + "\n" +
		truncate(p.summary, m.w-8) + "\n" +
		sOK.Render("[y]") + " yes   " + sAccent.Render("[a]") + " always   " + sErr.Render("[n]") + " no"
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(m.w - 2).Render(body)
}

func (m *Model) statusBar() string {
	left := sDim.Render("auto-routing")
	if m.discovering {
		left = m.moving("discovering models")
	} else if m.model != nil {
		left = sAccent.Render("◆ "+untrusted(m.model.Key())) + " " + tierBadge(m.model.Tier)
	}
	l := m.ledger
	right := sDim.Render(fmt.Sprintf("↑%s ↓%s", kfmt(l.Usage.In+l.Usage.CacheRead+l.Usage.CacheWrite), kfmt(l.Usage.Out)))
	if l.Usage.CacheRead > 0 {
		right += sDim.Render(fmt.Sprintf(" ⚡%.0f%%", l.CacheRate()*100))
	}
	right += "  " + sAccent.Render(fmt.Sprintf("$%.4f", l.Cost))
	if mt := m.meter(); mt != "" {
		right = mt + "  " + right
	}
	if lim, _ := m.App.Agent.Caps(); lim.TurnUSD > 0 {
		if spent := l.Cost - m.turnCost0; spent >= 0.75*lim.TurnUSD { // warn before the turn limit stops work
			right = sWarn.Render(fmt.Sprintf("turn $%.2f/$%.2f", spent, lim.TurnUSD)) + "  " + right
		}
	}
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return " " + left + strings.Repeat(" ", gap) + right
}

func (m *Model) welcome() string {
	var sb strings.Builder
	sb.WriteString("\n" + shine("  ◆ ternly", 3) + sDim.Render("  "+m.App.Version+" · Code, ternly.") + "\n")
	if m.discovering {
		sb.WriteString(sDim.Render("  discovering providers and local models…"))
		return sb.String()
	}
	ms := m.App.Router.Models()
	byProv := map[string][3]int{}
	for _, x := range ms {
		c := byProv[x.ProvID]
		c[0]++
		if x.Tools {
			c[1]++
		}
		if x.Local() {
			c[2] = 1
		}
		byProv[x.ProvID] = c
	}
	var names []string
	for p := range byProv {
		names = append(names, p)
	}
	sort.Strings(names)
	if len(names) == 0 {
		sb.WriteString(sErr.Render("  no providers found") + sDim.Render(" — export a key (ANTHROPIC_API_KEY, OPENROUTER_API_KEY, GEMINI_API_KEY, GROQ_API_KEY…)\n  or start Ollama / LM Studio, then /refresh"))
		return sb.String()
	}
	for _, p := range names {
		c := byProv[p]
		kind := "api"
		if c[2] == 1 {
			kind = "local"
		}
		sb.WriteString(fmt.Sprintf("  %s %-11s %s\n", sOK.Render("●"), p, sDim.Render(fmt.Sprintf("%d models, %d with tools · %s", c[0], c[1], kind))))
	}
	sb.WriteString(sDim.Render("  /help or Ctrl+K for commands · @ for files · tasks are routed to the cheapest capable model and escalated only on failure"))
	return sb.String()
}

// ─────────────────────────── helpers ───────────────────────────

func tierBadge(t int) string {
	c := []string{"#6B7280", "#22D3EE", "#A78BFA", "#F472B6"}[min(max(t, 0), 3)]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(fmt.Sprintf("T%d", t))
}

func prettyTool(t string) string {
	switch t {
	case "read_file":
		return "Read"
	case "edit_file":
		return "Edit"
	case "write_file":
		return "Write"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "bash":
		return "Bash"
	case "verify":
		return "Verify"
	case "rewind":
		return "Rewind"
	case "find_symbol":
		return "Symbols"
	case "references", "callers", "callees", "implementations", "impact":
		return strings.ToUpper(t[:1]) + t[1:]
	case "related_files":
		return "Related"
	}
	if strings.HasPrefix(t, "mcp__") {
		return "MCP " + strings.ReplaceAll(strings.TrimPrefix(t, "mcp__"), "__", ":")
	}
	return t
}

// maxExpanded bounds a tool's output in the transcript when expanded.
const maxExpanded = 200

func firstLines(s string, n int) string {
	s = strings.TrimSpace(s)
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = append(ls[:n], fmt.Sprintf("… +%d lines", len(ls)-n))
	}
	return strings.Join(ls, "\n")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if n < 8 {
		n = 8
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func dur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func kfmt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// AgentMsg wraps an agent event for tea.Program.Send.
func AgentMsg(e agent.Event) tea.Msg { return agentMsg(e) }
