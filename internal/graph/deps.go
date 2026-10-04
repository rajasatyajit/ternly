package graph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Dependency graphs are symbol tables (exported declarations, method sets,
// spans) built from compiler export data and shared by every project on the
// machine:
//
//	~/.cache/ternly/graphs/<ecosystem>/<module>@<version>/<content-hash>/<package>.gob
//
// A cached table is reused only when ecosystem, module, exact version and
// content hash all match. The content hash is the module's go.sum h1: hash,
// or — for replaced/local modules without one — a hash of the package's
// source; the standard library is keyed by the Go version.

// depKey identifies one dependency package's cached table.
type depKey struct {
	Ecosystem, Module, Version, Hash, Package string
}

func (k depKey) path(cacheDir string) string {
	return filepath.Join(cacheDir, "graphs", k.Ecosystem, escape(k.Module)+"@"+escape(k.Version), k.Hash, escape(strings.ReplaceAll(k.Package, "/", "%"))+".gob")
}

// escape follows the Go module cache's case encoding (Upper → !upper) so
// case-insensitive filesystems can't merge distinct modules.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// readSums collects go.sum h1: hashes ("module version" → hash) from the
// workspace's go.sum and go.work.sum files.
func readSums(root string, moduleDirs []string) map[string]string {
	sums := map[string]string{}
	files := []string{filepath.Join(root, "go.work.sum")}
	for _, d := range append([]string{root}, moduleDirs...) {
		files = append(files, filepath.Join(d, "go.sum"))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) == 3 && !strings.HasSuffix(fl[1], "/go.mod") {
				sums[fl[0]+" "+fl[1]] = fl[2]
			}
		}
	}
	return sums
}

// depKeys assigns every non-workspace package with export data its cache key.
func depKeys(root, goVersion string, lps []*listPkg) map[string]depKey {
	var modDirs []string
	for _, p := range lps {
		if p.Module != nil && p.Module.Main && p.Module.Dir != "" && !contains(modDirs, p.Module.Dir) {
			modDirs = append(modDirs, p.Module.Dir)
		}
	}
	sums := readSums(root, modDirs)
	keys := map[string]depKey{}
	for _, p := range lps {
		if p.ForTest != "" || p.Export == "" || strings.Contains(p.ImportPath, " ") || workspace(p, root) || (p.Module != nil && p.Module.Main) {
			continue
		}
		k := depKey{Ecosystem: "go", Package: p.ImportPath}
		switch {
		case p.Standard:
			k.Module, k.Version, k.Hash = "std", goVersion, hashBytes([]byte(goVersion+"\x00"+runtime.GOOS+"/"+runtime.GOARCH))
		case p.Module != nil:
			k.Module, k.Version = p.Module.Path, p.Module.Version
			if h := sums[k.Module+" "+k.Version]; h != "" {
				k.Hash = hashBytes([]byte(h))
			} else { // replaced or local module: hash the package's source
				var all []byte
				for _, f := range p.GoFiles {
					b, _ := os.ReadFile(filepath.Join(p.Dir, f))
					all = append(append(all, hashBytes(b)...), f...)
				}
				k.Hash = hashBytes(all)
			}
			if k.Version == "" {
				k.Version = "local"
			}
		default:
			continue
		}
		keys[p.ImportPath] = k
	}
	return keys
}

// buildDeps writes missing dependency tables (in parallel) and returns the
// shard paths for this workspace.
func (s *Service) buildDeps(ctx context.Context, b *builder, keys map[string]depKey, dirs map[string]string) []string {
	var paths []string
	var todo []string
	for pkg, k := range keys {
		p := k.path(s.cacheDir)
		paths = append(paths, p)
		if !exists(p) {
			todo = append(todo, pkg)
		}
	}
	sort.Strings(paths)
	work := make(chan string)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pkg := range work {
				tp, err := b.shared.Import(pkg)
				if err != nil {
					continue
				}
				syms := depSymbols(tp, b.fset, dirs[pkg])
				var buf bytes.Buffer
				if gob.NewEncoder(&buf).Encode(syms) == nil {
					p := keys[pkg].path(s.cacheDir)
					if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
						_ = writeAtomic(p, buf.Bytes())
					}
				}
			}
		}()
	}
	for _, pkg := range todo {
		if ctx.Err() != nil {
			break
		}
		work <- pkg
	}
	close(work)
	wg.Wait()
	return paths
}

// depSymbols lists a dependency package's exported declarations.
func depSymbols(tp *types.Package, fset *token.FileSet, dir string) []Symbol {
	var out []Symbol
	pkg := tp.Path()
	pos := func(p token.Pos) Pos {
		x := fset.Position(p)
		f := x.Filename
		if r, err := filepath.Rel(dir, f); dir != "" && err == nil && !strings.HasPrefix(r, "..") {
			f = filepath.ToSlash(r)
		}
		f = strings.TrimPrefix(f, "$GOROOT/src/") // standard library: relative to GOROOT/src, i.e. the import path
		return Pos{File: f, Line: int32(x.Line), Col: int32(x.Column)}
	}
	sc := tp.Scope()
	for _, name := range sc.Names() {
		obj := sc.Lookup(name)
		if !obj.Exported() {
			continue
		}
		s := Symbol{ID: pkg + "." + name, Name: name, Pkg: pkg, Pos: pos(obj.Pos()), Sig: types.TypeString(obj.Type(), qual)}
		switch o := obj.(type) {
		case *types.Func:
			s.Kind = KFunc
		case *types.Var:
			s.Kind = KVar
		case *types.Const:
			s.Kind = KConst
		case *types.TypeName:
			s.Kind, s.Sig = KType, types.TypeString(o.Type().Underlying(), qual)
			if _, ok := o.Type().Underlying().(*types.Interface); ok {
				s.Kind = KInterface
			}
			s.Methods = methodSet(o.Type())
			if n, ok := o.Type().(*types.Named); ok {
				for i := range n.NumMethods() {
					if m := n.Method(i); m.Exported() {
						out = append(out, Symbol{ID: s.ID + "." + m.Name(), Name: m.Name(), Kind: KMethod, Pkg: pkg, Pos: pos(m.Pos()), Sig: types.TypeString(m.Type(), qual)})
					}
				}
				if it, ok := n.Underlying().(*types.Interface); ok {
					for i := range it.NumExplicitMethods() {
						if m := it.ExplicitMethod(i); m.Exported() {
							out = append(out, Symbol{ID: s.ID + "." + m.Name(), Name: m.Name(), Kind: KMethod, Pkg: pkg, Pos: pos(m.Pos()), Sig: types.TypeString(m.Type(), qual)})
						}
					}
				}
			}
			if st, ok := o.Type().Underlying().(*types.Struct); ok {
				for i := range st.NumFields() {
					if f := st.Field(i); f.Exported() {
						out = append(out, Symbol{ID: s.ID + "." + f.Name(), Name: f.Name(), Kind: KField, Pkg: pkg, Pos: pos(f.Pos()), Sig: types.TypeString(f.Type(), qual)})
					}
				}
			}
		default:
			continue
		}
		out = append(out, s)
	}
	return out
}

// loadDeps reads the dependency tables listed in the manifest into a graph.
func loadDeps(root string, paths []string) *Graph {
	g := newGraph(root)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f, err := os.Open(p)
			if err != nil {
				return
			}
			defer f.Close()
			var syms []Symbol
			if gob.NewDecoder(f).Decode(&syms) != nil || len(syms) == 0 {
				return
			}
			mu.Lock()
			g.put(&Package{Path: syms[0].Pkg, Symbols: syms, Typed: true})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return g
}
