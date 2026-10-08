// Command perfgate reads benchstat's CSV and applies the performance budget
// in bench/perf.json (ADR 017).
//
//	perfgate check -config bench/perf.json -csv ab.csv [-size BASE,HEAD]
//	    A/B: fails on a significant regression beyond budget, on a benchmark
//	    that disappeared, or on binary growth beyond its budget.
//	perfgate suites -config bench/perf.json
//	    Prints the suites as "pkg<TAB>regexp<TAB>benchtime" lines, then
//	    "count", "alpha" (for bench/run.sh).
//	    With -attribution (from perfgate attribute) and -release (the latest
//	    v* tag), a failing benchmark gets a line saying whether the PR changed
//	    the code it runs, and a waiver in bench/perf.json may excuse it.
//	    -failures writes the failing benchmarks ("pkg<TAB>name") for
//	    bench/run.sh to attribute; -no-waivers is the A/A run's mode.
//	perfgate attribute -pkg DIR -bench NAME -n N -cov1 F -cov2 F -changed F -module M -root R -out attr.json
//	    Profile-diff attribution of one benchmark (see attribute.go).
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

// waiver excuses one benchmark's regression for one release, when none of
// the code it runs changed (ADR 017 amendment). It is never honoured for a
// benchmark whose attributed code the PR touched, nor after the release it
// names has been succeeded by another v* tag.
type waiver struct {
	ID        string `json:"id"`        // a name, unique, for the table and the CHANGELOG
	Benchmark string `json:"benchmark"` // BenchmarkX or BenchmarkX/sub, exactly
	Unit      string `json:"unit"`      // optional: sec/op, B/op or allocs/op; empty means every unit
	Evidence  string `json:"evidence"`  // why it is noise: measurements, attribution, links
	ADR       string `json:"adr"`       // the ADR recording it
	Release   string `json:"release"`   // the latest v* tag when it was added; it expires at the next one
}

var (
	reRelease = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	reWaiver  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)
)

func (w waiver) validate() error {
	switch {
	case !reWaiver.MatchString(w.ID):
		return fmt.Errorf("waiver id %q: lowercase letters, digits and dashes, 3–64 long", w.ID)
	case !strings.HasPrefix(w.Benchmark, "Benchmark") || strings.ContainsAny(w.Benchmark, "*^$|() "):
		return fmt.Errorf("waiver %s: benchmark %q must name one benchmark exactly", w.ID, w.Benchmark)
	case w.Unit != "" && w.Unit != "sec/op" && w.Unit != "B/op" && w.Unit != "allocs/op":
		return fmt.Errorf("waiver %s: unit %q", w.ID, w.Unit)
	case len(strings.TrimSpace(w.Evidence)) < 40:
		return fmt.Errorf("waiver %s: needs evidence (at least a sentence: what was measured, and why it is noise)", w.ID)
	case w.ADR == "":
		return fmt.Errorf("waiver %s: needs an adr", w.ID)
	case !reRelease.MatchString(w.Release):
		return fmt.Errorf("waiver %s: release %q must be the latest v* tag when it was added (vX.Y.Z)", w.ID, w.Release)
	}
	return nil
}

