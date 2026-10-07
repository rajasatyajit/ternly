package e2ejudge

import (
	"testing"
)

// The answers below are verbatim from e2e reports (bench/results, 2026-10-05
// to 07): the first group were scored "wrongly included" though correct.
func TestProseCallSites(t *testing.T) {
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
		if m, x := ProseCallSites(list+"\n"+note, want, wrong); len(m)+len(x) > 0 {
			t.Errorf("a correct answer failed (missing %v, extra %v): %q", m, x, note)
		}
	}
	for _, bad := range []string{
		list + "app/run.py:5\n",
		list + "- app/run.py:5  `b.flush()`  # Buffer\n",
		"app/run.py:5, app/run.py:6, app/stop.py:5, server/server.py:9, server/server.py:10\n",
	} {
		if _, x := ProseCallSites(bad, want, wrong); len(x) != 1 {
			t.Errorf("a wrong inclusion passed: %q", bad)
		}
	}
	if m, _ := ProseCallSites("app/run.py:6\napp/stop.py:5\n", want, wrong); len(m) != 2 {
		t.Errorf("missing sites not reported: %v", m)
	}
}

func TestFinalJSON(t *testing.T) {
	var a struct {
		CallSites []string `json:"call_sites"`
	}
	reply := "Here they are.\n\n```json\n{\"call_sites\": [\"a.py:1\"]}\n```\n\nAnd the final:\n```json\n{\"call_sites\": [\"app/run.py:6\", \"./app/stop.py:5\"]}\n```\n"
	if err := FinalJSON(reply, &a); err != nil || len(a.CallSites) != 2 {
		t.Fatalf("the last block: %v %v", a, err)
	}
	if m, x := CallSites(a.CallSites, []string{"app/run.py:6", "app/stop.py:5"}, []string{"app/run.py:5"}); len(m)+len(x) > 0 {
		t.Fatalf("missing %v extra %v", m, x)
	}
	for _, bad := range []string{
		"app/run.py:6 and app/stop.py:5",                   // prose only
		"```json\n{\"call_sites\": \"app/run.py:6\"}\n```", // wrong type
		"```json\n{\"sites\": [\"app/run.py:6\"]}\n```",    // wrong field
		"```json\n{\"call_sites\": [\n```",                 // truncated
	} {
		var b struct {
			CallSites []string `json:"call_sites"`
		}
		if err := FinalJSON(bad, &b); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if m, x := CallSites([]string{"app/run.py:6", "app/run.py:5"}, []string{"app/run.py:6"}, []string{"app/run.py:5"}); len(m) != 0 || len(x) != 1 {
		t.Errorf("a listed wrong site passed: %v %v", m, x)
	}
}

func TestSameFile(t *testing.T) {
	for got, ok := range map[string]bool{"store/shared.go": true, "./store/shared.go": true, "pkg/store/shared.go": false, "shared.go": false} {
		if SameFile(got, "store/shared.go") != ok {
			t.Errorf("%q", got)
		}
	}
}
