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

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// ─────────────────────────── palette ───────────────────────────

var (
	cAccent = lipgloss.Color("#8B5CF6")
	cCyan   = lipgloss.Color("#22D3EE")
	cGreen  = lipgloss.Color("#34D399")
	cRed    = lipgloss.Color("#F87171")
	cAmber  = lipgloss.Color("#FBBF24")
	cDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	cFaint  = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"}

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
	infoMsg string
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
	state    int // 0 running, 1 ok, 2 fail
	detail   string
	elapsed  time.Duration
}

// ─────────────────────────── model ───────────────────────────

type Model struct {
	App         *App
	vp          viewport.Model
	ta          textarea.Model
	md          *glamour.TermRenderer
	style       string
	w, h        int
	blocks      []*block
	busy        bool
	cancel      context.CancelFunc
	frame       int
	dirty       bool
	activity    string
	model       *discover.Model
	reason      string
	ledger      agent.Ledger
	perm        *permMsg
	queue       []string
	hist        []string
	histIx      int
	quitAt      time.Time
	ready       bool
	discovering bool
}

// App bundles the long-lived services the UI drives.
type App struct {
	Agent    *agent.Agent
	Router   *discover.Router
	Reg      *tools.Registry
	Discover func() ([]*discover.Model, []string)
	Notes    []string
	Version  string
}

func New(app *App, dark bool) *Model {
	ta := textarea.New()
	ta.Placeholder = "Ask ternly to build, fix or explain…  (/help)"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	fs, bs := textarea.DefaultStyles()
	fs.CursorLine = lipgloss.NewStyle()
	fs.Placeholder = sDim
	ta.FocusedStyle, ta.BlurredStyle = fs, bs
	ta.Focus()
	style := "dark"
	if !dark {
		style = "light"
	}
	m := &Model{App: app, ta: ta, vp: viewport.New(80, 20), style: style, discovering: true}
	m.blocks = append(m.blocks, &block{kind: bInfo, text: m.welcome()})
	for _, n := range app.Notes {
		m.blocks = append(m.blocks, &block{kind: bInfo, text: sWarn.Render("! ") + n})
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tick(), func() tea.Msg {
		ms, w := m.App.Discover()
		return discoveredMsg{ms, w}
	})
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
		cmds = append(cmds, tick())

	case discoveredMsg:
		m.discovering = false
		m.App.Router.SetModels(msg.models)
		m.blocks[0].text, m.blocks[0].rendered = m.welcome(), ""
		for _, w := range msg.warn {
			m.addInfo(sWarn.Render("! ") + w)
		}
		m.refresh(true)

	case permMsg:
		m.perm = &msg
		m.layout()
		m.refresh(true)

	case infoMsg:
		m.addInfo(string(msg))

	case agentMsg:
		m.onAgent(agent.Event(msg))

	case tea.KeyMsg:
		if c, handled := m.onKey(msg); handled {
			return m, c
		}
	}
	var c tea.Cmd
	m.ta, c = m.ta.Update(msg)
	cmds = append(cmds, c)
	if lc := min(8, max(1, m.ta.LineCount())); lc != m.ta.Height() {
		m.ta.SetHeight(lc)
		m.layout()
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) onKey(k tea.KeyMsg) (tea.Cmd, bool) {
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
		if m.busy && !strings.HasPrefix(v, "/") {
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
		m.vp.HalfPageUp()
		return nil, true
	case "pgdown", "ctrl+d":
		m.vp.HalfPageDown()
		return nil, true
	case "ctrl+l":
		m.blocks = m.blocks[:1]
		m.refresh(true)
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
		return m.command(v)
	}
	m.blocks = append(m.blocks, &block{kind: bUser, text: v})
	return m.start(v)
}

func (m *Model) start(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.busy, m.activity = cancel, true, "Routing"
	if m.discovering {
		m.activity = "Discovering models"
	}
	m.refresh(true)
	return func() tea.Msg {
		m.App.Agent.Run(ctx, prompt)
		return nil
	}
}

