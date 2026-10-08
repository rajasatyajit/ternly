package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Perf-gate benchmarks (ADR 017) on the testdata/fix module; the kubernetes
// numbers come from bench/run.sh graph.

// Each iteration's service is cancelled as soon as it has answered, and they
// are all waited for after the loop: a Wait sits out the watcher's 500 ms
// poll (watch_linux.go), which isn't build cost.
var running []*Service

func started(b *testing.B, root, cache string) (*Service, context.CancelFunc) {
	s := NewService(root, cache, "fix", localRun)
	ctx, cancel := context.WithCancel(bg)
	s.Start(ctx)
	running = append(running, s)
	return s, cancel
}

// settled waits for the work a first build leaves running in the background
// (depsAfterBuild type-checks the dependencies and saves their list), so it
// doesn't overlap a timed loop: on 4 CPUs it made BenchmarkGraphLoad
// bimodal (~0.45 or ~1.2 ms/op), and the gate failed identical code (PR #16).
func settled(b *testing.B, s *Service) {
	b.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		done := s.Timing.Deps > 0
		s.mu.Unlock()
		if done {
			return
		}
	}
	b.Fatal("the dependency build after the first build did not finish")
}

func stopAll(b *testing.B) func() {
	return func() {
		b.StopTimer()
		for _, s := range running {
			s.Wait()
		}
		running = nil
	}
}

func BenchmarkGraphBuild(b *testing.B) {
	root := fixture(b)
	defer stopAll(b)()
	b.ReportAllocs()
	for b.Loop() {
		s, cancel := started(b, root, b.TempDir())
		waitBuilt(b, s)
		if _, _, err := s.Graph(bg, time.Minute); err != nil {
			b.Fatal(err)
		}
		cancel()
	}
}

func BenchmarkGraphLoad(b *testing.B) {
	root, cache := fixture(b), b.TempDir()
	s0, _ := build(b, root, cache, localRun)
	settled(b, s0)
	defer stopAll(b)()
	b.ReportAllocs()
	for b.Loop() {
		s, cancel := started(b, root, cache)
		if _, _, err := s.Graph(bg, time.Minute); err != nil || s.Timing.Mode != "load" {
			b.Fatalf("mode %q: %v", s.Timing.Mode, err)
		}
		cancel()
	}
}

// BenchmarkGraphIncremental: a body-only edit, re-checked (one package).
func BenchmarkGraphIncremental(b *testing.B) {
	root := fixture(b)
	s, _ := build(b, root, b.TempDir(), localRun)
	settled(b, s)
	main := filepath.Join(root, "app", "main.go")
	src, _ := os.ReadFile(main)
	vs := [2][]byte{[]byte(strings.Replace(string(src), "NewSquare(3)", "NewSquare(4)", 1)), src}
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		_ = os.WriteFile(main, vs[i%2], 0o644)
		i++
		b.StopTimer()
		if d := seen(b, s, "app/main.go"); d > 10*time.Millisecond { // the watcher's latency isn't the update's cost (issue #12)
			b.Logf("the watcher reported the edit after %v", d)
		}
		b.StartTimer()
		if _, _, err := s.Graph(bg, time.Second); err != nil || s.Timing.Mode != "incremental" {
			b.Fatalf("mode %q: %v", s.Timing.Mode, err)
		}
	}
}
