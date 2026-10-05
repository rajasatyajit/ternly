package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Guidance is added to the system prompt when the graph tools are available.
const Guidance = `Code graph (Go, Python, TypeScript/JavaScript, Rust, Java): find_symbol, references, callers, callees, implementations, related_files and impact answer structural questions about this codebase with file:line citations, and cost far fewer tokens than grep plus read_file. Use them first; then read_file only the cited lines (offset/limit). Every edge is tagged: [typed] edges were resolved by the Go type checker; [name match] edges (other languages, and Go before its typed build finishes) were matched by name and may belong to another symbol with the same name. Report name-matched results as candidates ("a call named flush at app/run.py:5, probably Buffer.flush") and confirm them in the code before stating them as fact. Use grep for text that is not an identifier (strings, comments, config).`

// buildWait is how long a graph tool waits for the first build before telling
// the model to fall back to grep/read.
const buildWait = 20 * time.Second

type args struct {
	Query, Kind, Symbol, Path string
	Limit, Depth              int
	IncludeDeps               bool `json:"include_deps"`
}

// Tools exposes the graph to the agent. All are read-only and cite file:line.
// ws is the workspace graph; deps holds dependencies' exported API.
func Tools(svc *Service) []*tools.Tool {
	sym := `"symbol":{"type":"string","description":"symbol ID or name: pkg/path.Name, Type.Method, or a bare name"}`
	lim := `"limit":{"type":"integer","minimum":1,"maximum":500}`
	mk := func(name, desc, props, req string, run func(ws, deps *Graph, a args, src *source) string, summary func(a args) string) *tools.Tool {
		return &tools.Tool{Kind: tools.ReadOnly,
			Spec: llm.ToolSpec{Name: name, Description: desc,
				Schema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":[%s],"additionalProperties":false}`, props, req))},
			Summary: func(raw json.RawMessage) string { var a args; _ = json.Unmarshal(raw, &a); return summary(a) },
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a args
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 50
				}
				ws, note, err := svc.Graph(ctx, buildWait)
				if err != nil {
					return "", err
				}
				out := run(ws, svc.Deps(), a, newSource(svc.Root))
				if note != "" {
					out = "(" + note + ")\n" + out
				}
				return out, nil
			}}
	}
	bySym := func(a args) string { return a.Symbol }
	return []*tools.Tool{
		mk("find_symbol", "Find Go declarations by name in the code graph (faster and cheaper than grep). Returns kind, ID, file:line and signature. include_deps also searches dependencies and the standard library (exported API).",
			`"query":{"type":"string"},"kind":{"type":"string","enum":["func","method","type","interface","field","var","const"]},"include_deps":{"type":"boolean"},`+lim, `"query"`,
			func(ws, deps *Graph, a args, src *source) string {
				var kind Kind
				for k := KFunc; k <= KConst; k++ {
					if k.String() == a.Kind {
						kind = k
					}
				}
				var ss []*Symbol
				if strings.Contains(a.Query, ".") { // qualified (Type.Method, pkg.Func, full ID): resolve exactly first
					ss = ws.Resolve(a.Query)
					if len(ss) == 0 && a.IncludeDeps {
						ss = deps.Resolve(a.Query)
					}
				}
				if len(ss) == 0 {
					q := a.Query[strings.LastIndex(a.Query, ".")+1:]
					ss = ws.Search(q, kind, a.Limit+1)
					if a.IncludeDeps || len(ss) == 0 {
						ss = append(ss, deps.Search(q, kind, a.Limit+1-len(ss))...)
					}
				}
				return listSymbolsWithBody(ss, a.Limit, "no symbol matches "+a.Query, src)
			}, func(a args) string { return a.Query }),
		mk("references", "Every use of a Go symbol (calls included), with file:line and the enclosing declaration. Use instead of grepping for a name.",
			sym+","+lim, `"symbol"`, func(ws, deps *Graph, a args, src *source) string {
				return withSymbol(ws, deps, a.Symbol, func(s *Symbol) string {
					return listRefs(ws.References(s.ID, false), a.Limit, "no references to "+s.ID, src)
				})
			}, bySym),
		mk("callers", "Functions and methods that call a Go function or method (static calls and calls through interfaces), with file:line.",
			sym+","+lim, `"symbol"`, func(ws, deps *Graph, a args, src *source) string {
				return withSymbol(ws, deps, a.Symbol, func(s *Symbol) string {
					return listRefs(ws.References(s.ID, true), a.Limit, "no callers of "+s.ID, src)
				})
			}, bySym),
		mk("callees", "Functions and methods a Go function or method calls, with file:line.",
			sym+","+lim, `"symbol"`, func(ws, deps *Graph, a args, src *source) string {
				return withSymbol(ws, nil, a.Symbol, func(s *Symbol) string {
					return listCallees(ws, deps, ws.Callees(s.ID), a.Limit, s.ID+" makes no resolved calls", src)
				})
			}, bySym),
		mk("implementations", "For a Go interface (workspace or dependency, e.g. io.Writer): the workspace types implementing it. For a type: the interfaces it implements, dependencies' included.",
			sym+","+lim, `"symbol"`, func(ws, deps *Graph, a args, _ *source) string {
				return withSymbol(ws, deps, a.Symbol, func(s *Symbol) string {
					found := ws.ImplementationsOf(s)
					if s.Kind == KType {
						found = append(found, deps.ImplementationsOf(s)...)
					}
					return listSymbols(found, a.Limit, "no implementations found for "+s.ID+" (generic types are not matched)")
				})
			}, bySym),
		mk("related_files", "Files most related to a file: its test pair, its package, and files it references or that reference it.",
			`"path":{"type":"string"},`+lim, `"path"`, func(ws, _ *Graph, a args, _ *source) string {
				rs := ws.RelatedFiles(strings.TrimPrefix(a.Path, "./"))
				if len(rs) == 0 {
					return a.Path + " is not in the code graph"
				}
				var b strings.Builder
				for i, r := range rs {
					if i == a.Limit {
						fmt.Fprintf(&b, "… %d more\n", len(rs)-i)
						break
					}
					fmt.Fprintf(&b, "%s  (%s, score %d)\n", r.File, r.Reason, r.Score)
				}
				return b.String()
			}, func(a args) string { return a.Path }),
		mk("impact", "What may break if a Go symbol (or every symbol in a file) changes: declarations that depend on it transitively, the tests that reach it, and affected packages.",
			sym+`,"path":{"type":"string"},"depth":{"type":"integer","minimum":1,"maximum":6},`+lim, ``, func(ws, _ *Graph, a args, _ *source) string {
				var ids []string
				switch {
				case a.Path != "":
					for _, s := range ws.FileSymbols(strings.TrimPrefix(a.Path, "./")) {
						ids = append(ids, s.ID)
					}
					if len(ids) == 0 {
						return a.Path + " declares nothing in the code graph"
					}
				case a.Symbol != "":
					ss := ws.Resolve(a.Symbol)
					if len(ss) != 1 {
						return ambiguous(a.Symbol, ss)
					}
					ids = []string{ss[0].ID}
				default:
					return "error: give symbol or path"
				}
				depth := a.Depth
				if depth == 0 {
					depth = 3
				}
				r := ws.Impact(ids, depth)
				var b strings.Builder
				fmt.Fprintf(&b, "%d dependent declarations in %d packages (depth %d); %d tests reach it\n", len(r.Symbols), len(r.Packages), depth, len(r.Tests))
				if len(r.Tests) > 0 {
					b.WriteString("tests:\n" + listSymbols(r.Tests, a.Limit, ""))
				}
				b.WriteString("packages: " + strings.Join(r.Packages, " ") + "\n")
				b.WriteString("dependents:\n" + listSymbols(r.Symbols, a.Limit, "none"))
				return b.String()
			}, func(a args) string { return a.Symbol + a.Path }),
	}
}

