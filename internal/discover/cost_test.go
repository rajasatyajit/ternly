package discover

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic models for routing v2 (ADR 018). The owner's real model list is
// replayed in routing_replay_test.go (package main).

var (
	ollama = &Provider{ID: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Local: true}
	paid   = &Provider{ID: "openai", BaseURL: "https://api.openai.com/v1"}
)

func local(id string, tier int, gpu float64, basis string) *Model {
	return &Model{Provider: ollama, ProvID: "ollama", ID: id, Tier: tier, Tools: true, Ctx: 131072, Priced: true, GPU: gpu, GPUBasis: basis}
}

func cloud(id string, tier int) *Model {
	return &Model{Provider: ollama, ProvID: "ollama", ID: id, Tier: tier, Tools: true, Ctx: 262144, Priced: true, Cloud: true, GPU: -1}
}

func api(id string, tier int, in, out float64) *Model {
	return &Model{Provider: paid, ProvID: "openai", ID: id, Tier: tier, Tools: true, Ctx: 400000, Priced: true, In: in, Out: out, GPU: -1}
}

func measured(m *Model, lo float64) *Model {
	m.Measure = &Measurement{Tier: m.Tier, Runs: 3, PassLo: lo}
	return m
}

func router(c *CostModel, ms ...*Model) *Router {
	r := NewRouter()
	r.SetModels(ms)
	r.SetCostModel(c)
	return r
}

func speeds(t *testing.T, kv map[string]Speed) *SpeedStore {
	s := OpenSpeeds("")
	for k, v := range kv {
		s.m[k] = &v
	}
	return s
}

var v2 = &CostModel{TimeValue: DefaultTimeValue, TurnLimit: 30 * time.Minute}

// A slow local model must not win just because it's free (the Phase A bug);
// a fast one on a GPU should (the same rule, on another machine).
func TestSlowLocalLosesFastLocalWins(t *testing.T) {
	slow := measured(local("qwen3.6:latest", 3, 0.16, "loaded"), 0.85)
	cl := cloud("glm-5.3:cloud", 3)
	for d := 1; d <= 3; d++ {
		if m, why := router(v2, slow, cl).PickFor(d, 20000, 36000, nil); m != cl {
			t.Errorf("T%d: slow local (16%% GPU) → %s (%s)", d, m.ID, why)
		}
	}
	fast := measured(local("qwen3-coder:30b", 2, 1, "loaded"), 0.80)
	c := &CostModel{TimeValue: DefaultTimeValue, Speeds: speeds(t, map[string]Speed{"ollama/qwen3-coder:30b": {PrefillTPS: 3000, GenTPS: 90, BaseS: 0.2, Samples: 5}})}
	for d := 1; d <= 2; d++ {
		if m, why := router(c, fast, cl).PickFor(d, 20000, 36000, nil); m != fast {
			t.Errorf("T%d: fast local (100%% GPU, 90 tok/s) lost to %s (%s)", d, m.ID, why)
		}
	}
}

// λ = 0 is price-only routing: v2's pick equals v1's on random model sets.
func TestLambdaZeroEqualsV1(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 500 {
		var ms []*Model
		for j := range 2 + rng.IntN(10) {
			tier := 1 + rng.IntN(3)
			var m *Model
			switch rng.IntN(4) {
			case 0:
				m = local(fmt.Sprintf("l%d", j), tier, rng.Float64(), "estimated")
			case 1:
				m = cloud(fmt.Sprintf("c%d", j), tier)
			case 2:
				m = api(fmt.Sprintf("a%d", j), tier, rng.Float64()*15, rng.Float64()*60)
			default:
				m = api(fmt.Sprintf("u%d", j), tier, 0, 0)
				m.Priced = false
			}
			if rng.IntN(3) == 0 {
				measured(m, rng.Float64())
			}
			ms = append(ms, m)
		}
		v1, v0 := router(nil, ms...), router(&CostModel{TimeValue: 0}, ms...)
		for d := 1; d <= 3; d++ {
			a, _ := v1.Pick(d, 1000)
			b, _ := v0.PickFor(d, 50000, 1000, nil)
			if a != b {
				t.Fatalf("set %d T%d: v1 %s, v2(λ=0) %s", i, d, a.Key(), b.Key())
			}
		}
	}
}

