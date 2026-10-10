// Package status is the core's side of the ADR 021 contract: it implements
// surface.Status and surface.Actions from discovery, the router and the
// agent, so a UI reads connections, quota, models with trust, routing and
// the meter through internal/surface only.
package status

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/surface"
)

// Router is what the core reads from routing (a *discover.Router).
type Router interface {
	Models() []*discover.Model
	SetModels([]*discover.Model)
	Pin(key string) (*discover.Model, error)
	Pinned() *discover.Model
	V2() bool
	Explain(d, ctx, need int) []discover.Estimate
	ExhaustedUntil(key string) time.Time
}

// Agent is what the core reads from the agent (an *agent.Agent).
type Agent interface {
	Ledger() agent.Ledger
	Current() *discover.Model
	Context() agent.ContextUse
	PlanSteps() []agent.PlanStep
}

// Rediscover re-runs discovery (Reconnect).
type Rediscover func(ctx context.Context) ([]*discover.Model, []discover.Connection, []string)

// Core implements surface.Status and surface.Actions. Snapshot reads only
// in-memory state: no I/O, never waits on a turn (ADR 021 budget: ≤ 50 µs
// for 50 models and 10 connections).
type Core struct {
	router     Router
	agent      Agent
	rediscover Rediscover

	mu       sync.Mutex
	conns    []discover.Connection
	why      string // why the current turn's model was chosen
	switched string // why it changed during the turn
	inTurn   bool
	subs     []chan struct{}
	now      func() time.Time
}

var (
	_ surface.Status  = (*Core)(nil)
	_ surface.Actions = (*Core)(nil)
)

// New builds the status core. rediscover may be nil (Reconnect then fails).
func New(r Router, a Agent, conns []discover.Connection, rediscover Rediscover) *Core {
	return &Core{router: r, agent: a, conns: conns, rediscover: rediscover, now: time.Now}
}

// Observe takes the agent's events (chain it into the event sink): model
// choices and switches set Routing; any event may change the meter.
func (c *Core) Observe(ev agent.Event) {
	c.mu.Lock()
	switch ev.Kind {
	case agent.EvModel:
		if !c.inTurn {
			c.why, c.switched, c.inTurn = ev.Reason, "", true
		} else {
			c.switched = ev.Reason
		}
	case agent.EvDone:
		c.inTurn = false
	}
	c.mu.Unlock()
	c.notify()
}

func (c *Core) notify() {
	c.mu.Lock()
	subs := append([]chan struct{}(nil), c.subs...)
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default: // coalesced: one pending notification is enough
		}
	}
}

