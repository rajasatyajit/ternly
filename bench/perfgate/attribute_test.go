package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readGzCover(t *testing.T, p string) map[string]int {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	c, err := readCover(z)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The real case (ADR 017 amendment): on PR #14 the gate failed BenchmarkFrame
// +7.4%, and none of the code it runs had changed. The method must say so
// from the recorded profiles.
func TestAttributionFramePR14(t *testing.T) {
	d := filepath.Join("testdata", "frame-pr14")
	c1, c2 := readGzCover(t, filepath.Join(d, "cov100.gz")), readGzCover(t, filepath.Join(d, "cov200.gz"))
	files := perOp(c1, c2, 100, "github.com/rajasatyajit/ternly")
	if !slices.Contains(files, "internal/tools/guard.go") {
		t.Fatalf("per-op files %v: the injection flagger (guard.go) runs per op", files)
	}
	for _, f := range files {
		if strings.HasPrefix(f, "internal/llm/") {
			t.Fatalf("per-op files %v: quota detection (internal/llm) never runs on this path", files)
		}
	}
	llm := 0
	for blk := range c2 {
		if strings.Contains(blk, "/internal/llm/") {
			llm++
		}
	}
	if llm == 0 {
		t.Fatal("test premise: internal/llm is instrumented in the profile")
	}
	ch, _ := os.ReadFile(filepath.Join(d, "changed.txt"))
	changed := strings.Split(string(ch), "\n")
	if !slices.Contains(changed, "internal/llm/llm.go") || !slices.Contains(changed, "internal/tools/security.go") {
		t.Fatal("test premise: PR #14 changed internal/llm/llm.go and internal/tools/security.go")
	}
	a := attribute("internal/tools", "BenchmarkFrame", 100, files, []string{"internal/tools/guard_test.go", "internal/tools/testdata/"}, changed)
	if !a.clean() || !strings.HasPrefix(a.line(), "none of the code this benchmark runs changed") {
		t.Fatalf("attribution %+v: %s", a, a.line())
	}
}

// What makes a benchmark "touched": a per-op file, its own _test.go or
// testdata, or go.mod/go.sum (dependency code isn't attributed).
func TestAttributeTouched(t *testing.T) {
	path := []string{"internal/tools/guard.go"}
	own := []string{"internal/tools/guard_test.go", "internal/tools/testdata/"}
	for _, c := range []struct {
		name    string
		changed []string
		clean   bool
		line    string
	}{
		{"unrelated", []string{"internal/llm/llm.go", "internal/tools/security.go", "internal/tools/restrict_test.go"}, true, "none of the code"},
		{"a per-op file", []string{"internal/tools/guard.go"}, false, "on the path: internal/tools/guard.go"},
		{"the benchmark's own file", []string{"internal/tools/guard_test.go"}, false, "on the path: internal/tools/guard_test.go"},
		{"its testdata", []string{"internal/tools/testdata/corpus.txt"}, false, "on the path: internal/tools/testdata/corpus.txt"},
		{"dependencies", []string{"go.sum"}, false, "go.mod/go.sum changed"},
	} {
		a := attribute("internal/tools", "BenchmarkFrame", 100, path, own, c.changed)
		if a.clean() != c.clean || !strings.HasPrefix(a.line(), c.line) {
			t.Errorf("%s: clean=%v line=%q", c.name, a.clean(), a.line())
		}
	}
}

// Per-op means grown by at least n/50 between the n and 2n runs; setup runs
// the same number of times in both.
func TestPerOp(t *testing.T) {
	m := "example.com/m"
	c1 := map[string]int{m + "/a.go:1.1,2.2 1": 101, m + "/setup.go:1.1,2.2 1": 1, m + "/rare.go:1.1,2.2 1": 3}
	c2 := map[string]int{m + "/a.go:1.1,2.2 1": 201, m + "/setup.go:1.1,2.2 1": 1, m + "/rare.go:1.1,2.2 1": 4, "fmt/print.go:1.1,2.2 1": 900}
	if got := perOp(c1, c2, 100, m); !slices.Equal(got, []string{"a.go"}) {
		t.Fatalf("perOp = %v, want [a.go]", got)
	}
}

func TestBenchRE(t *testing.T) {
	if got := benchRE("BenchmarkPick/v2"); got != "^BenchmarkPick$/^v2$" {
		t.Fatal(got)
	}
	if got := benchRE("BenchmarkX/a.b"); got != `^BenchmarkX$/^a\.b$` {
		t.Fatal(got)
	}
}
