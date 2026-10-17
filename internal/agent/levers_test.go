package agent

import (
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/testutil"
)

func TestParseLevers(t *testing.T) {
	l, tools, err := ParseLevers("plan_first, best_of=2,no_verify_escalation,outline_reads,no_schema_repair")
	if err != nil || !l.PlanFirst || l.BestOf != 2 || !l.NoVerifyEscalation || strings.Join(tools, ",") != "outline_reads,no_schema_repair" {
		t.Fatalf("%+v %v %v", l, tools, err)
	}
	if l, _, err := ParseLevers(""); err != nil || l != (Levers{}) {
		t.Fatalf("empty: %+v %v", l, err)
	}
	for _, bad := range []string{"best_of=0", "best_of=x", "best_of=9", "turbo"} {
		if _, _, err := ParseLevers(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// withCheckpoints gives a test agent a checkpoint store, as a real session has.
func withCheckpoints(t *testing.T, a *Agent) {
	t.Helper()
	testutil.Require(t, "git", testutil.Have("git"))
	repo, err := checkpoint.OpenRepo(a.Reg.Root, t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	a.CP, _ = repo.Session("t")
}

// BestOf: a task that ends with its check failing is rewound (code and
// conversation) and tried again; the retry that passes is kept.
func TestBestOfRetriesFromScratch(t *testing.T) {
	f := newFake(t,
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"bad\n"}`)}}, reply{text: "done"},
		reply{text: "I can't make it pass"},
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"good\n"}`)}}, reply{text: "done"},
	)
	a, _ := newAgent(t, "yolo", model(f.URL, "m", 3, 1, 5))
	withCheckpoints(t, a)
	a.SetVerify("grep -q good a.txt")
	a.Levers.BestOf = 2
	a.RunTask(bg, "make a.txt say good")
	if got := read(a, "a.txt"); got != "good\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if s := a.Stats(); s.Retries != 1 {
		t.Fatalf("retries = %d, want 1", s.Retries)
	}
	if v := a.Verdict(); v != VerdictVerified {
		t.Fatalf("verdict = %q", v)
	}
	if n := len(a.Turns()); n != 1 {
		t.Fatalf("turns = %d, want 1 (the failed attempt was rewound)", n)
	}
	for _, m := range f.requests()[3] { // the retry starts from scratch: no trace of the first attempt
		if s, _ := m["content"].(string); strings.Contains(s, "can't make it pass") {
			t.Fatal("the retry's context still holds the failed attempt")
		}
	}
}

// Off (and on a first-try pass) nothing is retried.
func TestBestOfOffOrPassing(t *testing.T) {
	f := newFake(t,
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"bad\n"}`)}}, reply{text: "done"},
		reply{text: "I can't make it pass"},
	)
	a, _ := newAgent(t, "yolo", model(f.URL, "m", 3, 1, 5))
	withCheckpoints(t, a)
	a.SetVerify("grep -q good a.txt")
	a.RunTask(bg, "make a.txt say good")
	if s := a.Stats(); s.Retries != 0 || read(a, "a.txt") != "bad\n" {
		t.Fatalf("lever off retried: %+v", s)
	}
	g := newFake(t, reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"good\n"}`)}}, reply{text: "done"})
	b, _ := newAgent(t, "yolo", model(g.URL, "m", 3, 1, 5))
	withCheckpoints(t, b)
	b.SetVerify("grep -q good a.txt")
	b.Levers.BestOf = 3
	b.RunTask(bg, "make a.txt say good")
	if s := b.Stats(); s.Retries != 0 || len(g.requests()) != 2 {
		t.Fatalf("a passing task was retried: %+v, %d requests", s, len(g.requests()))
	}
}

