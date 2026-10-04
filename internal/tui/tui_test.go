package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/tools"
)

func TestStatusBarTurnBudget(t *testing.T) {
	reg, err := tools.NewRegistry(t.TempDir(), tools.NewPolicy("ask", nil), tools.NewSandbox(false, false, nil), tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.New(reg, discover.NewRouter(), func(agent.Event) {})
	ag.SetCaps(agent.Limits{TurnUSD: 2}, 0)
	m := &Model{App: &App{Agent: ag, Reg: reg}, w: 140, turnCost0: 0.50}
	for spent, want := range map[float64]bool{1.00: false, 1.49: false, 1.50: true, 1.90: true} {
		m.ledger.Cost = 0.50 + spent
		bar := m.statusBar()
		if got := strings.Contains(bar, "turn $"); got != want {
			t.Errorf("spent $%.2f of $2: shown=%v want %v (%q)", spent, got, want, bar)
		}
		if label := fmt.Sprintf("turn $%.2f/$2.00", spent); want && !strings.Contains(bar, label) {
			t.Errorf("want %q in %q", label, bar)
		}
	}
	ag.SetCaps(agent.Limits{}, 0) // limit off: never shown
	m.ledger.Cost = 100
	if strings.Contains(m.statusBar(), "turn $") {
		t.Error("shown with the turn limit disabled")
	}
}