func TestHysteresisKeepsPrevious(t *testing.T) {
	a, b := cloud("glm-5.3:cloud", 3), cloud("kimi-k3:cloud", 3)
	c := &CostModel{TimeValue: DefaultTimeValue, Speeds: speeds(t, map[string]Speed{
		"ollama/glm-5.3:cloud": {GenTPS: 60, PrefillTPS: 4000, BaseS: 1.5, Samples: 3},
		"ollama/kimi-k3:cloud": {GenTPS: 70, PrefillTPS: 4000, BaseS: 1.5, Samples: 3}, // ~10% faster
	})}
	r := router(c, a, b)
	if m, _ := r.PickFor(2, 20000, 36000, nil); m != b {
		t.Fatalf("no previous model: %s, want the faster", m.ID)
	}
	if m, why := r.PickFor(2, 20000, 36000, a); m != a || !strings.Contains(why, "kept") {
		t.Fatalf("previous within 25%%: %s (%s), want it kept", m.ID, why)
	}
	c.Speeds.m["ollama/kimi-k3:cloud"].GenTPS = 200 // now far better
	if m, _ := r.PickFor(2, 20000, 36000, a); m != b {
		t.Fatalf("previous far worse: kept %s", m.ID)
	}
}

func TestEscalateFromFreeModelMoves(t *testing.T) {
	q := measured(local("qwen3.6:latest", 3, 0.16, "loaded"), 0.85)
	cl := cloud("glm-5.3:cloud", 3)
	if _, ok := router(nil, q, cl).Escalate(q, 1000); ok {
		t.Fatal("v1 escalated from a free T3 model; the test no longer shows the old dead end")
	}
	up, ok, _ := router(v2, q, cl).EscalateFor(q, 3, 20000, 36000, nil)
	if !ok || up != cl {
		t.Fatalf("v2 escalate → %v %v", up, ok)
	}
	if _, ok, hint := router(v2, q, cl).EscalateFor(q, 3, 20000, 36000, map[string]bool{cl.Key(): true}); ok || !strings.Contains(hint, "API key") {
		t.Fatalf("all tried: ok=%v hint=%q", ok, hint)
	}
}

func TestFailoverIdentity(t *testing.T) {
	q := local("qwen3.6:latest", 3, 0.16, "loaded")
	cl := cloud("glm-5.3:cloud", 3)
	if Identity(q) == Identity(cl) {
		t.Fatal("local Ollama and Ollama Cloud share an identity")
	}
	if _, ok := router(nil, q, cl).Failover(q, 1000); ok {
		t.Fatal("v1 failed over between the same daemon's models; the test no longer shows the old behaviour")
	}
	if alt, ok := router(v2, q, cl).FailoverFor(q, 2, 20000, 36000, nil); !ok || alt != cl {
		t.Fatalf("v2 failover from local → %v %v", alt, ok)
	}
	cl2 := cloud("kimi-k3:cloud", 3)
	if alt, ok := router(v2, q, cl, cl2).FailoverFor(cl, 2, 20000, 36000, nil); ok && alt.Cloud {
		t.Fatalf("failover from Ollama Cloud went to Ollama Cloud (%s): same provider, same outage", alt.ID)
	}
}

func TestUtilityLocalOnlyFullyOnGPU(t *testing.T) {
	part := local("qwen3.6:latest", 3, 0.84, "loaded")
	full := local("gemma4:e4b", 2, 1, "loaded")
	cl := cloud("glm-5.3:cloud", 3)
	if u := router(v2, part, cl).Utility(4000); u != cl {
		t.Errorf("84%% on GPU → %s; want cloud", u.ID)
	}
	if u := router(v2, part, full, cl).Utility(4000); u != full {
		t.Errorf("a T2 model fully on the GPU → %s; want it", u.ID)
	}
	if u := router(v2, part).Utility(4000); u != part {
		t.Errorf("nothing else → %v; want the local model", u)
	}
}

