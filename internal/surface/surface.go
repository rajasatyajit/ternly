// Package surface is the contract between ternly's core and its user
// interfaces (ADR 021): the status a UI shows and the actions it may take.
//
// Track 1 (providers, routing, evals) produces it; Track 2 (the TUI)
// consumes it. Neither side reaches past it for the data listed here. The
// package holds types and interfaces only: no logic, no I/O, no imports of
// other ternly packages, so either side can change behind it freely.
// Changing a type or method here needs an amendment to ADR 021.
package surface

import (
	"context"
	"time"
)

// Status is what the core exposes to a UI. Snapshot must be cheap (no
// I/O, no blocking on a turn in progress; budget in ADR 021) because a UI
// calls it on every redraw it needs; Changes says when to call it again.
type Status interface {
	Snapshot() Snapshot
	// Changes delivers a value whenever the snapshot may differ (coalesced:
	// one pending notification at most). Closed when ctx ends.
	Changes(ctx context.Context) <-chan struct{}
}

// Actions are the status-related things a UI may ask the core to do.
// Each returns an error the UI shows as is (written for a person).
type Actions interface {
	// Pin routes every turn to one model (key as in Model.Key); "" unpins.
	Pin(key string) error
	// Explain says how routing would rank every model for a task of the
	// given difficulty (0: the current turn's) and context size.
	Explain(difficulty, contextTokens int) Explanation
	// Reconnect re-runs discovery for one connection (Connection.ID).
	Reconnect(ctx context.Context, id string) error
}

// Snapshot is the whole status at one moment. Values, not pointers into
// the core: a UI may keep it while the core moves on.
type Snapshot struct {
	At          time.Time
	Connections []Connection // every source discovered at startup (Phase B)
	Models      []Model
	Routing     Routing
	Meter       Meter
	Plan        []PlanItem
}

// Connection is one source of models: how ternly reaches it and whether it
// works. Phase B adds kinds; a UI shows unknown kinds by Label.
type Connection struct {
	ID     string // stable, e.g. "ollama-local", "ollama-cloud", "claude-cli", "openai-key"
	Label  string // for people: "Ollama (local)", "Claude subscription via the claude CLI"
	Kind   string // "daemon", "cli-bridge", "api-key"
	How    string // how it was found or signs in, e.g. "signed-in daemon", "OPENAI_API_KEY"
	State  string // "connected", "not-installed", "signed-out", "unreachable", "error"
	Detail string // the next step when not connected, written for a person
	Models int    // models it offers
	Quota  *Quota // nil: the provider gives no official signal
	// Never a secret: no keys, tokens or cookies; an account label only if
	// the provider's official interface reports one. TestNoSecrets.
}

// Quota is a usage limit as the provider officially reports it.
type Quota struct {
	Used, Limit    float64 // in Unit; Limit 0: unknown
	Unit           string  // "requests", "tokens", "USD", "%"
	ResetsAt       time.Time
	ExhaustedUntil time.Time // set while routing skips this source (failover, ADR 018)
	Source         string    // the official signal, e.g. "x-ratelimit-remaining header", "claude /status"
}

// Model is one routable model.
type Model struct {
	Key        string // provider/id, as --model takes it
	Connection string // Connection.ID
	Local      bool
	Tier       int
	TierBasis  string  // "measured", "name", "config"
	Price      string  // for people: "$3/$15 per Mtok", "free (local)", "subscription"
	GPU        float64 // local: fraction on the GPU, -1 unknown
	Trust      Trust
	Pinned     bool
}

// Trust is a model's trust state (ADR 020).
type Trust struct {
	Lost        bool
	CleanStreak int // consecutive clean bait trials
	Needed      int // clean trials in a row that regain it
	Measured    bool
}

// Routing is the routing state and the last decision.
type Routing struct {
	Version  string // "v2", "v1"
	Current  string // model key of the current turn ("" between turns)
	Why      string // one line: why that model
	Switched string // set when the turn switched model (escalation, failover): why
}

// Explanation ranks every model as routing would.
type Explanation struct {
	Difficulty, ContextTokens int
	Rows                      []Estimate // best first; ineligible last
}

// Estimate is one model's expected cost of finishing (ADR 018).
type Estimate struct {
	Key           string
	Eligible      bool
	Why           string  // why not eligible
	P             float64 // probability of success, Wilson lower bound
	Seconds       float64
	Money, Quota  float64 // USD
	Score         float64 // (money + quota + λ·time) / p; lower is better
	PBasis, Speed string  // where P and the speed came from
}

// Meter is the session's running totals.
type Meter struct {
	ContextUsed, ContextMax int
	CostUSD                 float64
	Turns                   int
	CacheRate               float64 // 0..1
}

// PlanItem is one entry of the model's plan or todo list.
type PlanItem struct {
	Text  string
	State string // "pending", "active", "done"
}

// ─── Amendment 1 (ADR 021): per-hunk review of edits ───
//
// When the permission policy would ask a person about an edit, the core
// shows the proposed change as hunks and applies only those accepted.
// Lines are the workspace's and the model's text: a UI must show them as
// text, never as terminal sequences (ADR 023).

// EditProposal is a change to one file awaiting a person's decision.
type EditProposal struct {
	Tool    string // the edit tool: edit_file, write_file
	Path    string // workspace-relative
	Why     string // why a person is asked, e.g. the model's trust is lost (ADR 020); "" for the mode's usual ask
	NewFile bool
	Hunks   []Hunk
}

// Hunk is one contiguous change, as in a unified diff.
type Hunk struct {
	OldStart, OldLines int      // 1-based, in the file as it is
	NewStart, NewLines int      // 1-based, in the file as proposed
	Lines              []string // each starts with ' ' (context), '-' or '+'
}

// EditDecision answers an EditProposal. Apply has one entry per hunk; none
// true means the edit is declined. Always allows edits without asking for
// the rest of the session; the core ignores it where its rules don't allow
// that (a model whose trust is lost, ADR 020).
type EditDecision struct {
	Apply  []bool
	Always bool
}

// Reviewer asks a person about an edit. The UI provides it; the core calls
// it only when a person is there to answer (never headless) and only where
// it would otherwise ask yes or no.
type Reviewer func(ctx context.Context, p EditProposal) EditDecision
