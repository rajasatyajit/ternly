package main

import (
	"os/exec"
	"path/filepath"
	"sort"
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