// withSymbol resolves q in the workspace, then (if deps is given) in the dependencies.
func withSymbol(ws, deps *Graph, q string, f func(*Symbol) string) string {
	ss := ws.Resolve(q)
	if len(ss) == 0 && deps != nil {
		ss = deps.Resolve(q)
	}
	if len(ss) != 1 {
		return ambiguous(q, ss)
	}
	return f(ss[0])
}

func ambiguous(q string, ss []*Symbol) string {
	if len(ss) == 0 {
		return fmt.Sprintf("no symbol named %q in the code graph — try find_symbol with part of the name", q)
	}
	return fmt.Sprintf("%q matches %d symbols; call again with one of these IDs:\n%s", q, len(ss), listSymbols(ss, 20, ""))
}

func listSymbols(ss []*Symbol, limit int, empty string) string {
	if len(ss) == 0 {
		return empty + "\n"
	}
	var b strings.Builder
	for i, s := range ss {
		if i == limit {
			fmt.Fprintf(&b, "… more (raise limit)\n")
			break
		}
		sig := s.Sig
		if len(sig) > 120 {
			sig = sig[:120] + "…"
		}
		fmt.Fprintf(&b, "%s %s  %s:%d  %s\n", s.Kind, s.ID, s.Pos.File, s.Pos.Line, sig)
	}
	return b.String()
}

