package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/surface"
	"github.com/rajasatyajit/ternly/internal/surface/fake"
)

func surfaceModel(t *testing.T) (*Model, *fake.Core) {
	t.Helper()
	m := testModel(t)
	core := &fake.Core{}
	m.App.Surface, m.App.Actions = core, core
	core.Set(surface.Snapshot{
		Connections: []surface.Connection{
			{ID: "ollama-cloud", Label: "Ollama (cloud)", Kind: "daemon", How: "signed-in daemon", State: "connected", Models: 9,
				Quota: &surface.Quota{Used: 80, Limit: 100, Unit: "%", ResetsAt: time.Now().Add(time.Hour)}},
			{ID: "claude-cli", Label: "Claude subscription via the claude CLI", Kind: "cli-bridge", How: "claude", State: "signed-out",
				Detail: "run claude and sign in, then /refresh"},
		},
		Models: []surface.Model{{Key: "ollama/kimi-k3:cloud", Connection: "ollama-cloud", Trust: surface.Trust{Lost: true, Needed: 20}}},
		Routing: surface.Routing{Version: "v2", Current: "ollama/kimi-k3:cloud"},
		Meter:   surface.Meter{ContextUsed: 92_000, ContextMax: 100_000},
	})
	m.Init()
	return m, core
}

// The status bar shows context fill, the current model's quota and its
// trust, from the surface snapshot (ADR 021).
func TestMeterFromSurface(t *testing.T) {
	m, core := surfaceModel(t)
	bar := stripANSI(m.statusBar())
	for _, want := range []string{"ctx 92.0k/100.0k", "quota 80%", "resets", "trust lost"} {
		if !strings.Contains(bar, want) {
			t.Errorf("status bar lacks %q: %q", want, bar)
		}
	}
	// a change notification brings the next snapshot in
	core.Set(surface.Snapshot{Routing: surface.Routing{Current: "x"}, Meter: surface.Meter{ContextUsed: 1000, ContextMax: 100_000}})
	m.Update(m.watchSurface(m.surfaceCh)())
	if bar := stripANSI(m.statusBar()); !strings.Contains(bar, "ctx 1.0k/100.0k") || strings.Contains(bar, "trust lost") {
		t.Fatalf("after a change: %q", bar)
	}
}

// /why renders the explanation; /status lists connections with the next step.
func TestWhyAndConnections(t *testing.T) {
	m, core := surfaceModel(t)
	core.Explains = []surface.Explanation{{ContextTokens: 92_000, Rows: []surface.Estimate{
		{Key: "ollama/kimi-k3:cloud", Eligible: true, P: 0.9, Seconds: 40, Quota: 0.002, Score: 0.25},
		{Key: "ollama/qwen3.6", Why: "too slow at this context"},
	}}}
	m.cmdWhy("")
	m.cmdStatus("")
	v := stripANSI(m.View().Content)
	for _, want := range []string{"routing for ~92.0k tokens", "kimi-k3:cloud", "0.90", "too slow at this context",
		"connections", "Ollama (cloud)", "● connected", "○ signed-out", "run claude and sign in"} {
		if !strings.Contains(v, want) {
			t.Errorf("screen lacks %q", want)
		}
	}
}

// Without a surface nothing changes (the adapter lands in Phase B).
func TestNoSurface(t *testing.T) {
	m := testModel(t)
	if m.meter() != "" || m.connectionLines() != nil || m.startSurface() != nil {
		t.Fatal("no surface: nothing should show")
	}
	m.cmdWhy("")
	if v := stripANSI(m.View().Content); !strings.Contains(v, "/models why works meanwhile") {
		t.Fatal("/why without the interface should point to /models why")
	}
}