// Changes implements surface.Status.
func (c *Core) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		for i, s := range c.subs {
			if s == ch {
				c.subs = append(c.subs[:i], c.subs[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		close(ch)
	}()
	return ch
}

// Snapshot implements surface.Status.
func (c *Core) Snapshot() surface.Snapshot {
	c.mu.Lock()
	conns := c.conns
	why, switched := c.why, c.switched
	c.mu.Unlock()

	models := c.router.Models()
	pinned := c.router.Pinned()
	exhausted := map[string]time.Time{} // by connection, the latest failover hold
	s := surface.Snapshot{At: c.now(), Models: make([]surface.Model, 0, len(models))}
	for _, m := range models {
		conn := m.ProvID
		if m.ProvID == "ollama" && m.Cloud {
			conn = "ollama-cloud"
		}
		if t := c.router.ExhaustedUntil(m.Key()); t.After(exhausted[conn]) {
			exhausted[conn] = t
		}
		row := surface.Model{Key: m.Key(), Connection: conn, Local: m.Local(), Tier: m.Tier, TierBasis: m.Basis,
			Price: discover.Price(m), GPU: m.GPU, Pinned: m == pinned}
		if ms := m.Measure; ms != nil {
			row.Trust = surface.Trust{Lost: ms.Baitable, CleanStreak: ms.TrustClean, Needed: ms.TrustNeeded, Measured: true}
		}
		s.Models = append(s.Models, row)
	}
	s.Connections = make([]surface.Connection, 0, len(conns))
	for _, k := range conns {
		sc := surface.Connection{ID: k.ID, Label: k.Label, Kind: k.Kind, How: k.How, State: k.State, Detail: k.Detail, Models: k.Models}
		if q := k.Quota; q != nil {
			sc.Quota = &surface.Quota{Used: q.Used, Limit: q.Limit, Unit: q.Unit, ResetsAt: q.ResetsAt, Source: q.Source}
		}
		if t := exhausted[k.ID]; t.After(s.At) {
			if sc.Quota == nil {
				sc.Quota = &surface.Quota{Source: "the provider said its limit was reached (ternly fails over until then)"}
			}
			sc.Quota.ExhaustedUntil = t
		}
		s.Connections = append(s.Connections, sc)
	}
	s.Routing = surface.Routing{Version: "v1", Why: why, Switched: switched}
	if c.router.V2() {
		s.Routing.Version = "v2"
	}
	if c.agent != nil {
		if m := c.agent.Current(); m != nil {
			s.Routing.Current = m.Key()
		}
		l := c.agent.Ledger()
		u := c.agent.Context()
		s.Meter = surface.Meter{ContextUsed: u.System + u.Tools + u.Notes + u.User + u.Assistant + u.ToolResults, ContextMax: u.Window,
			CostUSD: l.Cost, Turns: l.Turns, CacheRate: l.CacheRate()}
		for _, p := range c.agent.PlanSteps() { // the PlanFirst lever's plan (ADR 029)
			s.Plan = append(s.Plan, surface.PlanItem{Text: p.Text, State: p.State})
		}
	}
	return s
}

// Pin implements surface.Actions: "" unpins.
func (c *Core) Pin(key string) error {
	_, err := c.router.Pin(key)
	if err == nil {
		c.notify()
	}
	return err
}

// Explain implements surface.Actions. difficulty 0 means a T2 task, the
// common case (the TUI's /models default); contextTokens 0, the current
// context.
func (c *Core) Explain(difficulty, contextTokens int) surface.Explanation {
	if difficulty <= 0 {
		difficulty = 2
	}
	if contextTokens <= 0 && c.agent != nil {
		u := c.agent.Context()
		contextTokens = u.System + u.Tools + u.Notes + u.User + u.Assistant + u.ToolResults
	}
	ests := c.router.Explain(difficulty, contextTokens, contextTokens)
	e := surface.Explanation{Difficulty: difficulty, ContextTokens: contextTokens, Rows: make([]surface.Estimate, 0, len(ests))}
	for _, x := range ests {
		e.Rows = append(e.Rows, surface.Estimate{Key: x.Model.Key(), Eligible: x.Eligible, Why: x.Why, P: x.P, Seconds: x.Seconds,
			Money: x.Money, Quota: x.Quota, Score: x.Score, PBasis: x.PBasis, Speed: x.SpeedBasis})
	}
	return e // the router orders them: eligible first, best first
}

// Reconnect implements surface.Actions: discovery runs again (every
// connection: one source can't be listed alone without re-ranking the
// rest), the router gets the new models, and the result for id is checked.
func (c *Core) Reconnect(ctx context.Context, id string) error {
	if c.rediscover == nil {
		return fmt.Errorf("reconnecting isn't available here")
	}
	ms, conns, _ := c.rediscover(ctx)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("reconnecting %s was cancelled", id)
	}
	pinned := c.router.Pinned()
	c.router.SetModels(ms)
	if pinned != nil {
		_, _ = c.router.Pin(pinned.Key()) // keep the pin if the model is still there
	}
	c.mu.Lock()
	c.conns = conns
	c.mu.Unlock()
	c.notify()
	for _, k := range conns {
		if k.ID == id {
			if k.State != "connected" {
				return fmt.Errorf("%s: %s", k.Label, k.Detail)
			}
			return nil
		}
	}
	return fmt.Errorf("%s isn't configured: nothing was found for it", id)
}

// SetConnections replaces the connections after a discovery run.
func (c *Core) SetConnections(conns []discover.Connection) {
	c.mu.Lock()
	c.conns = conns
	c.mu.Unlock()
	c.notify()
}

// Lines describes the connections for /doctor, /status and --models: what's
// connected, how, what it costs or how much is left, and the next step for
// what isn't; then the official CLIs ternly found but doesn't drive.
func Lines(conns []surface.Connection, clis []discover.CLI, now time.Time) []string {
	out := []string{"connections:"}
	if len(conns) == 0 {
		out = append(out, "  none found: start Ollama (`ollama serve`) or set an API key such as ANTHROPIC_API_KEY or OPENAI_API_KEY")
	}
	for _, c := range conns {
		mark := "✓"
		if c.State != "connected" {
			mark = "✗"
		}
		line := fmt.Sprintf("  %s %s — %s", mark, c.Label, c.How)
		if c.State == "connected" {
			line += fmt.Sprintf(" · %d model%s", c.Models, plural(c.Models))
			line += " · " + usage(c, now)
		}
		if c.Detail != "" {
			line += " · " + c.Detail
		}
		out = append(out, line)
	}
	for _, x := range clis {
		out = append(out, fmt.Sprintf("  · %s CLI found (%s), not used as a backend: %s", x.Name, x.Path, x.Why))
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// usage says what a connection costs or has left, from official signals.
func usage(c surface.Connection, now time.Time) string {
	q := c.Quota
	var s string
	switch {
	case q != nil && q.Limit > 0 && q.Unit == "USD":
		s = fmt.Sprintf("$%.2f of $%.2f used", q.Used, q.Limit)
	case q != nil && q.Limit > 0:
		s = fmt.Sprintf("%.0f%s of %.0f%s used", q.Used, q.Unit, q.Limit, q.Unit)
	case c.Kind == "api-key":
		s = "pay per token (prices in the model list)"
	case c.ID == "ollama-cloud":
		s = "usage unknown (Ollama reports it only to OLLAMA_API_KEY: set one to see it)"
	default:
		s = "free (this machine)"
	}
	if q != nil && !q.ResetsAt.IsZero() && q.Limit > 0 {
		s += ", resets " + q.ResetsAt.Local().Format("Jan 2 15:04")
	}
	if q != nil && q.ExhaustedUntil.After(now) {
		s += fmt.Sprintf("; limit reached: skipped until %s", q.ExhaustedUntil.Local().Format("15:04"))
	}
	return s
}
