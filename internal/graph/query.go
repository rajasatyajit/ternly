package graph

import (
	"sort"
	"strings"
)

// Resolve finds symbols for a query: an exact ID ("pkg/path.Type.Method"),
// a qualified suffix ("Type.Method", "pkg.Func", "path/pkg.Func") or a bare
// name (case-insensitive). Exact matches win; results are sorted by ID.
func (g *Graph) Resolve(q string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.resolve(q)
}

func (g *Graph) resolve(q string) []*Symbol {
	q = strings.TrimSpace(q)
	if s := g.byID[q]; s != nil {
		return []*Symbol{s}
	}
	name := q
	if i := strings.LastIndex(q, "."); i >= 0 {
		name = q[i+1:]
	}
	var exact, fold []*Symbol
	for _, s := range g.byName[strings.ToLower(name)] {
		switch {
		case !strings.Contains(q, "."):
			if s.Name == q {
				exact = append(exact, s)
			} else {
				fold = append(fold, s)
			}
		case strings.HasSuffix(s.ID, "."+q) || strings.HasSuffix(s.ID, "/"+q):
			exact = append(exact, s)
		}
	}
	out := exact
	if len(out) == 0 {
		out = fold
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Search lists symbols whose name contains q (case-insensitive), best first:
// exact name, then prefix, then substring; non-test before test.
func (g *Graph) Search(q string, kind Kind, limit int) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	lq := strings.ToLower(q)
	type hit struct {
		s     *Symbol
		score int
	}
	var hits []hit
	for name, ss := range g.byName {
		score := -1
		switch {
		case name == lq:
			score = 0
		case strings.HasPrefix(name, lq):
			score = 1
		case strings.Contains(name, lq):
			score = 2
		}
		if score < 0 {
			continue
		}
		for _, s := range ss {
			if kind != 0 && s.Kind != kind {
				continue
			}
			sc := score * 2
			if s.Test {
				sc++
			}
			hits = append(hits, hit{s, sc})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return hits[i].s.ID < hits[j].s.ID
	})
	out := make([]*Symbol, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.s)
	}
	return out
}

// References lists every use of id (calls included), by file and line.
func (g *Graph) References(id string, callsOnly bool) []*Ref {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []*Ref
	for _, r := range g.refsTo[id] {
		if !callsOnly || r.Call {
			out = append(out, r)
		}
	}
	sortRefs(out)
	return out
}

// Callees lists the calls made inside id's body.
func (g *Graph) Callees(id string) []*Ref {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p := g.owner[id]
	if p == nil {
		return nil
	}
	var out []*Ref
	for i := range p.Refs {
		if r := &p.Refs[i]; r.From == id && r.Call {
			out = append(out, r)
		}
	}
	sortRefs(out)
	return out
}

func sortRefs(rs []*Ref) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Pos.File != rs[j].Pos.File {
			return rs[i].Pos.File < rs[j].Pos.File
		}
		return rs[i].Pos.Line < rs[j].Pos.Line
	})
}

// Implementations: for an interface, the types whose method set covers it;
// for a type, the interfaces it satisfies. Generic types are not matched.
func (g *Graph) Implementations(id string) []*Symbol {
	g.mu.RLock()
	s := g.byID[id]
	g.mu.RUnlock()
	if s == nil {
		return nil
	}
	return g.ImplementationsOf(s)
}

