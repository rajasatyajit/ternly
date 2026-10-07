package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// The routing replay test (ADR 018 review): the owner's real model list,
// recorded from their Ollama daemon (internal/discover/testdata/replay-*),
// replayed through discovery and both routers. It is the evidence that v2
// fixes the Phase A bug on that machine: hard → cloud, and so on.

const replay = "internal/discover/testdata/replay-2026-10-07"

// fakeOllama serves the recorded responses.
func fakeOllama(t *testing.T, ps string) string {
	t.Helper()
	file := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(replay, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) { w.Write(file("v1-models.json")) })
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) { w.Write(file("tags.json")) })
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(ps)) })
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Write(file("show/" + strings.NewReplacer("/", "_", ":", "_").Replace(req.Model) + ".json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// replayRouter discovers the recorded machine and returns its router with
// routing v2 at λ (nil cost model: v1).
func replayRouter(t *testing.T, ps string, cost *discover.CostModel) (*discover.Router, map[string]*discover.Model) {
	t.Helper()
	var hw discover.Hardware
	if err := json.Unmarshal(must(os.ReadFile(filepath.Join(replay, "hardware.json"))), &hw); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	_ = os.WriteFile(filepath.Join(cache, "catalog.json"), []byte(`{"offline-test":{}}`), 0o600) // no network
	ms, warn := discover.Discover(context.Background(), discover.Options{
		CacheDir: cache, Keys: map[string]string{"OLLAMA_HOST": fakeOllama(t, ps)},
		Measured: measurements(t.TempDir()), // the shipped measurements, as main uses them
		Hardware: &hw,
	})
	if len(warn) > 0 || len(ms) < 20 {
		t.Fatalf("discovery: %d models, warnings %v", len(ms), warn)
	}
	r := discover.NewRouter()
	r.SetModels(ms)
	r.SetCostModel(cost)
	byID := map[string]*discover.Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	return r, byID
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

const ctx20k = 20000 // a typical agent context (ADR 018: 10–60 k)

func TestRoutingReplayV1PicksQwen(t *testing.T) {
	r, _ := replayRouter(t, `{"models":[]}`, nil)
	for d := 1; d <= 3; d++ {
		if m, _ := r.PickFor(d, ctx20k, ctx20k+16000, nil); m.ID != "qwen3.6:latest" {
			t.Errorf("v1 T%d → %s; the recorded bug is qwen3.6 for every difficulty", d, m.ID)
		}
	}
}

func TestRoutingReplayV2(t *testing.T) {
	speeds := discover.OpenSpeeds(filepath.Join(replay, "speeds.json"))
	for _, c := range []struct {
		name   string
		ps     string
		speeds *discover.SpeedStore
	}{
		{"priors only, nothing loaded", `{"models":[]}`, nil},
		{"qwen3.6 loaded (16% GPU, as measured)", `{"models":[{"name":"qwen3.6:latest","size":25103119151,"size_vram":4022400449}]}`, nil},
		{"measured speeds", `{"models":[]}`, speeds},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, by := replayRouter(t, c.ps, &discover.CostModel{TimeValue: discover.DefaultTimeValue, TurnLimit: 30 * time.Minute, Speeds: c.speeds})
			for d := 1; d <= 3; d++ {
				m, why := r.PickFor(d, ctx20k, ctx20k+16000, nil)
				if !m.Cloud {
					t.Errorf("T%d → %s (%s); want an Ollama Cloud model", d, m.Key(), why)
				}
				if d == 3 && m.Tier < 3 {
					t.Errorf("hard task → %s (T%d); want T3", m.Key(), m.Tier)
				}
				t.Logf("T%d → %s: %s", d, m.Key(), why)
			}
			qwen := by["qwen3.6:latest"]
			if u := r.Utility(4000); u == nil || u.Local() {
				t.Errorf("utility → %v; a local model only when fully on the GPU", u)
			}
			if up, ok, hint := r.EscalateFor(qwen, 2, ctx20k, ctx20k+16000, nil); !ok || up == qwen || !up.Cloud {
				t.Errorf("escalate from qwen → %v %v (%s): the v1 dead end", up, ok, hint)
			}
			if alt, ok := r.FailoverFor(qwen, 2, ctx20k, ctx20k+16000, nil); !ok || !alt.Cloud {
				t.Errorf("failover from local qwen → %v %v; local Ollama and Ollama Cloud are different providers", alt, ok)
			}
		})
	}
}

// With the cloud quota gone, the local model is what's left, and escalation
// says what would help instead of silently staying put.
func TestRoutingReplayQuotaExhausted(t *testing.T) {
	r, by := replayRouter(t, `{"models":[]}`, &discover.CostModel{TimeValue: discover.DefaultTimeValue})
	for _, m := range r.Models() {
		if m.Cloud {
			r.MarkExhausted(m.Key(), time.Now().Add(time.Hour))
		}
	}
	m, why := r.PickFor(3, ctx20k, ctx20k+16000, nil)
	if !m.Local() || m.ID != "qwen3.6:latest" {
		t.Fatalf("quota exhausted → %s (%s); want the strongest local model", m.Key(), why)
	}
	up, ok, _ := r.EscalateFor(by["qwen3.6:latest"], 3, ctx20k, ctx20k+16000, map[string]bool{})
	if ok && up.Tier >= 3 {
		t.Fatalf("escalated to %s with every T3 cloud model exhausted", up.Key())
	}
	tried := map[string]bool{}
	for _, m := range r.Models() {
		tried[m.Key()] = true
	}
	if _, ok, hint := r.EscalateFor(by["qwen3.6:latest"], 3, ctx20k, ctx20k+16000, tried); ok || !strings.Contains(hint, "quota resets") || !strings.Contains(hint, "API key") {
		t.Fatalf("nowhere to go: ok=%v hint=%q", ok, hint)
	}
}

// λ = 0 reproduces price-only routing (ADR 018 review, decision 1): on the
// recorded machine, at every difficulty and context size.
func TestRoutingReplayLambdaZeroIsV1(t *testing.T) {
	v1, _ := replayRouter(t, `{"models":[]}`, nil)
	v0, _ := replayRouter(t, `{"models":[]}`, &discover.CostModel{TimeValue: 0})
	for d := 1; d <= 3; d++ {
		for _, ctx := range []int{0, 2000, 20000, 100000} {
			a, _ := v1.PickFor(d, ctx, ctx+16000, nil)
			b, _ := v0.PickFor(d, ctx, ctx+16000, nil)
			if a.Key() != b.Key() {
				t.Errorf("T%d ctx %d: v1 %s, v2 at λ=0 %s", d, ctx, a.Key(), b.Key())
			}
		}
	}
}

func TestRoutingConfig(t *testing.T) {
	for _, c := range []struct {
		json, flag string
		v2         bool
		lambda     float64
		bad        bool
	}{
		{`{}`, "", defaultRouting == "v2", discover.DefaultTimeValue, false},
		{`{"routing":"v1"}`, "", false, 0, false},
		{`{"routing":"v2"}`, "", true, discover.DefaultTimeValue, false},
		{`{"routing":{"version":"v2","time_value_usd_per_hour":0}}`, "", true, 0, false},
		{`{"routing":{"version":"v2","time_value_usd_per_hour":45}}`, "", true, 45, false},
		{`{"routing":"v2"}`, "v1", false, 0, false}, // the flag wins
		{`{"routing":"v1"}`, "v2", true, discover.DefaultTimeValue, false},
		{`{"routing":"v3"}`, "", false, 0, true},
		{`{"routing":{"version":"v2","time_value_usd_per_hour":-1}}`, "", false, 0, true},
	} {
		var fc fileConfig
		if err := json.Unmarshal([]byte(c.json), &fc); err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		cm, err := fc.Routing.costModel(c.flag, t.TempDir(), 30*time.Minute)
		if (err != nil) != c.bad || (cm != nil) != c.v2 || cm != nil && cm.TimeValue != c.lambda {
			t.Errorf("%s flag=%q: cm=%+v err=%v", c.json, c.flag, cm, err)
		}
	}
}

func TestWatchdogSetting(t *testing.T) {
	for in, want := range map[string]watchdogSetting{
		`{"reasoning_watchdog": 6000}`:                          {Tokens: 6000, legacy: true}, // a v0.1 config keeps working
		`{"reasoning_watchdog": -1}`:                            {Tokens: -1, legacy: true},
		`{"reasoning_watchdog": {"tokens": 8000}}`:              {Tokens: 8000},
		`{"reasoning_watchdog": {"seconds": 120}}`:              {Seconds: 120},
		`{"reasoning_watchdog": {"tokens": -1, "seconds": 60}}`: {Tokens: -1, Seconds: 60},
	} {
		var fc fileConfig
		if err := json.Unmarshal([]byte(in), &fc); err != nil || fc.ReasoningWatchdog == nil || *fc.ReasoningWatchdog != want {
			t.Errorf("%s: %+v %v", in, fc.ReasoningWatchdog, err)
		}
	}
}
