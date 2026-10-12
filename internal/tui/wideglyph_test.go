package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/surface"
)

// The always-on regions — the header, the status line and the lanes — are
// redrawn often, and the renderer (ultraviolet 006e29f) re-sends a whole line
// holding a wide glyph on every frame (ADR 030, F2.1: the status line's ⚡ was
// 59 % of a long session's bytes). Until that's fixed upstream, nothing ternly
// draws there may be wider than one column. The content here is ASCII, so any
// wide glyph found is ternly's own.
func TestAlwaysOnRegionsHaveNoWideGlyphs(t *testing.T) {
	states := map[string]func(m *Model){
		"idle after a turn": func(m *Model) {},
		"running": func(m *Model) {
			m.busy, m.turnStart, m.activity = true, time.Now().Add(-1500*time.Millisecond), "Thinking"
		},
		"errors, unverified, cache": func(m *Model) {
			m.errors, m.verify = 2, agent.VerdictUnverified
			m.ledger.Usage.CacheRead = 4000
		},
		"verify failed": func(m *Model) { m.verify = agent.VerdictFailed },
		"quota out, trust lost": func(m *Model) {
			key := m.model.Key()
			m.snap = surface.Snapshot{Models: []surface.Model{{Key: key, Connection: "cloud", Trust: surface.Trust{Lost: true, Measured: true}}},
				Connections: []surface.Connection{{ID: "cloud", Quota: &surface.Quota{ExhaustedUntil: time.Now().Add(time.Hour), UntilEstimated: true}}}}
			m.snapGen++
		},
		"quota percent": func(m *Model) {
			key := m.model.Key()
			m.snap = surface.Snapshot{Models: []surface.Model{{Key: key, Connection: "cloud"}},
				Connections: []surface.Connection{{ID: "cloud", Quota: &surface.Quota{Used: 95, Limit: 100, Unit: "%", ResetsAt: time.Now().Add(time.Hour)}}}}
			m.snapGen++
		},
		"escalated":   func(m *Model) { m.reason = "escalated: verification failed" },
		"failover":    func(m *Model) { m.reason = "failover from x" },
		"pinned":      func(m *Model) { m.reason = "pinned" },
		"discovering": func(m *Model) { m.discovering = true },
		"lanes": func(m *Model) {
			for i, st := range []string{"queued", "running", "done", "failed", "running"} {
				l := lane(i+1, "agent"+string(rune('a'+i)), st, i)
				m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: l})
			}
		},
	}
	for _, w := range []int{200, 120, 80} {
		for name, set := range states {
			m := statusModel(t, w, 40)
			set(m)
			m.layout()
			regions := map[string]string{"header": m.header(), "status line": m.statusBar(), "lanes": m.lanesView()}
			for region, s := range regions {
				for _, line := range strings.Split(stripANSI(s), "\n") {
					for _, r := range line {
						if ansi.StringWidth(string(r)) > 1 {
							t.Errorf("%d cols, %s: the %s has the wide glyph %q (%U): %q", w, name, region, r, r, line)
						}
					}
				}
			}
		}
	}
}
