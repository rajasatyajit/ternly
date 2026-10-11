package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/surface"
)

// The status line (ADR 030, level 0): the facts a person checks between
// turns, always on screen. Fields are in priority order; each has a full and
// a short form. A line that doesn't fit shortens its lowest-priority fields
// first, then drops them, so the model, context and cost are the last to go.
// It takes as few lines as fit, at most two; one when the screen has fewer
// than 24 rows, when the input has grown, or when two would leave the
// transcript fewer than 16 rows.

// statusField is one fact on the status line.
type statusField struct {
	full, short string // styled; short may equal full; "" short: drop instead
	line        int    // 0: where am I / what does it cost; 1: this turn
}

// glyphs are the status line's symbols, Unicode or ASCII (Access.ASCII).
type glyphs struct{ model, routed, pinned, escalated, failover, in, out, ok, fail, unknown, cache, sep string }

var (
	unicodeGlyphs = glyphs{"◆", "▸", "◆", "↑", "⇄", "↑", "↓", "✓", "✗", "?", "⚡", " · "}
	asciiGlyphs   = glyphs{"*", ">", "*", "^", "~", "^", "v", "ok", "x", "?", "cache ", " | "}
)

func (m *Model) glyph() glyphs {
	if m.App.Access.ASCII {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// statusFields lists the status line's facts, most important first.
func (m *Model) statusFields() []statusField {
	g := m.glyph()
	var fs []statusField
	add := func(line int, full, short string) { fs = append(fs, statusField{full, short, line}) }

	// the model and why it was chosen
	switch {
	case m.discovering:
		add(0, m.moving("discovering models"), sDim.Render("discovering"))
	case m.model == nil:
		add(0, sDim.Render("auto-routing"), sDim.Render("auto"))
	default:
		key := untrusted(m.model.Key())
		why, mark := m.routeWhy(g)
		add(0, sAccent.Render(g.model+" "+key)+" "+tierBadge(m.model.Tier)+" "+sDim.Render(mark+" "+why),
			sAccent.Render(key)+sDim.Render(mark))
	}
	// a turn about to hit its spending limit
	if lim, _ := m.App.Agent.Caps(); lim.TurnUSD > 0 {
		if spent := m.ledger.Cost - m.turnCost0; spent >= 0.75*lim.TurnUSD {
			s := fmt.Sprintf("turn $%.2f/$%.2f", spent, lim.TurnUSD)
			add(0, sWarn.Render(s), sWarn.Render(s))
		}
	}
	// context used / window
	if mt := m.snap.Meter; mt.ContextMax > 0 {
		pct := 100 * mt.ContextUsed / mt.ContextMax
		st := sDim
		switch {
		case pct >= 90:
			st = sErr
		case pct >= 70:
			st = sWarn
		}
		add(0, st.Render(fmt.Sprintf("ctx %s/%s", kfmt(mt.ContextUsed), kfmt(mt.ContextMax))), st.Render(fmt.Sprintf("ctx%d%%", pct)))
	}
	// cost: unknown when the model has no price, never a made-up $0
	cost := fmt.Sprintf("$%.4f", m.ledger.Cost)
	short := fmt.Sprintf("$%.2f", m.ledger.Cost)
	if m.model != nil && m.ledger.Cost == 0 && discover.Price(m.model) == "?" {
		cost, short = "cost unknown", "$"+g.unknown
	}
	add(0, sAccent.Render(cost), sAccent.Render(short))
	// errors this session
	if m.errors > 0 {
		warn := "⚠"
		if m.App.Access.ASCII {
			warn = "!"
		}
		word := "errors"
		if m.errors == 1 {
			word = "error"
		}
		add(0, sErr.Render(fmt.Sprintf("%s %d %s", warn, m.errors, word)), sErr.Render(fmt.Sprintf("%s%d", warn, m.errors)))
	}
	// quota (only when the provider reports one) and lost trust (ADR 020)
	if md, c := m.currentSurfaceModel(); md != nil {
		if c != nil && c.Quota != nil {
			if q := quotaLabel(*c.Quota, time.Now()); q != "" {
				short := sDim.Render(fmt.Sprintf("q%.0f%%", 100*c.Quota.Used/max(c.Quota.Limit, 1)))
				if c.Quota.ExhaustedUntil.After(time.Now()) {
					short = sErr.Render(m.shortFlag("quota"))
				}
				add(0, q, short)
			}
		}
		if md.Trust.Lost {
			add(0, sWarn.Render("trust lost"), sWarn.Render(m.shortFlag("trust")))
		}
	}
	// this turn: tokens, time, the last change's verification, plan, undo
	if in, out, ok := m.turnTokens(); ok {
		add(1, sDim.Render(fmt.Sprintf("turn %s%s %s%s", kfmt(in), g.in, kfmt(out), g.out)), sDim.Render(kfmt(in)+g.in))
	}
	if d, ok := m.turnElapsed(); ok {
		s := sDim.Render(durShort(d))
		add(1, s, s)
	}
	switch m.verify {
	case agent.VerdictVerified:
		add(1, sOK.Render("verify "+g.ok), sOK.Render(g.ok))
	case agent.VerdictFailed:
		add(1, sErr.Render("verify "+g.fail), sErr.Render(g.fail))
	case agent.VerdictUnverified:
		add(1, sWarn.Render("verify "+g.unknown), sWarn.Render(g.unknown))
	}
	if p := m.snap.Plan; len(p) > 0 {
		k, text := 0, ""
		for i, it := range p {
			if it.State == "active" || (text == "" && it.State != "done") {
				k, text = i+1, it.Text
			}
		}
		if k == 0 {
			k = len(p)
		}
		add(1, sDim.Render(fmt.Sprintf("plan %d/%d %s", k, len(p), untrusted(truncate(text, 24)))), sDim.Render(fmt.Sprintf("p%d/%d", k, len(p))))
	}
	if n := m.App.Agent.TurnCount(); n > 0 {
		add(1, sDim.Render(fmt.Sprintf("undo %d", n)), sDim.Render(fmt.Sprintf("u%d", n)))
	}
	if l := m.ledger; l.Usage.CacheRead > 0 {
		add(1, sDim.Render(fmt.Sprintf("%s%.0f%%", g.cache, l.CacheRate()*100)), "")
	}
	return fs
}

// routeWhy is why the current model was chosen, in a word, and its glyph.
func (m *Model) routeWhy(g glyphs) (string, string) {
	switch r := m.reason; {
	case r == "pinned":
		return "pinned", g.pinned
	case r == "failover":
		return "failover", g.failover
	case strings.HasPrefix(r, "escalated"):
		return "escalated", g.escalated
	}
	return "cheapest", g.routed
}

// currentSurfaceModel is the current model as the core reports it, and the
// connection it runs on (nil, nil without a surface).
func (m *Model) currentSurfaceModel() (*surface.Model, *surface.Connection) {
	cur := m.snap.Routing.Current
	if cur == "" && m.model != nil {
		cur = m.model.Key()
	}
	for i := range m.snap.Models {
		if md := &m.snap.Models[i]; md.Key == cur {
			for j := range m.snap.Connections {
				if c := &m.snap.Connections[j]; c.ID == md.Connection {
					return md, c
				}
			}
			return md, nil
		}
	}
	return nil, nil
}

// turnTokens is the current (or last) turn's tokens in and out.
func (m *Model) turnTokens() (in, out int, ok bool) {
	if m.turnStart.IsZero() {
		return 0, 0, false
	}
	u, u0 := m.ledger.Usage, m.turnUsage0
	in = (u.In + u.CacheRead + u.CacheWrite) - (u0.In + u0.CacheRead + u0.CacheWrite)
	return max(in, 0), max(u.Out-u0.Out, 0), true
}

// turnElapsed is the running turn's time so far, or the last turn's.
func (m *Model) turnElapsed() (time.Duration, bool) {
	switch {
	case m.turnStart.IsZero():
		return 0, false
	case m.busy:
		return time.Since(m.turnStart), true
	}
	return m.turnTook, true
}

// durShort is a duration in a few characters: 0.4s, 38s, 2m05s, 1h12m.
func durShort(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// statusLines is how many lines the status line may take.
func (m *Model) statusLines() int {
	if m.h < 24 || m.ta.Height() > 1 || m.linear() {
		return 1
	}
	if m.h-1-(m.ta.Height()+2)-2-m.panelHeight() < 16 { // the transcript keeps 16 rows
		return 1
	}
	return 2
}

// statusBar renders the status line at the model's width. It is drawn every
// frame but changes rarely (never on a keystroke), so it is kept until one
// of its inputs changes: statusKey lists them all.
func (m *Model) statusBar() string {
	key := m.statusKey()
	if key == m.statusCache.key && m.statusCache.out != "" {
		return m.statusCache.out
	}
	out := strings.Join(layoutStatus(m.statusFields(), m.w-1, m.statusLines(), m.glyph().sep), "\n")
	m.statusCache.key, m.statusCache.out = key, out
	return out
}

// statusKey is everything statusFields and the layout read; the elapsed time
// is in it as shown (to the tenth of a second, or the second).
func (m *Model) statusKey() statusKey {
	k := statusKey{w: m.w, lines: m.statusLines(), discovering: m.discovering, model: m.model, reason: m.reason,
		ledger: m.ledger, turnCost0: m.turnCost0, turnUsage0: m.turnUsage0, snapGen: m.snapGen, ascii: m.App.Access.ASCII,
		verify: m.verify, errors: m.errors, turns: m.App.Agent.TurnCount(), started: !m.turnStart.IsZero()}
	if d, ok := m.turnElapsed(); ok {
		k.elapsed = durShort(d)
	}
	if _, c := m.currentSurfaceModel(); c != nil && c.Quota != nil { // a quota runs out, or comes back, as time passes
		if now := time.Now(); now.Before(c.Quota.ExhaustedUntil) {
			k.quotaOut = quotaOut(*c.Quota, now) // an estimate counts down ("about 1h", "about 40m")
		}
	}
	if m.discovering {
		k.frame = m.frame // the "discovering" shimmer moves
	}
	if lim, _ := m.App.Agent.Caps(); lim.TurnUSD > 0 {
		k.turnLimit = lim.TurnUSD
	}
	return k
}

type statusKey struct {
	w, lines, errors, turns, frame, snapGen int
	discovering, ascii, started             bool
	quotaOut                                string
	model                                   *discover.Model
	reason, verify, elapsed                 string
	ledger                                  agent.Ledger
	turnCost0, turnLimit                    float64
	turnUsage0                              llm.Usage
}

// layoutStatus lays fields out in at most maxLines lines of width w (each
// line starts with a space): one line if every field fits in full; else,
// with two lines allowed, line 0's fields and line 1's apart; else
// everything on one line. A line that doesn't fit shortens,
// then drops, its fields from the last (least important) up; the first
// field is kept and truncated if nothing else is left.
func layoutStatus(fs []statusField, w, maxLines int, sep string) []string {
	if maxLines < 2 {
		one, _ := fitLine(fs, w, sep, true)
		return []string{one}
	}
	if one := " " + joinFull(fs, sep); lipgloss.Width(one) <= w { // everything, in full
		return []string{one}
	}
	var a, b []statusField
	for _, f := range fs {
		if f.line == 0 {
			a = append(a, f)
		} else {
			b = append(b, f)
		}
	}
	l0, _ := fitLine(a, w, sep, true)
	if len(b) == 0 {
		return []string{l0}
	}
	l1, _ := fitLine(b, w, sep, true)
	return []string{l0, l1}
}

// fitLine joins fs within w columns, shortening then dropping from the end.
// ok reports whether every field kept at least its short form; force makes
// it drop and truncate as needed instead of failing.
func fitLine(fs []statusField, w int, sep string, force bool) (string, bool) {
	cur := make([]string, len(fs))
	for i, f := range fs {
		cur[i] = f.full
	}
	n := len(fs)
	join := func() string { return " " + strings.Join(cur[:n], sDim.Render(sep)) }
	for i := n - 1; i >= 0 && lipgloss.Width(join()) > w; i-- { // shorten, least important first
		cur[i] = fs[i].short
		if cur[i] == "" {
			cur[i] = fs[i].full
		}
	}
	if lipgloss.Width(join()) <= w {
		return join(), true
	}
	if !force {
		return "", false
	}
	for n > 1 && lipgloss.Width(join()) > w { // drop, least important first
		n--
	}
	return ansi.Truncate(join(), max(w, 1), "…"), n == len(fs)
}

func joinFull(fs []statusField, sep string) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.full
	}
	return strings.Join(parts, sDim.Render(sep))
}

// shortFlag is a field's short form when something is wrong: quota✗, or
// quota:x in ASCII (a bare "quotax" doesn't read as a word and a mark).
func (m *Model) shortFlag(word string) string {
	if m.App.Access.ASCII {
		return word + ":" + asciiGlyphs.fail
	}
	return word + unicodeGlyphs.fail
}
