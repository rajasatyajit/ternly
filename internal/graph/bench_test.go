package graph

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/gitenv"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Set TERNLY_GRAPH_BENCH to a large Go repository (e.g. a kubernetes clone).
// Uses the real bubblewrap sandbox for `go list`, as the product does.
func TestLargeRepoBenchmark(t *testing.T) {
	root := os.Getenv("TERNLY_GRAPH_BENCH")
	if root == "" {
		t.Skip("TERNLY_GRAPH_BENCH not set")
	}
	sb := tools.NewSandbox(true, false, nil)
	run := func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
		return sb.Output(ctx, dir, env, argv...)
	}
	cache := t.TempDir()
	var peak uint64
	stop := make(chan struct{})
	go func() { // sample heap while building
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapInuse)
			}
		}
	}()
	t0 := time.Now()
	s := NewService(root, cache, "bench", run)
	s.Start(context.Background())
	g, note, err := s.Graph(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first answer (approximate graph): %v (%s, %d packages, %d symbols, %d refs; note=%q)",
		time.Since(t0).Round(time.Millisecond), s.Timing.Mode, g.Stats().Packages, g.Stats().Symbols, g.Stats().Refs, note)
	waitBuilt(t, s)
	g, note, err = s.Graph(context.Background(), time.Hour)
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	st := g.Stats()
	t.Logf("full build: %v total (go list %v, parse+typecheck %v on %d workers, save %v); %d packages, %d files, %d symbols, %d refs, %d untyped; peak heap %d MB; note=%q",
		time.Since(t0).Round(time.Millisecond), s.Timing.List.Round(time.Millisecond), s.Timing.Check.Round(time.Millisecond), runtime.GOMAXPROCS(0),
		s.Timing.Save.Round(time.Millisecond), st.Packages, st.Files, st.Symbols, st.Refs, st.Untyped, peak>>20, note)
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		s.mu.Lock()
		n := len(s.man.Deps)
		s.mu.Unlock()
		if n > 0 {
			break
		}
	}
	s.mu.Lock()
	t.Logf("dependency tables: %d packages in %v (background, after the workspace graph is usable)", s.Timing.DepPkgs, s.Timing.Deps.Round(time.Millisecond))
	s.mu.Unlock()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("graph resident: %d MB heap in use after GC", m.HeapInuse>>20)
	du, _ := exec.Command("du", "-sh", filepath.Join(cache, "graphs")).Output()
	t.Logf("on disk: %s", strings.TrimSpace(string(du)))

	t0 = time.Now()
	s2 := NewService(root, cache, "bench", run)
	s2.Start(context.Background())
	waitBuilt(t, s2)
	g2, _, _ := s2.Graph(context.Background(), time.Hour)
	t.Logf("load from cache: %v (%+v)", time.Since(t0).Round(time.Millisecond), g2.Stats())

	time.Sleep(time.Second) // let the watcher settle
	t0 = time.Now()
	_, _, _ = s.Graph(context.Background(), time.Second)
	t.Logf("refresh with nothing changed (watcher): %v", time.Since(t0))

	t0 = time.Now()
	changed, gone := s.scan()
	t.Logf("full stat scan without watcher: %v (%d changed, %d gone; sample %v)", time.Since(t0).Round(time.Millisecond), len(changed), len(gone), changed[:min(5, len(changed))])
	t.Logf("unowned files tracked: %d", len(s.man.Unowned))
	t0 = time.Now()
	_ = gitenv.Command(context.Background(), "-C", root, "status", "--porcelain", "--untracked-files=normal").Run()
	t.Logf("git status, for comparison: %v", time.Since(t0).Round(time.Millisecond))

	edit := func(rel, from, to string) {
		p := filepath.Join(root, rel)
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), from) {
			t.Fatalf("edit %s: %v", rel, err)
		}
		_ = os.WriteFile(p, []byte(strings.Replace(string(b), from, to, 1)), 0o644)
		t.Cleanup(func() { _ = os.WriteFile(p, b, 0o644) })
	}
	uniq := fmt.Sprintf("%d", time.Now().UnixNano()) // never a build-cache hit
	for _, c := range []struct{ label, file, from, to string }{
		{"body-only edit (leaf package)", os.Getenv("BENCH_LEAF_FILE"), os.Getenv("BENCH_LEAF_FROM"), strings.ReplaceAll(os.Getenv("BENCH_LEAF_TO"), "UNIQ", uniq)},
		{"API change (widely imported package)", os.Getenv("BENCH_API_FILE"), os.Getenv("BENCH_API_FROM"), strings.ReplaceAll(os.Getenv("BENCH_API_TO"), "UNIQ", uniq)},
	} {
		if c.file == "" {
			continue
		}
		edit(c.file, c.from, c.to)
		t0 = time.Now()
		if _, _, err := s.Graph(context.Background(), time.Second); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %v (%s: go list %v, check %v, save %v, %d packages re-checked)", c.label, time.Since(t0).Round(time.Millisecond), s.Timing.Mode,
			s.Timing.List.Round(time.Millisecond), s.Timing.Check.Round(time.Millisecond), s.Timing.Save.Round(time.Millisecond), s.Timing.Rechecked)
	}
}

