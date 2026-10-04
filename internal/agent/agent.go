// Package agent runs the plan→act→verify loop. Cost control lives here:
// difficulty-based routing, provider failover, cascade escalation only on
// failure, context compaction with the cheapest model, and a budget cap.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

type EventKind int

const (
	EvText EventKind = iota
	EvModel
	EvToolStart
	EvToolEnd
	EvStatus
	EvUsage
	EvVerify
	EvDone
	EvError
)

type Event struct {
	Kind    EventKind
	Text    string
	Model   *discover.Model
	Reason  string
	ToolID  string
	Tool    string
	OK      bool
	Elapsed time.Duration
	Ledger  Ledger
}

type Ledger struct {
	Usage llm.Usage
	Cost  float64
	Turns int
}

func (l Ledger) CacheRate() float64 {
	tot := l.Usage.In + l.Usage.CacheRead + l.Usage.CacheWrite
	if tot == 0 {
		return 0
	}
	return float64(l.Usage.CacheRead) / float64(tot)
}

type Agent struct {
	Reg      *tools.Registry
	Router   *discover.Router
	Emit     func(Event)
	Verify   string // command run after edits; "" disables
	Budget   float64
	MaxSteps int

	mu      sync.Mutex
	history []llm.Message
	system  string
	ledger  Ledger
	current *discover.Model
}

func New(reg *tools.Registry, r *discover.Router, emit func(Event)) *Agent {
	a := &Agent{Reg: reg, Router: r, Emit: emit, MaxSteps: 60}
	a.system = systemPrompt(reg.Root)
	return a
}

func (a *Agent) Ledger() Ledger { a.mu.Lock(); defer a.mu.Unlock(); return a.ledger }
func (a *Agent) Reset()         { a.mu.Lock(); a.history = nil; a.mu.Unlock() }
func (a *Agent) Current() *discover.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// estTokens: ~3.6 chars/token is close enough for budgeting decisions.
func estTokens(sys string, msgs []llm.Message) int {
	n := len(sys)
	for _, m := range msgs {
		n += len(m.Content) + 16
		for _, tc := range m.ToolCalls {
			n += len(tc.Args) + len(tc.Name)
		}
	}
	return n * 10 / 36
}

// Run handles one user turn end-to-end.
func (a *Agent) Run(ctx context.Context, prompt string) {
	defer func() { a.Emit(Event{Kind: EvDone, Ledger: a.Ledger()}) }()
	select {
	case <-a.Router.Ready():
	case <-ctx.Done():
		return
	}
	a.mu.Lock()
	a.history = append(a.history, llm.Message{Role: "user", Content: prompt})
	a.ledger.Turns++
	a.mu.Unlock()

	failures := 0
	diff := discover.Classify(prompt, 0)
	need := estTokens(a.system, a.history) + 16000
	model, reason := a.Router.Pick(diff, need)
	if model == nil {
		a.Emit(Event{Kind: EvError, Text: "No usable model found. Set an API key (e.g. ANTHROPIC_API_KEY, OPENROUTER_API_KEY, GEMINI_API_KEY) or start Ollama / LM Studio, then /refresh."})
		return
	}
	a.setModel(model, reason)

	edited := false
	verifyTries := 0
	for step := 0; step < a.MaxSteps; step++ {
		if ctx.Err() != nil {
			return
		}
		if a.Budget > 0 && a.Ledger().Cost >= a.Budget {
			a.Emit(Event{Kind: EvError, Text: fmt.Sprintf("Budget of $%.2f reached — stopping. Raise with --budget or /budget.", a.Budget)})
			return
		}
		a.maybeCompact(ctx, model)

		msg, calls, err := a.step(ctx, model)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if alt, ok := a.Router.Failover(model, need); ok && llm.IsRetryable(err) {
				a.Emit(Event{Kind: EvStatus, Text: fmt.Sprintf("%s failed (%v) — failing over", model.Key(), short(err))})
				model = alt
				a.setModel(model, "failover")
				continue
			}
			a.Emit(Event{Kind: EvError, Text: err.Error()})
			return
		}
		a.mu.Lock()
		a.history = append(a.history, msg)
		a.mu.Unlock()

		if len(calls) == 0 {
			// Model thinks it's done. If it changed code, prove it.
			if !edited || a.Verify == "" {
				return
			}
			out, ok := a.verify(ctx)
			edited = false
			if ok {
				return
			}
			verifyTries++
			if verifyTries > 3 {
				a.Emit(Event{Kind: EvError, Text: "verification still failing after 3 fix attempts — stopping so you can take a look"})
				return
			}
			failures++
			if failures >= 2 { // cascade: only pay for a stronger model when the cheap one demonstrably failed
				if up, ok := a.Router.Escalate(model, need); ok {
					model = up
					a.setModel(model, "escalated after repeated verification failure")
				}
			}
			a.mu.Lock()
			a.history = append(a.history, llm.Message{Role: "user", Content: "Automatic verification (`" + a.Verify + "`) failed:\n```\n" + out + "\n```\nFix the root cause, then stop."})
			a.mu.Unlock()
			continue
		}
		results := a.runTools(ctx, calls)
		for i, tc := range calls {
			if t := a.Reg.Get(tc.Name); t != nil && t.Kind == tools.Edit && !results[i].isErr {
				edited = true
			}
		}
		a.mu.Lock()
		for i, tc := range calls {
			a.history = append(a.history, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: results[i].out})
		}
		a.mu.Unlock()
	}
	a.Emit(Event{Kind: EvError, Text: fmt.Sprintf("stopped after %d steps", a.MaxSteps)})
}

