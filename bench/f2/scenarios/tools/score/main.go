// score computes each F2 scenario cell's outcome from its saved recording and
// fixture: score <runtime-dir R> <box.sh> → scores.tsv on stdout. S2's tests
// run model-written code, so they run inside box.sh's boundary. Outcomes are judged
// only on the harness's output: lines that contain the scenario's prompt are
// removed first (the first batch's S1 check matched the prompt itself, which
// contains "eviction" and "simplelru/lru.go", and passed a Codex run that was
// never submitted). S2 re-runs the fixture's tests.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var prompts = map[string]string{
	"S1": "Explain what simplelru/lru.go does",
	"S2": "Bug report: in package simplelru",
	"S3": "Plan, then implement in src/index.ts",
	"S4": "Review the uncommitted change",
	"S5": "name one Go standard library package",
}

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }

// output: the recording without any line that echoes a prompt (S5's turn
// prompts share a prefix, so every "Turn n:" line goes too).
func output(text, sc string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, prompts[sc]) || strings.Contains(l, "Turn ") && strings.Contains(l, ":") && sc == "S5" {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

var (
	reS1    = regexp.MustCompile(`(?i)least[ -]recently|removeOldest|MoveToFront|evictList|oldest (entry|item|element)|back of the list|recency`)
	rePeek  = regexp.MustCompile(`(?i)peek`)
	reRecen = regexp.MustCompile(`(?i)MoveToFront|recency|recently|move.{0,20}front`)
	reResz  = regexp.MustCompile(`(?i)resize`)
	reOff   = regexp.MustCompile(`(?i)<=|off[ -]by[ -]one|one too many|extra (entry|eviction|item)|evicts? one more`)
	reErr   = regexp.MustCompile(`(?i)error|fail|refused|connect|unavailable|unreachable|timed? ?out|retry`)
	reOnce  = regexp.MustCompile(`once\s*[(<:]`)
	reCount = regexp.MustCompile(`(?s)emit[^{]*\{.*return\s+\w`)
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: score <R> <box.sh>")
		os.Exit(2)
	}
	R, box := os.Args[1], os.Args[2]
	cells, _ := filepath.Glob(filepath.Join(R, "out", "*", "S[1-5]"))
	sort.Strings(cells)
	fmt.Println("harness\tscenario\toutcome\tdetail\twall")
	for _, c := range cells {
		sc := filepath.Base(c)
		hn := filepath.Base(filepath.Dir(c))
		res := read(filepath.Join(c, "result.txt"))
		wall := ""
		if m := regexp.MustCompile(`wall=(\d+s)`).FindStringSubmatch(res); m != nil {
			wall = m[1]
		}
		for _, terminal := range []string{"timeout", "not-ready", "launch-failed", "prep-failed", "harness-error"} {
			if strings.Contains(res, "outcome="+terminal) {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", hn, sc, terminal, strings.TrimSpace(res), wall)
				goto next
			}
		}
		{
			out := output(read(filepath.Join(c, "scrollback.txt")), sc)
			fx := filepath.Join(R, "fx", hn+"-"+sc)
			diff := strings.TrimSpace(read(filepath.Join(c, "diffstat.txt")))
			var outcome, detail string
			switch sc {
			case "S1":
				outcome = map[bool]string{true: "answered", false: "no-answer"}[reS1.MatchString(out)]
			case "S2":
				home := filepath.Join(R, "homes", hn+"-"+sc)
				_ = os.MkdirAll(home, 0o755)
				t := exec.Command(box, fx, home, "--", "env", "HOME="+home, "GOCACHE="+home+"/.cache/go-build",
					"GOMODCACHE="+home+"/go/pkg/mod", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "go", "test", "-count=1", "./simplelru/")
				t.Env = append(os.Environ(), "R="+R)
				err := t.Run()
				outcome = map[bool]string{true: "pass", false: "fail"}[err == nil]
				detail = "changed=" + map[bool]string{true: "y", false: "n"}[diff != ""]
			case "S3":
				src := read(filepath.Join(fx, "src", "index.ts"))
				a, b := reOnce.MatchString(src), reCount.MatchString(src)
				outcome = map[bool]string{true: "both", false: "partial"}[a && b]
				if !a && !b {
					outcome = "none"
				}
				detail = fmt.Sprintf("once=%v count=%v changed=%v", a, b, diff != "")
			case "S4":
				a := rePeek.MatchString(out) && reRecen.MatchString(out)
				b := reResz.MatchString(out) && reOff.MatchString(out)
				outcome = fmt.Sprintf("%d/2", map[bool]int{true: 1}[a]+map[bool]int{true: 1}[b])
				detail = fmt.Sprintf("peek_bug=%v resize_bug=%v", a, b)
			case "S5":
				tm := read(filepath.Join(c, "timings.txt"))
				turns := len(regexp.MustCompile(`(?m)^t\d+ idle`).FindAllString(tm, -1))
				errShown := reErr.MatchString(output(read(filepath.Join(c, "error-screen.txt")), sc))
				rec := strings.Contains(tm, "recover idle")
				rss := regexp.MustCompile(`rss_kb (\d+)`).FindStringSubmatch(tm)
				outcome = fmt.Sprintf("turns=%d/30", turns)
				detail = fmt.Sprintf("error_shown=%v recovered=%v", errShown, rec)
				if rss != nil {
					detail += " rss_kb=" + rss[1]
				}
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\n", hn, sc, outcome, detail, wall)
		}
	next:
	}
}
