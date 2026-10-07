// Command perfgate reads benchstat's CSV and applies the performance budget
// in bench/perf.json (ADR 017).
//
//	perfgate check -config bench/perf.json -csv ab.csv [-size BASE,HEAD]
//	    A/B: fails on a significant regression beyond budget, on a benchmark
//	    that disappeared, or on binary growth beyond its budget.
//	perfgate suites -config bench/perf.json
//	    Prints the suites as "pkg<TAB>regexp<TAB>benchtime" lines, then
//	    "count", "alpha" (for bench/run.sh).
//	perfgate summary -csv one.csv
//	    One input: prints {"name": {"sec/op": {"median": …, "ci": "…"}, …}} for
//	    bench/baseline.json.
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type budget struct {
	Pct float64 `json:"pct"`
	ADR string  `json:"adr"` // a non-default budget needs one
	Why string  `json:"why"`
}

type suite struct {
	Pkg       string `json:"pkg"`
	Bench     string `json:"bench"`
	Benchtime string `json:"benchtime"`
}

type config struct {
	Count           int               `json:"count"`
	Suites          []suite           `json:"suites"`
	BudgetPct       float64           `json:"budget_pct"`
	Alpha           float64           `json:"alpha"`
	BinaryBudgetPct float64           `json:"binary_budget_pct"`
	Budgets         map[string]budget `json:"budgets"`
}

// row is one benchmark's line in one benchstat table (a unit: sec/op, B/op, allocs/op).
type row struct {
	pkg, name, unit string
	cells           []string
	only            string // the single input of a one-column table ("head.txt" in an A/B run: a new package)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: perfgate check|summary [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	cfgPath := fs.String("config", "bench/perf.json", "budget config")
	csvPath := fs.String("csv", "", "benchstat -format csv output")
	size := fs.String("size", "", "binary sizes in bytes: BASE,HEAD")
	retriesPath := fs.String("retries", "", "bench/run.sh's retries.tsv (side, package, first failure line)")
	_ = fs.Parse(os.Args[2:])
	if os.Args[1] == "suites" {
		if err := suites(os.Stdout, *cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "perfgate:", err)
			os.Exit(2)
		}
		return
	}
	rows, err := readRows(*csvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perfgate:", err)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "summary":
		err = summary(os.Stdout, rows)
	case "check":
		var cfg config
		if cfg, err = loadConfig(*cfgPath); err == nil {
			var failed bool
			failed, err = check(os.Stdout, cfg, rows, *size)
			if err == nil && *retriesPath != "" {
				err = retries(os.Stdout, *retriesPath)
			}
			if err == nil && failed {
				os.Exit(1)
			}
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "perfgate:", err)
		os.Exit(2)
	}
}

func loadConfig(p string) (config, error) {
	var c config
	b, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	if c.BudgetPct <= 0 || c.BinaryBudgetPct <= 0 || c.Alpha <= 0 || c.Count < 5 || len(c.Suites) == 0 {
		return c, fmt.Errorf("%s: needs budget_pct, binary_budget_pct and alpha > 0, count ≥ 5, and suites", p)
	}
	for name, b := range c.Budgets {
		if b.ADR == "" || b.Pct <= 0 {
			return c, fmt.Errorf("%s: budget for %s needs pct > 0 and an adr", p, name)
		}
	}
	return c, nil
}

func suites(w io.Writer, cfgPath string) error {
	c, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	for _, s := range c.Suites {
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Pkg, s.Bench, s.Benchtime)
	}
	fmt.Fprintf(w, "count\t%d\nalpha\t%g\n", c.Count, c.Alpha)
	return nil
}

var procSuffix = regexp.MustCompile(`-\d+$`)

// readRows parses benchstat's CSV: "pkg: …" lines, a header naming the unit
// (",sec/op,CI,…"), then one row per benchmark; geomean rows are skipped.
func readRows(p string) ([]row, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	var (
		out       []row
		pkg, unit string
		files     []string // the table's inputs, from its first header row
	)
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch {
		case len(rec) == 1 && strings.HasPrefix(rec[0], "pkg: "):
			pkg = strings.TrimPrefix(rec[0], "pkg: ")
		case len(rec) == 1: // goos:, goarch:, cpu:, blank
		case rec[0] == "" && strings.HasSuffix(rec[1], "/op"): // ",sec/op,CI,…"
			unit = rec[1]
		case rec[0] == "": // ",base.txt,,head.txt,,," (or only one of them)
			files = files[:0]
			for _, f := range rec[1:] {
				if f != "" {
					files = append(files, f)
				}
			}
		case rec[0] == "geomean":
		default:
			only := ""
			if len(files) == 1 {
				only = files[0]
			}
			out = append(out, row{pkg: pkg, name: "Benchmark" + procSuffix.ReplaceAllString(rec[0], ""), unit: unit, cells: rec[1:], only: only})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no benchmark rows", p)
	}
	return out, nil
}

