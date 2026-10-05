package tools

import (
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Commands that run without confirmation (ADR 014, from dogfooding: every
// model's first exploration command was a chain like `ls -la && cat go.mod`).
// The command is parsed as bash (mvdan.cc/sh) and classified on the syntax
// tree (ADR 015), not by rewriting its text: it qualifies when it is only
// simple commands joined by &&, ||, ;, newlines and |, each a known
// read-only, build or test command whose words are plain literals (quotes
// allowed, nothing that expands), with no redirection but `2>&1` and
// output to /dev/null.

// envOK are the variables a command may set in front of itself
// (`CGO_ENABLED=0 go build`): build knobs that can't load code or change
// where it comes from (not GOFLAGS, LD_PRELOAD, PATH, …).
var envOK = map[string]bool{"CGO_ENABLED": true, "GOOS": true, "GOARCH": true, "GOARM": true, "GOAMD64": true, "GOEXPERIMENT": true,
	"NODE_ENV": true, "RUST_BACKTRACE": true, "CI": true, "TZ": true, "LANG": true, "LC_ALL": true, "NO_COLOR": true}

var (
	reEnvValue = regexp.MustCompile(`^[\w.,:/+-]*$`)
	reCd       = regexp.MustCompile(`^cd\s+[^-~\s]\S*$`) // into the workspace (paths are checked); a bare cd would go home
	reMkdir    = regexp.MustCompile(`^mkdir(\s+-p)?(\s+[^-\s]\S*)+$`)
	reExecArg  = regexp.MustCompile(`(^|\s)(--pre|--pre-glob|-toolexec|--exec)(=|\s|$)`)
)

// safeCommand reports whether cmd may run without confirmation; mkdir (in
// the workspace) counts only when writes is set (edits and yolo modes).
func (p *Policy) safeCommand(cmd string, writes bool) bool {
	argvs, ok := commands(cmd)
	if !ok || len(argvs) == 0 {
		return false
	}
	for _, words := range argvs {
		if !p.safeArgv(words, writes) {
			return false
		}
	}
	return true
}

// ReadOnlyCommand reports whether cmd only reads (or builds and tests):
// what plan mode allows, and what can't have changed the workspace.
func (p *Policy) ReadOnlyCommand(cmd string) bool { return p.safeCommand(cmd, false) }

// safeArgv checks one simple command's words (env prefixes already removed).
func (p *Policy) safeArgv(words []string, writes bool) bool {
	words = append([]string(nil), words...)
	for i, w := range words {
		if strings.ContainsAny(w, " \t\n") && i == 0 {
			return false // a command name with spaces is not one we know
		}
		for _, v := range pathsIn(w) {
			switch {
			case strings.HasPrefix(v, "~"), v == "..", strings.HasPrefix(v, "../"), strings.Contains(v, "/../"), strings.HasSuffix(v, "/.."):
				return false
			case strings.HasPrefix(v, "/") && !p.inRoot(v):
				return false
			}
		}
		if strings.HasPrefix(w, "/") {
			words[i] = "./" + w // in the workspace: as safe as a relative path
		}
	}
	s := strings.Join(words, " ")
	if reForbidden.MatchString(s) || reDanger.MatchString(s) || reUnsafeArg.MatchString(s) || reExecArg.MatchString(s) {
		return false
	}
	return reSafe.MatchString(s) || reCd.MatchString(s) || writes && reMkdir.MatchString(s)
}

// commands parses cmd and returns the argv of each simple command, or false
// when anything in it is more than a chain of simple commands with literal
// words and harmless redirections.
func commands(cmd string) ([][]string, bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, false
	}
	var out [][]string
	for _, st := range f.Stmts {
		if !stmt(st, &out) {
			return nil, false
		}
	}
	return out, true
}

func stmt(st *syntax.Stmt, out *[][]string) bool {
	if st.Negated || st.Background || st.Coprocess || st.Disown {
		return false
	}
	for _, r := range st.Redirs {
		if !harmlessRedirect(r) {
			return false
		}
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		if len(c.Args) == 0 { // a bare assignment
			return false
		}
		for _, a := range c.Assigns {
			if a.Append || a.Naked || a.Index != nil || a.Array != nil || !envOK[a.Name.Value] {
				return false
			}
			v, ok := literal(a.Value)
			if !ok || !reEnvValue.MatchString(v) {
				return false
			}
		}
		argv := make([]string, 0, len(c.Args))
		for _, w := range c.Args {
			s, ok := literal(w)
			if !ok {
				return false
			}
			argv = append(argv, s)
		}
		*out = append(*out, argv)
		return true
	case *syntax.BinaryCmd:
		switch c.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe:
			return stmt(c.X, out) && stmt(c.Y, out)
		}
	}
	return false // subshells, blocks, functions, loops, conditionals, |&, …
}

// harmlessRedirect: 2>&1, and output (fd 1 or 2) to /dev/null.
func harmlessRedirect(r *syntax.Redirect) bool {
	if r.Hdoc != nil {
		return false
	}
	w, ok := literal(r.Word)
	if !ok {
		return false
	}
	n := ""
	if r.N != nil {
		n = r.N.Value
	}
	switch r.Op {
	case syntax.DplOut: // n>&w
		return n == "2" && w == "1"
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll:
		return (n == "" || n == "1" || n == "2") && w == "/dev/null"
	}
	return false
}

// literal returns a word's value when nothing in it expands: plain text,
// single quotes, and double quotes holding plain text. Unquoted braces and a
// leading ~ (which bash expands) and backslash escapes are refused.
func literal(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", true
	}
	var b strings.Builder
	for i, part := range w.Parts {
		switch x := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(x.Value, "{}\\") || i == 0 && strings.HasPrefix(x.Value, "~") {
				return "", false
			}
			b.WriteString(x.Value)
		case *syntax.SglQuoted:
			if x.Dollar { // $'…' interprets escapes
				return "", false
			}
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			if x.Dollar {
				return "", false
			}
			for _, q := range x.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok || strings.Contains(l.Value, "\\") {
					return "", false
				}
				b.WriteString(l.Value)
			}
		default: // $x, ${x}, $(…), `…`, $((…)), <(…), extglob
			return "", false
		}
	}
	if strings.IndexFunc(b.String(), func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", false // control characters: what's checked must be what bash sees (found by FuzzClassifier)
	}
	return b.String(), true
}

// pathsIn returns what in a word may be a path: the word, the value of
// --flag=value, and the value attached to a short flag (-f/etc/x).
func pathsIn(w string) []string {
	out := []string{w}
	if _, v, ok := strings.Cut(w, "="); ok {
		out = append(out, v)
	}
	if len(w) > 2 && w[0] == '-' && w[1] != '-' {
		out = append(out, w[2:])
	}
	return out
}

func (p *Policy) inRoot(path string) bool {
	if p.Root == "" {
		return false
	}
	rel, err := filepath.Rel(p.Root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