// PlanFirst: the strongest model plans read-only, then routing picks the
// implementer (the cheaper capable model); the plan's steps are kept.
func TestPlanFirst(t *testing.T) {
	f := newFake(t,
		reply{calls: [][2]string{call("write_file", `{"path":"x.txt","content":"no"}`)}}, // the planner tries to edit: refused in plan mode
		reply{text: "Plan.\n```json\n{\"plan\": [\"write a.txt\", \"check it\"]}\n```"},
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"good\n"}`)}}, reply{text: "done"},
	)
	strong, cheap := model(f.URL, "strong", 3, 10, 50), model(f.URL, "cheap", 3, 0.1, 0.5)
	strong.Tier, cheap.Tier = 3, 2
	a, rec := newAgent(t, "edits", strong, cheap)
	a.SetVerify("grep -q good a.txt")
	a.Levers.PlanFirst = true
	a.RunTask(bg, "make a.txt say good")
	if exists(a, "x.txt") {
		t.Fatal("the planner edited a file")
	}
	if read(a, "a.txt") != "good\n" {
		t.Fatal("the plan wasn't implemented")
	}
	var used []string
	for _, e := range rec.events {
		if e.Kind == EvModel {
			used = append(used, e.Model.ID)
		}
	}
	if len(used) < 2 || used[0] != "strong" || used[len(used)-1] != "cheap" {
		t.Fatalf("models used %v: want the strong planner, then the cheap implementer", used)
	}
	steps := a.PlanSteps()
	if len(steps) != 2 || steps[0].Text != "write a.txt" || steps[1].State != "done" {
		t.Fatalf("plan steps %+v", steps)
	}
	if a.Router.Pinned() != nil || a.Reg.Policy.Mode() != "edits" {
		t.Fatalf("pin %v / mode %q not restored", a.Router.Pinned(), a.Reg.Policy.Mode())
	}
	if s := a.Stats(); s.Plans != 1 {
		t.Fatalf("plans = %d", s.Plans)
	}
}

// NoVerifyEscalation is the control arm: repeated verification failure stays on the model.
func TestNoVerifyEscalation(t *testing.T) {
	f := newFake(t,
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"bad\n"}`)}}, reply{text: "done"},
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"bad2\n"}`)}}, reply{text: "done"},
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"bad3\n"}`)}}, reply{text: "done"},
		reply{text: "giving up"},
	)
	cheap, strong := model(f.URL, "cheap", 2, 0.1, 0.5), model(f.URL, "strong", 3, 10, 50)
	a, rec := newAgent(t, "yolo", cheap, strong)
	a.SetVerify("grep -q good a.txt")
	a.Levers.NoVerifyEscalation = true
	a.Run(bg, "make a.txt say good")
	for _, e := range rec.events {
		if e.Kind == EvModel && e.Model.ID == "strong" {
			t.Fatal("escalated with NoVerifyEscalation on")
		}
	}
}

// Under routing v2 the implementer is routed afresh: with the cheap model
// scoring better than the planner but within hysteresis' keep factor
// (ADR 018), keeping the planner as the turn's model would keep it.
func TestPlanFirstV2RoutesImplementerAfresh(t *testing.T) {
	f := newFake(t,
		reply{text: "Plan.\n```json\n{\"plan\": [\"write a.txt\"]}\n```"},
		reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"good\n"}`)}}, reply{text: "done"},
	)
	strong, cheap := model(f.URL, "strong", 3, 14, 14), model(f.URL, "cheap", 2, 0.5, 0.5)
	a, rec, _ := v2Agent(t, []*discover.Model{strong, cheap}, strong, cheap) // both measured: only price and tier differ
	a.SetVerify("grep -q good a.txt")
	a.Levers.PlanFirst = true
	a.RunTask(bg, "make a.txt say good")
	var used []string
	for _, e := range rec.events {
		if e.Kind == EvModel {
			used = append(used, e.Model.ID+" ("+e.Reason+")")
		}
	}
	if len(used) < 2 || !strings.HasPrefix(used[0], "strong") || !strings.HasPrefix(used[len(used)-1], "cheap") {
		t.Fatalf("models used %q: want strong (plan), then cheap (implement)", used)
	}
}