type config struct {
	Waivers         []waiver          `json:"waivers"`
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
	var o checkOpts
	fs.StringVar(&o.failures, "failures", "", "check: write the failing benchmarks here (pkg<TAB>name), for attribution")
	fs.StringVar(&o.attrPath, "attribution", "", "check: attributions from perfgate attribute")
	fs.StringVar(&o.release, "release", "", "check: the latest v* tag reachable from the head (waivers expire at the next one)")
	fs.BoolVar(&o.noWaivers, "no-waivers", false, "check: honour no waiver (the A/A noise run)")
	var a struct {
		pkg, bench, cov1, cov2, changed, module, root, out string
		n                                                  int
	}
	fs.StringVar(&a.pkg, "pkg", "", "attribute: the benchmark's package dir, module-relative")
	fs.StringVar(&a.bench, "bench", "", "attribute: the benchmark (BenchmarkX or BenchmarkX/sub)")
	fs.IntVar(&a.n, "n", 0, "attribute: iterations of the first coverage run (the second ran 2n)")
	fs.StringVar(&a.cov1, "cov1", "", "attribute: coverprofile at n")
	fs.StringVar(&a.cov2, "cov2", "", "attribute: coverprofile at 2n")
	fs.StringVar(&a.changed, "changed", "", "attribute: the PR's changed files, one per line")
	fs.StringVar(&a.module, "module", "github.com/rajasatyajit/ternly", "attribute: the module path")
	fs.StringVar(&a.root, "root", ".", "attribute: the module root")
	fs.StringVar(&a.out, "out", "", "attribute: the attributions file to add to")
	_ = fs.Parse(os.Args[2:])
	if os.Args[1] == "benchre" && len(os.Args) == 3 {
		fmt.Println(benchRE(os.Args[2]))
		return
	}
	if os.Args[1] == "attribute" {
		if a.pkg == "" || a.bench == "" || a.n <= 0 || a.cov1 == "" || a.cov2 == "" || a.changed == "" || a.out == "" {
			fmt.Fprintln(os.Stderr, "perfgate attribute: needs -pkg -bench -n -cov1 -cov2 -changed -out")
			os.Exit(2)
		}
		if err := attributeCmd(a.pkg, a.bench, a.n, a.cov1, a.cov2, a.changed, a.module, a.root, a.out); err != nil {
			fmt.Fprintln(os.Stderr, "perfgate:", err)
			os.Exit(2)
		}
		return
	}
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
			if o.attr, err = loadAttributions(o.attrPath); err == nil {
				failed, err = checkWith(os.Stdout, cfg, rows, *size, o)
			}
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
	ids := map[string]bool{}
	for _, w := range c.Waivers {
		if err := w.validate(); err != nil {
			return c, fmt.Errorf("%s: %w", p, err)
		}
		if ids[w.ID] {
			return c, fmt.Errorf("%s: waiver id %q used twice", p, w.ID)
		}
		ids[w.ID] = true
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

// checkOpts are check's attribution and waiver inputs.
type checkOpts struct {
	failures  string // write failing benchmarks here
	attrPath  string
	attr      attributions
	release   string // the latest v* tag reachable from the head
	noWaivers bool
}

// check applies the budgets to an A/B table (cells: base, CI, head, CI, delta, P).
// benchstat prints a delta only when the difference is significant at its
// alpha (passed to it by bench/run.sh), and "~" otherwise.
func check(w io.Writer, cfg config, rows []row, size string) (bool, error) {
	return checkWith(w, cfg, rows, size, checkOpts{})
}

// modRel is a benchmark package's module-relative dir.
func modRel(pkg string) string {
	return strings.TrimPrefix(strings.TrimPrefix(pkg, "github.com/rajasatyajit/ternly"), "/")
}

// waive decides a regression beyond budget: the waiver honoured, or why not.
func waive(cfg config, o checkOpts, r row) (honoured *waiver, why string) {
	if o.noWaivers {
		return nil, ""
	}
	for i := range cfg.Waivers {
		wv := &cfg.Waivers[i]
		if wv.Benchmark != r.name || wv.Unit != "" && wv.Unit != r.unit {
			continue
		}
		a, ok := o.attr[attrKey(modRel(r.pkg), r.name)]
		switch {
		case o.release == "":
			return nil, fmt.Sprintf("waiver %s refused: the release couldn't be determined (no v* tag)", wv.ID)
		case o.release != wv.Release:
			return nil, fmt.Sprintf("waiver %s refused: expired (added at %s; %s has been released since): remove it", wv.ID, wv.Release, o.release)
		case !ok:
			return nil, fmt.Sprintf("waiver %s refused: no attribution for this benchmark", wv.ID)
		case !a.clean():
			return nil, fmt.Sprintf("waiver %s refused: %s", wv.ID, a.line())
		}
		return wv, ""
	}
	return nil, ""
}

func checkWith(w io.Writer, cfg config, rows []row, size string, o checkOpts) (bool, error) {
	failed := false
	var failing, notes []string
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
				failing = append(failing, r.pkg+"\t"+r.name)
				if a, ok := o.attr[attrKey(modRel(r.pkg), r.name)]; ok {
					notes = append(notes, fmt.Sprintf("- %s %s: %s", r.name, r.unit, a.line()))
				}
				if wv, why := waive(cfg, o, r); wv != nil {
					verdict = fmt.Sprintf("**WAIVED (%s, ADR %s, until the release after %s): +%.1f%% > %g%%**", wv.ID, wv.ADR, wv.Release, pct, lim)
				} else {
					verdict, failed = fmt.Sprintf("**FAIL: +%.1f%% > %g%%**", pct, lim), true
					if why != "" {
						notes = append(notes, fmt.Sprintf("- %s %s: %s", r.name, r.unit, why))
					}
				}
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
	if len(notes) > 0 {
		fmt.Fprintln(w, "\nAttribution (the code each failing benchmark runs per op, against the PR's changes):")
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
	}
	for _, wv := range cfg.Waivers {
		if o.release != "" && wv.Release != o.release && !o.noWaivers {
			fmt.Fprintf(w, "\nNote: waiver %s expired (added at %s; %s has been released): remove it from bench/perf.json.\n", wv.ID, wv.Release, o.release)
		}
	}
	if failed {
		fmt.Fprintln(w, "\nA regression beyond budget fails the PR. Fix it, or record why it is worth it in an ADR and set a budget for that benchmark in bench/perf.json. A waiver (ADR 017) applies only when none of the code the benchmark runs changed.")
	}
	if o.failures != "" {
		b := strings.Join(failing, "\n")
		if b != "" {
			b += "\n"
		}
		if err := os.WriteFile(o.failures, []byte(b), 0o644); err != nil {
			return failed, err
		}
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
