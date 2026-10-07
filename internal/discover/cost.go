package discover

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Routing v2 (ADR 018): route on the expected cost of finishing a task
// successfully, not on token price alone.
//
//	score(m, d) = (money + quota + λ·time) / p
//
// money is pay-per-token spend; quota is a small shadow price for a
// subscription with headroom; time is the expected wall time on this machine,
// valued at λ dollars per hour; p is the probability of success at
// difficulty d. Dividing by p is the expected cost of retrying until it
// succeeds.

// CostModel holds routing v2's settings. A nil *CostModel on the Router means
// routing v1 (price only).
type CostModel struct {
	TimeValue   float64       // λ: USD per hour of the user's waiting; 0 = price only (v1's picks)
	TurnLimit   time.Duration // a model expected to take longer is not eligible (0: no limit)
	QuotaShadow float64       // USD per model round-trip on a subscription with quota left
	Speeds      *SpeedStore   // measured speeds; nil = priors only
}

// DefaultTimeValue is λ's default (ADR 018 review: $20/hour).
const DefaultTimeValue = 20.0

// Task shape by difficulty: model round-trips, output tokens per round-trip
// (reasoning included) and context growth per round-trip. Priors from the
// e2e reports (ADR 017 baseline: median 10–60 k input tokens, a few hundred
// output tokens per step); Phase C replaces them with measured task classes.
var (
	shapeSteps  = [4]float64{0, 4, 10, 20}
	shapeOut    = [4]float64{0, 300, 500, 800}
	shapeGrowth = 1500.0
)

// Prior success probability for an unmeasured model by its name tier. Each
// sits below the lowest lower bound a *measured* model of that tier can have
// (T3 ≥ 0.80, T2 ≥ 0.60; ADR 013), so evidence beats a name.
var priorP = [4]float64{0, 0.30, 0.50, 0.70}

// pFloor keeps p away from 0, so one bad measurement can't make a model's
// score infinite (ADR 018 review: Wilson lower bound with a floor).
const pFloor = 0.10

// Hysteresis: the previous turn's model is kept while its score is within
// this factor of the best (ADR 018 review): no flapping mid-task, and the
// provider's prompt cache survives.
const keepFactor = 1.25

// Estimate is one model's score for one task, with its terms (shown by /models).
type Estimate struct {
	Model      *Model
	Diff       int
	P          float64
	PBasis     string
	Seconds    float64
	SpeedBasis string
	Money      float64 // USD, pay-per-token
	Quota      float64 // USD, subscription shadow price
	Score      float64
	Eligible   bool
	Why        string // why not eligible
	OverTime   bool   // ineligible only because it is expected to exceed the turn limit
}

// speedOf is the speed routing assumes for m: measured parts first, priors
// for the rest (from GPU placement for local models).
func (c *CostModel) speedOf(m *Model, gpu float64) (prefill, gen, base float64, basis string) {
	switch {
	case m.Local():
		f := gpu
		if f < 0 {
			f = 0.5 // unknown placement
		}
		// per-token times mix in proportion to what runs where (CPU is ~15x
		// slower to evaluate a prompt and ~5x slower to generate)
		prefill = 1 / (f/2500 + (1-f)/150)
		gen = 1 / (f/50 + (1-f)/10)
		base = 0.3
		basis = "prior: local, GPU unknown"
		if gpu >= 0 {
			basis = fmt.Sprintf("prior: local, GPU %.0f%%", 100*gpu)
		}
	default:
		prefill, gen, base = 4000, 60, 1.5
		basis = "prior: remote"
	}
	if c != nil {
		if s, ok := c.Speeds.Get(m.Key()); ok && s.Samples > 0 {
			var parts []string
			if s.PrefillTPS > 0 {
				prefill = s.PrefillTPS
				parts = append(parts, fmt.Sprintf("prefill %.0f tok/s", s.PrefillTPS))
			}
			if s.GenTPS > 0 {
				gen = s.GenTPS
				parts = append(parts, fmt.Sprintf("%.0f tok/s", s.GenTPS))
			}
			if s.BaseS > 0 {
				base = s.BaseS
			}
			if len(parts) > 0 {
				basis = fmt.Sprintf("measured (%d): %s", s.Samples, strings.Join(parts, ", "))
			}
		}
	}
	return
}

