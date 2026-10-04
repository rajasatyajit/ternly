package graph

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const formatVersion = 1

// manifest maps each package to its shard; it is replaced atomically, so a
// reader sees either the old graph or the new one.
type manifest struct {
	Version  int
	Root     string
	Packages map[string]manEntry
	Unowned  map[string]FileInfo // .go files in the tree that no workspace package claims (nested modules, …)
	Exports  map[string]string   // import path → compiler export data (GOCACHE), for incremental re-checks
	Deps     []string            // dependency symbol tables (shared cache) this workspace uses
}

type manEntry struct {
	Shard   string // content-addressed file name
	APIHash string
	Imports []string
	Files   []FileInfo
}

// Service owns a workspace's graph: it builds or loads it in the background,
// keeps it current, and persists it under dir (shared by all sessions of the
// project).
type Service struct {
	Root     string
	dir      string
	cacheDir string
	run      Runner

	depMu sync.Mutex
	deps  *Graph // dependency symbols, loaded on first use

	mu     sync.Mutex
	g      *Graph
	man    *manifest
	ready  chan struct{}
	err    error
	note   string // e.g. "syntax-only: go list failed"
	Timing Timing

	building  bool      // a typed build runs in the background; the current graph is served meanwhile
	inexact   bool      // incremental API-change re-checks mixed type views: rebuild when idle
	lastQuery time.Time // for idle detection
	// IdleRebuild is how long queries must pause before an inexact graph is rebuilt in full.
	IdleRebuild time.Duration

	bg sync.WaitGroup // background goroutines (see Wait)

	// Workers and MemBudget size full builds (0: from available memory; see sizing).
	Workers   int
	MemBudget int64

	dmu      sync.Mutex      // guards the watcher's state (never held across builds)
	dirty    map[string]bool // files the watcher saw change ("*": rescan everything)
	watching bool
	w        *watcher
}

// Timing records what the last build or update cost.
type Timing struct {
	Mode                          string // load, full, incremental
	List, Check, Save, Load, Deps time.Duration
	Packages, Rechecked, DepPkgs  int
}

// NewService prepares a graph for root stored under cacheDir/graphs/projects/key.
func NewService(root, cacheDir, key string, run Runner) *Service {
	return &Service{Root: root, dir: filepath.Join(cacheDir, "graphs", "projects", key), cacheDir: cacheDir, run: run, ready: make(chan struct{}), dirty: map[string]bool{}, IdleRebuild: 2 * time.Minute}
}

// Start builds or loads the graph in the background.
func (s *Service) Start(ctx context.Context) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.watchInit() // before building: edits made meanwhile are caught by the next refresh
		err := s.refresh(ctx)
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.ready)
		s.spawn(func() { s.idleLoop(ctx) })
		s.watchLoop(ctx)
	}()
}

// spawn runs f in a goroutine that Wait waits for.
func (s *Service) spawn(f func()) {
	s.bg.Add(1)
	go func() { defer s.bg.Done(); f() }()
}

// Wait returns when every background goroutine has stopped (cancel the
// context given to Start first).
func (s *Service) Wait() { s.bg.Wait() }

// idleLoop rebuilds an inexact graph (after API-change incremental updates)
// once queries have paused for IdleRebuild.
func (s *Service) idleLoop(ctx context.Context) {
	for {
		s.mu.Lock()
		tick := min(time.Second, max(s.IdleRebuild/4, 10*time.Millisecond))
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
		s.mu.Lock()
		if s.inexact && !s.building && time.Since(s.lastQuery) >= s.IdleRebuild {
			s.building = true
			s.spawn(func() { s.background(ctx, "idle") })
		}
		s.mu.Unlock()
	}
}

