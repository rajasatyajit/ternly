package graph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/testutil"
)

var bg = context.Background()

func localRun(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir, c.Env = dir, append(os.Environ(), env...)
	return c.Output()
}

// fixture copies testdata/fix into a temp workspace.
func fixture(t testing.TB) string {
	t.Helper()
	testutil.Require(t, "go", testutil.Have("go"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	err := filepath.WalkDir("testdata/fix", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/fix", p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// build starts a service and waits until any background (typed) build is done.
func build(t testing.TB, root, cache string, run Runner) (*Service, *Graph) {
	t.Helper()
	s := NewService(root, cache, "fix", run)
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(func() { cancel(); s.Wait() })
	s.Start(ctx)
	waitBuilt(t, s)
	g, _, err := s.Graph(bg, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s, g
}

func waitBuilt(t testing.TB, s *Service) {
	t.Helper()
	<-s.ready
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		b := s.building
		s.mu.Unlock()
		if !b {
			return
		}
	}
	t.Fatal("background build did not finish")
}

func ids(ss []*Symbol) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return strings.Join(out, " ")
}

func refsFrom(rs []*Ref) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.From+"@"+r.Pos.File)
	}
	return strings.Join(out, " ")
}

const pk = "example.com/fix/shapes"

func TestGraphQueries(t *testing.T) {
	root := fixture(t)
	_, g := build(t, root, t.TempDir(), localRun)
	if st := g.Stats(); st.Packages != 2 || st.Untyped != 0 {
		t.Fatalf("stats %+v", st)
	}
	if got := ids(g.Resolve("Square.Area")); got != pk+".Square.Area" {
		t.Errorf("resolve Square.Area: %s", got)
	}
	if got := ids(g.Resolve("Area")); !strings.Contains(got, "Circle.Area") || !strings.Contains(got, "Square.Area") || !strings.Contains(got, "Shape.Area") {
		t.Errorf("bare name should list all: %s", got)
	}
	s := g.Symbol(pk + ".Square.Area")
	if s.Pos.File != "shapes/shape.go" || s.Pos.Line != 17 || s.Kind != KMethod || s.Sig != "func() float64" {
		t.Errorf("Square.Area symbol: %+v", s)
	}
	if got := refsFrom(g.References(pk+".NewSquare", false)); !strings.Contains(got, "example.com/fix/app.main@app/main.go") || !strings.Contains(got, "shapes.TestTotal@shapes/shape_test.go") || !strings.Contains(got, "shapes_test.TestX@shapes/x_test.go") {
		t.Errorf("references NewSquare: %s", got)
	}
	if got := refsFrom(g.References(pk+".Shape.Area", true)); !strings.Contains(got, pk+".Total@shapes/shape.go") {
		t.Errorf("callers of Shape.Area (interface call) should include Total: %s", got)
	}
	var callees []string
	for _, r := range g.Callees("example.com/fix/app.main") {
		callees = append(callees, r.To)
	}
	if got := strings.Join(callees, " "); !strings.Contains(got, pk+".NewSquare") || !strings.Contains(got, pk+".Total") || !strings.Contains(got, "fmt.Println") {
		t.Errorf("callees of main: %s", got)
	}
	if got := ids(g.Implementations(pk + ".Shape")); got != pk+".Square" {
		t.Errorf("implementations of Shape = %q (Circle has no Name: must not match)", got)
	}
	if got := ids(g.Implementations(pk + ".Square")); got != pk+".Shape" {
		t.Errorf("Square implements: %q", got)
	}
	if got := refsFrom(g.References(pk+".base.ID", false)); !strings.Contains(got, "app/main.go") {
		t.Errorf("promoted field sq.ID should reference base.ID: %q", got)
	}
	imp := g.Impact([]string{pk + ".Square.Area"}, 3)
	if got := ids(imp.Tests); !strings.Contains(got, "TestX") {
		t.Errorf("impact tests of Square.Area: %s", got)
	}
	if rel := g.RelatedFiles("shapes/shape.go"); len(rel) == 0 || rel[0].File != "shapes/shape_test.go" {
		t.Errorf("related files: %+v", rel)
	}
}