// successP is p(m, d): the Wilson lower bound of the measured pass rate
// (never the raw rate), or a name-tier prior lowered for each newer model of
// the same family (ADR 018 §4: the latest of a family is presumed best until
// measured); halved per tier the model is short of d; floored.
func successP(m *Model, d int, older int) (float64, string) {
	var p float64
	var basis string
	if ms := m.Measure; ms != nil && ms.Runs > 0 {
		p, basis = ms.PassLo, fmt.Sprintf("measured lower bound (%d runs)", ms.Runs)
	} else {
		p, basis = priorP[clampTier(m.Tier)], fmt.Sprintf("prior (%s tier T%d)", orBasis(m.Basis), m.Tier)
		if older > 0 {
			p -= 0.05 * float64(older)
			basis += fmt.Sprintf(", %d newer in its family", older)
		}
	}
	if gap := d - m.Tier; gap > 0 {
		p *= math.Pow(0.5, float64(gap))
		basis += fmt.Sprintf(", T%d task", d)
	}
	if p < pFloor {
		p, basis = pFloor, basis+", floored"
	}
	return p, basis
}

func clampTier(t int) int { return min(3, max(1, t)) }

func orBasis(b string) string {
	if b == "" {
		return "name"
	}
	return b
}

// estimate scores m for a task of difficulty d with ctx tokens of context.
func (c *CostModel) estimate(m *Model, d, ctx int, gpu float64, exhausted bool, older int) Estimate {
	d = clampTier(d)
	e := Estimate{Model: m, Diff: d, Eligible: true}
	e.P, e.PBasis = successP(m, d, older)
	prefill, gen, base, basis := c.speedOf(m, gpu)
	steps := shapeSteps[d]
	evaluated := float64(ctx) + steps*shapeGrowth // the first step reads the context, later ones the new tail
	e.Seconds = steps*base + evaluated/prefill + steps*shapeOut[d]/gen
	if m.Local() && m.GPUBasis != "loaded" && m.Size > 0 {
		e.Seconds += float64(m.Size) / 1e9 // load at ~1 GB/s (qwen3.6: 23 GB in 26.8 s)
		basis += ", not loaded"
	}
	e.SpeedBasis = basis
	switch {
	case m.Cloud:
		e.Quota = c.quotaShadow() * steps
	case m.Free():
	default:
		in, out, cache := m.In, m.Out, m.CacheIn
		if !m.Priced { // unknown price: mid-range, as v1 assumes
			in, out = 3, 3
		}
		if cache == 0 {
			cache = in * 0.1
		}
		avgCtx := float64(ctx) + steps*shapeGrowth/2
		e.Money = steps * (avgCtx*(0.2*in+0.8*cache) + shapeOut[d]*out) / 1e6 // agents re-send context; most of it cache hits
	}
	lambda := 0.0
	if c != nil {
		lambda = c.TimeValue / 3600
	}
	e.Score = (e.Money + e.Quota + lambda*e.Seconds) / e.P
	switch {
	case m.NoFit:
		e.Eligible, e.Why = false, "too large for VRAM + available RAM"
	case exhausted:
		e.Eligible, e.Why = false, "quota exhausted (rate limited)"
	case c != nil && c.TurnLimit > 0 && e.Seconds > c.TurnLimit.Seconds():
		e.Eligible, e.OverTime, e.Why = false, true, fmt.Sprintf("expected %s > the %s turn limit", humanDur(e.Seconds), c.TurnLimit)
	}
	return e
}

func (c *CostModel) quotaShadow() float64 {
	if c == nil || c.QuotaShadow == 0 {
		return 0.002
	}
	return c.QuotaShadow
}