// Type-error drift: after an API change is patched in incrementally (importers
// re-checked against the in-memory package), how many type errors do the
// patched packages report versus a fresh full build of the same tree?
func TestIncrementalPrecision(t *testing.T) {
	root := os.Getenv("TERNLY_GRAPH_BENCH")
	file, from, to := os.Getenv("BENCH_API_FILE"), os.Getenv("BENCH_API_FROM"), os.Getenv("BENCH_API_TO")
	if root == "" || file == "" {
		t.Skip("TERNLY_GRAPH_BENCH / BENCH_API_* not set")
	}
	errs := func(g *Graph) (n, refs int) {
		g.mu.RLock()
		defer g.mu.RUnlock()
		for _, p := range g.pkgs {
			n += p.Errors
			refs += len(p.Refs)
		}
		return
	}
	s := NewService(root, t.TempDir(), "bench", localRun)
	s.Start(context.Background())
	waitBuilt(t, s)
	g, _, _ := s.Graph(context.Background(), time.Hour)
	e0, r0 := errs(g)
	p := filepath.Join(root, file)
	b, _ := os.ReadFile(p)
	_ = os.WriteFile(p, []byte(strings.Replace(string(b), from, strings.ReplaceAll(to, "UNIQ", fmt.Sprint(time.Now().UnixNano())), 1)), 0o644)
	defer os.WriteFile(p, b, 0o644)
	g, _, _ = s.Graph(context.Background(), time.Hour)
	e1, r1 := errs(g)
	fresh := NewService(root, t.TempDir(), "bench", localRun)
	fresh.Start(context.Background())
	waitBuilt(t, fresh)
	gf, _, _ := fresh.Graph(context.Background(), time.Hour)
	e2, r2 := errs(gf)
	t.Logf("type errors: before %d, after incremental %d, fresh full build %d; refs %d → %d (fresh %d)", e0, e1, e2, r0, r1, r2)
}

// One full typed build per process, sized by TERNLY_GRAPH_WORKERS /
// TERNLY_GRAPH_BUDGET_MB; reports peak RSS (VmHWM) for the whole process.
func TestBuildMemory(t *testing.T) {
	root := os.Getenv("TERNLY_GRAPH_BENCH")
	if root == "" || os.Getenv("TERNLY_GRAPH_MEM") == "" {
		t.Skip("TERNLY_GRAPH_BENCH + TERNLY_GRAPH_MEM not set")
	}
	s := NewService(root, t.TempDir(), "bench", localRun)
	fmt.Sscan(os.Getenv("TERNLY_GRAPH_WORKERS"), &s.Workers)
	var mb int64
	fmt.Sscan(os.Getenv("TERNLY_GRAPH_BUDGET_MB"), &mb)
	s.MemBudget = mb << 20
	budget, workers := s.sizing()
	t0 := time.Now()
	r := s.buildFull(context.Background())
	elapsed := time.Since(t0)
	st := r.g.Stats()
	status, _ := os.ReadFile("/proc/self/status")
	var hwm string
	for _, ln := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(ln, "VmHWM:") {
			hwm = strings.TrimSpace(strings.TrimPrefix(ln, "VmHWM:"))
		}
	}
	t.Logf("workers=%d budget=%d MB: full build %v (check %v), %d packages, peak RSS %s", workers, budget>>20, elapsed.Round(time.Millisecond), r.timing.Check.Round(time.Millisecond), st.Packages, hwm)
}
