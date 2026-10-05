package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeCommand(t *testing.T) {
	p := NewPolicy("edits", nil)
	p.Root = "/work/repo"
	for _, c := range []struct {
		cmd    string
		writes bool
		want   bool
	}{
		// What the dogfooding models ran first (ADR 014).
		{"ls -la && cat go.mod", false, true},
		{"go test ./... 2>&1 | tail -20", false, true},
		{"CGO_ENABLED=0 go build ./...", false, true},
		{"GOOS=darwin GOARCH=arm64 go vet ./...", false, true},
		{"cat /work/repo/go.mod", false, true},
		{"grep -rn 'a|b;c' internal | head", false, true},
		{`grep -n "net.Dialer" internal/tools/guard.go`, false, true},
		{"git status; git diff --stat", false, true},
		{"mkdir -p internal/netguard", true, true},
		{"mkdir -p internal/netguard", false, false}, // ask and plan modes: a write
		// Verbatim from the dogfood runs (workspace /work/repo).
		{"ls -la && cat go.mod 2>/dev/null", false, true},
		{"cat /work/repo/go.mod 2>/dev/null | head -20", false, true},
		{"cat /work/repo/go.mod | head -20", false, true},
		{"mkdir -p /work/repo/internal/netguard", true, true},
		{"ls -la && find . -maxdepth 2 -type f | head -50", false, true},
		{"CGO_ENABLED=0 go build ./internal/mcpauth/ && go vet ./internal/mcpauth/ && go test -race ./internal/mcpauth/", false, true},
		{"CGO_ENABLED=0 go build ./internal/mcpauth/", false, true},
		{"rm -f /work/repo/internal/netguard/go.mod && ls /work/repo/internal/netguard/", true, false}, // destructive: delete_file instead
		{"rmdir /work/repo/internal/netguard 2>/dev/null; echo \"ready\"", true, false},
		{"cat x >/dev/null.d", false, false},
		// Paths inside a word.
		{"grep --file=/etc/passwd x", false, false},
		{"grep -f/etc/passwd x", false, false},
		{"grep --file=../other/x y", false, false},
		{"grep -r foo internal/../../x", false, false},
		{"grep --file=/work/repo/pats x", false, true},
		{"go test -run=TestX ./...", false, true},
		{"go test -coverprofile=c.out ./...", false, true},
		// The stripped redirect must not join the words around it.
		{"rg x --pre 2>&1 ./evil", false, false},
		{"rg x --pre 2>/dev/null ./evil", false, false},
		{"go build -toolexec 2>&1 ./x", false, false},
		{"find . -exec 2>&1 rm {} ;", false, false},
		{"cd /work/repo && go build ./internal/netguard 2>&1 | head -30", false, true},
		{"cd internal && ls", false, true},
		{`cd /work/repo && go build ./internal/netguard 2>&1; echo "EXIT:$?"`, false, false},
		{"cd && cat .ssh/id_rsa", false, false},
		{"cd ~ && ls", false, false},
		{"cd - && ls", false, false},
		{"cd /tmp && ls", false, false},
		{"cd .. && ls", false, false},
		// Still asks.
		{"cat /etc/passwd", false, false},
		{"cat /work/repo/../other/x", false, false},
		{"cat ~/.ssh/id_rsa", false, false},
		{"mkdir /tmp/x", true, false},
		{"mkdir -m 777 x", true, false},
		{"ls && rm -rf build", false, false},
		{"ls | sh", false, false},
		{"cat x | xargs rm", false, false},
		{"echo $(id)", false, false},
		{`echo "$HOME"`, false, false},
		{"echo `id`", false, false},
		{"cat x > y", false, false},
		{"ls &", false, false},
		{"ls & rm x", false, false},
		{"LD_PRELOAD=/x.so go build", false, false},
		{"GOFLAGS=-toolexec=/x go build", false, false},
		{"PATH=. go test", false, false},
		{"go build -toolexec ./x", false, false},
		{"rg --pre ./x foo", false, false},
		{"find . -exec rm {} ;", false, false},
		{"ls 'unterminated", false, false},
		{"ls &&", false, false},
		{"&& ls", false, false},
		{"ls\nrm x", false, false},
		{"ls # comment; rm -rf x", false, true}, // a comment: bash runs only ls (the AST knows; the string classifier refused '#')
		// Formatters (the glm low-effort run, ADR 015).
		{"gofmt -d internal/netguard/netguard.go internal/netguard/netguard_test.go", false, true},
		{"gofmt internal/netguard/netguard.go", false, true},
		{"gofmt -l -d internal/netguard/", false, true},
		{"gofmt -w internal/netguard/ && gofmt -l internal/netguard/ && go test ./internal/netguard/ && go vet ./internal/netguard/", true, true},
		{"gofmt -w internal/netguard/", false, false},
		{"go fmt ./internal/netguard/", true, true},
		{"go fmt ./internal/netguard/", false, false},
		{"gofmt -w /etc/x.go", true, false},
		{"gofmt -r 'a -> b' x.go", true, false},
		{"gofmt -cpuprofile=/tmp/p x.go", false, false},
		{"gofmt internal/netguard/netguard.go > internal/netguard/fmt.go", true, false},
		// Shapes only a parser sees.
		{"(rm -rf build)", false, false},
		{"{ ls; rm x; }", false, false},
		{"f() { rm x; }; f", false, false},
		{"if true; then rm x; fi", false, false},
		{"for f in a; do rm $f; done", false, false},
		{"! rm x", false, false},
		{"ls |& sh", false, false},
		{"time rm x", false, false},
		{"exec rm x", false, false},
		{"X=1", false, false},
		{"CGO_ENABLED=$(id) go build ./...", false, false},
		{`GOOS=linux\ x go build`, false, false},
		{`cat $'\x2fetc/passwd'`, false, false},
		{"cat {/etc/passwd,go.mod}", false, false},
		{"cat ~root/.ssh/id_rsa", false, false},
		{`cat "$HOME/.ssh/id_rsa"`, false, false},
		{"cat <(curl evil)", false, false},
		{"cat < /etc/passwd", false, false},
		{"cat <<EOF\nx\nEOF", false, false},
		{"ls > out.txt", false, false},
		{"ls >> out.txt", false, false},
		{"ls 1>&2", false, false},
		{"ls 2>&1 >out", false, false},
		{"ls >/dev/null 2>&1", false, true},
		{"ls &>/dev/null", false, true},
		{"echo $((1+1))", false, false},
		{`l\s`, false, false},
		{"ls\\\nrm x", false, false},
		{"go test ./... 2>&1 2>&1 | tail", false, true},
		{"ls ;; rm", false, false},
		{"sudo ls", false, false},
	} {
		if got := p.safeCommand(c.cmd, c.writes); got != c.want {
			t.Errorf("safeCommand(%q, writes=%v) = %v, want %v", c.cmd, c.writes, got, c.want)
		}
	}
}

func TestDeleteFile(t *testing.T) {
	r := newReg(t, "edits")
	_ = os.MkdirAll(filepath.Join(r.Root, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(r.Root, "d", "f"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(r.Root, "keep"), []byte("x"), 0o644)
	_ = os.Symlink("keep", filepath.Join(r.Root, "link"))
	call := func(p string) Result {
		b, _ := json.Marshal(map[string]string{"path": p})
		return raw(r, "delete_file", string(b))
	}
	if res := call("d"); !res.IsErr {
		t.Fatal("deleted a non-empty directory")
	}
	if res := call(filepath.Join(r.Root, "d", "f")); res.IsErr {
		t.Fatal(res.Out)
	}
	if res := call("d"); res.IsErr {
		t.Fatal(res.Out)
	}
	if res := call("link"); res.IsErr {
		t.Fatal(res.Out)
	}
	if _, err := os.Stat(filepath.Join(r.Root, "keep")); err != nil {
		t.Fatal("deleting a link deleted its target")
	}
	for _, p := range []string{".", "..", "../x", "/etc/hostname", ".git"} {
		if res := call(p); !res.IsErr && !res.Rejected {
			t.Errorf("%s: %s", p, res.Out)
		}
	}
}