func (a *Agent) setModel(m *discover.Model, reason string) {
	a.mu.Lock()
	a.current = m
	a.mu.Unlock()
	a.Emit(Event{Kind: EvModel, Model: m, Reason: reason})
}

func (a *Agent) step(ctx context.Context, m *discover.Model) (llm.Message, []llm.ToolCall, error) {
	a.mu.Lock()
	req := llm.Request{Model: m.ID, System: a.system, Messages: append([]llm.Message(nil), a.history...), Tools: a.Reg.Specs()}
	a.mu.Unlock()
	cl := llm.New(m.Provider.Endpoint())
	var text strings.Builder
	var calls []llm.ToolCall
	for ev := range cl.Stream(ctx, req) {
		switch ev.Kind {
		case llm.EvText:
			text.WriteString(ev.Text)
			a.Emit(Event{Kind: EvText, Text: ev.Text})
		case llm.EvToolCall:
			calls = append(calls, ev.Call)
		case llm.EvUsage:
			a.account(m, ev.Usage)
		case llm.EvError:
			return llm.Message{}, nil, ev.Err
		}
	}
	return llm.Message{Role: "assistant", Content: text.String(), ToolCalls: calls}, calls, nil
}

func (a *Agent) account(m *discover.Model, u llm.Usage) {
	a.mu.Lock()
	a.ledger.Usage.Add(u)
	a.ledger.Cost += m.Cost(u)
	l := a.ledger
	a.mu.Unlock()
	a.Emit(Event{Kind: EvUsage, Ledger: l})
}

type result struct {
	out   string
	isErr bool
}

// runTools executes read-only calls concurrently, everything else in order.
func (a *Agent) runTools(ctx context.Context, calls []llm.ToolCall) []result {
	res := make([]result, len(calls))
	run := func(i int) {
		tc := calls[i]
		summary := tc.Name
		if t := a.Reg.Get(tc.Name); t != nil {
			summary = t.Summary([]byte(tc.Args))
		}
		a.Emit(Event{Kind: EvToolStart, ToolID: tc.ID, Tool: tc.Name, Text: summary})
		t0 := time.Now()
		out, isErr := a.Reg.Call(ctx, tc)
		res[i] = result{out, isErr}
		a.Emit(Event{Kind: EvToolEnd, ToolID: tc.ID, Tool: tc.Name, OK: !isErr, Text: out, Elapsed: time.Since(t0)})
	}
	i := 0
	for i < len(calls) {
		j := i
		for j < len(calls) {
			t := a.Reg.Get(calls[j].Name)
			if t == nil || t.Kind != tools.ReadOnly {
				break
			}
			j++
		}
		if j > i { // a run of read-only calls → parallel
			var wg sync.WaitGroup
			for k := i; k < j; k++ {
				wg.Add(1)
				go func(k int) { defer wg.Done(); run(k) }(k)
			}
			wg.Wait()
			i = j
			continue
		}
		run(i)
		i++
	}
	return res
}

func (a *Agent) verify(ctx context.Context) (string, bool) {
	a.Emit(Event{Kind: EvVerify, Text: a.Verify})
	t0 := time.Now()
	out, code, err := a.Reg.Sandbox.Run(ctx, a.Reg.Root, a.Verify, 600)
	ok := err == nil && code == 0
	a.Emit(Event{Kind: EvToolEnd, ToolID: "verify", Tool: "verify", OK: ok, Text: out, Elapsed: time.Since(t0)})
	if err != nil {
		out += "\n" + err.Error()
	}
	return tools.Cap(a.Reg.Redact.Apply(out), 6000), ok
}

// maybeCompact summarises old turns with the cheapest model once the context
// passes ~55% of the window (or 120k tokens), keeping the recent tail verbatim.
func (a *Agent) maybeCompact(ctx context.Context, m *discover.Model) {
	a.mu.Lock()
	est := estTokens(a.system, a.history)
	limit := min(m.Ctx*55/100, 120_000)
	a.mu.Unlock()
	if est < limit {
		return
	}
	_ = a.Compact(ctx)
}

