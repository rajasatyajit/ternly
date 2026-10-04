package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Runner runs a command in the workspace (in the sandbox) and returns stdout.
type Runner func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error)

// listPkg is the subset of `go list -json` the graph needs.
type listPkg struct {
	ImportPath     string
	Name           string
	Dir            string
	Export         string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string // excluded by build constraints: tracked for changes, not parsed
	Imports        []string
	TestImports    []string
	XTestImports   []string
	Standard       bool
	DepOnly        bool
	ForTest        string
	Module         *struct {
		Path    string
		Version string
		Dir     string
		Main    bool
	}
	Error *struct{ Err string }
}

const listFields = "ImportPath,Name,Dir,Export,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,IgnoredGoFiles,Imports,TestImports,XTestImports,Standard,DepOnly,ForTest,Module,Error"

// goEnv: no silent toolchain downloads (the repo is untrusted).
var goEnv = []string{"GOTOOLCHAIN=local", "GOFLAGS="}

// listPackages lists the workspace's packages (all main modules) with
// export data for every dependency, test dependencies included. With light,
// it only lists the named packages' files and imports (no compilation).
func listPackages(ctx context.Context, run Runner, root string, patterns []string, light bool) ([]*listPkg, error) {
	if patterns == nil {
		out, err := run(ctx, root, goEnv, "go", "list", "-m", "-json")
		if err != nil {
			return nil, err
		}
		for dec := json.NewDecoder(bytes.NewReader(out)); ; {
			var m struct{ Path string }
			if err := dec.Decode(&m); err != nil {
				break
			}
			patterns = append(patterns, m.Path+"/...")
		}
	}
	if len(patterns) == 0 {
		return nil, errors.New("no Go modules here")
	}
	args := []string{"go", "list", "-e", "-json=" + listFields, "-export", "-deps", "-test"}
	if light {
		args = args[:4]
	}
	args = append(args, patterns...)
	out, err := run(ctx, root, goEnv, args...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var pkgs []*listPkg
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		p := &listPkg{}
		if err := dec.Decode(p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// workspace reports whether p is one of the workspace's own (non-test-variant) packages.
func workspace(p *listPkg, root string) bool {
	if p.ForTest != "" || strings.Contains(p.ImportPath, " ") || strings.HasSuffix(p.ImportPath, ".test") || p.DepOnly {
		return false
	}
	if p.Module == nil || !p.Module.Main {
		return false
	}
	rel, err := filepath.Rel(root, p.Dir)
	return err == nil && !strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel+"/", "vendor/")
}

// builder type-checks workspace packages in parallel.
type builder struct {
	root    string
	fset    *token.FileSet
	export  map[string]string // import path → export data file
	shared  *sharedImporter
	workers int // 0: GOMAXPROCS
}

func newBuilder(root string, pkgs []*listPkg) *builder {
	exports := map[string]string{}
	for _, p := range pkgs {
		if p.ForTest == "" && p.Export != "" && !strings.Contains(p.ImportPath, " ") {
			exports[p.ImportPath] = p.Export
		}
	}
	return newBuilderFrom(root, exports, nil)
}

// newBuilderFrom builds from known export data; packages in local (fresh,
// type-checked from source) take precedence over their export data.
func newBuilderFrom(root string, exports map[string]string, local map[string]*types.Package) *builder {
	b := &builder{root: root, fset: token.NewFileSet(), export: exports}
	b.shared = &sharedImporter{done: map[string]*types.Package{}}
	for path, p := range local {
		b.shared.done[path] = p
	}
	b.shared.gc = importer.ForCompiler(b.fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := b.export[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(f)
	})
	return b
}

// sharedImporter is one export-data importer for all workers: each
// dependency is decoded once (per-worker caches cost ~2 GB each on
// kubernetes). Imports are serialised, and every imported package is resolved
// eagerly while the lock is held, so the type checkers running in parallel
// only ever read fully constructed objects.
type sharedImporter struct {
	mu   sync.Mutex
	gc   types.Importer
	done map[string]*types.Package
}

func (si *sharedImporter) Import(path string) (*types.Package, error) {
	si.mu.Lock()
	defer si.mu.Unlock()
	if p := si.done[path]; p != nil {
		return p, nil
	}
	p, err := si.gc.Import(path)
	if err != nil {
		return nil, err
	}
	resolveAll(p, map[*types.Package]bool{})
	si.done[path] = p
	return p, nil
}

// resolveAll forces lazy imported objects (and those of p's imports) into existence.
func resolveAll(p *types.Package, seen map[*types.Package]bool) {
	if seen[p] {
		return
	}
	seen[p] = true
	sc := p.Scope()
	for _, n := range sc.Names() {
		obj := sc.Lookup(n)
		t := obj.Type()
		_ = t.Underlying()
		if nt, ok := t.(*types.Named); ok {
			for i := range nt.NumMethods() {
				_ = nt.Method(i).Type()
			}
			if it, ok := nt.Underlying().(*types.Interface); ok {
				_ = it.NumMethods()
			}
		}
	}
	for _, imp := range p.Imports() {
		resolveAll(imp, seen)
	}
}

// importer resolves path from local (the package under test, for its
// external tests) or the shared export-data importer.
func (b *builder) importer(local map[string]*types.Package) types.Importer {
	return importerFunc(func(path string) (*types.Package, error) {
		if p := local[path]; p != nil {
			return p, nil
		}
		return b.shared.Import(path)
	})
}

type importerFunc func(string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// buildAll type-checks every job with GOMAXPROCS workers. It returns the
// graph slices and the type-checked packages (for importers re-checked next).
func (b *builder) buildAll(ctx context.Context, jobs []*listPkg) ([]*Package, map[string]*types.Package) {
	out := make([]*Package, len(jobs))
	tps := make([]*types.Package, len(jobs))
	work := make(chan int)
	var wg sync.WaitGroup
	n := b.workers
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				out[i], tps[i] = b.check(jobs[i])
			}
		}()
	}
	for i := range jobs {
		if ctx.Err() != nil {
			break
		}
		work <- i
	}
	close(work)
	wg.Wait()
	typed := map[string]*types.Package{}
	for i, tp := range tps {
		if tp != nil {
			typed[jobs[i].ImportPath] = tp
		}
	}
	return out, typed
}

