package status

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/surface"
)

type fakeAgent struct {
	cur *discover.Model
	l   agent.Ledger
	u   agent.ContextUse
}

func (f *fakeAgent) Ledger() agent.Ledger      { return f.l }
func (f *fakeAgent) Current() *discover.Model  { return f.cur }
func (f *fakeAgent) Context() agent.ContextUse { return f.u }

var (
	local  = &discover.Provider{ID: "ollama", Name: "Ollama", Local: true}
	remote = &discover.Provider{ID: "anthropic", Name: "Anthropic"}
)

func models() []*discover.Model {
	return []*discover.Model{
		{Provider: local, ProvID: "ollama", ID: "qwen3.6", Ctx: 32000, Tier: 3, Tools: true, Basis: "measured", GPU: 0.16,
			Measure: &discover.Measurement{Tier: 3, Baitable: true, TrustClean: 3, TrustNeeded: 20}},
		{Provider: local, ProvID: "ollama", ID: "glm-5.1:cloud", Cloud: true, Ctx: 128000, Tier: 3, Tools: true, Basis: "name", GPU: -1},
		{Provider: remote, ProvID: "anthropic", ID: "claude-x", Ctx: 200000, Tier: 3, Tools: true, Priced: true, In: 3, Out: 15, Basis: "name", GPU: -1},
	}
}

func core(t *testing.T) (*Core, *discover.Router, *fakeAgent) {
	t.Helper()
	r := discover.NewRouter()
	ms := models()
	r.SetModels(ms)
	a := &fakeAgent{cur: ms[0], l: agent.Ledger{Cost: 0.42, Turns: 3, Usage: llm.Usage{In: 100, CacheRead: 300}},
		u: agent.ContextUse{System: 1000, Tools: 2000, User: 500, Window: 32000}}
	conns := []discover.Connection{
		{ID: "ollama", Label: "Ollama (local)", Kind: "daemon", State: "connected", Models: 1},
		{ID: "ollama-cloud", Label: "Ollama Cloud", Kind: "daemon", State: "connected", Models: 1},
		{ID: "anthropic", Label: "Anthropic", Kind: "api-key", How: "ANTHROPIC_API_KEY", State: "connected", Models: 1,
			Quota: &discover.Quota{Used: 1, Limit: 10, Unit: "USD", Source: "test"}},
	}
	return New(r, a, conns, nil), r, a
}

func TestSnapshot(t *testing.T) {
	c, r, _ := core(t)
	if _, err := r.Pin("anthropic/claude-x"); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if len(s.Models) != 3 || len(s.Connections) != 3 {
		t.Fatalf("%d models, %d connections", len(s.Models), len(s.Connections))
	}
	q := s.Models[0]
	if q.Key != "ollama/qwen3.6" || !q.Local || q.Connection != "ollama" || q.GPU != 0.16 || q.TierBasis != "measured" ||
		q.Trust != (surface.Trust{Lost: true, CleanStreak: 3, Needed: 20, Measured: true}) {
		t.Errorf("qwen row: %+v", q)
	}
	if g := s.Models[1]; g.Connection != "ollama-cloud" || g.Local || g.Trust.Measured {
		t.Errorf("cloud row: %+v", g)
	}
	if a := s.Models[2]; !a.Pinned || a.Price == "" || s.Models[0].Pinned {
		t.Errorf("pin: %+v", a)
	}
	if s.Routing.Current != "ollama/qwen3.6" || s.Routing.Version != "v1" {
		t.Errorf("routing: %+v", s.Routing)
	}
	if m := s.Meter; m.ContextUsed != 3500 || m.ContextMax != 32000 || m.CostUSD != 0.42 || m.Turns != 3 || m.CacheRate != 0.75 {
		t.Errorf("meter: %+v", m)
	}
	if k := s.Connections[2]; k.Quota == nil || k.Quota.Limit != 10 || k.How != "ANTHROPIC_API_KEY" {
		t.Errorf("connection: %+v", k)
	}
}

// A model on hold after a quota hit shows on its connection, with or
// without an official quota signal.
func TestExhaustedShowsOnConnection(t *testing.T) {
	c, r, _ := core(t)
	until := time.Now().Add(time.Hour).Round(time.Second)
	r.MarkExhausted("ollama/glm-5.1:cloud", until)
	r.MarkExhausted("anthropic/claude-x", until)
	for _, k := range c.Snapshot().Connections {
		switch k.ID {
		case "ollama-cloud":
			if k.Quota == nil || !k.Quota.ExhaustedUntil.Equal(until) || !strings.Contains(k.Quota.Source, "fails over") {
				t.Errorf("cloud: %+v", k.Quota)
			}
		case "anthropic":
			if k.Quota == nil || !k.Quota.ExhaustedUntil.Equal(until) || k.Quota.Limit != 10 {
				t.Errorf("anthropic: %+v", k.Quota)
			}
		case "ollama":
			if k.Quota != nil {
				t.Errorf("local: %+v", k.Quota)
			}
		}
	}
}

