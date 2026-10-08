package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frameCSV is the PR #14 case: BenchmarkFrame +7.4% in internal/tools.
const frameCSV = "goos: linux\ngoarch: amd64\npkg: github.com/rajasatyajit/ternly/internal/tools\ncpu: X\n" +
	",base.txt,,head.txt,,,\n,sec/op,CI,sec/op,CI,vs base,P\n" +
	"Frame-4,7.97e-05,1%,8.56e-05,1%,+7.40%,p=0.000 n=10\ngeomean,1,,1,,+0.00%,\n\n"

func frameWaiver() waiver {
	return waiver{ID: "frame-layout-pr14", Benchmark: "BenchmarkFrame", Unit: "sec/op", ADR: "017",
		Evidence: "layout artefact: count-mode coverage shows quota detection never runs per op (ADR 017 amendment)", Release: "v0.1.1"}
}

func clean() attribution {
	return attribution{Pkg: "internal/tools", Benchmark: "BenchmarkFrame", N: 100, PerOp: []string{"internal/tools/guard.go"}}
}

func touched() attribution {
	a := clean()
	a.Touched = []string{"internal/tools/guard.go"}
	return a
}

func gateWith(t *testing.T, ws []waiver, o checkOpts) (bool, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ab.csv")
	if err := os.WriteFile(p, []byte(frameCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := readRows(p)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	failed, err := checkWith(&out, config{BudgetPct: 5, Alpha: 0.05, BinaryBudgetPct: 5, Waivers: ws}, rows, "", o)
	if err != nil {
		t.Fatal(err)
	}
	return failed, out.String()
}

func TestWaivers(t *testing.T) {
	attr := func(a attribution) attributions { return attributions{"internal/tools BenchmarkFrame": a} }
	for _, c := range []struct {
		name string
		ws   []waiver
		o    checkOpts
		fail bool
		want string
	}{
		{"no waiver: fails, with its attribution", nil, checkOpts{attr: attr(clean()), release: "v0.1.1"}, true,
			"none of the code this benchmark runs changed"},
		{"honoured: untouched code, same release", []waiver{frameWaiver()}, checkOpts{attr: attr(clean()), release: "v0.1.1"}, false,
			"**WAIVED (frame-layout-pr14"},
		{"expired: a release since", []waiver{frameWaiver()}, checkOpts{attr: attr(clean()), release: "v0.1.2"}, true,
			"waiver frame-layout-pr14 refused: expired"},
		{"refused: the PR touched its code", []waiver{frameWaiver()}, checkOpts{attr: attr(touched()), release: "v0.1.1"}, true,
			"waiver frame-layout-pr14 refused: on the path: internal/tools/guard.go"},
		{"refused: dependencies moved", []waiver{frameWaiver()}, checkOpts{attr: attr(attribution{DepsMoved: true}), release: "v0.1.1"}, true,
			"refused: go.mod/go.sum changed"},
		{"refused: no attribution", []waiver{frameWaiver()}, checkOpts{release: "v0.1.1"}, true,
			"refused: no attribution"},
		{"refused: release unknown", []waiver{frameWaiver()}, checkOpts{attr: attr(clean())}, true,
			"the release couldn't be determined"},
		{"A/A run honours none", []waiver{frameWaiver()}, checkOpts{attr: attr(clean()), release: "v0.1.1", noWaivers: true}, true,
			"**FAIL: +7.4% > 5%**"},
		{"another unit isn't waived", []waiver{func() waiver { w := frameWaiver(); w.Unit = "B/op"; return w }()}, checkOpts{attr: attr(clean()), release: "v0.1.1"}, true,
			"**FAIL"},
		{"another benchmark isn't waived", []waiver{func() waiver { w := frameWaiver(); w.Benchmark = "BenchmarkGrep"; return w }()}, checkOpts{attr: attr(clean()), release: "v0.1.1"}, true,
			"**FAIL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			failed, out := gateWith(t, c.ws, c.o)
			if failed != c.fail || !strings.Contains(out, c.want) {
				t.Errorf("failed=%v (want %v), output lacks %q:\n%s", failed, c.fail, c.want, out)
			}
			if !c.fail && strings.Contains(out, "| ok") && !strings.Contains(out, "WAIVED") {
				t.Errorf("a waived regression must read WAIVED, not ok:\n%s", out)
			}
		})
	}
}

func TestMalformedWaivers(t *testing.T) {
	base := `{"budget_pct":5,"binary_budget_pct":5,"alpha":0.05,"count":10,"suites":[{"pkg":"internal/tools","bench":"^BenchmarkFrame$","benchtime":"200ms"}],"waivers":[%s]}`
	good := `{"id":"frame-layout-pr14","benchmark":"BenchmarkFrame","evidence":"layout artefact: quota detection never runs per op, by coverage counts","adr":"017","release":"v0.1.1"}`
	for _, c := range []struct{ name, waivers, want string }{
		{"valid", good, ""},
		{"no id", strings.Replace(good, `"id":"frame-layout-pr14",`, "", 1), "waiver id"},
		{"a pattern, not one benchmark", strings.Replace(good, `"BenchmarkFrame"`, `"BenchmarkF.*"`, 1), "must name one benchmark exactly"},
		{"no evidence", strings.Replace(good, `"layout artefact: quota detection never runs per op, by coverage counts"`, `"noise"`, 1), "needs evidence"},
		{"no adr", strings.Replace(good, `"adr":"017",`, "", 1), "needs an adr"},
		{"no release", strings.Replace(good, `,"release":"v0.1.1"`, "", 1), "release"},
		{"release not a tag", strings.Replace(good, `"v0.1.1"`, `"next"`, 1), "release"},
		{"bad unit", strings.Replace(good, `"evidence"`, `"unit":"ns","evidence"`, 1), "unit"},
		{"duplicate id", good + "," + good, "used twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "perf.json")
			if err := os.WriteFile(p, []byte(strings.Replace(base, "%s", c.waivers, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(p)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("valid waiver refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// -failures lists every regression beyond budget, waived or not, for attribution.
func TestFailuresFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "failures.tsv")
	gateWith(t, nil, checkOpts{failures: f})
	b, err := os.ReadFile(f)
	if err != nil || string(b) != "github.com/rajasatyajit/ternly/internal/tools\tBenchmarkFrame\n" {
		t.Fatalf("failures file %q (%v)", b, err)
	}
}