// check parses and type-checks one package (with its in-package tests), then
// its external test package against the in-memory result.
func (b *builder) check(lp *listPkg) (*Package, *types.Package) {
	p := &Package{Path: lp.ImportPath, Dir: b.rel(lp.Dir), Imports: lp.Imports, Typed: true}
	srcs := append(append(append([]string{}, lp.GoFiles...), lp.CgoFiles...), lp.TestGoFiles...)
	files := b.parse(p, lp.Dir, srcs)
	ex := newExtractor(b, p)
	tpkg := ex.check(lp.ImportPath, files, b.importer(nil))
	if len(lp.XTestGoFiles) > 0 {
		xfiles := b.parse(p, lp.Dir, lp.XTestGoFiles)
		local := map[string]*types.Package{}
		if tpkg != nil {
			local[lp.ImportPath] = tpkg
		}
		ex.check(lp.ImportPath+"_test", xfiles, b.importer(local))
	}
	for _, n := range lp.IgnoredGoFiles {
		path := filepath.Join(lp.Dir, n)
		if src, err := os.ReadFile(path); err == nil {
			info := FileInfo{Name: b.rel(path), Hash: hashBytes(src), Ignored: true}
			if fi, err := os.Stat(path); err == nil {
				info.Size, info.MTime = fi.Size(), fi.ModTime().UnixNano()
			}
			p.Files = append(p.Files, info)
		}
	}
	p.APIHash = apiHash(p)
	return p, tpkg
}

// apiHash fingerprints what importers can see: exported declarations and
// their signatures and method sets (not function bodies).
func apiHash(p *Package) string {
	var lines []string
	for _, s := range p.Symbols {
		if !s.Test && token.IsExported(s.Name) {
			lines = append(lines, s.ID+"|"+s.Sig+"|"+strings.Join(s.Methods, ";"))
		}
	}
	sort.Strings(lines)
	return hashBytes([]byte(strings.Join(lines, "\n")))
}

func (b *builder) parse(p *Package, dir string, names []string) []*ast.File {
	var files []*ast.File
	for _, n := range names {
		path := filepath.Join(dir, n)
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fi, _ := os.Stat(path)
		info := FileInfo{Name: b.rel(path), Hash: hashBytes(src), Test: strings.HasSuffix(n, "_test.go")}
		if fi != nil {
			info.Size, info.MTime = fi.Size(), fi.ModTime().UnixNano()
		}
		p.Files = append(p.Files, info)
		f, err := parser.ParseFile(b.fset, path, src, parser.SkipObjectResolution)
		if f != nil { // keep partial ASTs of files with syntax errors
			files = append(files, f)
		}
		if err != nil {
			p.Errors++
		}
	}
	return files
}