func TestSuccessPWilsonFloorAndGap(t *testing.T) {
	m := measured(local("x", 3, 1, "loaded"), 0.85)
	m.Measure.Pass = 0.97 // the raw rate must not be used
	if p, _ := successP(m, 3, 0); p != 0.85 {
		t.Errorf("p = %v, want the lower bound 0.85", p)
	}
	if p, _ := successP(m, 3, 0); p == m.Measure.Pass {
		t.Error("raw rate used")
	}
	low := measured(local("y", 1, 1, "loaded"), 0.02)
	if p, b := successP(low, 3, 0); p != pFloor || !strings.Contains(b, "floored") {
		t.Errorf("p = %v (%s), want the floor", p, b)
	}
	u := cloud("glm-5.1:cloud", 2)
	if p, _ := successP(u, 3, 0); p != priorP[2]/2 {
		t.Errorf("T2 prior on a T3 task: %v", p)
	}
	if p3, _ := successP(cloud("t3", 3), 3, 0); p3 >= 0.80 {
		t.Errorf("an unmeasured T3 prior (%v) must sit below a measured T3's lowest lower bound (0.80)", p3)
	}
}

func TestTimeLimit(t *testing.T) {
	slow := local("big:70b", 3, 0, "estimated")
	c := &CostModel{TimeValue: DefaultTimeValue, TurnLimit: 2 * time.Minute}
	e := router(c, slow).Explain(3, 60000, 1000)
	if e[0].Eligible || !e[0].OverTime {
		t.Fatalf("%+v", e[0])
	}
	if m, _ := router(c, slow).PickFor(3, 60000, 1000, nil); m != slow {
		t.Fatal("only an over-time model: it should still run (slow beats nothing)")
	}
	nofit := local("huge:405b", 3, 0, "estimated")
	nofit.NoFit = true
	if m, _ := router(c, nofit, slow).PickFor(3, 60000, 1000, nil); m != slow {
		t.Fatalf("a model that can't load was picked: %s", m.ID)
	}
}

func TestQuotaExhausted(t *testing.T) {
	q := measured(local("qwen3.6:latest", 3, 0.16, "loaded"), 0.85)
	cl := cloud("glm-5.3:cloud", 3)
	r := router(v2, q, cl)
	r.MarkExhausted(cl.Key(), time.Now().Add(time.Hour))
	if m, _ := r.PickFor(3, 20000, 36000, nil); m != q {
		t.Fatalf("exhausted cloud picked: %s", m.ID)
	}
	r.MarkExhausted(cl.Key(), time.Now().Add(-time.Second))
	if m, _ := r.PickFor(3, 20000, 36000, nil); m != cl {
		t.Fatalf("quota back: %s", m.ID)
	}
}

func TestPaidModelCostsMoney(t *testing.T) {
	cheap := api("gpt-5-mini", 3, 0.25, 2)
	dear := api("gpt-5-pro", 3, 15, 120)
	if m, _ := router(v2, dear, cheap).PickFor(2, 20000, 36000, nil); m != cheap {
		t.Fatalf("equal speed and p: the dearer model won (%s)", m.ID)
	}
	if e := router(v2, dear).Explain(2, 20000, 1000)[0]; e.Money <= 0 {
		t.Fatalf("money term %v", e.Money)
	}
}

func TestFamilyRanks(t *testing.T) {
	ms := []*Model{cloud("glm-5.1:cloud", 3), cloud("glm-5.3:cloud", 3), cloud("kimi-k2.6:cloud", 3), cloud("kimi-k3:cloud", 3),
		cloud("kimi-k2.7-code:cloud", 3), local("gemma4:31b", 1, 0, ""), local("gemma4:26b", 1, 0, ""), cloud("minimax-m2.7:cloud", 3), cloud("minimax-m3:cloud", 3)}
	got := familyRanks(ms)
	want := map[string]int{"glm-5.1:cloud": 1, "kimi-k2.6:cloud": 1, "minimax-m2.7:cloud": 1}
	for _, m := range ms {
		if got[m] != want[m.ID] {
			t.Errorf("%s: %d newer, want %d", m.ID, got[m], want[m.ID])
		}
	}
}

func TestSpeedStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "speed.json")
	s := OpenSpeeds(p)
	s.Observe("ollama/q", 20000, 400, 63*time.Second, 12*time.Second) // ADR 018's qwen3.6 numbers
	s.Observe("ollama/q", 200, 10, 300*time.Millisecond, 0)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	v, ok := OpenSpeeds(p).Get("ollama/q")
	if !ok || v.Samples != 2 || int(v.PrefillTPS) != 317 || int(v.GenTPS) != 33 || v.BaseS != 0.3 {
		t.Fatalf("%+v", v)
	}
	// another version is ignored, never misread
	_ = os.WriteFile(p, []byte(`{"version":99,"models":{"ollama/q":{"gen_tps":1}}}`), 0o600)
	if _, ok := OpenSpeeds(p).Get("ollama/q"); ok {
		t.Fatal("a file of another version was read")
	}
	_ = os.WriteFile(p, []byte(`not json`), 0o600)
	if _, ok := OpenSpeeds(p).Get("ollama/q"); ok {
		t.Fatal("garbage was read")
	}
}

func TestHardware(t *testing.T) {
	if v := nvidiaVRAM("6144\n24576\n"); v != 24576<<20 {
		t.Errorf("nvidia %d", v)
	}
	drm := t.TempDir()
	_ = os.MkdirAll(filepath.Join(drm, "card1", "device"), 0o755)
	_ = os.WriteFile(filepath.Join(drm, "card1", "device", "mem_info_vram_total"), []byte("17163091968\n"), 0o644)
	if v := amdVRAM(drm); v != 17163091968 {
		t.Errorf("amd %d", v)
	}
	mi := filepath.Join(t.TempDir(), "meminfo")
	_ = os.WriteFile(mi, []byte("MemTotal:       64000000 kB\nMemAvailable:   43372008 kB\n"), 0o644)
	if v := memAvailable(mi); v != 43372008<<10 {
		t.Errorf("meminfo %d", v)
	}
	hw := Hardware{VRAM: 6 << 30, RAMAvail: 40 << 30}
	q := local("qwen3.6:latest", 3, 0, "")
	q.Size = 23938333577
	Place(q, hw, map[string]loaded{"qwen3.6:latest": {Size: 25103119151, VRAM: 4022400449}})
	if q.GPUBasis != "loaded" || q.GPU < 0.15 || q.GPU > 0.17 {
		t.Errorf("loaded placement %v %s (ollama ps: 16%%)", q.GPU, q.GPUBasis)
	}
	Place(q, hw, nil)
	if q.GPUBasis != "estimated" || q.GPU < 0.15 || q.GPU > 0.25 {
		t.Errorf("estimated placement %v %s", q.GPU, q.GPUBasis)
	}
	big := local("huge:405b", 3, 0, "")
	big.Size = 240 << 30
	if Fits(big, hw) || !Fits(q, hw) {
		t.Error("fits")
	}
	cl := cloud("glm-5.3:cloud", 3)
	if Place(cl, hw, nil); cl.GPU != -1 {
		t.Error("a cloud model got a placement")
	}
}

// Unmeasured models of one family: the newest is presumed best (ADR 018 §4).
func TestNewestOfFamilyPreferred(t *testing.T) {
	old, cur := cloud("glm-5.1:cloud", 3), cloud("glm-5.3:cloud", 3)
	old.Ctx = cur.Ctx * 2 // the tie-break alone would pick the older one
	if m, why := router(v2, old, cur).PickFor(3, 20000, 36000, nil); m != cur {
		t.Fatalf("picked %s (%s); want the newer glm-5.3", m.ID, why)
	}
	es := router(v2, old, cur).Explain(3, 20000, 1000)
	if es[1].Model != old || !strings.Contains(es[1].PBasis, "1 newer in its family") {
		t.Fatalf("explanation: %+v", es[1])
	}
}