// ImplementationsOf is Implementations for a symbol that may live in another
// graph (e.g. a dependency interface such as io.Writer).
func (g *Graph) ImplementationsOf(s *Symbol) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if s == nil || len(s.Methods) == 0 && s.Kind != KInterface {
		return nil
	}
	var out []*Symbol
	for _, ss := range g.byName {
		for _, o := range ss {
			switch {
			case o == s || o.Kind != KType && o.Kind != KInterface:
			case s.Kind == KInterface && o.Kind == KType && len(s.Methods) > 0 && covers(o.Methods, s.Methods):
				out = append(out, o)
			case s.Kind == KType && o.Kind == KInterface && len(o.Methods) > 0 && covers(s.Methods, o.Methods):
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// covers reports whether set contains every method in want (both sorted by go/types order: by name).
func covers(set, want []string) bool {
	if len(set) < len(want) {
		return false
	}
	have := make(map[string]bool, len(set))
	for _, m := range set {
		have[m] = true
	}
	for _, m := range want {
		if !have[m] {
			return false
		}
	}
	return true
}

// Related is a file and why it relates.
type Related struct {
	File   string
	Score  int
	Reason string
}

// RelatedFiles ranks files connected to file: its package's files and tests,
// then files it references or that reference it, weighted by reference count.
func (g *Graph) RelatedFiles(file string) []Related {
	g.mu.RLock()
	defer g.mu.RUnlock()
	score := map[string]int{}
	reason := map[string]string{}
	bump := func(f string, n int, why string) {
		if f == file || f == "" {
			return
		}
		score[f] += n
		if reason[f] == "" {
			reason[f] = why
		}
	}
	pkg := g.pkgs[g.byFile[file]]
	if pkg == nil {
		return nil
	}
	base := strings.TrimSuffix(strings.TrimSuffix(file, ".go"), "_test")
	for _, f := range pkg.Files {
		if f.Ignored {
			continue
		}
		switch strings.TrimSuffix(strings.TrimSuffix(f.Name, ".go"), "_test") {
		case base:
			bump(f.Name, 50, "test pair")
		default:
			bump(f.Name, 5, "same package")
		}
	}
	for _, p := range g.pkgs { // uses in this file of symbols declared elsewhere, and vice versa
		for i := range p.Refs {
			r := &p.Refs[i]
			to := g.byID[r.To]
			if to == nil {
				continue
			}
			if r.Pos.File == file {
				bump(to.Pos.File, 1, "uses "+to.Name)
			} else if to.Pos.File == file {
				bump(r.Pos.File, 1, "uses "+to.Name)
			}
		}
	}
	out := make([]Related, 0, len(score))
	for f, n := range score {
		out = append(out, Related{f, n, reason[f]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].File < out[j].File
	})
	return out
}

// ImpactResult is what may break if a symbol changes.
type ImpactResult struct {
	Symbols  []*Symbol // transitively depending declarations (closest first)
	Tests    []*Symbol // test functions among them
	Packages []string
}

// Impact walks references backwards from ids up to depth levels.
func (g *Graph) Impact(ids []string, depth int) ImpactResult {
	g.mu.RLock()
	defer g.mu.RUnlock()
	seen := map[string]bool{}
	frontier := ids
	for _, id := range ids {
		seen[id] = true
	}
	var res ImpactResult
	pkgs := map[string]bool{}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, id := range frontier {
			for _, r := range g.refsTo[id] {
				if r.From == "" || seen[r.From] {
					continue
				}
				seen[r.From] = true
				next = append(next, r.From)
				if s := g.byID[r.From]; s != nil {
					res.Symbols = append(res.Symbols, s)
					pkgs[strings.TrimSuffix(s.Pkg, "_test")] = true
					if s.Test && isTestFunc(s.Name) {
						res.Tests = append(res.Tests, s)
					}
				}
			}
		}
		frontier = next
	}
	for p := range pkgs {
		res.Packages = append(res.Packages, p)
	}
	sort.Strings(res.Packages)
	return res
}

func isTestFunc(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// FileSymbols lists the symbols declared in file.
func (g *Graph) FileSymbols(file string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p := g.pkgs[g.byFile[file]]
	if p == nil {
		return nil
	}
	var out []*Symbol
	for i := range p.Symbols {
		if p.Symbols[i].Pos.File == file {
			out = append(out, &p.Symbols[i])
		}
	}
	return out
}

// Symbol returns the symbol with this ID.
func (g *Graph) Symbol(id string) *Symbol { g.mu.RLock(); defer g.mu.RUnlock(); return g.byID[id] }
