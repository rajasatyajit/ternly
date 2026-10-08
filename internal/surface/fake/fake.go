// Package fake is a surface.Status and surface.Actions for UI tests
// (ADR 021): set Snap, call Notify, and read what the UI asked for.
package fake

import (
	"context"
	"sync"

	"github.com/rajasatyajit/ternly/internal/surface"
)

// Core is a scripted core.
type Core struct {
	mu       sync.Mutex
	snap     surface.Snapshot
	subs     []chan struct{}
	Pinned   []string // every Pin call, in order
	Explains []surface.Explanation
	Errs     map[string]error // by method name: "Pin", "Reconnect"

	Proposals []surface.EditProposal                          // every Review call, in order
	Decide    func(surface.EditProposal) surface.EditDecision // nil: accept every hunk
}

var (
	_ surface.Status  = (*Core)(nil)
	_ surface.Actions = (*Core)(nil)
)

// Set replaces the snapshot and notifies subscribers.
func (c *Core) Set(s surface.Snapshot) {
	c.mu.Lock()
	c.snap = s
	subs := append([]chan struct{}(nil), c.subs...)
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default: // one pending notification is enough
		}
	}
}

func (c *Core) Snapshot() surface.Snapshot { c.mu.Lock(); defer c.mu.Unlock(); return c.snap }

func (c *Core) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, s := range c.subs {
			if s == ch {
				c.subs = append(c.subs[:i], c.subs[i+1:]...)
				break
			}
		}
		close(ch)
	}()
	return ch
}

func (c *Core) Pin(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Pinned = append(c.Pinned, key)
	return c.Errs["Pin"]
}

func (c *Core) Explain(d, ctx int) surface.Explanation {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.Explains {
		if e.Difficulty == d && e.ContextTokens == ctx {
			return e
		}
	}
	return surface.Explanation{Difficulty: d, ContextTokens: ctx}
}

func (c *Core) Reconnect(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Errs["Reconnect"]
}

// Review is a surface.Reviewer: it records the proposal and answers with
// Decide (every hunk accepted when Decide is nil).
func (c *Core) Review(_ context.Context, p surface.EditProposal) surface.EditDecision {
	c.mu.Lock()
	c.Proposals = append(c.Proposals, p)
	decide := c.Decide
	c.mu.Unlock()
	if decide != nil {
		return decide(p)
	}
	all := make([]bool, len(p.Hunks))
	for i := range all {
		all[i] = true
	}
	return surface.EditDecision{Apply: all}
}

var _ surface.Reviewer = (&Core{}).Review
