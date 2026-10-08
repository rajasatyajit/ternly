package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/e2ejudge"
)

// Levers are Phase C's quality levers (ADR 029). Each is a feature flag,
// off unless config ("levers") or TERNLY_LEVERS turns it on; a lever
// becomes a default only if it measurably helps on the task suite.
type Levers struct {
	// PlanFirst: the strongest model plans the task read-only (the
	// generalised /architect), then routing picks who implements it.
	PlanFirst bool
	// BestOf: when a task ends with its verification failing, rewind the
	// workspace and conversation and try again, up to BestOf attempts in
	// all (0 or 1: off). The tests or compiler are the judge; a failed
	// check is what marks the task as hard, so easy tasks never pay.
	BestOf int
	// NoVerifyEscalation: don't move to a stronger model after repeated
	// verification failure (the control arm for that lever).
	NoVerifyEscalation bool
}

// ParseLevers reads a comma-separated list: plan_first, best_of=N,
// no_verify_escalation; tool levers (outline_reads, no_schema_repair) are
// returned in tools for the caller to set on the registry. Unknown names
// are an error.
func ParseLevers(s string) (l Levers, tools []string, err error) {
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		name, val, _ := strings.Cut(f, "=")
		switch name {
		case "":
		case "plan_first":
			l.PlanFirst = true
		case "best_of":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > 5 {
				return l, nil, fmt.Errorf("best_of=%q: want 1..5", val)
			}
			l.BestOf = n
		case "no_verify_escalation":
			l.NoVerifyEscalation = true
		case "outline_reads", "no_schema_repair":
			tools = append(tools, name)
		default:
			return l, nil, fmt.Errorf("unknown lever %q (plan_first, best_of=N, no_verify_escalation, outline_reads, no_schema_repair)", name)
		}
	}
	return l, tools, nil
}

// PlanStep is one step of the plan a planning turn produced (the data
// behind ADR 021's Snapshot.Plan).
type PlanStep struct {
	Text  string
	State string // pending | done
}

// PlanSteps returns the current task's plan (nil: none).
func (a *Agent) PlanSteps() []PlanStep {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]PlanStep(nil), a.plan...)
}

// Verdict is the last turn's final verification verdict ("" when no check ran).
func (a *Agent) Verdict() string { a.mu.Lock(); defer a.mu.Unlock(); return a.verdict }

// RunTask runs one task with the levers that are on: a planning turn first
// (PlanFirst), and whole-task retries from a rewind while verification
// fails (BestOf). With no lever on it is Run.
func (a *Agent) RunTask(ctx context.Context, prompt string) {
	attempts := max(a.Levers.BestOf, 1)
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if !a.retryFromScratch(ctx, i, attempts) {
				return
			}
		}
		if a.Levers.PlanFirst {
			a.runPlanned(ctx, prompt)
		} else {
			a.Run(ctx, prompt)
		}
		if ctx.Err() != nil || a.Verdict() != VerdictFailed {
			return
		}
	}
}

// retryFromScratch rewinds the code and conversation to before this task's
// first turn (a planned task has two) and says so.
func (a *Agent) retryFromScratch(ctx context.Context, i, attempts int) bool {
	turns := a.Turns()
	back := 1
	if a.Levers.PlanFirst {
		back = 2
	}
	n := len(turns) - back + 1
	if n < 1 {
		return false
	}
	p, err := a.PlanRewind(ctx, n, RewindBoth)
	if err != nil {
		a.Emit(Event{Kind: EvStatus, Text: "can't retry from scratch: " + err.Error()})
		return false
	}
	if _, err := a.Rewind(ctx, p); err != nil {
		a.Emit(Event{Kind: EvStatus, Text: "can't retry from scratch: " + err.Error()})
		return false
	}
	a.count(func(s *Stats) { s.Retries++ })
	a.mu.Lock()
	a.plan = nil
	a.mu.Unlock()
	a.Emit(Event{Kind: EvStatus, Text: fmt.Sprintf("verification failed — starting the task again from scratch (attempt %d of %d)", i+1, attempts)})
	return true
}

const planShape = `{"plan": ["<step>", ...]}`

// runPlanned is the generalised /architect: the strongest tool-capable model
// plans in plan mode (read-only), then the plan is implemented by whatever
// routing picks (or the pinned model). The plan's steps become PlanSteps.
func (a *Agent) runPlanned(ctx context.Context, prompt string) {
	top := strongest(a.Router.Models())
	prevPin := a.Router.Pinned()
	if top != nil && prevPin == nil {
		_, _ = a.Router.Pin(top.Key())
	}
	mode := a.Reg.Policy.Mode()
	a.Reg.Policy.SetMode("plan")
	a.SetNextEffort("high") // the plan is where reasoning pays (ADR 015)
	a.count(func(s *Stats) { s.Plans++ })
	a.Run(ctx, "As the architect, plan this task for an implementer who will follow your plan exactly: name the files, functions and concrete edits, in order, and how to verify them. Read what you need; do not edit.\n\nTask: "+prompt+e2ejudge.Instruction(planShape))
	a.Reg.Policy.SetMode(mode)
	if prevPin == nil {
		_, _ = a.Router.Pin("auto")
	}
	if ctx.Err() != nil {
		return
	}
	_, answer, _, _ := a.LastTurn()
	var v struct {
		Plan []string `json:"plan"`
	}
	if err := e2ejudge.FinalJSON(answer, &v); err == nil {
		steps := make([]PlanStep, 0, len(v.Plan))
		for _, s := range v.Plan {
			if s = strings.TrimSpace(s); s != "" {
				steps = append(steps, PlanStep{Text: s, State: "pending"})
			}
		}
		a.mu.Lock()
		a.plan = steps
		a.mu.Unlock()
	}
	// Routed afresh: hysteresis (ADR 018) would otherwise keep the planner,
	// the most expensive model, for the implementation too.
	// With a plan to follow, implementing is one tier easier than the task
	// (the lever's premise); without this the implementation prompt itself
	// ("implement", "architect") would classify as the hardest tier.
	a.mu.Lock()
	if prevPin == nil {
		a.current = nil
	}
	a.nextDiff = max(1, discover.Classify(prompt, 0)-1)
	a.mu.Unlock()
	a.Run(ctx, "Implement the plan above exactly, as the implementer: make the edits, then make sure they build and the tests pass.\n\nThe task was: "+prompt)
	if a.Verdict() == VerdictVerified {
		a.mu.Lock()
		for i := range a.plan {
			a.plan[i].State = "done"
		}
		a.mu.Unlock()
	}
}

// strongest is the planner: the highest tier among tool-capable models, the
// cheapest of those (the TUI's /architect choice).
func strongest(ms []*discover.Model) *discover.Model {
	var best *discover.Model
	for _, x := range ms {
		if x.Tools && (best == nil || x.Tier > best.Tier || (x.Tier == best.Tier && x.Blended() < best.Blended())) {
			best = x
		}
	}
	return best
}
