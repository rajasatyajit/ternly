package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/surface"
)

// Status from the core arrives through internal/surface (ADR 021): the
// context/quota/trust meter in the status bar, /why and the connections in
// /status. Until Phase B's adapter exists App.Surface is nil and none of
// this shows.

type surfaceMsg struct{}

// watchSurface waits for the next coalesced change notification.
func (m *Model) watchSurface(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return surfaceMsg{}
	}
}

// startSurface subscribes for the program's lifetime and takes the first snapshot.
func (m *Model) startSurface() tea.Cmd {
	if m.App.Surface == nil {
		return nil
	}
	m.snap = m.App.Surface.Snapshot()
	m.surfaceCh = m.App.Surface.Changes(context.Background())
	return m.watchSurface(m.surfaceCh)
}

func (m *Model) onSurface() tea.Cmd {
	m.snap = m.App.Surface.Snapshot()
	return m.watchSurface(m.surfaceCh)
}

// meter is the status bar's surface part: context fill, the current
// model's quota and trust. Empty without a surface.
func (m *Model) meter() string {
	if m.App.Surface == nil {
		return ""
	}
	var parts []string
	if mt := m.snap.Meter; mt.ContextMax > 0 {
		pct := 100 * mt.ContextUsed / mt.ContextMax
		s := fmt.Sprintf("ctx %s/%s", kfmt(mt.ContextUsed), kfmt(mt.ContextMax))
		switch {
		case pct >= 90:
			s = sErr.Render(s)
		case pct >= 70:
			s = sWarn.Render(s)
		default:
			s = sDim.Render(s)
		}
		parts = append(parts, s)
	}
	cur := m.snap.Routing.Current
	if cur == "" && m.model != nil {
		cur = m.model.Key()
	}
	for _, md := range m.snap.Models {
		if md.Key != cur {
			continue
		}
		if md.Trust.Lost {
			parts = append(parts, sWarn.Render("trust lost"))
		}
		for _, c := range m.snap.Connections {
			if c.ID == md.Connection && c.Quota != nil {
				parts = append(parts, quotaLabel(*c.Quota, time.Now()))
			}
		}
	}
	return strings.Join(parts, sDim.Render(" · "))
}

func quotaLabel(q surface.Quota, now time.Time) string {
	if q.ExhaustedUntil.After(now) {
		return sErr.Render("quota out until " + q.ExhaustedUntil.Local().Format("15:04"))
	}
	if q.Limit <= 0 {
		return ""
	}
	pct := 100 * q.Used / q.Limit
	s := fmt.Sprintf("quota %.0f%%", pct)
	if !q.ResetsAt.IsZero() {
		s += " · resets " + q.ResetsAt.Local().Format("15:04")
	}
	switch {
	case pct >= 90:
		return sErr.Render(s)
	case pct >= 70:
		return sWarn.Render(s)
	}
	return sDim.Render(s)
}

// cmdWhy shows how routing ranks every model for the current context.
func (m *Model) cmdWhy(arg string) tea.Cmd {
	if m.App.Actions == nil {
		m.addInfo(sDim.Render("  routing explanations need the status interface (ADR 021); /models why works meanwhile"))
		return nil
	}
	e := m.App.Actions.Explain(0, m.snap.Meter.ContextUsed)
	var b strings.Builder
	b.WriteString(sAccent.Render(fmt.Sprintf("  routing for ~%s tokens of context", kfmt(e.ContextTokens))) + "\n")
	b.WriteString(sDim.Render(fmt.Sprintf("  %-34s %6s %8s %9s %9s  %s", "model", "p", "time", "money", "quota", "score")) + "\n")
	for i, r := range e.Rows {
		name := untrusted(r.Key)
		if !r.Eligible {
			b.WriteString(sDim.Render(fmt.Sprintf("  %-34s %s", truncate(name, 34), "— "+untrusted(r.Why))) + "\n")
			continue
		}
		line := fmt.Sprintf("  %-34s %6.2f %7.0fs %9s %9s  %.4g", truncate(name, 34), r.P, r.Seconds, usd(r.Money), usd(r.Quota), r.Score)
		if i == 0 {
			line = sOK.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(e.Rows) == 0 {
		b.WriteString(sDim.Render("  no models") + "\n")
	}
	m.addInfo(strings.TrimRight(b.String(), "\n"))
	return nil
}

func usd(v float64) string {
	if v == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", v)
}

// connectionLines are /status's connections section (Phase B fills them).
func (m *Model) connectionLines() []string {
	if m.App.Surface == nil || len(m.snap.Connections) == 0 {
		return nil
	}
	out := []string{"connections"}
	for _, c := range m.snap.Connections {
		state := c.State
		switch c.State {
		case "connected":
			state = sOK.Render("●") + " connected"
		default:
			state = sWarn.Render("○") + " " + untrusted(c.State)
		}
		line := fmt.Sprintf("  %-34s %s · %s · %d models", untrusted(c.Label), state, untrusted(c.How), c.Models)
		if c.Quota != nil {
			if q := quotaLabel(*c.Quota, time.Now()); q != "" {
				line += " · " + q
			}
		}
		out = append(out, line)
		if c.Detail != "" && c.State != "connected" {
			out = append(out, sDim.Render("      "+untrusted(c.Detail)))
		}
	}
	return out
}
