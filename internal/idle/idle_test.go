package idle

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func gate(running *bool, ac *bool, c *clock) *Gate {
	g := &Gate{Running: func() bool { return *running }, After: After, Now: c.now,
		Power: func() (bool, bool) { return *ac, true }}
	g.Touch()
	return g
}

// Each condition alone keeps background work off; all three let it run.
func TestGateConditions(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	running, ac := false, true
	g := gate(&running, &ac, c)
	if ok, why := g.Allowed(); ok || why == "" {
		t.Fatalf("just after input: allowed (%q)", why)
	}
	c.t = c.t.Add(After - time.Second)
	if ok, _ := g.Allowed(); ok {
		t.Fatal("allowed before the idle time")
	}
	c.t = c.t.Add(time.Second)
	if ok, why := g.Allowed(); !ok {
		t.Fatalf("idle, on AC, no turn: %s", why)
	}
	running = true
	if ok, why := g.Allowed(); ok || why != "a turn is running" {
		t.Fatalf("turn running: %v %q", ok, why)
	}
	running = false
	g.Touch() // the user types: not idle again for After
	if ok, _ := g.Allowed(); ok {
		t.Fatal("allowed right after a keystroke")
	}
	c.t = c.t.Add(After)
	ac = false
	c.t = c.t.Add(powerCache) // the cached reading expires
	if ok, why := g.Allowed(); ok || why != "on battery" {
		t.Fatalf("on battery: %v %q", ok, why)
	}
}

// New starts the idle clock: nothing runs in the first minutes after start.
func TestNewWaitsAfterStart(t *testing.T) {
	g := New(func() bool { return false })
	g.Power = func() (bool, bool) { return true, true }
	if ok, _ := g.Allowed(); ok {
		t.Fatal("background work allowed at start")
	}
}

func TestWaitReturnsWhenAllowed(t *testing.T) {
	old := Poll
	Poll = time.Millisecond
	defer func() { Poll = old }()
	var running atomic.Bool
	running.Store(true)
	g := &Gate{Running: running.Load, Power: func() (bool, bool) { return true, true }} // After 0: idle at once
	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()
	select {
	case <-done:
		t.Fatal("Wait returned while a turn runs")
	case <-time.After(20 * time.Millisecond):
	}
	running.Store(false)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running.Store(true)
	go func() { done <- g.Wait(ctx) }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Wait ignored its context")
	}
}

func supply(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range files {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSysfsPower(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supplies  map[string]map[string]string
		ac, known bool
	}{
		{"none (container)", nil, true, false},
		{"laptop on mains", map[string]map[string]string{"ACAD": {"type": "Mains", "online": "1"}, "BAT1": {"type": "Battery"}}, true, true},
		{"laptop on battery", map[string]map[string]string{"ACAD": {"type": "Mains", "online": "0"}, "BAT1": {"type": "Battery"}}, false, true},
		{"usb-c charging", map[string]map[string]string{"ucsi": {"type": "USB", "online": "1"}, "BAT0": {"type": "Battery", "scope": "System"}}, true, true},
		{"desktop", map[string]map[string]string{"hidpp_battery_0": {"type": "Battery", "scope": "Device"}}, true, true},
		{"battery only", map[string]map[string]string{"BAT0": {"type": "Battery"}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, fs := range tc.supplies {
				supply(t, dir, n, fs)
			}
			if ac, known := sysfsPower(dir); ac != tc.ac || known != tc.known {
				t.Fatalf("got ac=%v known=%v, want %v %v", ac, known, tc.ac, tc.known)
			}
		})
	}
	if ac, known := sysfsPower(filepath.Join(t.TempDir(), "missing")); !ac || known {
		t.Fatal("a missing directory isn't unknown-as-AC")
	}
}

func TestPmsetPower(t *testing.T) {
	for out, want := range map[string][2]bool{
		"Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged":         {true, true},
		"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t80%; discharging": {false, true},
		"Now drawing from 'UPS Power'": {false, true},
		"something else":               {true, false},
	} {
		if ac, known := pmsetPower(out); ac != want[0] || known != want[1] {
			t.Errorf("%q: %v %v, want %v", out, ac, known, want)
		}
	}
}
