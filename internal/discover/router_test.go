package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

func llmUsage(in, out int) llm.Usage { return llm.Usage{In: in, Out: out} }

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

	if gm := mk(api, "gpt-5-mini", 0.25, 2); gm.Tier != 2 { // "mini" still caps frontier families
		t.Errorf("gpt-5-mini tier %d", gm.Tier)
	}
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

// fakeOllama serves the three endpoints discovery reads, shaped like a real
// Ollama 0.x with one local and two cloud models (remote_host/remote_model in
// /api/tags; empty /api/show for cloud models).
func fakeOllama(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"llama3.1:8b"},{"id":"glm-5.1:cloud"},{"id":"qwen3-coder:480b-cloud"},{"id":"gpt-oss:120b-cloud"},{"id":"nemotron-3-ultra:cloud"},{"id":"gemma4:cloud"},{"id":"deepseek-v4.1-flash:cloud"},{"id":"minimax-m3:cloud"}]}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3.1:8b"},
				{"name":"glm-5.1:cloud","remote_model":"glm-5.1","remote_host":"https://ollama.com:443"},
				{"name":"qwen3-coder:480b-cloud","remote_model":"qwen3-coder:480b","remote_host":"https://ollama.com:443"},
				{"name":"gpt-oss:120b-cloud","remote_model":"gpt-oss:120b","remote_host":"https://ollama.com","details":{"parameter_size":"117B"}},
				{"name":"nemotron-3-ultra:cloud","remote_model":"nemotron-3-ultra","remote_host":"https://ollama.com","details":{"parameter_size":"550B"}},
				{"name":"gemma4:cloud","remote_model":"gemma4:31b","remote_host":"https://ollama.com","details":{"parameter_size":"32.7B"}},
				{"name":"deepseek-v4.1-flash:cloud","remote_model":"deepseek-v4.1-flash","remote_host":"https://ollama.com","details":{"parameter_size":"763B"}},
				{"name":"minimax-m3:cloud","remote_model":"minimax-m3","remote_host":"https://ollama.com","details":{"parameter_size":"0"}}]}`)
		case "/api/show":
			var b struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Model == "llama3.1:8b" {
				fmt.Fprint(w, `{"capabilities":["completion","tools"],"model_info":{"llama.context_length":131072}}`)
				return
			}
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaCloudModels(t *testing.T) {
	srv := fakeOllama(t)
	cache := t.TempDir()
	_ = os.WriteFile(filepath.Join(cache, "catalog.json"), []byte(`{"glm-5-1":{"Ctx":200000,"Tools":true,"In":1,"Out":3}}`), 0o600)
	opts := Options{CacheDir: cache, Keys: map[string]string{"OLLAMA_HOST": srv.URL}}
	ms, _ := Discover(context.Background(), opts)
	by := map[string]*Model{}
	for _, m := range ms {
		if m.ProvID == "ollama" {
			by[m.ID] = m
		}
	}
	// Shapes seen on 2026-10-05: remote_host without :443, parameter_size in details.
	for id, want := range map[string]int{"gpt-oss:120b-cloud": 2, "nemotron-3-ultra:cloud": 3, "gemma4:cloud": 1, "deepseek-v4.1-flash:cloud": 2, "minimax-m3:cloud": 3} {
		m := by[id]
		if m == nil || !m.Cloud || m.Local() || m.Free() || m.Tier != want {
			t.Errorf("%s: %+v, want cloud tier %d", id, m, want)
		}
	}
	local, glm, qwen := by["llama3.1:8b"], by["glm-5.1:cloud"], by["qwen3-coder:480b-cloud"]
	if local == nil || glm == nil || qwen == nil {
		t.Fatalf("models: %v", ms)
	}
	if !local.Local() || local.Cloud || !local.Free() || Price(local) != "local" {
		t.Errorf("local model misclassified: %+v", local)
	}
	for _, m := range []*Model{glm, qwen} {
		if !m.Cloud || m.Local() || m.Free() || Price(m) != "cloud·quota" || m.Cost(llmUsage(1000, 100)) != 0 {
			t.Errorf("%s: cloud=%v local=%v free=%v price=%q", m.ID, m.Cloud, m.Local(), m.Free(), Price(m))
		}
	}
	if glm.Tier != 3 || qwen.Tier != 3 {
		t.Errorf("tiers by underlying model: glm=%d qwen3-coder 480b=%d, want 3 and 3", glm.Tier, qwen.Tier)
	}
	if glm.Ctx != 200000 {
		t.Errorf("cloud context should come from the underlying model's catalog entry: %d", glm.Ctx)
	}
	opts.LocalOnly = true
	ms, _ = Discover(context.Background(), opts)
	for _, m := range ms {
		if m.Cloud {
			t.Errorf("--local-only kept a cloud model: %s", m.ID)
		}
	}
	// Routing: free local first, then prepaid cloud quota, then pay-per-token.
	paid := &Model{Provider: &Provider{ID: "anthropic"}, ProvID: "anthropic", ID: "claude-sonnet-4-5", Priced: true, In: 3, Out: 15, Tools: true, Ctx: 200000, Tier: 3}
	r := NewRouter()
	r.SetModels([]*Model{paid, glm, local})
	if m, _ := r.Pick(3, 1000); m != glm {
		t.Errorf("T3 should use the cloud quota before paying: %s", m.Key())
	}
	local.Tier = 3
	if m, _ := r.Pick(3, 1000); m != local {
		t.Errorf("a free local model still comes first: %s", m.Key())
	}
}

func TestTierNameColonForms(t *testing.T) {
	for id, want := range map[string]int{"qwen3-coder:480b-cloud": 3, "qwen3-coder:30b": 2, "kimi-k2.6:cloud": 3, "deepseek-v4-flash:cloud": 2, "gpt-oss:120b-cloud": 0, "minimax-m2.7:cloud": 3} {
		m := &Model{ID: id, Cloud: strings.Contains(id, "cloud"), Provider: &Provider{Local: true}}
		if got := tierOf(m); want != 0 && got != want {
			t.Errorf("%s: tier %d, want %d (judged as %q)", id, got, want, tierName(m))
		}
	}
}

// A measured capability decides the tier (ADR 012); the name table is the
// fallback, labelled; config beats both. Memory autonomy follows the
// measurement, else "verify" for T1.
func TestMeasuredTiers(t *testing.T) {
	srv := fakeOllama(t)
	opts := Options{CacheDir: t.TempDir(), Keys: map[string]string{"OLLAMA_HOST": srv.URL},
		Measured:  map[string]Measurement{"ollama/llama3.1:8b": {Tier: 3, Autonomy: "off"}, "ollama/gemma4:cloud": {Tier: 2, Autonomy: "verify"}},
		Overrides: map[string]int{"ollama/gemma4:cloud": 1}}
	ms, _ := Discover(context.Background(), opts)
	by := map[string]*Model{}
	for _, m := range ms {
		by[m.Key()] = m
	}
	llama, gemma, glm := by["ollama/llama3.1:8b"], by["ollama/gemma4:cloud"], by["ollama/glm-5.1:cloud"]
	if llama == nil || gemma == nil || glm == nil {
		t.Fatalf("models: %v", ms)
	}
	if llama.Tier != 3 || llama.Basis != "measured" || llama.MemoryAutonomy() != "off" {
		t.Errorf("measured: tier %d basis %s autonomy %s", llama.Tier, llama.Basis, llama.MemoryAutonomy())
	}
	if gemma.Tier != 1 || gemma.Basis != "config" {
		t.Errorf("config override: tier %d basis %s", gemma.Tier, gemma.Basis)
	}
	if glm.Basis != "name" || glm.MemoryAutonomy() != "full" {
		t.Errorf("unmeasured: basis %s autonomy %s", glm.Basis, glm.MemoryAutonomy())
	}
	if (&Model{Tier: 1}).MemoryAutonomy() != "verify" {
		t.Error("unmeasured T1 should verify memory notes")
	}
}