func humanDur(s float64) string {
	switch {
	case s < 90:
		return fmt.Sprintf("%.0f s", s)
	case s < 5400:
		return fmt.Sprintf("%.1f min", s/60)
	}
	return fmt.Sprintf("%.1f h", s/3600)
}

// Identity is a model's provider identity for failover (ADR 018 review):
// endpoint + locality, so a local Ollama model and an Ollama Cloud model
// behind the same daemon count as different providers.
func Identity(m *Model) string {
	loc := "remote"
	switch {
	case m.Cloud:
		loc = "cloud"
	case m.Local():
		loc = "local"
	}
	base := ""
	if m.Provider != nil {
		base = m.Provider.BaseURL
	}
	return base + "|" + loc
}

// ─────────────────────────── the router, v2 ───────────────────────────

// SetCostModel switches the router to routing v2 (nil: v1, price only).
func (r *Router) SetCostModel(c *CostModel) {
	r.mu.Lock()
	r.cost = c
	r.mu.Unlock()
}

// V2 reports whether routing v2 is on.
func (r *Router) V2() bool { r.mu.RLock(); defer r.mu.RUnlock(); return r.cost != nil }

// MarkExhausted takes a model out of routing until t (a quota or rate limit).
func (r *Router) MarkExhausted(key string, until time.Time) {
	r.mu.Lock()
	if r.exhausted == nil {
		r.exhausted = map[string]time.Time{}
	}
	r.exhausted[key] = until
	r.mu.Unlock()
}

// Observe records a streamed request's timings (routing v2 measures speed
// on every request; v1 ignores it).
func (r *Router) Observe(m *Model, prompt, out int, ttft, gen time.Duration) {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	if c != nil && m != nil {
		c.Speeds.Observe(m.Key(), prompt, out, ttft, gen)
	}
}

// SaveSpeeds persists measured speeds (a turn boundary).
func (r *Router) SaveSpeeds() error {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	if c == nil {
		return nil
	}
	return c.Speeds.Save()
}

// SetPlacement updates a local model's measured GPU fraction (after a load).
func (r *Router) SetPlacement(key string, gpu float64) {
	r.mu.Lock()
	if r.place == nil {
		r.place = map[string]float64{}
	}
	r.place[key] = gpu
	r.mu.Unlock()
}

// Explain scores every tool-capable model with enough context for a task of
// difficulty d (best first): what /models shows.
func (r *Router) Explain(d, ctx, need int) []Estimate {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	return r.estimates(c, d, ctx, need, nil)
}

func (r *Router) estimates(c *CostModel, d, ctx, need int, skip func(*Model) bool) []Estimate {
	cands := r.usable(need)
	r.mu.RLock()
	now := time.Now()
	newer := familyRanks(r.models)
	out := make([]Estimate, 0, len(cands))
	for _, m := range cands {
		if skip != nil && skip(m) {
			continue
		}
		gpu := m.GPU
		if g, ok := r.place[m.Key()]; ok {
			gpu = g
		}
		out = append(out, c.estimate(m, d, ctx, gpu, now.Before(r.exhausted[m.Key()]), newer[m]))
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Eligible != b.Eligible:
			return a.Eligible
		case a.Score != b.Score:
			return a.Score < b.Score
		case a.Model.Ctx != b.Model.Ctx: // ties (priors only): more context, then the larger model
			return a.Model.Ctx > b.Model.Ctx
		case a.Model.Params != b.Model.Params:
			return a.Model.Params > b.Model.Params
		}
		return a.Model.Key() < b.Model.Key()
	})
	return out
}

var (
	reVersion = regexp.MustCompile(`\d+(?:\.\d+)*`)
	reVerTail = regexp.MustCompile(`[-:](cloud|latest)$`)
)

