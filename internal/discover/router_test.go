package discover

import "testing"

func TestClassify(t *testing.T) {
	if d := Classify("fix the typo in README", 0); d != 1 {
		t.Errorf("easy task → %d", d)
	}
	if d := Classify("refactor the auth module and fix the race condition in the session cache", 0); d != 3 {
		t.Errorf("hard task → %d", d)
	}
	if d := Classify("fix the typo in README", 2); d != 3 {
		t.Errorf("failures must escalate → %d", d)
	}
}

func TestRouting(t *testing.T) {
	local := &Provider{ID: "ollama", Local: true}
	api := &Provider{ID: "anthropic"}
	mk := func(p *Provider, id string, in, out float64) *Model {
		m := &Model{Provider: p, ProvID: p.ID, ID: id, In: in, Out: out, Priced: true, Tools: true, Ctx: 200000}
		m.Tier = tierOf(m)
		return m
	}
	small := mk(local, "qwen2.5-coder:7b", 0, 0)
	haiku := mk(api, "claude-haiku-4-5", 1, 5)
	sonnet := mk(api, "claude-sonnet-4-5", 3, 15)
	opus := mk(api, "claude-opus-4-1", 15, 75)
	r := NewRouter()
	r.SetModels([]*Model{small, haiku, sonnet, opus})

	if small.Tier != 1 || haiku.Tier != 2 || sonnet.Tier != 3 || opus.Tier != 3 {
		t.Fatalf("tiers: %d %d %d %d", small.Tier, haiku.Tier, sonnet.Tier, opus.Tier)
	}
	if m, _ := r.Pick(1, 1000); m != small {
		t.Errorf("T1 should use free local model, got %s", m.Key())
	}
	if m, _ := r.Pick(2, 1000); m != haiku {
		t.Errorf("T2 → %s", m.Key())
	}
	if m, _ := r.Pick(3, 1000); m != sonnet {
		t.Errorf("T3 should pick cheapest frontier, got %s", m.Key())
	}
	if m, ok := r.Escalate(sonnet, 1000); !ok || m != opus {
		t.Errorf("escalate sonnet → %v", m.Key())
	}
	if _, err := r.Pin("opus"); err != nil || r.Pinned() != opus {
		t.Error("pin by substring failed")
	}
}

func TestNorm(t *testing.T) {
	if norm("claude-sonnet-4-5-20250929") != norm("anthropic/claude-sonnet-4.5") {
		t.Error("anthropic ids should normalise to the same catalog key")
	}
}
