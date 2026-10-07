// Command evalaudit re-judges saved fabrication-eval answers
// (bench/results/fabrication/*.json) with today's trap judges, for the traps
// whose judge reads only the answer, and prints every disagreement and every
// outcome that isn't "checked", for reading.
//
//	go run ./bench/evalaudit [-dir bench/results/fabrication] [-all]
//	go run ./bench/evalaudit -apply bench/eval-audit.json   # rates, tier and trust, recorded vs corrected
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rajasatyajit/ternly/internal/eval"
)

// answerOnly are the traps whose judge needs nothing but the answer.
var answerOnly = map[string]bool{"nonexistent-method": true, "fake-cli-flag": true, "fake-npm-package": true, "missing-file": true,
	"citation": true, "name-matched-callers": true, "memory-wrong-value": true, "memory-wrong-function": true, "fake-test-flag": true}

func main() {
	dir := flag.String("dir", "bench/results/fabrication", "saved eval records")
	all := flag.Bool("all", false, "print every outcome, not only disagreements and non-checked ones")
	apply := flag.String("apply", "", "manual verdicts to apply (bench/eval-audit.json): print each model's summary, recorded and corrected")
	flag.Parse()
	if *apply != "" {
		if err := applyAudit(*dir, *apply); err != nil {
			fmt.Fprintln(os.Stderr, "evalaudit:", err)
			os.Exit(1)
		}
		return
	}
	judges := map[string]func(*eval.Result) (eval.Score, string){}
	for _, t := range eval.Traps {
		judges[t.Name] = t.Judge
	}
	paths, _ := filepath.Glob(filepath.Join(*dir, "*.json"))
	sort.Strings(paths)
	disagree := 0
	for _, p := range paths {
		var rec struct {
			Model    string `json:"model"`
			Version  string `json:"eval_version"`
			Outcomes []struct {
				Trap, Score, Why, Answer string
			} `json:"outcomes"`
		}
		b, _ := os.ReadFile(p)
		if json.Unmarshal(b, &rec) != nil {
			continue
		}
		for i, o := range rec.Outcomes {
			now, why := "", ""
			if answerOnly[o.Trap] && judges[o.Trap] != nil {
				s, w := judges[o.Trap](&eval.Result{Answer: o.Answer})
				now, why = string(s), w
			}
			differs := now != "" && now != o.Score
			if differs {
				disagree++
			}
			if *all || differs || o.Score != "checked" {
				a := strings.Join(strings.Fields(o.Answer), " ")
				fmt.Printf("%s v%s #%d %-22s recorded %-10s now %-10s %s\n    %.400s\n", filepath.Base(p), rec.Version, i, o.Trap, o.Score, orDash(now), why, a)
			}
		}
	}
	fmt.Printf("\n%d disagreements with today's judges\n", disagree)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type override struct {
	Model    string
	Outcome  int
	Trap     string
	From, To string
	Why      string
}

// applyAudit recomputes each model's record with the manual verdicts and
// prints recorded against corrected: rates, pass interval, tier, memory
// autonomy, and whether it counts as easily baited.
func applyAudit(dir, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var audit struct{ Overrides []override }
	if err := json.Unmarshal(b, &audit); err != nil {
		return err
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "ollama_*.json")) // the v2 records defaults.json came from
	sort.Strings(paths)
	used := map[int]bool{}
	for _, p := range paths {
		var rec struct {
			Model    string         `json:"model"`
			Runs     int            `json:"runs"`
			Outcomes []eval.Outcome `json:"outcomes"`
		}
		b, _ := os.ReadFile(p)
		if err := json.Unmarshal(b, &rec); err != nil {
			return fmt.Errorf("%s: %v", p, err)
		}
		fixed := append([]eval.Outcome(nil), rec.Outcomes...)
		for k, o := range audit.Overrides {
			if o.Model != rec.Model {
				continue
			}
			if o.Outcome >= len(fixed) || fixed[o.Outcome].Trap != o.Trap || string(fixed[o.Outcome].Score) != o.From {
				return fmt.Errorf("override %d (%s #%d %s) doesn't match the record", k, o.Model, o.Outcome, o.Trap)
			}
			fixed[o.Outcome].Score = eval.Score(o.To)
			used[k] = true
		}
		for _, x := range []struct {
			label string
			outs  []eval.Outcome
		}{{"recorded ", rec.Outcomes}, {"corrected", fixed}} {
			r := eval.Summarise(rec.Model, rec.Runs, x.outs)
			lo, hi := r.PassInterval()
			fmt.Printf("%-24s %s  fab %d/%d  mem %d/%d  bait %d/%d  pass %.2f [%.2f, %.2f]  tier T%d  memory %s  baitable %v\n",
				rec.Model, x.label, r.Fab.Bad, r.Fab.N, r.Mem.Bad, r.Mem.N, r.Inj.Bad, r.Inj.N, r.Pass, lo, hi, r.Tier(), r.Autonomy(), r.Baitable())
		}
	}
	for k, o := range audit.Overrides {
		if !used[k] {
			return fmt.Errorf("override %d (%s #%d) matched no record", k, o.Model, o.Outcome)
		}
	}
	return nil
}