func TestToolsOutput(t *testing.T) {
	root := fixture(t)
	svc, _ := build(t, root, t.TempDir(), localRun)
	byName := map[string]func(string) string{}
	for _, tl := range Tools(svc) {
		tl := tl
		byName[tl.Spec.Name] = func(args string) string {
			if !json.Valid(tl.Spec.Schema) {
				t.Fatalf("%s: invalid schema", tl.Spec.Name)
			}
			out, err := tl.Run(bg, json.RawMessage(args))
			if err != nil {
				t.Fatalf("%s: %v", tl.Spec.Name, err)
			}
			return out
		}
	}
	for tool, c := range map[string][2]string{
		"find_symbol":     {`{"query":"square"}`, "type " + pk + ".Square  shapes/shape.go:12"},
		"references":      {`{"symbol":"NewSquare"}`, "app/main.go:10  in example.com/fix/app.main call"},
		"callers":         {`{"symbol":"Shape.Area"}`, "shapes/shape.go:33  in " + pk + ".Total call"},
		"callees":         {`{"symbol":"Total"}`, pk + ".Shape.Area  called at shapes/shape.go:33  declared shapes/shape.go:5"},
		"implementations": {`{"symbol":"Shape"}`, "type " + pk + ".Square"},
		"related_files":   {`{"path":"shapes/shape.go"}`, "shapes/shape_test.go  (test pair"},
		"impact":          {`{"symbol":"NewSquare"}`, "tests:"},
	} {
		if out := byName[tool](c[0]); !strings.Contains(out, c[1]) {
			t.Errorf("%s %s:\nwant %q in\n%s", tool, c[0], c[1], out)
		}
	}
	if out := byName["references"](`{"symbol":"NewSquare"}`); !strings.Contains(out, "app/main.go:10  in example.com/fix/app.main call [typed]  │ sq := shapes.NewSquare(3)") {
		t.Errorf("reference lines should carry their source line:\n%s", out)
	}
	if out := byName["find_symbol"](`{"query":"NewSquare"}`); !strings.Contains(out, "    │ func NewSquare(n float64) *Square { return &Square{Side: n} }") {
		t.Errorf("find_symbol should show the declaration's first lines:\n%s", out)
	}
	for _, q := range []string{"Square.Area", pk + ".Square.Area", "shapes.Square.Area"} { // qualified queries resolve exactly
		if out := byName["find_symbol"](`{"query":"` + q + `"}`); !strings.HasPrefix(out, "method "+pk+".Square.Area  shapes/shape.go:17") {
			t.Errorf("find_symbol %s:\n%s", q, out)
		}
	}
	if out := byName["references"](`{"symbol":"Area"}`); !strings.Contains(out, "matches 3 symbols") {
		t.Errorf("ambiguous name should list candidates:\n%s", out)
	}
}

func TestIncrementalAndPersistence(t *testing.T) {
	root := fixture(t)
	cache := t.TempDir()
	s, g := build(t, root, cache, localRun)
	if s.Timing.Mode != "first → typed" {
		t.Fatalf("first build mode %q", s.Timing.Mode)
	}
	// body-only edit in app: only app is re-checked
	main := filepath.Join(root, "app", "main.go")
	src, _ := os.ReadFile(main)
	_ = os.WriteFile(main, []byte(strings.Replace(string(src), "NewSquare(3)", "NewSquare(4)", 1)), 0o644)
	if g, _, _ = s.Graph(bg, time.Second); s.Timing.Mode != "incremental" || s.Timing.Rechecked != 1 {
		t.Fatalf("body edit: %+v", s.Timing)
	}
	// API change in shapes: a new function, called from app
	shape := filepath.Join(root, "shapes", "shape.go")
	b, _ := os.ReadFile(shape)
	_ = os.WriteFile(shape, append(b, []byte("\nfunc Double(s Shape) float64 { return 2 * s.Area() }\n")...), 0o644)
	src, _ = os.ReadFile(main)
	_ = os.WriteFile(main, []byte(strings.Replace(string(src), "func main() {", "func main() {\n\t_ = shapes.Double(nil)", 1)), 0o644)
	g, _, _ = s.Graph(bg, time.Second)
	if got := refsFrom(g.References(pk+".Double", true)); !strings.Contains(got, "app/main.go") {
		t.Fatalf("new API not linked after incremental update: %q (%+v)", got, s.Timing)
	}
	// a deleted file disappears
	_ = os.Remove(filepath.Join(root, "shapes", "x_test.go"))
	g, _, _ = s.Graph(bg, time.Second)
	if len(g.Resolve("TestX")) != 0 {
		t.Fatal("symbols of a deleted file survived")
	}
	st := g.Stats()
	// a new service on the same cache loads instead of rebuilding
	s2 := NewService(root, cache, "fix", localRun)
	ctx2, cancel2 := context.WithCancel(bg)
	t.Cleanup(func() { cancel2(); s2.Wait() })
	s2.Start(ctx2)
	g2, _, _ := s2.Graph(bg, time.Minute)
	if s2.Timing.Mode != "load" || g2.Stats() != st {
		t.Fatalf("reload: %+v %+v vs %+v", s2.Timing, g2.Stats(), st)
	}
}

