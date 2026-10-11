package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaGroup(t *testing.T) {
	local := &Provider{ID: "ollama", Local: true}
	for _, tc := range []struct {
		m    *Model
		want string
	}{
		{&Model{ProvID: "ollama", Provider: local}, ""},
		{&Model{ProvID: "ollama", Provider: local, Cloud: true}, "ollama:cloud"},
		{&Model{ProvID: "openrouter", Provider: &Provider{ID: "openrouter"}}, "openrouter"},
		{nil, ""},
	} {
		if g := QuotaGroup(tc.m); g != tc.want {
			t.Errorf("%+v: %q, want %q", tc.m, g, tc.want)
		}
	}
}

// A hit keeps the group exhausted for background work until the later of
// the reset and a day after the hit; another process sees it.
func TestQuotaLogExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	l := OpenQuotaLog(path)
	if ok, _ := l.Exhausted("ollama:cloud", t0); ok {
		t.Fatal("exhausted with no hit")
	}
	if err := l.Mark("ollama:cloud", "ollama/glm-5.3:cloud", t0, t0.Add(time.Hour), "429"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("quota.json: %v %v", fi.Mode(), err)
	}
	other := OpenQuotaLog(path) // another process
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{{t0.Add(2 * time.Hour), true}, {t0.Add(QuotaRecheck - time.Second), true}, {t0.Add(QuotaRecheck), false}} {
		if ok, _ := other.Exhausted("ollama:cloud", tc.at); ok != tc.want {
			t.Errorf("at +%s: exhausted %v, want %v", tc.at.Sub(t0), ok, tc.want)
		}
	}
	if ok, _ := other.Exhausted("openrouter", t0.Add(time.Minute)); ok {
		t.Error("another group exhausted")
	}
	// A known reset weeks away (the balance API) holds until then.
	reset := t0.Add(20 * 24 * time.Hour)
	_ = l.Mark("ollama:cloud", "ollama/x:cloud", t0, reset, "balance")
	if ok, q := other.Exhausted("ollama:cloud", reset.Add(-time.Minute)); !ok || !q.Reset().Equal(reset) {
		t.Errorf("before the reset: %v %v", ok, q.Reset())
	}
	if ok, _ := other.Exhausted("ollama:cloud", reset); ok {
		t.Error("exhausted after the reset")
	}
	if !other.HitSince("ollama/x:cloud", t0) || other.HitSince("ollama/x:cloud", t0.Add(time.Second)) || other.HitSince("ollama/y:cloud", t0) {
		t.Error("HitSince")
	}
}

// The router records a cloud model's quota hit by group in its log.
func TestMarkExhaustedPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	r := NewRouter()
	m := &Model{ProvID: "ollama", ID: "glm-5.3:cloud", Cloud: true, Provider: &Provider{ID: "ollama", Local: true}}
	r.SetModels([]*Model{m})
	r.SetQuotaLog(OpenQuotaLog(path))
	r.MarkExhausted(m.Key(), time.Now().Add(time.Hour))
	if ok, q := OpenQuotaLog(path).Exhausted("ollama:cloud", time.Now()); !ok || q.Model != m.Key() {
		t.Fatalf("not recorded: %v %+v", ok, q)
	}
	r2 := NewRouter() // no log: nothing written, nothing breaks
	r2.SetModels([]*Model{m})
	r2.MarkExhausted(m.Key(), time.Now().Add(time.Hour))
}
