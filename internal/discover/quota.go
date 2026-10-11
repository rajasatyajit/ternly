package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// QuotaGroup is the quota a model draws on: a subscription's cloud models
// share one (Ollama Cloud's monthly limit covers all of them), a pay-per-token
// API's models share the account's. Local models have none ("").
func QuotaGroup(m *Model) string {
	switch {
	case m == nil || m.Local():
		return ""
	case m.Cloud:
		return m.ProvID + ":cloud"
	}
	return m.ProvID
}

// QuotaRecheck is how long a quota hit with no known reset keeps background
// work off its quota (ADR 031): a monthly limit isn't probed every hour by
// work nobody waits for; a day later one request finds out.
const QuotaRecheck = 24 * time.Hour

// QuotaMark is the last quota hit of a group.
type QuotaMark struct {
	Model string    `json:"model"` // the model whose request hit it
	Seen  time.Time `json:"seen"`
	Until time.Time `json:"until,omitempty"` // the provider's reset, if it said (Retry-After, the balance API)
	Why   string    `json:"why,omitempty"`
}

// over is when the mark stops keeping background work off: the later of
// the provider's reset (or the router's cooldown) and QuotaRecheck after the
// hit. A short Retry-After (a rate limit) still keeps background work off
// for a day: nobody waits for it, and a monthly limit looks the same.
func (q QuotaMark) over() time.Time {
	if t := q.Seen.Add(QuotaRecheck); t.After(q.Until) {
		return t
	}
	return q.Until
}

// QuotaLog persists quota hits (<data>/quota.json, 0600) so every ternly
// process sees them: an evaluation's trap runs record a 429 that its driver
// and the session's scheduler then read. Small; read afresh on each check.
type QuotaLog struct {
	path string
	mu   sync.Mutex
}

// OpenQuotaLog uses the file at path ("" : in memory only, nothing kept).
func OpenQuotaLog(path string) *QuotaLog { return &QuotaLog{path: path} }

const quotaLogVersion = 1

func (l *QuotaLog) read() map[string]QuotaMark {
	out := map[string]QuotaMark{}
	if l == nil || l.path == "" {
		return out
	}
	b, err := os.ReadFile(l.path)
	if err != nil {
		return out
	}
	var f struct {
		Version int                  `json:"version"`
		Groups  map[string]QuotaMark `json:"groups"`
	}
	if json.Unmarshal(b, &f) == nil && f.Version == quotaLogVersion && f.Groups != nil {
		out = f.Groups
	}
	return out
}

// Mark records a quota hit on group by model, with the provider's reset if
// known (zero: unknown).
func (l *QuotaLog) Mark(group, model string, seen, until time.Time, why string) error {
	if l == nil || l.path == "" || group == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	gs := l.read()
	gs[group] = QuotaMark{Model: model, Seen: seen, Until: until, Why: why}
	b, err := json.MarshalIndent(map[string]any{"version": quotaLogVersion, "groups": gs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".quota-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), l.path) // CreateTemp is 0600
}

// Exhausted reports whether group's quota counts as spent at now for
// background work, and the mark that says so.
func (l *QuotaLog) Exhausted(group string, now time.Time) (bool, QuotaMark) {
	if group == "" {
		return false, QuotaMark{}
	}
	q, ok := l.read()[group]
	if !ok || !now.Before(q.over()) {
		return false, QuotaMark{}
	}
	return true, q
}

// Reset is when an exhausted mark stops counting.
func (q QuotaMark) Reset() time.Time { return q.over() }

// HitSince reports whether model hit a quota at or after t (an evaluation
// stops when one of its trap runs did).
func (l *QuotaLog) HitSince(model string, t time.Time) bool {
	for _, q := range l.read() {
		if q.Model == model && !q.Seen.Before(t) {
			return true
		}
	}
	return false
}

// SetQuotaLog makes MarkExhausted also record hits in l, by quota group.
func (r *Router) SetQuotaLog(l *QuotaLog) {
	r.mu.Lock()
	r.quotaLog = l
	r.mu.Unlock()
}

// QuotaLog is the log set by SetQuotaLog (nil: none).
func (r *Router) QuotaLog() *QuotaLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.quotaLog
}