// check applies the budgets to an A/B table (cells: base, CI, head, CI, delta, P).
// benchstat prints a delta only when the difference is significant at its
// alpha (passed to it by bench/run.sh), and "~" otherwise.
func check(w io.Writer, cfg config, rows []row, size string) (bool, error) {
	failed := false
	fmt.Fprintf(w, "### Performance gate (budget %g%%, alpha %g)\n\n| benchmark | unit | base | head | change | verdict |\n|---|---|---|---|---|---|\n", cfg.BudgetPct, cfg.Alpha)
	for _, r := range rows {
		c := append(r.cells, make([]string, 6)...)[:6] // a benchmark only one side has: trailing cells are dropped
		switch {
		case strings.HasPrefix(r.only, "head"): // a package only the head has
			c = []string{"", "", c[0], c[1], "", ""}
		case strings.HasPrefix(r.only, "base"): // …or only the base
			c = []string{c[0], c[1], "", "", "", ""}
		}
		base, head, delta := c[0], c[2], c[4]
		lim := cfg.BudgetPct
		if b, ok := cfg.Budgets[r.name]; ok {
			lim = b.Pct
		}
		verdict := "ok"
		switch {
		case head == "":
			verdict, failed = "**FAIL: missing at head** (a deleted benchmark needs an ADR)", true
		case base == "":
			verdict = "new (no base)"
		case delta == "~" || delta == "":
			verdict = "ok (no significant change)"
		default:
			pct, err := strconv.ParseFloat(strings.TrimSuffix(delta, "%"), 64)
			if err != nil {
				return false, fmt.Errorf("%s %s: delta %q", r.name, r.unit, delta)
			}
			if pct > lim {
				verdict, failed = fmt.Sprintf("**FAIL: +%.1f%% > %g%%**", pct, lim), true
			}
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", r.name, r.unit, base, head, delta, verdict)
	}
	if size != "" {
		b, h, ok := strings.Cut(size, ",")
		bs, e1 := strconv.ParseFloat(b, 64)
		hs, e2 := strconv.ParseFloat(h, 64)
		if !ok || e1 != nil || e2 != nil || bs <= 0 {
			return false, fmt.Errorf("-size %q: want BASE,HEAD in bytes", size)
		}
		pct := 100 * (hs - bs) / bs
		verdict := "ok"
		if pct > cfg.BinaryBudgetPct {
			verdict, failed = fmt.Sprintf("**FAIL: +%.1f%% > %g%%**", pct, cfg.BinaryBudgetPct), true
		}
		fmt.Fprintf(w, "| binary (static, stripped) | bytes | %.0f | %.0f | %+.2f%% | %s |\n", bs, hs, pct, verdict)
	}
	if failed {
		fmt.Fprintln(w, "\nA regression beyond budget fails the PR. Fix it, or record why it is worth it in an ADR and set a budget for that benchmark in bench/perf.json.")
	}
	return failed, nil
}

// summary turns a single-input table (cells: median, CI) into JSON.
func summary(w io.Writer, rows []row) error {
	out := map[string]map[string]any{}
	for _, r := range rows {
		if len(r.cells) < 2 {
			return fmt.Errorf("%s: not a single-input table", r.name)
		}
		v, err := strconv.ParseFloat(r.cells[0], 64)
		if err != nil {
			return fmt.Errorf("%s %s: %q", r.name, r.unit, r.cells[0])
		}
		k := strings.TrimPrefix(r.pkg, "github.com/rajasatyajit/ternly/") + "." + r.name
		if out[k] == nil {
			out[k] = map[string]any{}
		}
		out[k][r.unit] = map[string]any{"median": v, "ci95": r.cells[1]}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out) // encoding/json sorts map keys
}

// retries reports the suites bench/run.sh had to re-run (ADR 017: a flaky
// benchmark is retried up to twice). They don't change the verdict, since a
// suite that never passes already failed the job, but they are counted so
// that a flaky benchmark gets noticed and fixed.
func retries(w io.Writer, p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	n := map[string]int{}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 2 {
			continue
		}
		n[f[0]]++
		lines = append(lines, fmt.Sprintf("- %s `./%s`: %s", f[0], f[1], strings.TrimSpace(strings.Join(f[2:], ""))))
	}
	fmt.Fprintf(w, "\nRetries: base %d, head %d", n["base"], n["head"])
	if len(lines) == 0 {
		fmt.Fprintln(w)
		return nil
	}
	fmt.Fprintln(w, " (a flaky benchmark; fix it rather than rely on the retry):")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return nil
}
