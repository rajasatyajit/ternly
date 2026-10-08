package tui

import (
	"sort"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestFrameBudgets (ADR 022): with a 10k-line transcript, a keystroke
// through Update to a rendered frame, and a redraw with new output, each
// take under 16 ms at the 95th percentile. Skipped under -race; CI runs it
// in its own step. The perf gate's BenchmarkKeystroke/BenchmarkRedraw10k
// guard relative regressions.
func TestFrameBudgets(t *testing.T) {
	if raceEnabled {
		t.Skip("frame times under the race detector aren't frame times")
	}
	const budget = 16 * time.Millisecond
	m := benchModel(t, 1000)
	if m.vp.total < 10000 {
		t.Fatalf("transcript is %d lines, want ≥ 10k", m.vp.total)
	}
	p95 := func(name string, f func()) {
		var ds []time.Duration
		for range 100 {
			t0 := time.Now()
			f()
			ds = append(ds, time.Since(t0))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		t.Logf("%s: median %v, p95 %v", name, ds[50], ds[95])
		if ds[95] > budget {
			t.Errorf("%s p95 %v, budget %v", name, ds[95], budget)
		}
	}
	k := tea.KeyPressMsg{Code: 'a', Text: "a"}
	p95("keystroke to frame", func() { m.Update(k); _ = m.View() })
	p95("redraw with new output", func() {
		m.blocks[len(m.blocks)-1].rendered = ""
		m.refresh(false)
		_ = m.View()
	})
}