// familyRanks counts, for each model, the newer models of its family on the
// same provider: same name with the version numbers taken out (glm-5.1 and
// glm-5.3 are "glm-", kimi-k2.6 and kimi-k3 are "kimi-k"; kimi-k2.7-code is
// its own family). Callers hold r.mu.
func familyRanks(ms []*Model) map[*Model]int {
	type ver struct {
		m *Model
		v []float64
	}
	fam := map[string][]ver{}
	for _, m := range ms {
		// sizes ("31b", "e4b") are not versions: gemma4:31b and gemma4:26b are one generation
		name := reParamsB.ReplaceAllString(strings.ToLower(reVerTail.ReplaceAllString(tierName(m), "")), "")
		var v []float64
		for _, s := range reVersion.FindAllString(name, -1) {
			for _, part := range strings.Split(s, ".") {
				f, _ := strconv.ParseFloat(part, 64)
				v = append(v, f)
			}
		}
		if len(v) == 0 {
			continue
		}
		k := m.ProvID + "|" + strings.Trim(reVersion.ReplaceAllString(name, ""), "-_.:")
		fam[k] = append(fam[k], ver{m, v})
	}
	out := map[*Model]int{}
	for _, vs := range fam {
		for _, a := range vs {
			for _, b := range vs {
				if slices.Compare(b.v, a.v) > 0 {
					out[a.m]++
				}
			}
		}
	}
	return out
}

// best returns the lowest-scoring eligible estimate, or, when the only
// candidates left are expected to exceed the turn limit, the fastest of them
// (slow beats nothing; a model that can't load or has no quota never runs).
func best(es []Estimate) (Estimate, bool) {
	if len(es) > 0 && es[0].Eligible {
		return es[0], true
	}
	var fast *Estimate
	for i := range es {
		if es[i].OverTime && (fast == nil || es[i].Seconds < fast.Seconds) {
			fast = &es[i]
		}
	}
	if fast == nil {
		return Estimate{}, false
	}
	return *fast, true
}

// PickFor is Pick with what v2 needs: the context the task starts with
// (prefill grows with it, so the ranking is redone every turn) and the
// previous turn's model (kept within keepFactor of the best). Under v1, or
// with λ = 0, it is exactly v1's Pick.
func (r *Router) PickFor(diff, ctx, need int, prev *Model) (*Model, string) {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	if p := r.Pinned(); p != nil || c == nil || c.TimeValue == 0 {
		return r.Pick(diff, need)
	}
	es := r.estimates(c, diff, ctx, need, nil)
	b, ok := best(es)
	if !ok {
		return r.Pick(diff, need)
	}
	if prev != nil && prev != b.Model {
		for _, e := range es {
			if e.Model == prev && e.Eligible && e.Score <= keepFactor*b.Score {
				return prev, fmt.Sprintf("T%d task → kept %s (est. %s, within %.0f%% of the best, %s; keeps the prompt cache)",
					diff, prev.ID, humanDur(e.Seconds), 100*(keepFactor-1), b.Model.ID)
			}
		}
	}
	return b.Model, reasonFor(b, es)
}

// reasonFor names the winner's terms and the runner-up it beat.
func reasonFor(b Estimate, es []Estimate) string {
	s := fmt.Sprintf("T%d task → est. %s, %s, p≥%.2f", b.Diff, humanDur(b.Seconds), costWord(b), b.P)
	for _, e := range es {
		if e.Model != b.Model {
			why := fmt.Sprintf("est. %s, %s", humanDur(e.Seconds), costWord(e))
			if !e.Eligible {
				why = e.Why
			}
			s += fmt.Sprintf("; next %s (%s)", e.Model.ID, why)
			break
		}
	}
	return s
}

func costWord(e Estimate) string {
	switch {
	case e.Money > 0:
		return fmt.Sprintf("$%.3f", e.Money)
	case e.Model.Cloud:
		return "quota"
	case e.Model.Local() && e.Model.GPU >= 0:
		return fmt.Sprintf("local, GPU %.0f%%", 100*e.Model.GPU)
	case e.Model.Local():
		return "local"
	}
	return "free"
}

