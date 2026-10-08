package main

// Profile-diff attribution (ADR 017 amendment): which of the module's files a
// benchmark executes per op, and whether the PR changed any of them.
//
// Method: run the benchmark with count-mode coverage over the module at N
// and at 2N iterations (-benchtime Nx). A block that runs per op grows by
// about N between the two runs; setup (before the timed loop, package init,
// the benchmark's own first N=1 run) is the same in both. A block that grows
// by at least N/50 is on the path; rarer ones (amortised growth, timers) are
// left out, since their cost per op is negligible.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// attribution is what the gate knows about one benchmark's code path.
type attribution struct {
	Pkg       string   `json:"pkg"`        // module-relative package dir
	Benchmark string   `json:"benchmark"`  // BenchmarkX or BenchmarkX/sub
	N         int      `json:"n"`          // iterations of the first run (the second ran 2N)
	PerOp     []string `json:"per_op"`     // module-relative files with blocks that run per op
	Own       []string `json:"own"`        // the benchmark's own source: its _test.go file, its package's testdata
	Touched   []string `json:"touched"`    // files of PerOp or Own the PR changed
	DepsMoved bool     `json:"deps_moved"` // go.mod or go.sum changed: code outside the module may have
}

// clean reports whether the PR left everything this benchmark runs alone.
func (a attribution) clean() bool { return len(a.Touched) == 0 && !a.DepsMoved }

// line is the job-summary sentence for a failing benchmark.
func (a attribution) line() string {
	switch {
	case a.DepsMoved:
		return "go.mod/go.sum changed: code outside the module may be on the path, so it can't be ruled out"
	case len(a.Touched) > 0:
		return "on the path: " + strings.Join(a.Touched, ", ")
	}
	return fmt.Sprintf("none of the code this benchmark runs changed (%d files per op)", len(a.PerOp))
}

// readCover parses a count-mode coverprofile into block → count (blocks are
// "file:start,end numStmts"; a block appears once per instrumented package
// that lists it, so counts are summed).
func readCover(r io.Reader) (map[string]int, error) {
	out := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		l := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(l, "mode: count") && !strings.HasPrefix(l, "mode: atomic") {
				return nil, fmt.Errorf("coverprofile mode %q: want count or atomic", l)
			}
			continue
		}
		i := strings.LastIndexByte(l, ' ')
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(l[i+1:])
		if err != nil {
			return nil, fmt.Errorf("coverprofile line %q", l)
		}
		out[l[:i]] += n
	}
	return out, sc.Err()
}

// perOp lists the module-relative files whose blocks grew by at least n/50
// (and at least 1) from the n-iteration profile to the 2n-iteration one.
func perOp(c1, c2 map[string]int, n int, module string) []string {
	min := max(1, n/50)
	files := map[string]bool{}
	for blk, v2 := range c2 {
		if v2-c1[blk] < min {
			continue
		}
		f, _, _ := strings.Cut(blk, ":")
		if rel, ok := strings.CutPrefix(f, module+"/"); ok {
			files[rel] = true
		}
	}
	return sorted(files)
}

// ownFiles is the benchmark's own source, which coverage doesn't instrument:
// the _test.go file declaring it, and the package's testdata.
func ownFiles(root, pkg, bench string) []string {
	top, _, _ := strings.Cut(bench, "/")
	decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(top) + `\(`)
	var out []string
	ents, _ := os.ReadDir(filepath.Join(root, pkg))
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(root, pkg, e.Name())); err == nil && decl.Match(b) {
			out = append(out, filepath.ToSlash(filepath.Join(pkg, e.Name())))
		}
	}
	return append(out, filepath.ToSlash(filepath.Join(pkg, "testdata"))+"/")
}

// attribute intersects a benchmark's path with the PR's changed files.
func attribute(pkg, bench string, n int, perOpFiles, own, changed []string) attribution {
	a := attribution{Pkg: pkg, Benchmark: bench, N: n, PerOp: perOpFiles, Own: own}
	path := map[string]bool{}
	for _, f := range perOpFiles {
		path[f] = true
	}
	touched := map[string]bool{}
	for _, c := range changed {
		c = filepath.ToSlash(strings.TrimSpace(c))
		switch {
		case c == "":
		case c == "go.mod" || c == "go.sum":
			a.DepsMoved = true
		case path[c]:
			touched[c] = true
		default:
			for _, o := range own {
				if c == o || strings.HasSuffix(o, "/") && strings.HasPrefix(c, o) {
					touched[c] = true
				}
			}
		}
	}
	a.Touched = sorted(touched)
	return a
}

// benchRE is a -test.bench pattern matching exactly one benchmark, each
// slash-separated part anchored ("BenchmarkPick/v2" → "^BenchmarkPick$/^v2$").
func benchRE(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// attributions is the file bench/run.sh builds, keyed by "pkg BenchmarkX".
type attributions map[string]attribution

func attrKey(pkg, bench string) string { return pkg + " " + bench }

func loadAttributions(p string) (attributions, error) {
	a := attributions{}
	if p == "" {
		return a, nil
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return a, nil
	}
	return a, json.Unmarshal(b, &a)
}

// attributeCmd: perfgate attribute -pkg P -bench B -n N -cov1 F -cov2 F
// -changed F -module M -root R -out attr.json (adds one entry).
func attributeCmd(pkg, bench string, n int, cov1, cov2, changedPath, module, root, out string) error {
	read := func(p string) (map[string]int, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readCover(f)
	}
	c1, err := read(cov1)
	if err != nil {
		return err
	}
	c2, err := read(cov2)
	if err != nil {
		return err
	}
	ch, err := os.ReadFile(changedPath)
	if err != nil {
		return err
	}
	a := attribute(pkg, bench, n, perOp(c1, c2, n, module), ownFiles(root, pkg, bench), strings.Split(string(ch), "\n"))
	all, err := loadAttributions(out)
	if err != nil {
		return err
	}
	all[attrKey(pkg, bench)] = a
	b, _ := json.MarshalIndent(all, "", "  ")
	return os.WriteFile(out, append(b, '\n'), 0o644)
}
