package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// Routing v2 through the agent (ADR 018): escalation away from a free model,
// telling the user when there is nowhere to go, quota exhaustion, and speeds
// measured from real streams.

func localModel(srv, id string, tier int) *discover.Model {
	p := &discover.Provider{ID: "ollama", Kind: "openai", BaseURL: srv, Local: true}
	return &discover.Model{Provider: p, ProvID: "ollama", ID: id, Ctx: 200000, Tools: true, Priced: true, Tier: tier, GPU: 1, GPUBasis: "loaded"}
}

func cloudModel(srv, id string, tier int) *discover.Model {
	p := &discover.Provider{ID: "ollama", Kind: "openai", BaseURL: srv, Local: true} // same daemon, served from ollama.com
	return &discover.Model{Provider: p, ProvID: "ollama", ID: id, Ctx: 200000, Tools: true, Priced: true, Tier: tier, Cloud: true, GPU: -1}
}

// v2Agent is newAgent with routing v2; fast makes those models look fast
// (measured), so v2 picks them first.
func v2Agent(t *testing.T, fast []*discover.Model, ms ...*discover.Model) (*Agent, *recorder, *discover.SpeedStore) {
	t.Helper()
	a, rec := newAgent(t, "yolo", ms...)
	sp := discover.OpenSpeeds("")
	for _, m := range fast {
		for range 3 {
			sp.Observe(m.Key(), 20000, 2000, 2*time.Second, 10*time.Second) // 10k tok/s prefill, 200 tok/s
		}
	}
	a.Router.SetCostModel(&discover.CostModel{TimeValue: discover.DefaultTimeValue, Speeds: sp})
	return a, rec, sp
}

// The Phase A dead end: a free model that stops making progress could never
// be escalated from (v1 needs a higher tier or a higher price).
func TestV2EscalatesFromStuckFreeModel(t *testing.T) {
	stuck := newFake(t, reply{calls: [][2]string{call("read_file", `{"path":"a.txt"}`)}})
	strong := newFake(t, reply{text: "a.txt contains x."})
	q, g := localModel(stuck.URL, "qwen3.6:latest", 3), cloudModel(strong.URL, "glm-5.3:cloud", 3)

	a, rec := newAgent(t, "yolo", q, g) // v1: the bug
	write(t, a, "a.txt", "x\n")
	a.Run(bg, "what is in a.txt")
	if len(strong.requests()) != 0 {
		t.Fatalf("v1 escalated (%d requests to the cloud model); the test no longer reproduces the dead end", len(strong.requests()))
	}
	_ = rec

	stuck2 := newFake(t, reply{calls: [][2]string{call("read_file", `{"path":"a.txt"}`)}})
	strong2 := newFake(t, reply{text: "a.txt contains x."})
	q2, g2 := localModel(stuck2.URL, "qwen3.6:latest", 3), cloudModel(strong2.URL, "glm-5.3:cloud", 3)
	a2, rec2, _ := v2Agent(t, []*discover.Model{q2}, q2, g2)
	write(t, a2, "a.txt", "x\n")
	a2.Run(bg, "what is in a.txt")
	if len(stuck2.requests()) == 0 || len(strong2.requests()) != 1 || rec2.text(EvError) != "" {
		t.Fatalf("v2: local %d, cloud %d, errors %q", len(stuck2.requests()), len(strong2.requests()), rec2.text(EvError))
	}
}

func TestV2NowhereToEscalateTellsUser(t *testing.T) {
	stuck := newFake(t, reply{calls: [][2]string{call("read_file", `{"path":"a.txt"}`)}})
	q := localModel(stuck.URL, "qwen3.6:latest", 3)
	a, rec, _ := v2Agent(t, nil, q)
	write(t, a, "a.txt", "x\n")
	a.Run(bg, "what is in a.txt")
	st := rec.text(EvStatus)
	if n := strings.Count(st, "no stronger model is available"); n != 1 || !strings.Contains(st, "API key") {
		t.Fatalf("hint shown %d times:\n%s", n, st)
	}
}

// A subscription's 429 takes the model out of routing and fails over to a
// different provider identity: here the local model behind the same daemon.
func TestV2QuotaExhaustedFailsOver(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":"you have reached your usage limit"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(limited.Close)
	backup := newFake(t, reply{text: "done locally"})
	g, q := cloudModel(limited.URL, "glm-5.3:cloud", 3), localModel(backup.URL, "qwen3.6:latest", 3)
	a, rec, _ := v2Agent(t, []*discover.Model{g}, g, q)
	a.Run(bg, "say hi")
	if len(backup.requests()) == 0 || !strings.Contains(rec.text(EvStatus), "failing over") || rec.text(EvError) != "" {
		t.Fatalf("local %d; status %q; errors %q", len(backup.requests()), rec.text(EvStatus), rec.text(EvError))
	}
	if w := a.Router.Explanations(1000)[g]; w == nil || w.T2.Eligible || !strings.Contains(w.T2.Why, "quota exhausted") {
		t.Fatalf("the rate-limited model is still routable: %+v", w)
	}
}

// Escalation is attempted after each repeated verification failure; the
// "nowhere to go" hint is shown once per turn, not every time.
func TestV2NowhereHintOncePerTurn(t *testing.T) {
	edit := reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"x"}`)}}
	done := reply{text: "Updated a.txt."}
	f := newFake(t, edit, done, edit, done, edit, done, edit, done, edit, done)
	q := localModel(f.URL, "qwen3.6:latest", 3)
	a, rec, _ := v2Agent(t, nil, q)
	a.SetVerify("false")
	a.Run(bg, "update a.txt")
	if n := strings.Count(rec.text(EvStatus), "no stronger model is available"); n != 1 {
		t.Fatalf("hint shown %d times:\n%s\nerrors: %s", n, rec.text(EvStatus), rec.text(EvError))
	}
}

func TestV2MeasuresSpeed(t *testing.T) {
	f := newFake(t, reply{text: strings.Repeat("word ", 50)})
	f.delay = 50 * time.Millisecond
	m := cloudModel(f.URL, "glm-5.3:cloud", 3)
	a, _, sp := v2Agent(t, nil, m)
	a.Run(bg, "say hi")
	s, ok := sp.Get(m.Key())
	if !ok || s.Samples != 1 || s.PrefillTPS == 0 || s.PrefillTPS > 1000/0.05 {
		t.Fatalf("speed after one turn: %+v %v (1000 prompt tokens after ≥ 50 ms)", s, ok)
	}
	if w := a.Router.Explanations(2000)[m]; w == nil || !strings.HasPrefix(w.T2.SpeedBasis, "measured (1)") {
		t.Fatalf("explanation: %+v", w)
	}
}