func (b *builder) rel(path string) string {
	if r, err := filepath.Rel(b.root, path); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return path
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

// syntaxGraph builds an approximate graph without types, in seconds: the
// first pass on a new workspace (served while the typed build runs) and the
// fallback when `go list` can't run. Symbols are exact; references are
// matched by name — imported-package members, same-package names, and
// method calls whose name is unique in the workspace — so they can miss
// uses and, with shadowing, include false ones.
func syntaxGraph(root string) []*Package {
	mods := map[string]string{} // module root dir → module path
	dirs := map[string][]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Name() == "go.mod":
			if b, err := os.ReadFile(path); err == nil {
				for _, ln := range strings.Split(string(b), "\n") {
					if f := strings.Fields(ln); len(f) == 2 && f[0] == "module" {
						mods[filepath.Dir(path)] = strings.Trim(f[1], `"`)
					}
				}
			}
		case strings.HasSuffix(path, ".go"):
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], d.Name())
		}
		return nil
	})
	importPath := func(dir string) string {
		for d := dir; ; d = filepath.Dir(d) {
			if m, ok := mods[d]; ok {
				rel, _ := filepath.Rel(d, dir)
				if rel == "." {
					return m
				}
				return m + "/" + filepath.ToSlash(rel)
			}
			if d == root || d == filepath.Dir(d) {
				rel, _ := filepath.Rel(root, dir)
				return filepath.ToSlash(rel)
			}
		}
	}
	type job struct {
		dir   string
		names []string
		p     *Package
	}
	var jobs []*job
	for dir, names := range dirs {
		sort.Strings(names)
		jobs = append(jobs, &job{dir: dir, names: names})
	}
	b := &builder{root: root, fset: token.NewFileSet()}
	parallel := func(f func(*job)) {
		work := make(chan *job)
		var wg sync.WaitGroup
		for range runtime.GOMAXPROCS(0) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range work {
					f(j)
				}
			}()
		}
		for _, j := range jobs {
			work <- j
		}
		close(work)
		wg.Wait()
	}
	// pass 1: declarations
	parallel(func(j *job) {
		p := &Package{Path: importPath(j.dir), Dir: b.rel(j.dir)}
		fset := token.NewFileSet()
		pb := &builder{root: root, fset: fset}
		files := pb.parse(p, j.dir, j.names)
		ex := newExtractor(pb, p)
		for _, f := range files {
			ex.declare(f, nil)
		}
		j.p = p
	})
	// index: package → name → ID, and method name → IDs
	pkgNames := map[string]map[string]string{}
	methods := map[string][]string{}
	for _, j := range jobs {
		m := map[string]string{}
		for _, s := range j.p.Symbols {
			switch s.Kind {
			case KMethod:
				methods[s.Name] = append(methods[s.Name], s.ID)
			case KField:
			default:
				m[s.Name] = s.ID
			}
		}
		pkgNames[j.p.Path] = m
	}
	// pass 2: references by name
	parallel(func(j *job) {
		fset := token.NewFileSet()
		pb := &builder{root: root, fset: fset}
		scratch := &Package{}
		files := pb.parse(scratch, j.dir, j.names)
		ex := newExtractor(pb, j.p)
		own := pkgNames[j.p.Path]
		for _, f := range files {
			imports := map[string]string{}
			for _, is := range f.Imports {
				path := strings.Trim(is.Path.Value, `"`)
				name := pathName(path)
				if is.Name != nil {
					name = is.Name.Name
				}
				imports[name] = path
			}
			ex.nameRefs(f, j.p.Path, imports, own, pkgNames, methods)
		}
	})
	out := make([]*Package, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.p)
	}
	return out
}

// pathName guesses an import's package name from its path (last element,
// skipping a /vN major-version suffix and go-/-go decorations).
func pathName(path string) string {
	parts := strings.Split(path, "/")
	n := parts[len(parts)-1]
	if len(parts) > 1 && len(n) > 1 && n[0] == 'v' && strings.Trim(n[1:], "0123456789") == "" {
		n = parts[len(parts)-2]
	}
	n = strings.TrimSuffix(strings.TrimPrefix(n, "go-"), "-go")
	return strings.ReplaceAll(n, "-", "_")
}