func (a *Agent) Compact(ctx context.Context) error {
	a.mu.Lock()
	h := append([]llm.Message(nil), a.history...)
	a.mu.Unlock()
	// cut on a user-message boundary so tool_call/result pairs stay intact
	cut := -1
	users := 0
	for i := len(h) - 1; i > 0; i-- {
		if h[i].Role == "user" {
			if users++; users == 2 {
				cut = i
				break
			}
		}
	}
	if cut <= 0 {
		return errors.New("nothing to compact yet")
	}
	um := a.Router.Utility(estTokens("", h[:cut]) + 4000)
	if um == nil {
		return errors.New("no model available for compaction")
	}
	a.Emit(Event{Kind: EvStatus, Text: "compacting context with " + um.Key()})
	var sb strings.Builder
	for _, m := range h[:cut] {
		fmt.Fprintf(&sb, "## %s\n%s\n", m.Role, tools.Cap(m.Content, 4000))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&sb, "[tool %s %s]\n", tc.Name, tools.Cap(tc.Args, 400))
		}
	}
	req := llm.Request{Model: um.ID, System: "You compress coding-session transcripts. Output a dense summary: user goals, decisions & rationale, files read/changed (paths), commands run & outcomes, current state, unresolved issues, next steps. No preamble.",
		Messages: []llm.Message{{Role: "user", Content: sb.String()}}, MaxTokens: 2000}
	sum, u, err := llm.Collect(llm.New(um.Provider.Endpoint()).Stream(ctx, req))
	a.account(um, u)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.history = append([]llm.Message{{Role: "user", Content: "[Summary of earlier conversation]\n" + sum}}, a.history[cut:]...)
	a.mu.Unlock()
	a.Emit(Event{Kind: EvStatus, Text: fmt.Sprintf("context compacted: ~%dk → ~%dk tokens", estTokens("", h)/1000, estTokens("", a.history)/1000)})
	return nil
}

// ─────────────────────────── prompt & project detection ───────────────────────────

func systemPrompt(root string) string {
	var sb strings.Builder
	sb.WriteString(`You are ternly, an expert software engineer working in the user's repository via tools.

Engineering standards (non-negotiable):
- Understand before changing: locate relevant code with grep/glob, read only the parts you need (use offset/limit).
- Make minimal, focused diffs with edit_file; match the project's existing style, naming and patterns.
- Handle errors explicitly, validate inputs, avoid global state, keep functions small; no dead code or TODO stubs.
- Never weaken security (no disabled TLS checks, no secrets in code, no shell injection, no unsafe deserialization).
- Add or update tests for behaviour you change when the project has tests.
- After changing code, build/test it with bash. Do not claim success you have not verified.
- Be token-efficient: no restating files, no long explanations unless asked. Finish with a 1–3 line summary of what changed.
- If a request is ambiguous or risky, ask one concise question instead of guessing.
`)
	fmt.Fprintf(&sb, "\nEnvironment: %s/%s, workspace %s, date %s.\n", runtime.GOOS, runtime.GOARCH, root, time.Now().Format("2006-01-02"))
	if br, err := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		fmt.Fprintf(&sb, "Git branch: %s\n", strings.TrimSpace(string(br)))
	}
	if v := DetectVerify(root); v != "" {
		fmt.Fprintf(&sb, "Project check command: %s\n", v)
	}
	for _, f := range []string{"TERNLY.md", "AGENTS.md", "CLAUDE.md", ".cursorrules"} { // tool-specific file wins
		if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			fmt.Fprintf(&sb, "\n# Project instructions (%s)\n%s\n", f, tools.Cap(string(b), 8000))
			break
		}
	}
	return sb.String()
}

// DetectVerify picks a fast, side-effect-free check for the project type.
func DetectVerify(root string) string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(root, f)); return err == nil }
	switch {
	case has("go.mod"):
		return "go build ./... && go vet ./..."
	case has("Cargo.toml"):
		return "cargo check --quiet --all-targets"
	case has("package.json"):
		b, _ := os.ReadFile(filepath.Join(root, "package.json"))
		for _, s := range []string{"typecheck", "lint", "build"} {
			if strings.Contains(string(b), `"`+s+`"`) {
				return "npm run -s " + s
			}
		}
		if has("tsconfig.json") {
			return "npx --no-install tsc --noEmit"
		}
	case has("pyproject.toml") || has("setup.py"):
		if _, err := exec.LookPath("ruff"); err == nil {
			return "ruff check . && python3 -m compileall -q ."
		}
		return "python3 -m compileall -q ."
	}
	return ""
}

func short(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
