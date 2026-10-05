package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Candidate is a ranked entry with the parts of its score.
type Candidate struct {
	Entry
	Score                                       float64
	Relevance, Trust, Footprint, Cover, Context float64
	Reason                                      string
}

// Search finds and ranks candidates for a need: relevance, then trust from
// the source's metadata, security footprint, how much of it ternly can load,
// and context cost. Entries ternly can't load at all are left out.
func (c *Catalog) Search(n Need, limit int) []Candidate {
	if c.open() != nil {
		return nil
	}
	hits := c.store.SearchText(n.Query+" "+n.Label, 80)
	var max float32
	for _, h := range hits {
		if v := h.BM25 * (0.5 + h.Cover); v > max {
			max = v
		}
	}
	var out []Candidate
	for _, h := range hits {
		e := fromItem(h.Item)
		if e.Coverage <= 0 {
			continue
		}
		cd := Candidate{Entry: e, Relevance: float64(h.BM25*(0.5+h.Cover)) / float64(max)}
		if mentions(e.Name+" "+e.Description, systemOf(n.Key)) {
			cd.Relevance = math.Min(1, cd.Relevance+0.3)
		}
		cd.Trust = trustScore(e)
		cd.Footprint = footprint(e)
		cd.Cover = e.Coverage
		cd.Context = 1 - math.Min(float64(e.Tokens), 2000)/2000
		cd.Score = 0.45*cd.Relevance + 0.25*cd.Trust + 0.10*cd.Footprint + 0.15*cd.Cover + 0.05*cd.Context
		cd.Reason = reason(e)
		out = append(out, cd)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func systemOf(key string) System {
	for _, s := range Systems {
		if s.Key == key {
			return s
		}
	}
	return System{Key: key, Words: []string{key}}
}

// trustScore uses only source metadata: listing, verification, adoption,
// maintenance recency and a declared license.
func trustScore(e Entry) float64 {
	t := 0.2
	switch {
	case e.Official:
		t = 1
	case strings.HasPrefix(e.Source, "marketplace:"):
		t = 0.6 // listed by Anthropic's community or topic marketplaces
	case e.Verified:
		t = 0.5 // the registry verified the namespace owner
	}
	if e.Popularity > 0 {
		t += math.Min(0.3, math.Log10(float64(e.Popularity)+1)/15)
	}
	if ts, err := time.Parse(time.RFC3339, e.Updated); err == nil && time.Since(ts) < 180*24*time.Hour {
		t += 0.1
	}
	if e.License != "" {
		t += 0.05
	}
	return math.Min(1, t)
}

// footprint: less executed, less network, fewer secrets is better.
func footprint(e Entry) float64 {
	f := 1.0
	switch {
	case strings.Contains(e.Runs, "npx") || strings.Contains(e.Runs, "uvx") || strings.Contains(e.Runs, "MCP"):
		f = 0.6
	case strings.Contains(e.Runs, "hooks"):
		f = 0.5
	}
	if e.Network {
		f -= 0.1
	}
	if len(e.Secrets) > 0 {
		f -= 0.2
	}
	return math.Max(0, f)
}

func reason(e Entry) string {
	var parts []string
	switch {
	case e.Official:
		parts = append(parts, "official ("+e.Source+")")
	case e.Verified:
		parts = append(parts, "registry-verified publisher")
	default:
		parts = append(parts, "listed in "+e.Source)
	}
	if e.Popularity > 0 {
		unit := "★"
		if e.Source == "npm" {
			unit = " downloads/week"
		}
		parts = append(parts, fmt.Sprintf("%d%s", e.Popularity, unit))
	}
	if len(e.Updated) >= 7 {
		parts = append(parts, "updated "+e.Updated[:7])
	}
	if len(e.Secrets) > 0 {
		parts = append(parts, "needs "+strings.Join(e.Secrets, ", "))
	}
	return strings.Join(parts, " · ")
}

// ─────────────────────────── suggestion policy ───────────────────────────

// Suggester decides whether to suggest: at most once per need per session,
// never after a dismissal in this project, and only at a turn boundary.
type Suggester struct {
	File string // per-project state: dismissed needs

	mu        sync.Mutex
	loaded    bool
	Dismissed map[string]time.Time `json:"dismissed"`
	shown     map[string]bool      // this session
	pending   *Suggestion
}

// Suggestion is a need and its top candidates (best first, preselected).
type Suggestion struct {
	Need       Need
	Candidates []Candidate
}

func (s *Suggester) load() {
	if s.loaded {
		return
	}
	s.loaded, s.Dismissed, s.shown = true, map[string]time.Time{}, map[string]bool{}
	if b, err := os.ReadFile(s.File); err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Dismissed == nil {
		s.Dismissed = map[string]time.Time{}
	}
}

// Offer queues a suggestion for the next turn boundary unless this need was
// shown this session or dismissed in this project. It reports whether it queued.
func (s *Suggester) Offer(sg Suggestion) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if len(sg.Candidates) == 0 || s.shown[sg.Need.Key] || !s.Dismissed[sg.Need.Key].IsZero() {
		return false
	}
	s.shown[sg.Need.Key] = true
	s.pending = &sg
	return true
}

// Take returns the queued suggestion (at a turn boundary), once.
func (s *Suggester) Take() *Suggestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	s.pending = nil
	return p
}

// Dismiss remembers, for this project, not to suggest for this need again.
func (s *Suggester) Dismiss(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.Dismissed[key] = time.Now()
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.File), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.File, b, 0o600)
}

// Service ties detection, the catalog and the suggestion policy together.
type Service struct {
	Catalog   *Catalog
	Detector  *Detector
	Suggester *Suggester
}

// AfterTurn looks at a finished turn and returns a suggestion to show, or nil.
func (s *Service) AfterTurn(ctx context.Context, t Turn) *Suggestion {
	n, ok := s.Detector.Detect(ctx, t)
	if !ok {
		return nil
	}
	if !s.Suggester.Offer(Suggestion{Need: n, Candidates: s.Catalog.Search(n, 3)}) {
		return nil
	}
	return s.Suggester.Take()
}
