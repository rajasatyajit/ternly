package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/discover"
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
	m.snap, m.snapGen = m.App.Surface.Snapshot(), m.snapGen+1
	m.surfaceCh = m.App.Surface.Changes(context.Background())
	return m.watchSurface(m.surfaceCh)
}

func (m *Model) onSurface() tea.Cmd {
	m.snap, m.snapGen = m.App.Surface.Snapshot(), m.snapGen+1
	return m.watchSurface(m.surfaceCh)
}

func quotaLabel(q surface.Quota, now time.Time) string {
	if q.ExhaustedUntil.After(now) {
		return sErr.Render(quotaOut(q, now))
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
	m.addInfo(m.whyText())
	return nil
}

// whyText is /why: how routing ranks every model for the current context.
func (m *Model) whyText() string {
	if m.App.Actions == nil {
		return sDim.Render("  routing explanations need the status interface (ADR 021); /models why works meanwhile")
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
	return strings.TrimRight(b.String(), "\n")
}

func usd(v float64) string {
	if v == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", v)
}

// connectionLines are /status's connections section, from the snapshot —
// a fallback: the core's own /status lines (App.Status: status.Lines, which
// also names the CLIs found) list connections when it provides them.
func (m *Model) connectionLines() []string {
	if m.App.Surface == nil || len(m.snap.Connections) == 0 || m.App.Status != nil {
		return nil
	}
	out := []string{"connections"}
	for _, c := range m.snap.Connections {
		state := sWarn.Render("○") + " " + untrusted(c.State)
		if c.State == "connected" {
			state = sOK.Render("●") + " connected"
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

// quotaOut says when a quota that ran out comes back: the provider's time
// (Retry-After, or its balance API's reset) as a clock time; ternly's own
// estimate only roughly, so a guess never reads as a fact.
func quotaOut(q surface.Quota, now time.Time) string {
	if q.UntilEstimated {
		return "quota out, retry in " + discover.About(q.ExhaustedUntil.Sub(now)) + " (estimated)"
	}
	return "quota out until " + q.ExhaustedUntil.Local().Format("15:04")
}