func (m *Model) onAgent(e agent.Event) {
	switch e.Kind {
	case agent.EvText:
		last := m.blocks[len(m.blocks)-1]
		if last.kind != bAssistant {
			last = &block{kind: bAssistant}
			m.blocks = append(m.blocks, last)
		}
		last.text += e.Text
		last.rendered = ""
		m.activity = "Writing"
		m.dirty = true
		return
	case agent.EvModel:
		m.model, m.reason = e.Model, e.Reason
		m.activity = "Thinking"
		if e.Reason != "pinned" {
			m.addInfo(sDim.Render(fmt.Sprintf("  ◆ %s  %s · %s · %s", e.Model.Key(), tierBadge(e.Model.Tier), discover.Price(e.Model), e.Reason)))
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
				b.state, b.elapsed, b.detail, b.rendered = 2, e.Elapsed, firstLines(e.Text, 3), ""
				if e.OK {
					b.state = 1
					b.detail = ""
					if e.Tool == "bash" || e.Tool == "verify" {
						b.detail = firstLines(e.Text, 2)
					}
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
		m.blocks = append(m.blocks, &block{kind: bError, text: e.Text})
	case agent.EvDone:
		m.ledger = e.Ledger
		m.busy, m.cancel = false, nil
		for _, b := range m.blocks {
			if b.kind == bTool && b.state == 0 {
				b.state, b.rendered = 2, ""
			}
		}
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.blocks = append(m.blocks, &block{kind: bUser, text: next})
			cmd := m.start(next)
			go func() { cmd() }()
		}
	}
	m.refresh(true)
}

func (m *Model) addInfo(s string) {
	m.blocks = append(m.blocks, &block{kind: bInfo, text: s})
	m.refresh(true)
}

// ─────────────────────────── slash commands ───────────────────────────

func (m *Model) command(v string) tea.Cmd {
	f := strings.Fields(v)
	arg := strings.TrimSpace(strings.TrimPrefix(v, f[0]))
	switch f[0] {
	case "/help", "/?":
		m.addInfo(helpText)
	case "/exit", "/quit", "/q":
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
			m.addInfo(sOK.Render("  ◆ pinned " + mod.Key()))
		}
	case "/cost":
		l := m.App.Agent.Ledger()
		m.addInfo(fmt.Sprintf("  session: %s  in %s · out %s · cache read %s / write %s (%.0f%% hit) · %d turns",
			sAccent.Render(fmt.Sprintf("$%.4f", l.Cost)), kfmt(l.Usage.In), kfmt(l.Usage.Out), kfmt(l.Usage.CacheRead), kfmt(l.Usage.CacheWrite), l.CacheRate()*100, l.Turns))
	case "/compact":
		go func() {
			if err := m.App.Agent.Compact(context.Background()); err != nil {
				m.App.Agent.Emit(agent.Event{Kind: agent.EvStatus, Text: err.Error()})
			}
		}()
	case "/mode":
		switch arg {
		case "ask", "edits", "yolo":
			m.App.Reg.Policy.Mode = arg
			m.addInfo(sOK.Render("  permission mode: " + arg))
		default:
			m.addInfo(sErr.Render("  usage: /mode ask|edits|yolo"))
		}
	case "/verify":
		if arg == "off" {
			arg = ""
		}
		if arg != "" {
			m.App.Agent.Verify = arg
		}
		m.addInfo(fmt.Sprintf("  verify: %s", orStr(m.App.Agent.Verify, "off")))
	case "/budget":
		var b float64
		if _, err := fmt.Sscanf(arg, "%f", &b); err == nil {
			m.App.Agent.Budget = b
		}
		m.addInfo(fmt.Sprintf("  budget: $%.2f (0 = unlimited)", m.App.Agent.Budget))
	case "/refresh":
		m.discovering = true
		m.addInfo(sDim.Render("  re-discovering providers…"))
		return func() tea.Msg { ms, w := m.App.Discover(); return discoveredMsg{ms, w} }
	case "/review":
		p := "Review the uncommitted changes (`git diff` and `git status`) as a senior engineer: correctness, edge cases, security, error handling, tests, readability. List concrete issues by severity with file:line, then propose fixes. Do not edit files."
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

func (m *Model) strongest() *discover.Model {
	var best *discover.Model
	for _, x := range m.App.Router.Models() {
		if x.Tools && (best == nil || x.Tier > best.Tier || (x.Tier == best.Tier && x.Blended() < best.Blended())) {
			best = x
		}
	}
	return best
}

const helpText = `  /models [filter]   list discovered models (tier · price $/Mtok in/out · context)
  /model <id|auto>   pin a model, or return to automatic cost-aware routing
  /review            review uncommitted changes with the strongest model
  /cost              tokens, cache hit-rate and spend this session
  /compact           summarise history with the cheapest model to cut context
  /mode ask|edits|yolo   permission mode      /verify <cmd|off>   post-edit check
  /budget <usd>      hard spend cap          /refresh            re-discover providers
  /clear             new conversation        /exit
  keys: Enter send · Alt+Enter newline · Esc interrupt · PgUp/PgDn scroll · ↑↓ history`

func (m *Model) modelsTable(filter string) string {
	ms := m.App.Router.Models()
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
		rows = append(rows, fmt.Sprintf("  %s %s %-12s %6s  %s", tierBadge(x.Tier), tl, discover.Price(x), kfmt(x.Ctx), x.Key()))
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
	return head + "\n" + strings.Join(rows, "\n")
}

// ─────────────────────────── rendering ───────────────────────────

func (m *Model) layout() {
	inputH := m.ta.Height() + 2
	permH := 0
	if m.perm != nil {
		permH = 5
	}
	m.vp.Width = m.w
	m.vp.Height = max(3, m.h-1-inputH-1-permH)
}

func (m *Model) refresh(force bool) {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom() || force
	var sb strings.Builder
	anim := m.busy
	for _, b := range m.blocks {
		if b.kind == bTool && b.state == 0 {
			anim = true
			sb.WriteString(m.renderBlock(b)) // spinner frame changes every tick
		} else {
			if b.rendered == "" {
				b.rendered = m.renderBlock(b)
			}
			sb.WriteString(b.rendered)
		}
		sb.WriteString("\n")
	}
	if m.busy {
		sb.WriteString("\n  " + shine(m.activity+"…", m.frame) + sDim.Render("  esc to interrupt") + "\n")
	}
	m.vp.SetContent(sb.String())
	if atBottom {
		m.vp.GotoBottom()
	}
	m.dirty = anim
}

func (m *Model) renderBlock(b *block) string {
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
		icon := spinFrames[m.frame%len(spinFrames)]
		switch b.state {
		case 1:
			icon = sOK.Render("●")
		case 2:
			icon = sErr.Render("●")
		}
		line := fmt.Sprintf("  %s %s %s", icon, sTool.Render(prettyTool(b.tool)), truncate(b.text, m.w-30))
		if b.elapsed > 0 {
			line += sDim.Render("  " + dur(b.elapsed))
		}
		if b.detail != "" {
			for _, l := range strings.Split(b.detail, "\n") {
				line += "\n" + sDim.Render("    ⎿ "+truncate(l, m.w-10))
			}
		}
		return line
	case bError:
		return sErr.Render("  ✗ ") + lipgloss.NewStyle().Width(m.w-6).Render(b.text)
	}
	return b.text
}

func (m *Model) View() string {
	if !m.ready {
		return "\n  " + shine("ternly", m.frame)
	}
	var sb strings.Builder
	sb.WriteString(m.header() + "\n")
	sb.WriteString(m.vp.View() + "\n")
	if m.perm != nil {
		sb.WriteString(m.permView() + "\n")
	}
	box := sBox
	if m.ta.Focused() {
		box = sBoxOn
	}
	sb.WriteString(box.Width(m.w-2).Render(m.ta.View()) + "\n")
	sb.WriteString(m.statusBar())
	return sb.String()
}

func (m *Model) header() string {
	logo := shimmer[len(shimmer)-1].Bold(true).Render("◆ ternly")
	cwd := m.App.Reg.Root
	if home, _ := os.UserHomeDir(); home != "" {
		cwd = strings.Replace(cwd, home, "~", 1)
	}
	right := sDim.Render(m.App.Reg.Sandbox.Mode() + " · " + m.App.Reg.Policy.Mode)
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
		left = shine("discovering models", m.frame)
	} else if m.model != nil {
		left = sAccent.Render("◆ "+m.model.Key()) + " " + tierBadge(m.model.Tier)
	}
	l := m.ledger
	right := sDim.Render(fmt.Sprintf("↑%s ↓%s", kfmt(l.Usage.In+l.Usage.CacheRead+l.Usage.CacheWrite), kfmt(l.Usage.Out)))
	if l.Usage.CacheRead > 0 {
		right += sDim.Render(fmt.Sprintf(" ⚡%.0f%%", l.CacheRate()*100))
	}
	right += "  " + sAccent.Render(fmt.Sprintf("$%.4f", l.Cost))
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
	sb.WriteString(sDim.Render("  /help for commands · tasks are routed to the cheapest capable model and escalated only on failure"))
	return sb.String()
}

// ─────────────────────────── helpers ───────────────────────────

func tierBadge(t int) string {
	c := []lipgloss.Color{"#6B7280", "#22D3EE", "#A78BFA", "#F472B6"}[min(max(t, 0), 3)]
	return lipgloss.NewStyle().Foreground(c).Render(fmt.Sprintf("T%d", t))
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
	}
	if strings.HasPrefix(t, "mcp__") {
		return "MCP " + strings.ReplaceAll(strings.TrimPrefix(t, "mcp__"), "__", ":")
	}
	return t
}

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
