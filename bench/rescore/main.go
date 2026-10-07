// Command rescore re-scores saved e2e reports (bench/results/*.json) with
// today's judges: the call-site checks automatically (e2ejudge.ProseCallSites,
// from each run's saved transcript), and every other check through the manual
// verdicts in bench/judge-audit.json. It prints what changed: runs, check
// verdicts against their thresholds, and report verdicts.
//
//	go run ./bench/rescore [-results bench/results] [-audit bench/judge-audit.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rajasatyajit/ternly/internal/e2ejudge"
)

type report struct {
	Model  string `json:"model"`
	OK     bool   `json:"ok"`
	Checks []struct {
		Name      string  `json:"name"`
		Threshold float64 `json:"threshold"`
		Runs      []struct {
			Err  string `json:"error"`
			Tail string `json:"transcript_tail"`
		} `json:"runs_detail"`
	} `json:"checks"`
}

type override struct {
	Report, Check string
	Run           int
	Pass          bool
	Why           string
}

// callSites are the call-site checks' expectations (e2e_live_checks_linux_test.go).
var callSites = map[string][2][]string{
	"graph-callsites-python": {{"app/run.py:6", "app/stop.py:5", "server/server.py:9", "server/server.py:10"}, {"app/run.py:5"}},
	"graph-callsites":        {{"cmd/run.go:7", "cmd/stop.go:6", "server/server.go:8", "server/server.go:11"}, {"cmd/run.go:6", "cmd/stop.go:10", "cmd/stop.go:11"}},
}

// answer is the model's final text: what follows the transcript's "done in" line.
func answer(tail string) (string, bool) {
	i := strings.LastIndex(tail, "\ndone in ")
	if i < 0 {
		return "", false
	}
	j := strings.Index(tail[i+1:], "\n")
	if j < 0 {
		return "", true
	}
	return tail[i+1+j+1:], true
}

func main() {
	dir := flag.String("results", "bench/results", "saved e2e reports")
	auditPath := flag.String("audit", "bench/judge-audit.json", "manual verdicts")
	flag.Parse()
	var audit struct{ Overrides []override }
	if b, err := os.ReadFile(*auditPath); err == nil {
		if err := json.Unmarshal(b, &audit); err != nil {
			fmt.Fprintln(os.Stderr, *auditPath+":", err)
			os.Exit(2)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(*dir, "2026*.json"))
	sort.Strings(paths)
	used := map[int]bool{}
	var changedRuns, flippedChecks, flippedReports int
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r report
		if json.Unmarshal(b, &r) != nil || len(r.Checks) == 0 {
			continue
		}
		name := filepath.Base(p)
		allOK, oldAllOK := true, true
		for _, c := range r.Checks {
			oldPass, newPass := 0, 0
			for i, run := range c.Runs {
				was := run.Err == ""
				is := was
				why := ""
				if exp, ok := callSites[c.Name]; ok && !strings.HasPrefix(run.Err, "timed out") && !strings.Contains(run.Err, "exited with") {
					if a, ok := answer(run.Tail); ok {
						m, x := e2ejudge.ProseCallSites(a, exp[0], exp[1])
						is = len(m)+len(x) == 0
						why = fmt.Sprintf("fixed call-site judge (missing %v, included %v)", m, x)
					}
				}
				for k, o := range audit.Overrides {
					if o.Report == name && o.Check == c.Name && o.Run == i {
						is, why, used[k] = o.Pass, "audit: "+o.Why, true
					}
				}
				if was {
					oldPass++
				}
				if is {
					newPass++
				}
				if was != is {
					changedRuns++
					fmt.Printf("run   %s %s #%d: %v → %v (%s)\n", name[:30], c.Name, i, verdict(was), verdict(is), why)
				}
			}
			n := float64(len(c.Runs))
			if n == 0 {
				continue
			}
			oldOK, newOK := float64(oldPass)/n >= c.Threshold, float64(newPass)/n >= c.Threshold
			if !newOK {
				allOK = false
			}
			if !oldOK {
				oldAllOK = false
			}
			if oldOK != newOK {
				flippedChecks++
				fmt.Printf("check %s %s: %d/%d → %d/%d (threshold %.2f): %v → %v\n", name[:30], c.Name, oldPass, len(c.Runs), newPass, len(c.Runs), c.Threshold, verdict(oldOK), verdict(newOK))
			}
		}
		// a partial run (TERNLY_E2E_ONLY, a cap, a skipped check) stays FAIL,
		// whatever the judges say: fewer checks than the manifest had, or a FAIL
		// with every check at threshold
		if !r.OK && (oldAllOK || len(r.Checks) < manifestSize(name)) {
			allOK = false
		}
		if allOK != r.OK {
			flippedReports++
			fmt.Printf("REPORT %s (%s): %v → %v\n", name, r.Model, okWord(r.OK), okWord(allOK))
		}
	}
	for k, o := range audit.Overrides {
		if !used[k] {
			fmt.Fprintf(os.Stderr, "audit entry not found: %s %s #%d\n", o.Report, o.Check, o.Run)
			os.Exit(1)
		}
	}
	fmt.Printf("\n%d runs changed, %d check verdicts flipped, %d report verdicts flipped (%d reports)\n", changedRuns, flippedChecks, flippedReports, len(paths))
}

// manifestSize is how many checks a full run had when the report was made:
// 16 until M7 added three (2026-10-05, between 07:52 and 14:23 UTC), 19 since.
func manifestSize(report string) int {
	if report < "20261005T140000Z" {
		return 16
	}
	return 19
}

func verdict(b bool) string {
	if b {
		return "pass"
	}
	return "fail"
}

func okWord(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}
