// Package graph builds and maintains a code graph of the workspace — packages,
// files, symbols and their references, calls, imports and test links — so the
// agent can answer "where is X / who uses X / what breaks if X changes" with
// file:line citations instead of grepping and reading.
//
// Go is analysed with go/types (dependencies imported from compiler export
// data produced by `go list -export`); the graph is persisted per project and
// updated incrementally (see ADR 007).
package graph

import (
	"sort"
	"strings"
	"sync"
)

// Kind of a symbol.
type Kind uint8

const (
	KFunc Kind = iota + 1
	KMethod
	KType
	KInterface
	KField
	KVar
	KConst
)

func (k Kind) String() string {
	return [...]string{"?", "func", "method", "type", "interface", "field", "var", "const"}[k]
}

// Pos is a source position; File is relative to the workspace root (or, for
// dependencies, to the module directory).
type Pos struct {
	File string
	Line int32
	Col  int32
}

// Symbol is a named declaration.
type Symbol struct {
	ID      string // pkg.Name, pkg.Recv.Name or pkg.Type.Field
	Name    string
	Kind    Kind
	Pkg     string
	Pos     Pos
	EndLine int32
	Sig     string   // type or signature
	Methods []string // method set as "Name(signature)" (types: pointer method set; interfaces: all methods)
	Test    bool     // declared in a _test.go file
}

// Ref is one use of a symbol inside a top-level declaration.
type Ref struct {
	From string // enclosing declaration's symbol ID ("" at file scope)
	To   string
	Pos  Pos
	Call bool
	// ByName: matched by name, not resolved by a type checker (languages
	// other than Go, and Go's approximate first pass). Approx: one of several
	// symbols with that name.
	ByName bool     `json:",omitempty"`
	Approx bool     `json:",omitempty"`
	pkg    *Package // owner, set when indexed
}

// FileInfo is what change detection compares.
type FileInfo struct {
	Name    string // relative to root
	Hash    string
	Size    int64
	MTime   int64
	Test    bool
	Ignored bool // excluded by build constraints
}

// Package is one package's slice of the graph (the unit of incremental update).
type Package struct {
	Path    string
	Dir     string // relative to root
	Files   []FileInfo
	Imports []string
	Symbols []Symbol
	Refs    []Ref
	APIHash string // of the exported declarations' signatures: importers need re-checking when it changes
	Errors  int    // type errors (the graph is best-effort for broken code)
	Typed   bool   // false: syntax-only fallback
}

// Graph is a workspace's graph with query indexes. Safe for concurrent use.
type Graph struct {
	Root string

	mu     sync.RWMutex
	pkgs   map[string]*Package
	byID   map[string]*Symbol
	byName map[string][]*Symbol // lower-case name
	refsTo map[string][]*Ref    // reverse index: symbol ID → uses
	byFile map[string]string    // file → package path
	owner  map[string]*Package  // symbol ID → package holding its body's refs
}

func newGraph(root string) *Graph {
	return &Graph{Root: root, pkgs: map[string]*Package{}, byID: map[string]*Symbol{},
		byName: map[string][]*Symbol{}, refsTo: map[string][]*Ref{}, byFile: map[string]string{}, owner: map[string]*Package{}}
}

// put replaces package p's slice of the graph (callers hold g.mu).
func (g *Graph) put(p *Package) {
	if old := g.pkgs[p.Path]; old != nil {
		g.remove(old)
	}
	g.pkgs[p.Path] = p
	for _, f := range p.Files {
		g.byFile[f.Name] = p.Path
	}
	for i := range p.Symbols {
		s := &p.Symbols[i]
		g.byID[s.ID] = s
		g.owner[s.ID] = p
		k := strings.ToLower(s.Name)
		g.byName[k] = append(g.byName[k], s)
	}
	for i := range p.Refs {
		r := &p.Refs[i]
		r.pkg = p
		g.refsTo[r.To] = append(g.refsTo[r.To], r)
	}
}

func (g *Graph) remove(p *Package) {
	delete(g.pkgs, p.Path)
	for _, f := range p.Files {
		if g.byFile[f.Name] == p.Path {
			delete(g.byFile, f.Name)
		}
	}
	for i := range p.Symbols {
		s := &p.Symbols[i]
		if g.byID[s.ID] == s {
			delete(g.byID, s.ID)
			delete(g.owner, s.ID)
		}
		k := strings.ToLower(s.Name)
		g.byName[k] = dropPtr(g.byName[k], s)
		if len(g.byName[k]) == 0 {
			delete(g.byName, k)
		}
	}
	touched := map[string]bool{}
	for i := range p.Refs {
		touched[p.Refs[i].To] = true
	}
	for to := range touched { // drop this package's refs from each target's list
		kept := g.refsTo[to][:0]
		for _, r := range g.refsTo[to] {
			if r.pkg != p {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(g.refsTo, to)
		} else {
			g.refsTo[to] = kept
		}
	}
}

func dropPtr[T any](xs []*T, x *T) []*T {
	out := xs[:0]
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// Stats summarises the graph.
type Stats struct{ Packages, Files, Symbols, Refs, Untyped int }

func (g *Graph) Stats() Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var s Stats
	for _, p := range g.pkgs {
		s.Packages++
		s.Files += len(p.Files)
		s.Symbols += len(p.Symbols)
		s.Refs += len(p.Refs)
		if !p.Typed {
			s.Untyped++
		}
	}
	return s
}

// Packages returns the package paths, sorted.
func (g *Graph) Packages() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.pkgs))
	for p := range g.pkgs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
