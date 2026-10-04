package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rajasatyajit/ternly/internal/checkpoint"
)

// TurnInfo describes a rewindable turn for /rewind.
type TurnInfo struct {
	N       int // 1-based
	Prompt  string
	At      time.Time
	Changed bool // this turn checkpointed before mutating the workspace
}

func (a *Agent) Turns() []TurnInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TurnInfo, len(a.turns))
	for i, t := range a.turns {
		out[i] = TurnInfo{N: i + 1, Prompt: t.prompt, At: t.at, Changed: t.tree != ""}
	}
	return out
}

// Rewind modes, matching Claude Code's /rewind options.
const (
	RewindBoth = "both" // code and conversation
	RewindCode = "code" // files only; conversation kept
	RewindChat = "chat" // conversation only; files kept
)

// Plan is what a rewind to before turn N would do; the UI shows it for confirmation.
type Plan struct {
	N       int
	Mode    string
	Prompt  string              // the rewound turn's prompt (put back in the input box)
	Tree    string              // workspace state before turn N ("" = unchanged since)
	Changes []checkpoint.Change // effect on the workspace
	Drop    int                 // conversation messages removed
	Secrets []string            // secret-like files a code restore never touches
}

// PlanRewind prepares a rewind to the state before turn n (1-based; 0 = last turn).
func (a *Agent) PlanRewind(ctx context.Context, n int, mode string) (Plan, error) {
	if a.running.Load() {
		return Plan{}, errors.New("a turn is running — interrupt it first (Esc)")
	}
	switch mode {
	case RewindBoth, RewindCode, RewindChat:
	default:
		return Plan{}, fmt.Errorf("unknown mode %q (use both, code or chat)", mode)
	}
	a.mu.Lock()
	if n == 0 {
		n = len(a.turns)
	}
	if n < 1 || n > len(a.turns) {
		a.mu.Unlock()
		return Plan{}, fmt.Errorf("no turn %d (this conversation has %d)", n, len(a.turns))
	}
	p := Plan{N: n, Mode: mode, Prompt: a.turns[n-1].prompt, Drop: len(a.history) - a.turns[n-1].hist}
	// The workspace before turn n equals the first checkpoint at or after it:
	// turns without a checkpoint changed nothing.
	for _, t := range a.turns[n-1:] {
		if t.tree != "" {
			p.Tree = t.tree
			break
		}
	}
	a.mu.Unlock()
	if mode == RewindChat {
		p.Tree = ""
	}
	if p.Tree != "" {
		if a.CP == nil {
			return Plan{}, errors.New("checkpoints are disabled, so files can't be restored (use mode chat)")
		}
		cs, err := a.CP.Pending(ctx, p.Tree)
		if err != nil {
			return Plan{}, err
		}
		p.Changes = cs
		p.Secrets, _ = a.CP.SecretFiles(ctx)
	}
	if mode == RewindCode {
		p.Drop = 0
	}
	return p, nil
}

// Rewind applies a plan from PlanRewind and returns what changed on disk.
func (a *Agent) Rewind(ctx context.Context, p Plan) ([]checkpoint.Change, error) {
	if a.running.Load() {
		return nil, errors.New("a turn is running — interrupt it first (Esc)")
	}
	var done []checkpoint.Change
	if p.Tree != "" {
		var err error
		if done, err = a.CP.Restore(ctx, p.Tree); err != nil {
			return nil, err
		}
	}
	if p.Mode != RewindCode {
		a.mu.Lock()
		if p.N <= len(a.turns) {
			a.history = a.history[:a.turns[p.N-1].hist]
			a.turns = a.turns[:p.N-1]
		}
		a.mu.Unlock()
	}
	return done, nil
}
