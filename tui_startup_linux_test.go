package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTUIStartupBudget (ADR 022): from process start to the first frame with
// the input box, under 100 ms (median of 7), for a real build (the test
// binary that re-runs itself as ternly is much bigger). Discovery runs in
// the background, so it isn't part of this. Skipped under -race
// (instrumented binaries start several times slower); CI runs it in its own
// step.
func TestTUIStartupBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("timing under the race detector isn't startup time; CI runs this without -race")
	}
	const budget = 100 * time.Millisecond
	bin := filepath.Join(t.TempDir(), "ternly")
	if out, err := exec.Command("go", "build", "-trimpath", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	f := newProvider(t)
	home := testHome(t, f.URL)
	var ds []time.Duration
	for range 7 {
		c := exec.Command(bin, "-model", "fake/m1", "-C", t.TempDir())
		c.Env = append(childEnv(home), "TERM=xterm-256color", "TERNLY_THEME=dark")
		scr := &screen{}
		t0 := time.Now()
		tty, err := startInPTY(c, scr, 120, 40)
		if err != nil {
			t.Fatalf("pseudo-terminal: %v", err)
		}
		for deadline := t0.Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			scr.mu.Lock()
			ok := strings.Contains(reANSI.ReplaceAllString(scr.buf.String(), ""), "Ask ternly")
			scr.mu.Unlock()
			if ok {
				break
			}
		}
		ds = append(ds, time.Since(t0))
		_ = c.Process.Kill()
		_ = c.Wait()
		_ = tty.Close()
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	t.Logf("startup to the input box: median %v (all %v)", ds[len(ds)/2], ds)
	if ds[len(ds)/2] > budget {
		t.Fatalf("startup median %v, budget %v", ds[len(ds)/2], budget)
	}
}

// TestTUIIdleCPU (ADR 022): an idle TUI uses ~0% CPU — under 0.5% of a core
// over 10 s, once start-up work has settled. What remains is Bubble Tea's
// renderer checking for changes at 60 fps (~0.3% here). Background services
// that aren't the UI's (capability suggestions) are off in the test config.
// Skipped under -race.
func TestTUIIdleCPU(t *testing.T) {
	if raceEnabled {
		t.Skip("CPU under the race detector isn't idle CPU; CI runs this without -race")
	}
	bin := filepath.Join(t.TempDir(), "ternly")
	if out, err := exec.Command("go", "build", "-trimpath", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	f := newProvider(t)
	home := testHome(t, f.URL)
	c := exec.Command(bin, "-model", "fake/m1", "-C", t.TempDir())
	c.Env = append(childEnv(home), "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 120, 40)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait(); _ = tty.Close() }()
	scr.waitFor(t, "fake") // discovery done
	time.Sleep(5 * time.Second)
	ticks := func() int {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", c.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+2:])
		u, _ := strconv.Atoi(f[11])
		s, _ := strconv.Atoi(f[12])
		return u + s
	}
	a := ticks()
	time.Sleep(10 * time.Second)
	pct := float64(ticks()-a) / 100 / 10 * 100 // clock ticks at 100 Hz
	t.Logf("idle CPU over 10 s: %.2f%%", pct)
	if pct > 0.5 {
		t.Fatalf("idle CPU %.2f%%, budget 0.5%%", pct)
	}
}
