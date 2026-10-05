package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Python, TypeScript, Rust and Java in one workspace without go.mod:
// definitions with qualified IDs and file:line, calls resolved by name
// (same file first, then directory, then unique), shared names marked
// approximate, and an edit picked up by the next query.
func TestForeignGraph(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/store.py": "class Store:\n    def flush(self):\n        return helper()\n\ndef helper():\n    return 1\n",
		"app/main.py":  "from app.store import Store\n\ndef run():\n    s = Store()\n    s.flush()\n    save()\n",
		"app/other.py": "def save():\n    pass\n",
		"app/cache.py": "class Cache:\n    def flush(self):\n        pass\n\ndef clear(c):\n    c.flush()\n",
		"web/store.ts": "export class Store {\n  flush(): number { return compute(); }\n}\nfunction compute(): number { return 2; }\n",
		"web/util.ts":  "export function save(): void {}\n",
		"src/lib.rs":   "pub struct Store { d: u8 }\nimpl Store {\n    pub fn flush(&self) -> u8 { tidy() }\n}\nfn tidy() -> u8 { 0 }\n",
		"j/A.java":     "class A {\n  int flush() { return tidy(); }\n  int tidy() { return 0; }\n}\n",
	}
	for p, c := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	if !HasSources(root) {
		t.Fatal("HasSources: false")
	}
	svc := NewService(root, t.TempDir(), "k", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.Wait() }()
	svc.Start(ctx)
	g, _, err := svc.Graph(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ss := g.Resolve("Store.flush")
	files2 := map[string]bool{}
	for _, s := range ss {
		files2[s.Pos.File] = true
		if s.Pos.Line != 2 && s.Pos.File != "src/lib.rs" {
			t.Errorf("%s at line %d", s.ID, s.Pos.Line)
		}
	}
	if !files2["app/store.py"] || !files2["web/store.ts"] {
		t.Errorf("Store.flush resolved to %v", symIDs(ss))
	}
	if s := g.Symbol("app/store.Store.flush"); s == nil || s.Kind != KMethod {
		t.Fatalf("app/store.Store.flush: %+v", s)
	}
	// helper: called in the same file → exact.
	refs := g.References("app/store.helper", true)
	if len(refs) != 1 || refs[0].From != "app/store.Store.flush" || refs[0].Approx {
		t.Errorf("helper callers: %+v", refs)
	}
	// flush: two in app/ (Store, Cache), more elsewhere. A call in a file
	// that defines one resolves to it; a call from app/main.py has two
	// same-directory candidates: both, marked approximate.
	if refs := g.References("app/cache.Cache.flush", true); len(refs) < 1 || refs[0].From != "app/cache.clear" {
		t.Errorf("Cache.flush callers: %+v", refs)
	}
	var fromMain []*Ref
	for _, id := range []string{"app/store.Store.flush", "app/cache.Cache.flush"} {
		for _, r := range g.References(id, true) {
			if r.From == "app/main.run" {
				fromMain = append(fromMain, r)
			}
		}
	}
	if len(fromMain) != 2 || !fromMain[0].Approx || !fromMain[1].Approx {
		t.Errorf("s.flush() in app/main.py: %+v", fromMain)
	}
	for _, r := range g.References("web/store.Store.flush", true) {
		if strings.HasPrefix(r.Pos.File, "app/") {
			t.Errorf("a Python call linked to the TypeScript method: %+v", r)
		}
	}
	// save(): app/other.py (same directory) wins over web/util.ts.
	if refs := g.References("app/other.save", true); len(refs) != 1 || refs[0].Approx {
		t.Errorf("save callers: %+v", refs)
	}
	if refs := g.References("web/util.save", true); len(refs) != 0 {
		t.Errorf("web/util.save got the Python caller: %+v", refs)
	}
	// tidy(): defined in Rust and in Java; each language's call stays home.
	for _, r := range g.References("src/lib.tidy", true) {
		if !strings.HasSuffix(r.Pos.File, ".rs") {
			t.Errorf("a non-Rust call linked to the Rust tidy: %+v", r)
		}
	}
	// An edit: a new function shows up on the next query.
	_ = os.WriteFile(filepath.Join(root, "app/other.py"), []byte("def save():\n    pass\n\ndef archive():\n    save()\n"), 0o644)
	time.Sleep(1100 * time.Millisecond) // scans are throttled to one a second
	g, _, _ = svc.Graph(ctx, time.Second)
	if g.Symbol("app/other.archive") == nil {
		t.Fatal("edit not picked up")
	}
}

func symIDs(ss []*Symbol) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return strings.Join(out, ", ")
}

// TestForeignBench (TERNLY_FOREIGN_BENCH=dir1,dir2…): full build (every file
// tagged), reload with an unchanged tree (nothing re-tagged), one file edited,
// and the graph's size.
func TestForeignBench(t *testing.T) {
	dirs := os.Getenv("TERNLY_FOREIGN_BENCH")
	if dirs == "" {
		t.Skip("TERNLY_FOREIGN_BENCH not set")
	}
	for _, root := range strings.Split(dirs, ",") {
		cache := filepath.Join(t.TempDir(), "foreign.json")
		f := newForeign(root, cache)
		t0 := time.Now()
		f.update(context.Background(), 16)
		full := time.Since(t0)
		t0 = time.Now()
		pk := f.packages()
		g := newGraph(root)
		for _, p := range pk {
			g.put(p)
		}
		index := time.Since(t0)
		syms, refs, approx := 0, 0, 0
		for _, p := range pk {
			syms += len(p.Symbols)
			refs += len(p.Refs)
			for _, r := range p.Refs {
				if r.Approx {
					approx++
				}
			}
		}
		f2 := newForeign(root, cache) // a new session: tags from the cache
		t0 = time.Now()
		f2.update(context.Background(), 16)
		reload := time.Since(t0)
		var one string
		for rel := range f2.files {
			one = rel
			break
		}
		p := filepath.Join(root, one)
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, append(b, '\n'), 0o644)
		t0 = time.Now()
		f2.update(context.Background(), 16)
		edit := time.Since(t0)
		_ = os.WriteFile(p, b, 0o644)
		fi, _ := os.Stat(cache)
		t.Logf("%s: %d files, %d symbols, %d call edges (%.0f%% name-ambiguous); full %v + index %v; reload %v; one edit %v; cache %d KB",
			filepath.Base(root), len(f.files), syms, refs, 100*float64(approx)/float64(max(refs, 1)), full.Round(time.Millisecond), index.Round(time.Millisecond),
			reload.Round(time.Millisecond), edit.Round(time.Millisecond), fi.Size()>>10)
	}
}
