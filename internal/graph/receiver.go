package graph

import (
	"regexp"
	"strings"
)

// Issue #1: name-matched calls apart from verified ones. Outside Go, a call
// edge is matched by name, so `b.flush()` is linked to every flush in the
// workspace. A model handed those edges as one list reports some that belong
// to another type. Before an edge is shown, the call's receiver is checked
// against local evidence in the source: an annotation (`s: Store`), a
// constructor assignment (`self.db = Store()`, `x = new Store()`,
// `let x = Store::new()`) or a typed declaration (`Store x`). The edge is then
// confirmed, excluded (the receiver is another type with that method), or
// left as a possible caller to check in the code.

// Verdicts for a name-matched method call.
const (
	recvUnknown  = iota // no local evidence: a possible caller
	recvSame            // the receiver is the queried method's type
	recvOther           // the receiver is another type that has the method
	recvTypeless        // not a method call on a receiver (a plain function)
)

// classify checks a name-matched call r to the method sym (Type.Method).
// It returns the verdict, the receiver's type, and the evidence line.
func classify(ws *Graph, src *source, r *Ref, sym *Symbol) (int, string, string) {
	typ, method := ownerType(sym), sym.Name
	if typ == "" {
		return recvTypeless, "", ""
	}
	line := src.raw(r.Pos.File, int(r.Pos.Line))
	recv := receiverAt(line, method, int(r.Pos.Col))
	if recv == "" {
		return recvUnknown, "", ""
	}
	// search the enclosing declaration, then (for self.x / this.x) its class
	from, to := int(r.Pos.Line), int(r.Pos.Line)
	if enc := ws.Symbol(r.From); enc != nil {
		from, to = int(enc.Pos.Line), int(max(enc.EndLine, enc.Pos.Line))
		if strings.HasPrefix(recv, "self.") || strings.HasPrefix(recv, "this.") {
			if cls := ws.Symbol(r.From[:max(0, strings.LastIndex(r.From, "."))]); cls != nil {
				from, to = int(cls.Pos.Line), int(max(cls.EndLine, cls.Pos.Line))
			}
		}
	}
	got, ev := evidence(src.rawLines(r.Pos.File, from, to), recv)
	switch {
	case got == "":
		return recvUnknown, "", ""
	case got == typ:
		return recvSame, got, ev
	case len(ws.Resolve(got+"."+method)) > 0:
		return recvOther, got, ev
	}
	return recvUnknown, got, ev
}

// ownerType is the type a method belongs to: the second-to-last segment of
// its ID (app/store.Store.flush → Store), "" for a plain function.
func ownerType(s *Symbol) string {
	if s.Kind != KMethod {
		return ""
	}
	id := strings.TrimSuffix(s.ID, "."+s.Name)
	return id[strings.LastIndex(id, ".")+1:]
}

var reIdentChain = regexp.MustCompile(`[A-Za-z_][\w]*(?:\s*\.\s*[A-Za-z_][\w]*)*$`)

// receiverAt returns the receiver expression of the call to method on line
// (`s` in `s.flush()`, `self.db` in `self.db.flush()`), choosing the
// occurrence nearest col; "" when the call has no plain receiver.
func receiverAt(line, method string, col int) string {
	re := regexp.MustCompile(`\.\s*` + regexp.QuoteMeta(method) + `\s*\(`)
	best, bestDist := "", 1<<30
	for _, m := range re.FindAllStringIndex(line, -1) {
		left := strings.TrimRight(line[:m[0]], " \t")
		recv := reIdentChain.FindString(left)
		if recv == "" {
			continue
		}
		if d := abs(m[0] - col); d < bestDist {
			best, bestDist = strings.Join(strings.Fields(recv), ""), d
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// evidence finds the receiver's type in lines: the last annotation,
// construction or typed declaration of it (Python, TypeScript/JavaScript,
// Java, Rust). Returns the type and the line that showed it.
func evidence(lines []string, recv string) (string, string) {
	v := strings.ReplaceAll(regexp.QuoteMeta(recv), `\.`, `\s*\.\s*`)
	pats := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[^\w.])` + v + `\s*:\s*&?(?:mut\s+)?([A-Za-z_]\w*)`),                         // s: Store, s: &mut Store
		regexp.MustCompile(`(?:^|[^\w.])` + v + `\s*(?::[^=]*)?=\s*(?:new\s+)?([A-Za-z_]\w*)\s*(?:\(|::|\{)`), // s = Store(), x = new Store(), x = Store::new()
		regexp.MustCompile(`\b([A-Z]\w*)(?:<[^>]*>)?\s+` + v + `\s*[=;,)]`),                                   // Store s = …, (Store s)
	}
	typ, ev := "", ""
	for _, l := range lines {
		for _, re := range pats {
			if m := re.FindStringSubmatch(l); m != nil && !builtinType[m[1]] {
				typ, ev = m[1], strings.TrimSpace(l)
			}
		}
	}
	return typ, ev
}

// builtinType: annotations that say nothing about a class.
var builtinType = map[string]bool{"int": true, "str": true, "float": true, "bool": true, "list": true, "dict": true, "Any": true,
	"any": true, "string": true, "number": true, "boolean": true, "object": true, "self": true, "Self": true, "mut": true, "var": true, "let": true, "const": true}

// raw is one source line, untrimmed and untruncated ("" when unavailable).
func (s *source) raw(file string, n int) string {
	if ls := s.rawLines(file, n, n); len(ls) == 1 {
		return ls[0]
	}
	return ""
}

func (s *source) rawLines(file string, from, to int) []string {
	s.lines(file, 0, 0) // load
	ls := s.files[file]
	if from < 1 {
		from = 1
	}
	if to > len(ls) {
		to = len(ls)
	}
	if from > to {
		return nil
	}
	return ls[from-1 : to]
}
