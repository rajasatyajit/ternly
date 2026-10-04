package discover

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Router picks the cheapest model that is strong enough for the task, and
// escalates (cascade) when verification fails or a provider errors.
type Router struct {
	mu     sync.RWMutex
	models []*Model
	pinned *Model
	ready  chan struct{}
	once   sync.Once
}

func NewRouter() *Router { return &Router{ready: make(chan struct{})} }

func (r *Router) SetModels(ms []*Model) {
	r.mu.Lock()
	r.models = ms
	r.mu.Unlock()
	r.once.Do(func() { close(r.ready) })
}

func (r *Router) Ready() <-chan struct{} { return r.ready }

func (r *Router) Models() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.models
}

func (r *Router) Pin(key string) (*Model, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == "" || key == "auto" {
		r.pinned = nil
		return nil, nil
	}
	var hits []*Model
	for _, m := range r.models {
		if m.Key() == key || m.ID == key {
			r.pinned = m
			return m, nil
		}
		if strings.Contains(m.Key(), key) {
			hits = append(hits, m)
		}
	}
	if len(hits) == 1 {
		r.pinned = hits[0]
		return hits[0], nil
	}
	if len(hits) > 1 {
		return nil, fmt.Errorf("%q is ambiguous (%d matches, e.g. %s)", key, len(hits), hits[0].Key())
	}
	return nil, fmt.Errorf("no model matches %q (try /models)", key)
}

func (r *Router) Pinned() *Model { r.mu.RLock(); defer r.mu.RUnlock(); return r.pinned }

var (
	reHard = regexp.MustCompile(`(?i)\b(architect\w*|design|refactor\w*|redesign|migrat\w*|concurren\w*|race|deadlock|secur\w*|vulnerab\w*|perf\w*|optimi[sz]\w*|debug\w*|root.?cause|implement\w*|from scratch|end.to.end|multi.?file|across|rewrite|algorithm|protocol|distributed|scal\w+)\b`)
	reEasy = regexp.MustCompile(`(?i)\b(rename|typo|comment|docstring|format|explain|what (is|does)|summari[sz]e|list|show|find|where|which|lint|bump|readme)\b`)
)

// Classify estimates difficulty 1..3 with zero tokens spent.
func Classify(prompt string, failures int) int {
	words := len(strings.Fields(prompt))
	hard := len(reHard.FindAllString(prompt, -1))
	d := 2
	switch {
	case hard >= 2 || (hard == 1 && words > 60) || words > 250:
		d = 3
	case hard == 0 && reEasy.MatchString(prompt) && words < 40:
		d = 1
	}
	return min(3, d+failures)
}

// Pick returns the cheapest tool-capable model with tier >= diff and enough context.
func (r *Router) Pick(diff, needCtx int) (*Model, string) {
	if p := r.Pinned(); p != nil {
		return p, "pinned"
	}
	cands := r.usable(needCtx)
	if len(cands) == 0 {
		return nil, "no tool-capable model discovered"
	}
	for want := diff; want >= 1; want-- {
		var ok []*Model
		for _, m := range cands {
			if m.Tier >= want {
				ok = append(ok, m)
			}
		}
		if len(ok) > 0 {
			sort.SliceStable(ok, func(i, j int) bool { return cheaper(ok[i], ok[j]) })
			reason := fmt.Sprintf("T%d task → cheapest T%d+", diff, want)
			if want < diff {
				reason += " (nothing stronger available)"
			}
			return ok[0], reason
		}
	}
	return cands[0], "fallback"
}

// Escalate returns the next model up: higher tier first, then pricier within tier.
func (r *Router) Escalate(cur *Model, needCtx int) (*Model, bool) {
	if r.Pinned() != nil {
		return cur, false
	}
	cands := r.usable(needCtx)
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Tier != cands[j].Tier {
			return cands[i].Tier < cands[j].Tier
		}
		return cheaper(cands[i], cands[j])
	})
	for i, m := range cands {
		if m == cur {
			for _, n := range cands[i+1:] {
				if n.Tier > cur.Tier || n.Blended() > cur.Blended() {
					return n, true
				}
			}
			return cur, false
		}
	}
	if len(cands) > 0 && cands[len(cands)-1] != cur {
		return cands[len(cands)-1], true
	}
	return cur, false
}

// Failover returns an equivalent model on a *different provider* (outage / rate limit).
func (r *Router) Failover(cur *Model, needCtx int) (*Model, bool) {
	var best *Model
	for _, m := range r.usable(needCtx) {
		if m.ProvID == cur.ProvID || m.Tier < cur.Tier {
			continue
		}
		if best == nil || cheaper(m, best) {
			best = m
		}
	}
	return best, best != nil
}

// Utility is the cheapest model for summaries/compaction (tier ≥ 1, big context).
func (r *Router) Utility(needCtx int) *Model {
	cands := r.usable(needCtx)
	sort.SliceStable(cands, func(i, j int) bool { return cheaper(cands[i], cands[j]) })
	for _, m := range cands {
		if !m.Local() || m.Tier >= 2 { // tiny local models write poor summaries
			return m
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return nil
}

func (r *Router) usable(needCtx int) []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Model
	for _, m := range r.models {
		if m.Tools && m.Ctx >= needCtx {
			out = append(out, m)
		}
	}
	return out
}

// cheaper: free/local first, then blended price; unknown price counts as mid-range.
func cheaper(a, b *Model) bool {
	pa, pb := price(a), price(b)
	if pa != pb {
		return pa < pb
	}
	return a.Tier > b.Tier
}

func price(m *Model) float64 {
	if !m.Priced {
		return 3
	}
	return m.Blended()
}