func TestSyntaxOnlyFallback(t *testing.T) {
	root := fixture(t)
	fail := func(context.Context, string, []string, ...string) ([]byte, error) {
		return nil, errors.New("go: go.mod requires go >= 9.99 (running go 1.27; GOTOOLCHAIN=local)")
	}
	s, g := build(t, root, t.TempDir(), fail)
	_, note, _ := s.Graph(bg, time.Second)
	if !strings.HasPrefix(note, "syntax-only (types unavailable") || len(g.Resolve("Square.Area")) != 1 || g.Stats().Untyped != 2 {
		t.Fatalf("note=%q stats=%+v", note, g.Stats())
	}
}

// Queries keep working (and stay consistent) while incremental updates run.
func TestQueriesDuringUpdates(t *testing.T) {
	root := fixture(t)
	s, _ := build(t, root, t.TempDir(), localRun)
	main := filepath.Join(root, "app", "main.go")
	src, _ := os.ReadFile(main)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				g, _, err := s.Graph(bg, time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				if len(g.References(pk+".NewSquare", false)) < 2 {
					t.Error("references vanished during an update")
					return
				}
			}
		}()
	}
	for i := range 5 {
		_ = os.WriteFile(main, []byte(strings.Replace(string(src), "NewSquare(3)", "NewSquare("+string(rune('5'+i))+")", 1)), 0o644)
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

// waitDeps waits for the background dependency-table build.
func waitDeps(t *testing.T, s *Service) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		s.mu.Lock()
		n := len(s.man.Deps)
		s.mu.Unlock()
		if n > 0 {
			return
		}
	}
	t.Fatal("dependency tables were not built")
}

func TestDependencyCache(t *testing.T) {
	root := fixture(t)
	cache := t.TempDir()
	svc, _ := build(t, root, cache, localRun)
	waitDeps(t, svc)
	run := map[string]func(string) string{}
	for _, tl := range Tools(svc) {
		run[tl.Spec.Name] = func(a string) string { out, _ := tl.Run(bg, json.RawMessage(a)); return out }
	}
	for tool, c := range map[string][2]string{
		"callees":         {`{"symbol":"example.com/fix/app.main"}`, "fmt.Println  called at app/main.go:11  declared in fmt fmt/print.go:"},
		"find_symbol":     {`{"query":"Stringer","include_deps":true}`, "interface fmt.Stringer  fmt/print.go:"},
		"implementations": {`{"symbol":"fmt.Stringer"}`, "type " + pk + ".Circle  shapes/shape.go:"},
	} {
		if out := run[tool](c[0]); !strings.Contains(out, c[1]) {
			t.Errorf("%s %s:\nwant %q in\n%s", tool, c[0], c[1], out)
		}
	}
	if out := run["implementations"](`{"symbol":"Circle"}`); !strings.Contains(out, "interface fmt.Stringer") {
		t.Errorf("Circle should implement fmt.Stringer:\n%s", out)
	}
	// Tables live under graphs/<ecosystem>/<module>@<version>/<hash>/ …
	tables, _ := filepath.Glob(filepath.Join(cache, "graphs", "go", "std@*", "*", "fmt.gob"))
	if len(tables) != 1 {
		t.Fatalf("fmt table: %v", tables)
	}
	fi, _ := os.Stat(tables[0])
	// … and a second project on the same machine reuses them instead of rebuilding.
	svc2, _ := build(t, fixture(t), cache, localRun)
	waitDeps(t, svc2)
	if fi2, _ := os.Stat(tables[0]); !fi2.ModTime().Equal(fi.ModTime()) {
		t.Error("cached dependency table was rebuilt")
	}
	svc2.mu.Lock()
	t.Logf("dependency tables: %d, second project built in %v (all cache hits)", len(svc2.man.Deps), svc2.Timing.Deps)
	svc2.mu.Unlock()
}