// EscalateFor is Escalate for v2: the best model not yet tried this turn,
// re-scored one difficulty up. When there is nowhere to go, the hint says
// what would help (ADR 018 review). Under v1 it is v1's Escalate.
func (r *Router) EscalateFor(cur *Model, diff, ctx, need int, tried map[string]bool) (*Model, bool, string) {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	if c == nil {
		m, ok := r.Escalate(cur, need)
		return m, ok, ""
	}
	if r.Pinned() != nil {
		return cur, false, "the model is pinned (/model auto lets ternly escalate)"
	}
	es := r.estimates(c, min(diff+1, 3), ctx, need, func(m *Model) bool { return m == cur || tried[m.Key()] })
	if b, ok := best(es); ok {
		return b.Model, true, ""
	}
	return cur, false, r.nowhere(es)
}

// FailoverFor is Failover for v2: the best model with a different provider
// identity (endpoint + locality).
func (r *Router) FailoverFor(cur *Model, diff, ctx, need int, tried map[string]bool) (*Model, bool) {
	r.mu.RLock()
	c := r.cost
	r.mu.RUnlock()
	if c == nil {
		return r.Failover(cur, need)
	}
	id := Identity(cur)
	es := r.estimates(c, diff, ctx, need, func(m *Model) bool { return Identity(m) == id || tried[m.Key()] })
	b, ok := best(es)
	if !ok {
		return nil, false
	}
	return b.Model, true
}

// nowhere explains what would give escalation somewhere to go.
func (r *Router) nowhere(left []Estimate) string {
	var hints []string
	r.mu.RLock()
	var until time.Time
	for _, t := range r.exhausted {
		if t.After(until) {
			until = t
		}
	}
	paid := false
	for _, m := range r.models {
		if !m.Local() && !m.Cloud {
			paid = true
		}
	}
	r.mu.RUnlock()
	if time.Now().Before(until) {
		hints = append(hints, "a cloud quota resets around "+until.Format("15:04"))
	}
	for _, e := range left {
		if e.Model.NoFit {
			hints = append(hints, e.Model.ID+" is too large for this machine")
			break
		}
	}
	if !paid {
		hints = append(hints, "an API key (ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY or GEMINI_API_KEY) adds stronger models")
	}
	hints = append(hints, "/model <name> pins one to try")
	return "no stronger model is available to escalate to: " + strings.Join(hints, "; ")
}

// utilityV2 picks the model for background calls (titles, summaries,
// compaction; ADR 018 review): a local model only if it runs fully on the
// GPU, otherwise the cheapest cloud or API model, fastest among equals.
func (r *Router) utilityV2(c *CostModel, need int) *Model {
	es := r.estimates(c, 1, 2000, need, nil)
	var cloud, gpuLocal, any []Estimate
	for _, e := range es {
		if !e.Eligible {
			continue
		}
		m := e.Model
		switch {
		case m.Local():
			gpu := m.GPU
			r.mu.RLock()
			if g, ok := r.place[m.Key()]; ok {
				gpu = g
			}
			r.mu.RUnlock()
			if gpu >= 0.999 && m.Tier >= 2 {
				gpuLocal = append(gpuLocal, e)
			}
		default:
			cloud = append(cloud, e)
		}
		any = append(any, e)
	}
	cheapest := func(es []Estimate) *Model {
		sort.SliceStable(es, func(i, j int) bool {
			ci, cj := es[i].Money+es[i].Quota, es[j].Money+es[j].Quota
			if ci != cj {
				return ci < cj
			}
			return es[i].Seconds < es[j].Seconds
		})
		return es[0].Model
	}
	switch {
	case len(gpuLocal) > 0:
		return cheapest(gpuLocal)
	case len(cloud) > 0:
		return cheapest(cloud)
	case len(any) > 0:
		return cheapest(any)
	}
	return nil
}
