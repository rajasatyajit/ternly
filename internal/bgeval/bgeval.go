// Package bgeval evaluates newly discovered models in the background, under
// caps, so routing can use measured capability instead of a name (ADR 018
// §4 and its review, decision 3):
//   - only the bundled fabrication eval runs (ternly --eval: synthetic trap
//     workspaces), never the user's repositories;
//   - Ollama Cloud quota and local models; a pay-per-token model only with an
//     explicit weekly budget (default $0: never);
//   - at most PerWeek evaluations in any 7 days, each capped at PerModel;
//   - only while no turn runs: a turn starting stops the evaluation, which
//     is retried later without counting against the caps;
//   - an off switch, and every model's state shown in /models.
package bgeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// Caps bound background evaluations.
type Caps struct {
	Enabled   bool
	PerModel  time.Duration // wall time for one model's evaluation
	PerWeek   int           // evaluations started in any 7 days
	PaidUSD   float64       // per week for pay-per-token models (0: never evaluated)
	LocalOnly bool          // --local-only: nothing leaves the machine
}

// DefaultCaps: one evaluation of the 14 traps takes 5–14 minutes (ADR 017:
// qwen3.6 locally, 13 min); three a week covers the models a provider
// typically adds.
var DefaultCaps = Caps{Enabled: true, PerModel: 20 * time.Minute, PerWeek: 3}

// Outcomes recorded in the ledger. Yielded runs (a turn started) don't count.
const (
	Measured = "measured"
	Failed   = "failed"
	Timeout  = "timed out"
	Yielded  = "yielded to a turn"
)