func TestRoutingWhyAndSwitch(t *testing.T) {
	c, _, _ := core(t)
	ms := models()
	c.Observe(agent.Event{Kind: agent.EvModel, Model: ms[0], Reason: "cheapest sufficient"})
	if r := c.Snapshot().Routing; r.Why != "cheapest sufficient" || r.Switched != "" {
		t.Fatalf("%+v", r)
	}
	c.Observe(agent.Event{Kind: agent.EvModel, Model: ms[2], Reason: "failover"})
	if r := c.Snapshot().Routing; r.Why != "cheapest sufficient" || r.Switched != "failover" {
		t.Fatalf("%+v", r)
	}
	c.Observe(agent.Event{Kind: agent.EvDone})
	c.Observe(agent.Event{Kind: agent.EvModel, Model: ms[1], Reason: "next turn"})
	if r := c.Snapshot().Routing; r.Why != "next turn" || r.Switched != "" {
		t.Fatalf("new turn: %+v", r)
	}
}

func TestChangesCoalesced(t *testing.T) {
	c, _, _ := core(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := c.Changes(ctx)
	for range 5 {
		c.Observe(agent.Event{Kind: agent.EvUsage})
	}
	if err := c.Pin("claude-x"); err != nil {
		t.Fatal(err)
	}
	<-ch
	select {
	case <-ch:
		t.Fatal("notifications not coalesced")
	default:
	}
	if err := c.Pin("nope-nothing"); err == nil {
		t.Fatal("pinning an unknown model succeeded")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("notified after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("not closed")
	}
}

func TestExplain(t *testing.T) {
	c, _, _ := core(t)
	e := c.Explain(0, 0)
	if e.Difficulty != 2 || e.ContextTokens != 3500 || len(e.Rows) == 0 {
		t.Fatalf("%+v", e)
	}
	seenIneligible := false
	for _, r := range e.Rows {
		if !r.Eligible {
			seenIneligible = true
		} else if seenIneligible {
			t.Fatalf("eligible after ineligible: %+v", e.Rows)
		}
	}
}

func TestReconnect(t *testing.T) {
	c, r, _ := core(t)
	_, _ = r.Pin("anthropic/claude-x")
	calls := 0
	state := "connected"
	c.rediscover = func(ctx context.Context) ([]*discover.Model, []discover.Connection, []string) {
		calls++
		return models(), []discover.Connection{{ID: "anthropic", Label: "Anthropic", State: state, Detail: "the key in ANTHROPIC_API_KEY was refused (HTTP 401)"}}, nil
	}
	if err := c.Reconnect(context.Background(), "anthropic"); err != nil || calls != 1 {
		t.Fatalf("reconnect: %v", err)
	}
	if p := r.Pinned(); p == nil || p.Key() != "anthropic/claude-x" {
		t.Fatalf("pin lost: %v", p)
	}
	state = "error"
	if err := c.Reconnect(context.Background(), "anthropic"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("failed reconnect: %v", err)
	}
	if err := c.Reconnect(context.Background(), "openai"); err == nil || !strings.Contains(err.Error(), "isn't configured") {
		t.Fatalf("unknown id: %v", err)
	}
	if len(c.Snapshot().Connections) != 1 {
		t.Fatal("connections not replaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Reconnect(ctx, "anthropic"); err == nil {
		t.Fatal("cancelled reconnect succeeded")
	}
	c.rediscover = nil
	if err := c.Reconnect(context.Background(), "anthropic"); err == nil {
		t.Fatal("reconnect without discovery succeeded")
	}
}

// ADR 021 §4, the adapter side: real discovery with keys in the
// environment (and hostile provider error bodies) yields snapshots that
// carry no key, token or provider text.
func TestSnapshotRedaction(t *testing.T) {
	const secretA, secretB, secretO = "sk-ant-api03-REDACTME-AAAA", "sk-proj-REDACTME-BBBB", "ollama-REDACTME-CCCC"
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":"echo-%s"}]}`, "model") // a provider can't make ternly echo the key
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprintf(w, `{"error":"key %s rejected <script>alert(1)</script>"}`, r.Header.Get("Authorization"))
	}))
	defer bad.Close()
	bal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprintf(w, "bad token %s", r.Header.Get("Authorization"))
	}))
	defer bal.Close()
	defer func(u string) { discover.OllamaCloudAPI = u }(discover.OllamaCloudAPI)
	discover.OllamaCloudAPI = bal.URL + "/api"
	keys := map[string]string{"A_KEY": secretA, "B_KEY": secretB, "OLLAMA_API_KEY": secretO, "OLLAMA_HOST": "127.0.0.1:1"}
	ms, conns, _ := discover.DiscoverAll(context.Background(), discover.Options{CacheDir: t.TempDir(), Keys: keys, Hardware: &discover.Hardware{},
		Extra: []discover.Provider{
			{ID: "a", Name: "A", Kind: "openai", BaseURL: ok.URL, EnvKeys: []string{"A_KEY"}},
			{ID: "b", Name: "B", Kind: "openai", BaseURL: bad.URL, EnvKeys: []string{"B_KEY"}},
		}})
	r := discover.NewRouter()
	r.SetModels(ms)
	c := New(r, &fakeAgent{}, conns, nil)
	b, _ := json.Marshal(c.Snapshot())
	b2, _ := json.Marshal(c.Explain(2, 1000))
	all := string(b) + string(b2)
	for _, s := range []string{"REDACTME", "Bearer", "<script>", "rejected"} {
		if strings.Contains(all, s) {
			t.Errorf("snapshot carries %q:\n%s", s, all)
		}
	}
	if !strings.Contains(all, `"How":"A_KEY"`) || !strings.Contains(all, "B_KEY was refused (HTTP 403)") {
		t.Errorf("expected the env var names and ternly's wording:\n%s", all)
	}
}