func listRefs(rs []*Ref, limit int, empty string, src *source) string {
	if len(rs) == 0 {
		return empty + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d uses\n", len(rs))
	for i, r := range rs {
		if i == limit {
			fmt.Fprintf(&b, "… %d more (raise limit)\n", len(rs)-i)
			break
		}
		call := ""
		if r.Call {
			call = " call"
		}
		call += " " + edgeTag(r)
		fmt.Fprintf(&b, "%s:%d  in %s%s  │ %s\n", r.Pos.File, r.Pos.Line, orStr(r.From, "(package scope)"), call, src.line(r.Pos.File, int(r.Pos.Line)))
	}
	return b.String()
}

func listCallees(ws, deps *Graph, rs []*Ref, limit int, empty string, src *source) string {
	if len(rs) == 0 {
		return empty + "\n"
	}
	var b strings.Builder
	seen := map[string]bool{}
	n := 0
	for _, r := range rs {
		if seen[r.To] {
			continue
		}
		seen[r.To] = true
		if n == limit {
			b.WriteString("… more (raise limit)\n")
			break
		}
		n++
		where := "(dependency)"
		if s := ws.Symbol(r.To); s != nil {
			where = fmt.Sprintf("declared %s:%d", s.Pos.File, s.Pos.Line)
		} else if s := deps.Symbol(r.To); s != nil {
			where = fmt.Sprintf("declared in %s %s:%d", s.Pkg, s.Pos.File, s.Pos.Line)
		}
		fmt.Fprintf(&b, "%s  called at %s:%d  %s %s  │ %s\n", r.To, r.Pos.File, r.Pos.Line, where, edgeTag(r), src.line(r.Pos.File, int(r.Pos.Line)))
	}
	return b.String()
}

// listSymbolsWithBody adds the first lines of each workspace declaration
// (signature and the start of the body) to the first results, so the model
// can often skip a read_file.
func listSymbolsWithBody(ss []*Symbol, limit int, empty string, src *source) string {
	if len(ss) == 0 {
		return empty + "\n"
	}
	var b strings.Builder
	for i, s := range ss {
		if i == limit {
			b.WriteString("… more (raise limit)\n")
			break
		}
		b.WriteString(listSymbols([]*Symbol{s}, 1, ""))
		if i < bodyResults && s.Kind != KField {
			for _, l := range src.lines(s.Pos.File, int(s.Pos.Line), int(min(s.EndLine, s.Pos.Line+bodyLines-1))) {
				b.WriteString("    │ " + l + "\n")
			}
		}
	}
	return b.String()
}

const (
	bodyResults = 10 // results that get a body excerpt
	bodyLines   = 4  // lines per excerpt
	lineWidth   = 160
)

// source reads workspace files (through os.Root, like the file tools) for
// excerpts, caching each file for the duration of one tool call.
type source struct {
	root  *os.Root
	files map[string][]string
}

func newSource(dir string) *source {
	r, _ := os.OpenRoot(dir)
	return &source{root: r, files: map[string][]string{}}
}

func (s *source) lines(file string, from, to int) []string {
	ls, ok := s.files[file]
	if !ok && s.root != nil && !filepath.IsAbs(file) {
		if b, err := s.root.ReadFile(filepath.FromSlash(file)); err == nil && len(b) < 4<<20 {
			ls = strings.Split(string(b), "\n")
		}
		s.files[file] = ls
	}
	var out []string
	for n := max(from, 1); n <= to && n <= len(ls); n++ {
		l := strings.TrimRight(ls[n-1], " \t\r")
		if len(l) > lineWidth {
			l = l[:lineWidth] + "…"
		}
		out = append(out, l)
	}
	return out
}

// line is one trimmed source line ("" when unavailable).
func (s *source) line(file string, n int) string {
	if ls := s.lines(file, n, n); len(ls) == 1 {
		return strings.TrimSpace(ls[0])
	}
	return ""
}

// edgeTag says how an edge was found: [typed] (the type checker resolved it)
// or [name match] (matched by name: a candidate, to confirm in the code).
func edgeTag(r *Ref) string {
	switch {
	case r.Approx:
		return "[name match, ambiguous: one of several symbols with this name]"
	case r.ByName:
		return "[name match]"
	}
	return "[typed]"
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