// Entry is one evaluation attempt.
type Entry struct {
	Model     string    `json:"model"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	Outcome   string    `json:"outcome"`
	BudgetUSD float64   `json:"budget_usd,omitempty"`
}

const ledgerVersion = 1

// Ledger is the record of attempts (<data>/background-eval.json, 0600).
type Ledger struct {
	mu      sync.Mutex
	path    string
	Entries []Entry
}

// OpenLedger reads the ledger; a missing, unreadable or other-version file
// starts empty (the worst case is one extra evaluation).
func OpenLedger(path string) *Ledger {
	l := &Ledger{path: path}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Version int     `json:"version"`
			Entries []Entry `json:"entries"`
		}
		if json.Unmarshal(b, &f) == nil && f.Version == ledgerVersion {
			l.Entries = f.Entries
		}
	}
	return l
}

func (l *Ledger) add(e Entry) error {
	l.mu.Lock()
	l.Entries = append(l.Entries, e)
	b, err := json.MarshalIndent(map[string]any{"version": ledgerVersion, "entries": l.Entries}, "", "  ")
	l.mu.Unlock()
	if err != nil || l.path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *Ledger) snapshot() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry(nil), l.Entries...)
}

// Scheduler runs evaluations at idle.
type Scheduler struct {
	Caps   Caps
	Ledger *Ledger
	Models func() []*discover.Model // the current model list (best candidates first)
	Busy   func() bool              // a turn is running
	// Run evaluates one model within ctx (main: `ternly --eval` as a child
	// process). budgetUSD > 0 only for a pay-per-token model.
	Run  func(ctx context.Context, m *discover.Model, budgetUSD float64) error
	Done func() // after a measurement: re-read capabilities, re-rank
	Now  func() time.Time
	Poll time.Duration // how often to look for work (default 30 s)

	mu      sync.Mutex
	running string // model key being evaluated
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// weekly returns the evaluations that count against the weekly cap, and the
// pay-per-token budget spent, in the 7 days before now.
func (s *Scheduler) weekly() (n int, paid float64) {
	since := s.now().Add(-7 * 24 * time.Hour)
	for _, e := range s.Ledger.snapshot() {
		if e.Outcome != Yielded && e.Started.After(since) {
			n++
			paid += e.BudgetUSD
		}
	}
	return n, paid
}

// last is the model's most recent attempt that wasn't yielded.
func (s *Scheduler) last(key string) *Entry {
	var out *Entry
	for _, e := range s.Ledger.snapshot() {
		if e.Model == key && e.Outcome != Yielded {
			out = &e
		}
	}
	return out
}

// Status is what /models shows for a model ("" when nothing applies).
func (s *Scheduler) Status(m *discover.Model) string {
	if s == nil || m.Measure != nil || !m.Tools {
		return ""
	}
	s.mu.Lock()
	running := s.running == m.Key()
	s.mu.Unlock()
	if running {
		return "evaluating now (bundled traps)"
	}
	_, why := s.eligible(m)
	return why
}

// eligible says whether m may be evaluated now, and why not.
func (s *Scheduler) eligible(m *discover.Model) (bool, string) {
	switch {
	case !s.Caps.Enabled:
		return false, "background evaluation off (routing.background_eval.enabled)"
	case m.Measure != nil || !m.Tools || m.NoFit:
		return false, ""
	case m.Cloud && s.Caps.LocalOnly:
		return false, "not evaluated: --local-only"
	}
	if e := s.last(m.Key()); e != nil {
		return false, fmt.Sprintf("evaluation %s %s", e.Outcome, e.Finished.Format("2006-01-02"))
	}
	n, paid := s.weekly()
	budget := 0.0
	if !m.Local() && !m.Cloud { // pay per token
		if s.Caps.PaidUSD <= 0 {
			return false, "not evaluated: a paid API (routing.background_eval.paid_usd_per_week is 0)"
		}
		if budget = s.Caps.PaidUSD - paid; budget <= 0 {
			return false, "queued: this week's paid evaluation budget is spent"
		}
	}
	if n >= s.Caps.PerWeek {
		return false, fmt.Sprintf("queued: %d evaluations this week (the cap)", s.Caps.PerWeek)
	}
	return true, "queued for evaluation when idle"
}

// next is the first eligible model.
func (s *Scheduler) next() *discover.Model {
	for _, m := range s.Models() {
		if ok, _ := s.eligible(m); ok {
			return m
		}
	}
	return nil
}

// Loop looks for work every Poll until ctx ends.
func (s *Scheduler) Loop(ctx context.Context) {
	poll := s.Poll
	if poll == 0 {
		poll = 30 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Step(ctx)
		}
	}
}

// Step runs at most one evaluation, if the session is idle and a model
// qualifies. It returns the outcome ("" when nothing ran).
func (s *Scheduler) Step(ctx context.Context) string {
	if !s.Caps.Enabled || s.Busy() {
		return ""
	}
	m := s.next()
	if m == nil {
		return ""
	}
	budget := 0.0
	if !m.Local() && !m.Cloud {
		_, paid := s.weekly()
		budget = s.Caps.PaidUSD - paid
	}
	s.mu.Lock()
	s.running = m.Key()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = ""; s.mu.Unlock() }()

	ectx, cancel := context.WithTimeout(ctx, s.Caps.PerModel)
	defer cancel()
	var yielded bool
	var once sync.Once
	stop := make(chan struct{})
	go func() { // a turn starting stops the evaluation
		tk := time.NewTicker(200 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				if s.Busy() {
					once.Do(func() { yielded = true; cancel() })
					return
				}
			}
		}
	}()
	started := s.now()
	err := s.Run(ectx, m, budget)
	close(stop)
	once.Do(func() {}) // the watcher can no longer set yielded
	out := Measured
	switch {
	case yielded:
		out = Yielded
	case errors.Is(ectx.Err(), context.DeadlineExceeded):
		out = Timeout
	case ctx.Err() != nil:
		return "" // ternly is exiting: not an attempt
	case err != nil:
		out = Failed
	}
	_ = s.Ledger.add(Entry{Model: m.Key(), Started: started, Finished: s.now(), Outcome: out, BudgetUSD: budget})
	if out == Measured && s.Done != nil {
		s.Done()
	}
	return out
}