// ADR 021 §3: Snapshot ≤ 50 µs with 50 models and 10 connections.
func BenchmarkSnapshot(b *testing.B) {
	r := discover.NewRouter()
	var ms []*discover.Model
	for i := range 50 {
		p := remote
		if i%3 == 0 {
			p = local
		}
		ms = append(ms, &discover.Model{Provider: p, ProvID: p.ID, ID: fmt.Sprintf("m%d", i), Ctx: 128000, Tier: 1 + i%3, Tools: true,
			Priced: true, In: 1, Out: 4, Measure: &discover.Measurement{Tier: 2, TrustNeeded: 20}})
	}
	r.SetModels(ms)
	r.MarkExhausted("anthropic/m1", time.Now().Add(time.Hour))
	var conns []discover.Connection
	for i := range 10 {
		conns = append(conns, discover.Connection{ID: fmt.Sprintf("c%d", i), Label: "C", State: "connected", Quota: &discover.Quota{Unit: "USD", Limit: 10}})
	}
	c := New(r, &fakeAgent{cur: ms[0]}, conns, nil)
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Snapshot()
	}
}

func TestLines(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ls := Lines([]surface.Connection{
		{ID: "ollama", Label: "Ollama (local)", Kind: "daemon", How: "the Ollama daemon at 127.0.0.1:11434", State: "connected", Models: 1},
		{ID: "ollama-cloud", Label: "Ollama Cloud", Kind: "daemon", How: "the signed-in Ollama daemon", State: "connected", Models: 7,
			Quota: &surface.Quota{ExhaustedUntil: now.Add(time.Hour)}},
		{ID: "x", Label: "X", Kind: "api-key", How: "X_KEY", State: "connected", Models: 3, Quota: &surface.Quota{Used: 14.5, Limit: 60, Unit: "USD", ResetsAt: now.Add(48 * time.Hour)}},
		{ID: "y", Label: "Y", Kind: "api-key", How: "Y_KEY", State: "error", Detail: "the key in Y_KEY was refused (HTTP 401): check it"},
	}, []discover.CLI{{Name: "claude", Path: "/bin/claude", Why: "terms (ADR 022)"}}, now)
	all := strings.Join(ls, "\n")
	for _, want := range []string{"✓ Ollama (local) — the Ollama daemon at 127.0.0.1:11434 · 1 model · free (this machine)",
		"7 models · usage unknown", "limit reached: skipped until", "$14.50 of $60.00 used, resets", "✗ Y — Y_KEY · the key in Y_KEY was refused",
		"claude CLI found (/bin/claude), not used as a backend: terms (ADR 022)"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if l := Lines(nil, nil, now); !strings.Contains(strings.Join(l, " "), "ollama serve") {
		t.Errorf("no connections: %q", l)
	}
}
