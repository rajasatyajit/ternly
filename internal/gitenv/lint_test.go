package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// workspace are the production call sites that keep the user's own git
// settings (gitenv.Workspace, ADR 024); they still drop an inherited
// repository. Every other git call uses gitenv.Command or gitenv.Clean.
var workspace = map[string]string{
	"internal/agent/agent.go systemPrompt":             "reads the branch of the user's workspace, as the user's git sees it",
	"internal/session/store.go rootCommit":             "keys sessions by the user's repository, as the user's git sees it",
	"internal/capability/validate.go (*Validator).git": "ls-remote of a URL: the user's credential and SSH settings (GIT_SSH_COMMAND, askpass) must apply",
	"internal/plugins/store.go gitFetch":               "clones a plugin: the user's credential and SSH settings must apply; config hardened with -c and GIT_CONFIG_NOSYSTEM",
}

// TestGitCallsIsolated: every git exec goes through this package, or is an
// allowed production site; in internal/checkpoint (ternly's own object
// store) every function that execs git sets its Env from this package.
func TestGitCallsIsolated(t *testing.T) {
	root, _ := filepath.Abs("../..")
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if n := d.Name(); rel != "." && (strings.HasPrefix(n, ".") || n == "dist" || n == "vendor" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasPrefix(filepath.ToSlash(rel), "internal/gitenv/") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := funcName(fn)
			// cmd vars whose Env this function sets from gitenv: c.Env = gitenv.X(…) or append(gitenv.X(…), …)
			isolated := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				sel, ok := as.Lhs[0].(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Env" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && mentionsGitenv(as.Rhs[0]) {
					isolated[id.Name] = true
				}
				return true
			})
			// each exec call, and the variable it is assigned to (if any)
			assigned := map[*ast.CallExpr]string{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
					if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
						if id, ok := as.Lhs[0].(*ast.Ident); ok {
							assigned[call] = id.Name
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isExecCommand(call) {
					return true
				}
				key := filepath.ToSlash(rel) + " " + name
				inCheckpoint := strings.HasPrefix(filepath.ToSlash(rel), "internal/checkpoint/") && !strings.HasSuffix(rel, "_test.go")
				if !inCheckpoint && !runsGit(call) {
					return true
				}
				if v, ok := assigned[call]; !ok || !isolated[v] {
					t.Errorf("%s: %s runs git with an inherited environment; set its Env from gitenv or use gitenv.Command (ADR 024)", fset.Position(call.Pos()), key)
					return true
				}
				if !inCheckpoint {
					if _, listed := workspace[key]; !listed || strings.HasSuffix(rel, "_test.go") {
						t.Errorf("%s: %s runs git directly; use gitenv.Command (ADR 024)", fset.Position(call.Pos()), key)
					} else {
						seen[key] = true
					}
				}
				return true
			})
		}
		return nil
	})
	for k := range workspace {
		if !seen[k] {
			t.Errorf("allowed entry %q no longer runs git: remove it", k)
		}
	}
}

func mentionsGitenv(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "gitenv" {
				found = true
			}
		}
		return !found
	})
	return found
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	var b strings.Builder
	switch r := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := r.X.(*ast.Ident); ok {
			b.WriteString("(*" + id.Name + ")")
		}
	case *ast.Ident:
		b.WriteString(r.Name)
	}
	return b.String() + "." + fn.Name.Name
}

func isExecCommand(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
}

// runsGit: the command is the literal "git" (first argument, or the second
// for CommandContext).
func runsGit(c *ast.CallExpr) bool {
	i := 0
	if c.Fun.(*ast.SelectorExpr).Sel.Name == "CommandContext" {
		i = 1
	}
	if len(c.Args) <= i {
		return false
	}
	lit, ok := c.Args[i].(*ast.BasicLit)
	return ok && lit.Value == `"git"`
}