// Graph waits (up to wait) for the first build, applies pending changes and
// returns the current graph. The note explains reduced fidelity.
func (s *Service) Graph(ctx context.Context, wait time.Duration) (*Graph, string, error) {
	select {
	case <-s.ready:
	case <-time.After(wait):
		return nil, "", errors.New("the code graph is still being built — use grep/read_file meanwhile, or retry shortly")
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	if err := s.refresh(ctx); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastQuery = time.Now()
	return s.g, s.note, s.err
}

// refresh loads the persisted graph if needed, then brings it up to date.
func (s *Service) refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.building { // serve the current graph; edits since are applied after the swap
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	lock, err := lockWait(ctx, filepath.Join(s.dir, "build.lock")) // one builder per project at a time
	if err != nil {
		return err
	}
	defer lock.Close()
	if s.g == nil {
		t0 := time.Now()
		s.g, s.man = s.load()
		s.Timing = Timing{Mode: "load", Load: time.Since(t0), Packages: len(s.man.Packages)}
		if len(s.man.Packages) == 0 { // first visit: an approximate graph now, the typed one in the background
			t0 = time.Now()
			g := newGraph(s.Root)
			for _, p := range syntaxGraph(s.Root) {
				g.put(p)
			}
			s.g = g
			s.note = "approximate: names matched without types while the typed graph builds"
			s.Timing = Timing{Mode: "syntax", Check: time.Since(t0), Packages: len(g.pkgs)}
			s.building = true
			s.spawn(func() { s.background(ctx, "first") })
			return nil
		}
	}
	s.drain() // pick up changes made just before this refresh
	s.dmu.Lock()
	dirty, watching := s.dirty, s.watching
	s.dirty = map[string]bool{}
	s.dmu.Unlock()
	if watching && len(dirty) == 0 && len(s.man.Packages) > 0 {
		return nil // the watcher saw nothing change
	}
	var changed, gone []string
	if watching && !dirty["*"] && len(s.man.Packages) > 0 {
		changed, gone = s.check(dirty)
	} else {
		changed, gone = s.scan()
	}
	if len(s.man.Packages) == 0 {
		return s.full(ctx)
	}
	if len(changed) == 0 && len(gone) == 0 {
		return nil
	}
	return s.incremental(ctx, changed, gone)
}

// buildResult is a complete graph, built without holding the service lock.
type buildResult struct {
	g      *Graph
	man    *manifest
	pkgs   []*Package
	b      *builder // nil: syntax-only
	lps    []*listPkg
	note   string
	timing Timing
}

// buildFull builds the whole workspace graph (typed, or syntax-only if `go
// list` fails). It touches no Service state, so queries keep being served.
func (s *Service) buildFull(ctx context.Context) buildResult {
	t0 := time.Now()
	lps, err := listPackages(ctx, s.run, s.Root, nil, false)
	tList := time.Since(t0)
	r := buildResult{g: newGraph(s.Root), lps: lps,
		man: &manifest{Version: formatVersion, Root: s.Root, Packages: map[string]manEntry{}, Unowned: map[string]FileInfo{}, Exports: map[string]string{}}}
	if err != nil {
		r.note = "syntax-only (types unavailable: " + firstLine(err.Error()) + ")"
		r.pkgs = syntaxGraph(s.Root)
	} else {
		r.b = newBuilder(s.Root, lps)
		budget, workers := s.sizing()
		r.b.workers = workers
		prev := debug.SetMemoryLimit(budget) // soft limit: the GC works harder instead of overshooting
		defer debug.SetMemoryLimit(prev)
		var jobs []*listPkg
		for _, p := range lps {
			if workspace(p, s.Root) {
				jobs = append(jobs, p)
			}
		}
		r.pkgs, _ = r.b.buildAll(ctx, jobs)
		for _, p := range lps {
			if p.ForTest == "" && p.Export != "" && !strings.Contains(p.ImportPath, " ") {
				r.man.Exports[p.ImportPath] = p.Export
			}
		}
	}
	for _, p := range r.pkgs {
		if p != nil {
			r.g.put(p)
		}
	}
	s.trackUnowned(r.g, r.man, nil)
	r.timing = Timing{Mode: "full", List: tList, Check: time.Since(t0) - tList, Packages: len(r.pkgs), Rechecked: len(r.pkgs)}
	return r
}

// install persists a build result and makes it current (s.mu held).
func (s *Service) install(ctx context.Context, r buildResult) error {
	t0 := time.Now()
	if err := s.save(r.man, r.pkgs); err != nil {
		return err
	}
	r.timing.Save = time.Since(t0)
	s.g, s.man, s.note, s.Timing, s.inexact = r.g, r.man, r.note, r.timing, false
	if r.b != nil { // dependency tables after the workspace graph is usable; the importer has them decoded already
		s.spawn(func() { s.depsAfterBuild(ctx, r.b, r.lps) })
	}
	return nil
}

// full builds and installs synchronously (s.mu held): the incremental fallback.
func (s *Service) full(ctx context.Context) error {
	return s.install(ctx, s.buildFull(ctx))
}

// background builds a full typed graph off the lock and swaps it in: after
// the first-visit approximate pass, and for idle rebuilds of inexact graphs.
func (s *Service) background(ctx context.Context, why string) {
	lock, err := lockWait(ctx, filepath.Join(s.dir, "build.lock"))
	if err != nil {
		s.mu.Lock()
		s.building = false
		s.mu.Unlock()
		return
	}
	r := s.buildFull(ctx)
	lock.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.building = false
	if ctx.Err() != nil {
		return
	}
	if err := s.install(ctx, r); err == nil {
		s.Timing.Mode = why + " → typed"
	}
}

func (s *Service) depsAfterBuild(ctx context.Context, b *builder, lps []*listPkg) {
	t0 := time.Now()
	ver := "unknown"
	if out, err := s.run(ctx, s.Root, goEnv, "go", "env", "GOVERSION"); err == nil {
		ver = strings.TrimSpace(string(out))
	}
	dirs := map[string]string{}
	for _, p := range lps {
		dirs[p.ImportPath] = p.Dir
		if p.Module != nil && p.Module.Dir != "" && !p.Standard {
			dirs[p.ImportPath] = p.Module.Dir
		}
	}
	paths := s.buildDeps(ctx, b, depKeys(s.Root, ver, lps), dirs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.man == nil {
		return
	}
	s.man.Deps = paths
	_ = s.save(s.man, nil)
	s.Timing.Deps, s.Timing.DepPkgs = time.Since(t0), len(paths)
	s.depMu.Lock()
	s.deps = nil // reload with the new list
	s.depMu.Unlock()
}

// Deps returns the dependency symbol graph (empty until it has been built).
func (s *Service) Deps() *Graph {
	s.mu.Lock()
	var paths []string
	if s.man != nil {
		paths = s.man.Deps
	}
	s.mu.Unlock()
	s.depMu.Lock()
	defer s.depMu.Unlock()
	if s.deps == nil {
		s.deps = loadDeps(s.Root, paths)
	}
	return s.deps
}

// incremental re-checks the packages owning changed files (and workspace
// importers of any whose API changed), patching graph and store in place.
func (s *Service) incremental(ctx context.Context, changed, gone []string) error {
	t0 := time.Now()
	var stillUnowned []string // edits to files no package claims: just re-hash them
	kept := changed[:0]
	for _, f := range changed {
		if _, ok := s.man.Unowned[f]; ok {
			stillUnowned = append(stillUnowned, f)
		} else {
			kept = append(kept, f)
		}
	}
	changed = kept
	if len(stillUnowned) > 0 {
		s.trackUnowned(s.g, s.man, stillUnowned)
		if len(changed) == 0 && len(gone) == 0 {
			return s.save(s.man, nil)
		}
	}
	dirs := map[string]bool{}
	for _, f := range append(append([]string{}, changed...), gone...) {
		dirs["./"+filepath.ToSlash(filepath.Dir(f))] = true
	}
	var patterns []string
	for d := range dirs {
		if hasGo(filepath.Join(s.Root, d)) {
			patterns = append(patterns, d)
		}
	}
	sort.Strings(patterns)
	removed := map[string]bool{}
	for path, e := range s.man.Packages { // packages whose directory lost all Go files
		if len(e.Files) > 0 && !hasGo(filepath.Join(s.Root, filepath.Dir(e.Files[0].Name))) {
			removed[path] = true
		}
	}
	// Changed packages are re-checked from source against the export data
	// recorded at the last full build (no compilation); if a package's API
	// changed, its workspace importers are re-checked against the fresh
	// in-memory package. Importers may then mix the new package with older
	// dependencies' view of it: such type errors are counted, edges are still
	// recorded, and the next full build is exact again.
	var rechecked []*Package
	var tList time.Duration
	local := map[string]*types.Package{}
	apiRound := false
	for round := 0; len(patterns) > 0 && round < 2; round++ {
		done := map[string]bool{}
		tl := time.Now()
		lps, err := listPackages(ctx, s.run, s.Root, patterns, true)
		if err != nil {
			return s.full(ctx) // fall back to a full (possibly syntax-only) build
		}
		var jobs []*listPkg
		for _, p := range lps {
			if workspace(p, s.Root) && !done[p.ImportPath] {
				jobs = append(jobs, p)
				done[p.ImportPath] = true
			}
		}
		if missing := s.missingExports(jobs, local); len(missing) > 0 { // e.g. a newly added import
			if more, err := listPackages(ctx, s.run, s.Root, missing, false); err == nil {
				for _, p := range more {
					if p.ForTest == "" && p.Export != "" && !strings.Contains(p.ImportPath, " ") {
						s.man.Exports[p.ImportPath] = p.Export
					}
				}
			}
		}
		tList += time.Since(tl)
		pkgs, typed := newBuilderFrom(s.Root, s.man.Exports, local).buildAll(ctx, jobs)
		for k, v := range typed {
			local[k] = v
		}
		patterns = nil
		for _, p := range pkgs {
			if p == nil {
				continue
			}
			rechecked = append(rechecked, p)
			// API changed: importers — including ones re-checked this round
			// against the old export data — are checked again against the new package.
			if old, ok := s.man.Packages[p.Path]; round == 0 && (!ok || old.APIHash != p.APIHash) {
				apiRound = true
				for path, e := range s.man.Packages {
					if path != p.Path && contains(e.Imports, p.Path) && len(e.Files) > 0 {
						patterns = append(patterns, "./"+filepath.Dir(e.Files[0].Name))
					}
				}
			}
		}
	}
	tCheck := time.Since(t0) - tList
	last := map[string]*Package{} // a package re-checked in both rounds: keep the later result
	for _, p := range rechecked {
		last[p.Path] = p
	}
	rechecked = rechecked[:0]
	for _, p := range last {
		rechecked = append(rechecked, p)
	}
	t1 := time.Now()
	s.g.mu.Lock()
	for path := range removed {
		if p := s.g.pkgs[path]; p != nil {
			s.g.remove(p)
		}
		delete(s.man.Packages, path)
	}
	for _, p := range rechecked {
		s.g.put(p)
	}
	s.g.mu.Unlock()
	for _, f := range gone {
		delete(s.man.Unowned, f)
	}
	s.trackUnowned(s.g, s.man, changed)
	if err := s.save(s.man, rechecked); err != nil {
		return err
	}
	s.Timing = Timing{Mode: "incremental", List: tList, Check: tCheck, Save: time.Since(t1), Packages: len(s.man.Packages), Rechecked: len(rechecked)}
	if apiRound {
		s.inexact = true // importers saw a mix of old and new types: rebuild exactly when idle
	}
	return ctx.Err()
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func hasGo(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	return len(m) > 0
}

// scan compares the workspace's Go files against the manifest: stat first,
// hashing only files whose size or mtime changed.
func (s *Service) scan() (changed, gone []string) {
	known := s.known()
	seen := map[string]bool{}
	s.walk(func(rel string, d fs.DirEntry) {
		seen[rel] = true
		k, ok := known[rel]
		if !ok {
			if len(known) > 0 {
				changed = append(changed, rel) // a new file
			}
			return
		}
		fi, err := d.Info()
		if err != nil || (fi.Size() == k.Size && fi.ModTime().UnixNano() == k.MTime) {
			return
		}
		if b, err := os.ReadFile(filepath.Join(s.Root, rel)); err == nil && hashBytes(b) != k.Hash {
			changed = append(changed, rel)
		}
	})
	for name := range known {
		if !seen[name] {
			gone = append(gone, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(gone)
	return changed, gone
}

// known is every file the manifest tracks, package-owned or not.
func (s *Service) known() map[string]FileInfo {
	known := map[string]FileInfo{}
	for _, e := range s.man.Packages {
		for _, f := range e.Files {
			known[f.Name] = f
		}
	}
	for n, f := range s.man.Unowned {
		known[n] = f
	}
	return known
}

// missingExports lists imports of jobs with no usable export data recorded.
func (s *Service) missingExports(jobs []*listPkg, local map[string]*types.Package) []string {
	inJobs := map[string]bool{}
	for _, j := range jobs {
		inJobs[j.ImportPath] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		for _, imp := range append(append(append([]string{}, j.Imports...), j.TestImports...), j.XTestImports...) {
			if seen[imp] || imp == "C" || imp == "unsafe" || inJobs[imp] || local[imp] != nil {
				continue
			}
			seen[imp] = true
			if f, ok := s.man.Exports[imp]; !ok || !exists(f) { // never seen, or trimmed from GOCACHE
				out = append(out, imp)
			}
		}
	}
	sort.Strings(out)
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// trackUnowned records Go files that no package claims, so scans don't report
// them as new every time. With paths nil it walks the whole tree (full build).
func (s *Service) trackUnowned(g *Graph, man *manifest, paths []string) {
	g.mu.RLock()
	owned := func(rel string) bool { _, ok := g.byFile[rel]; return ok }
	defer g.mu.RUnlock()
	note := func(rel string) {
		if owned(rel) {
			delete(man.Unowned, rel)
			return
		}
		full := filepath.Join(s.Root, rel)
		if b, err := os.ReadFile(full); err == nil {
			fi := FileInfo{Name: rel, Hash: hashBytes(b)}
			if st, err := os.Stat(full); err == nil {
				fi.Size, fi.MTime = st.Size(), st.ModTime().UnixNano()
			}
			man.Unowned[rel] = fi
		}
	}
	if paths != nil {
		for _, p := range paths {
			note(p)
		}
		return
	}
	s.walk(func(rel string, _ fs.DirEntry) { note(rel) })
}

// walk visits the workspace's Go files (relative paths).
func (s *Service) walk(visit func(rel string, d fs.DirEntry)) {
	_ = filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != s.Root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, path)
		visit(filepath.ToSlash(rel), d)
		return nil
	})
}

// check classifies the files the watcher reported.
func (s *Service) check(paths map[string]bool) (changed, gone []string) {
	known := s.known()
	for p := range paths {
		b, err := os.ReadFile(filepath.Join(s.Root, p))
		k, ok := known[p]
		switch {
		case err != nil && ok:
			gone = append(gone, p)
		case err == nil && (!ok || hashBytes(b) != k.Hash):
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	sort.Strings(gone)
	return changed, gone
}

// skipDir: directories that are not part of the workspace graph.
func skipDir(name string) bool {
	return name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// save writes changed packages' shards, then swaps the manifest, then deletes
// shards nothing references: a crash at any point leaves a consistent graph.
func (s *Service) save(man *manifest, pkgs []*Package) error {
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(p); err != nil {
			return err
		}
		name := hashBytes(buf.Bytes()) + ".gob"
		path := filepath.Join(s.dir, name)
		if _, err := os.Stat(path); err != nil {
			if err := writeAtomic(path, buf.Bytes()); err != nil {
				return err
			}
		}
		man.Packages[p.Path] = manEntry{Shard: name, APIHash: p.APIHash, Imports: p.Imports, Files: p.Files}
	}
	b, err := json.Marshal(man)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.dir, "manifest.json"), b); err != nil {
		return err
	}
	live := map[string]bool{}
	for _, e := range man.Packages {
		live[e.Shard] = true
	}
	old, _ := filepath.Glob(filepath.Join(s.dir, "*.gob"))
	for _, p := range old {
		if !live[filepath.Base(p)] {
			_ = os.Remove(p)
		}
	}
	return nil
}

// load reads the persisted graph (an empty one if absent or unreadable).
func (s *Service) load() (*Graph, *manifest) {
	g := newGraph(s.Root)
	empty := &manifest{Version: formatVersion, Root: s.Root, Packages: map[string]manEntry{}, Unowned: map[string]FileInfo{}, Exports: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(s.dir, "manifest.json"))
	var man manifest
	if err != nil || json.Unmarshal(b, &man) != nil || man.Version != formatVersion || man.Root != s.Root {
		return g, empty
	}
	if man.Unowned == nil {
		man.Unowned = map[string]FileInfo{}
	}
	if man.Exports == nil {
		man.Exports = map[string]string{}
	}
	type res struct {
		p   *Package
		err error
	}
	paths := make([]string, 0, len(man.Packages))
	for p := range man.Packages {
		paths = append(paths, p)
	}
	out := make([]res, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, path := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f, err := os.Open(filepath.Join(s.dir, man.Packages[path].Shard))
			if err != nil {
				out[i].err = err
				return
			}
			defer f.Close()
			p := &Package{}
			out[i] = res{p, gob.NewDecoder(f).Decode(p)}
		}()
	}
	wg.Wait()
	for _, r := range out {
		if r.err != nil {
			return newGraph(s.Root), empty // damaged cache: rebuild
		}
		g.put(r.p)
	}
	return g, &man
}

func writeAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lockWait takes an exclusive flock, waiting for another builder if needed.
func lockWait(ctx context.Context, p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
