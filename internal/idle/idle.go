// Package idle decides when ternly may do background work (ADR 031):
// background evaluations and the capability catalog's refresh run only while
// no turn runs, the user hasn't typed for a while, and the machine is on AC
// power. Work checks Allowed often and pauses as soon as it turns false.
package idle

import (
	"context"
	"sync"
	"time"
)

// After is how long after the last keystroke the session counts as idle.
// Five minutes: an evaluation takes 5–20 minutes and competes with the
// user's own turns for the model (and for the GPU, locally); a user who has
// read an answer and typed nothing for five minutes has usually stepped
// away, while a pause to think between prompts (seconds to a couple of
// minutes) doesn't start work that would then have to pause at once.
const After = 5 * time.Minute

// Gate is the policy. The zero value of a field means: no condition.
type Gate struct {
	Running func() bool // a turn is running (the agent)
	After   time.Duration
	Power   func() (onAC, known bool) // nil: OnAC
	Now     func() time.Time

	mu        sync.Mutex
	last      time.Time // the last user input
	powerAt   time.Time
	powerAC   bool
	powerSure bool
}

// New is a gate whose idle clock starts now: background work waits After
// from start, as if the user had just typed.
func New(running func() bool) *Gate {
	g := &Gate{Running: running, After: After}
	g.last = g.now()
	return g
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Touch records user input (a key, a paste).
func (g *Gate) Touch() {
	t := g.now()
	g.mu.Lock()
	g.last = t
	g.mu.Unlock()
}

// powerCache: the power source is read at most every 30 s (a few small
// sysfs reads; pmset is a process on macOS).
const powerCache = 30 * time.Second

func (g *Gate) onAC() (bool, bool) {
	now := g.now()
	g.mu.Lock()
	if !g.powerAt.IsZero() && now.Sub(g.powerAt) < powerCache {
		ac, sure := g.powerAC, g.powerSure
		g.mu.Unlock()
		return ac, sure
	}
	g.mu.Unlock()
	read := g.Power
	if read == nil {
		read = OnAC
	}
	ac, sure := read()
	g.mu.Lock()
	g.powerAt, g.powerAC, g.powerSure = now, ac, sure
	g.mu.Unlock()
	return ac, sure
}

// Allowed reports whether background work may run now, and if not, why.
func (g *Gate) Allowed() (bool, string) {
	if g.Running != nil && g.Running() {
		return false, "a turn is running"
	}
	g.mu.Lock()
	since := g.now().Sub(g.last)
	g.mu.Unlock()
	if since < g.After {
		return false, "waiting for " + g.After.String() + " without input"
	}
	if ac, _ := g.onAC(); !ac {
		return false, "on battery"
	}
	return true, ""
}

// Poll is how often Wait looks again.
var Poll = 5 * time.Second

// Wait blocks until Allowed or ctx ends.
func (g *Gate) Wait(ctx context.Context) error {
	for {
		if ok, _ := g.Allowed(); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(Poll):
		}
	}
}
