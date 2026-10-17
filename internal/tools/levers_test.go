package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

func bigGo(n int) string {
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; b.Len() < 1 || strings.Count(b.String(), "\n") < n; i++ {
		fmt.Fprintf(&b, "// F%d does a thing.\nfunc F%d() int {\n\treturn %d\n}\n\n", i, i, i)
	}
	return b.String()
}

// OutlineReads (ADR 029): a large file read without a range returns its
// declarations with line numbers; a range, a short file or the lever off
// read lines as before.
func TestOutlineReads(t *testing.T) {
	r := newReg(t, "edits")
	_ = os.WriteFile(filepath.Join(r.Root, "big.go"), []byte(bigGo(600)), 0o644)
	_ = os.WriteFile(filepath.Join(r.Root, "small.go"), []byte("package big\n\nfunc S() {}\n"), 0o644)
	read := func(args string) string {
		return r.Call(context.Background(), llm.ToolCall{Name: "read_file", Args: args}).Out
	}
	if out := read(`{"path":"big.go"}`); strings.Contains(out, "declarations") {
		t.Fatal("outlined with the lever off")
	}
	r.OutlineReads = true
	out := read(`{"path":"big.go"}`)
	if !strings.Contains(out, "declarations are below") || !strings.Contains(out, "func F0() int") || strings.Contains(out, "return 0") {
		t.Fatalf("outline:\n%.400s", out)
	}
	if out := read(`{"path":"big.go","offset":3,"limit":3}`); !strings.Contains(out, "func F0() int") || !strings.Contains(out, "return 0") {
		t.Fatalf("a ranged read must read lines:\n%s", out)
	}
	if out := read(`{"path":"small.go"}`); strings.Contains(out, "declarations") || !strings.Contains(out, "func S()") {
		t.Fatalf("a short file must read whole:\n%s", out)
	}
}

// NoSchemaRepair (the control arm): an integer written as 3.0 is refused
// instead of repaired.
func TestNoSchemaRepair(t *testing.T) {
	r := newReg(t, "edits")
	_ = os.WriteFile(filepath.Join(r.Root, "a.txt"), []byte("1\n2\n3\n4\n"), 0o644)
	call := func() Result {
		return r.Call(context.Background(), llm.ToolCall{Name: "read_file", Args: `{"path":"a.txt","offset":3.0}`})
	}
	if res := call(); res.IsErr || !strings.Contains(res.Out, "3") {
		t.Fatalf("repair on: %+v", res)
	}
	r.NoSchemaRepair = true
	if res := call(); !res.IsErr || !strings.Contains(res.Out, "wrong type") {
		t.Fatalf("repair off: %+v", res)
	}
}
