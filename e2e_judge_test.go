package main

import (
	"regexp"
	"strings"
	"testing"
)

// callsiteVerdict judges a call-site answer (the e2e checks graph-callsites
// and graph-callsites-python): every wanted file:line must appear, and a
// wrong one counts as included only on a line that doesn't say it was left
// out. Before 2026-10-07 any mention counted, so "app/run.py:5 is excluded
// (it calls Buffer.flush)" failed a correct answer; every "wrongly included"
// failure of the Python check from 2026-10-05 on was such an answer (ADR 018,
// "A judge bug").
func callsiteVerdict(answer string, want, wrong []string) (missing, extra []string) {
	for _, w := range want {
		if !strings.Contains(answer, w) {
			missing = append(missing, w)
		}
	}
	for _, w := range wrong {
		at := regexp.MustCompile(regexp.QuoteMeta(w) + `\b`)
		for _, line := range strings.Split(answer, "\n") {
			if at.MatchString(line) && !reLeftOut.MatchString(line) {
				extra = append(extra, w)
				break
			}
		}
	}
	return missing, extra
}

// reLeftOut: the line says the site was left out (not "Buffer" alone: a list
// line annotated "# Buffer" still includes it).
var reLeftOut = regexp.MustCompile(`(?i)\bexclud|\bomit|\bleft out\b|\bskip`)

// The answers below are verbatim from e2e reports (bench/results, 2026-10-05
// to 07): the first group were scored "wrongly included" though correct.
func TestCallsiteVerdict(t *testing.T) {
	want := []string{"app/run.py:6", "app/stop.py:5", "server/server.py:9", "server/server.py:10"}
	wrong := []string{"app/run.py:5"}
	list := "app/run.py:6\napp/stop.py:5\nserver/server.py:9\nserver/server.py:10\n"
	for _, note := range []string{
		"The 5th match (`app/run.py:5`) is a call to `Buffer.flush` and has been excluded.",
		"- **app/run.py:5** (`b.flush()`) is a `Buffer` call — excluded. Line 6 (`s.flush()`) is on the `Store`‑typed parameter `s`.",
		"(app/run.py:5 was excluded because it calls `Buffer.flush`, not `Store.flush`.)",
		"Note: `app/run.py:5` (`b.flush()`) was correctly excluded — that call targets `Buffer.flush`, not `Store.flush`.",
		"I excluded `app/run.py:5` because that calls `b.flush()` on a `Buffer` instance.",
		"Four total. The only other `.flush()` call in the repo is `app/run.py:5`, which calls `b.flush()` on a `Buffer` instance and was excluded per your request.",
		"Here are the **4 call sites** of `Store.flush` (excluding `Buffer.flush` on `app/run.py:5`):",
	} {
		if m, x := callsiteVerdict(list+"\n"+note, want, wrong); len(m)+len(x) > 0 {
			t.Errorf("a correct answer failed (missing %v, extra %v): %q", m, x, note)
		}
	}
	for _, bad := range []string{
		list + "app/run.py:5\n",
		list + "- app/run.py:5  `b.flush()`  # Buffer\n",
		"app/run.py:5, app/run.py:6, app/stop.py:5, server/server.py:9, server/server.py:10\n",
	} {
		if _, x := callsiteVerdict(bad, want, wrong); len(x) != 1 {
			t.Errorf("a wrong inclusion passed: %q", bad)
		}
	}
	if m, _ := callsiteVerdict("app/run.py:6\napp/stop.py:5\n", want, wrong); len(m) != 2 {
		t.Errorf("missing sites not reported: %v", m)
	}
}