// First visit: an approximate graph is served at once, then replaced in
// place by the typed graph.
func TestFirstPassUpgradesInPlace(t *testing.T) {
	root := fixture(t)
	gate := make(chan struct{})
	slow := func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
		<-gate // hold the typed build until the approximate graph has been checked
		return localRun(ctx, dir, env, argv...)
	}
	s := NewService(root, t.TempDir(), "fix", slow)
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(func() { cancel(); s.Wait() })
	s.Start(ctx)
	g, note, err := s.Graph(bg, 5*time.Second)
	if err != nil || !strings.HasPrefix(note, "approximate") || g.Stats().Untyped != 2 {
		t.Fatalf("first pass: err=%v note=%q stats=%+v", err, note, g.Stats())
	}
	if got := refsFrom(g.References(pk+".NewSquare", false)); !strings.Contains(got, "app/main.go") {
		t.Errorf("name-based refs should find shapes.NewSquare from app: %q", got)
	}
	if got := refsFrom(g.References(pk+".Total", true)); !strings.Contains(got, "app/main.go") {
		t.Errorf("name-based call refs: %q", got)
	}
	close(gate)
	waitBuilt(t, s)
	g, note, _ = s.Graph(bg, time.Second)
	if note != "" || g.Stats().Untyped != 0 || s.Timing.Mode != "first → typed" {
		t.Fatalf("upgrade: note=%q stats=%+v mode=%q", note, g.Stats(), s.Timing.Mode)
	}
	if got := refsFrom(g.References(pk+".Shape.Area", true)); !strings.Contains(got, pk+".Total") {
		t.Errorf("typed graph should resolve the interface call: %q", got)
	}
}

// After an API change is patched in incrementally, the graph is rebuilt in
// full once queries pause.
func TestIdleRebuild(t *testing.T) {
	root := fixture(t)
	s, _ := build(t, root, t.TempDir(), localRun)
	s.mu.Lock()
	s.IdleRebuild = 50 * time.Millisecond
	s.mu.Unlock()
	shape := filepath.Join(root, "shapes", "shape.go")
	b, _ := os.ReadFile(shape)
	_ = os.WriteFile(shape, append(b, []byte("\nfunc Triple(s Shape) float64 { return 3 * s.Area() }\n")...), 0o644)
	_, _, _ = s.Graph(bg, time.Second)
	s.mu.Lock()
	inexact := s.inexact
	s.mu.Unlock()
	if !inexact {
		t.Fatalf("an API change should mark the graph inexact (%+v)", s.Timing)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		done := !s.inexact && !s.building && s.Timing.Mode == "idle → typed"
		s.mu.Unlock()
		if done {
			g, _, _ := s.Graph(bg, time.Second)
			if len(g.Resolve("Triple")) != 1 {
				t.Fatal("rebuilt graph lost the new function")
			}
			return
		}
	}
	t.Fatal("no idle rebuild")
}

func TestKnown(t *testing.T) {
	g := newGraph("/r")
	g.put(&Package{Path: "app/store", Symbols: []Symbol{
		{ID: "app/store.Store", Name: "Store", Kind: KType},
		{ID: "app/store.Store.Flush", Name: "Flush", Kind: KMethod},
		{ID: "app/store.New", Name: "New", Kind: KFunc},
	}})
	for ref, want := range map[string][2]bool{
		"store.New":       {true, true},
		"store.Missing":   {false, true},
		"Store.Flush()":   {true, true},
		"Store.FlushAll":  {false, true},
		"strings.Builder": {false, false}, // not a workspace package: not judged
		"Flush":           {false, false},
	} {
		if e, d := g.Known(ref); e != want[0] || d != want[1] {
			t.Errorf("%s: exists=%v decidable=%v, want %v", ref, e, d, want)
		}
	}
}
