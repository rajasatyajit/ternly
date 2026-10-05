package tools

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// FuzzClassifier compares the classifier with the parser it's built on
// (ADR 015): every word it takes as a literal must expand, per mvdan's own
// expander, to exactly the string the classifier checked; and an approved
// command must print and re-parse to the same argv (no ambiguity in what
// bash will run).
func FuzzClassifier(f *testing.F) {
	for _, s := range []string{
		"ls -la && cat go.mod 2>/dev/null", "cat x | head -20", "CGO_ENABLED=0 go build ./...", `grep -n "a b" 'c|d' x`,
		"cd /work/repo && go build ./x 2>&1 | head -30", "mkdir -p internal/x", "rg x --pre 2>&1 ./evil", "cat $'\\x2f'",
		"cat {/etc/passwd,x}", "ls # c", "(ls)", "ls &", "echo $(id)", `echo "$HOME"`, "cat <<EOF\nx\nEOF", "ls >/dev/null 2>&1",
		`ls "a"'b'c`, "find . -name '*.go' | wc -l", "go test -run=TestX ./...", "a=1 b=2 go vet", "ls\nls", "l\\s", "ls ~/x",
	} {
		f.Add(s)
	}
	p := NewPolicy("edits", nil)
	p.Root = "/work/repo"
	cfg := &expand.Config{Env: expand.ListEnviron()}
	f.Fuzz(func(t *testing.T, cmd string) {
		ok := p.safeCommand(cmd, true) // no panic, whatever the input
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
		if err != nil {
			if ok {
				t.Fatalf("approved %q, which doesn't parse: %v", cmd, err)
			}
			return
		}
		syntax.Walk(file, func(n syntax.Node) bool {
			w, isWord := n.(*syntax.Word)
			if !isWord {
				return true
			}
			mine, lit := literal(w)
			if !lit {
				return true
			}
			theirs, err := expand.Literal(cfg, w)
			if err != nil || theirs != mine {
				t.Fatalf("%q: classifier reads word %q, the expander %q (err %v)", cmd, mine, theirs, err)
			}
			return true
		})
		if !ok {
			return
		}
		argv, _ := commands(cmd)
		var b strings.Builder
		if err := syntax.NewPrinter().Print(&b, file); err != nil {
			t.Fatal(err)
		}
		again, ok2 := commands(b.String())
		if !ok2 || len(again) != len(argv) {
			t.Fatalf("%q printed as %q: argv %q vs %q", cmd, b.String(), argv, again)
		}
		for i := range argv {
			if strings.Join(argv[i], "\x00") != strings.Join(again[i], "\x00") {
				t.Fatalf("%q printed as %q: argv %q vs %q", cmd, b.String(), argv, again)
			}
		}
	})
}
