package agent

import (
	"fmt"
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

// Issue #2: the token limit means the same whatever the chunk size. One
// provider sends 40-byte chunks, another 4-byte ones; both trip at ~100
// tokens (stream chunks would have meant 10x apart).
func TestWatchdogTokensNotChunks(t *testing.T) {
	for _, c := range []struct {
		name          string
		chunks, bytes int
	}{{"many small chunks", 500, 4}, {"few big chunks", 50, 40}} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, reply{reasoning: c.chunks, chunk: c.bytes, text: "never seen"}, reply{text: "done"})
			m := model(f.URL, "glm-5.3:cloud", 3, 1, 5)
			m.Reasoning = true
			a, rec := newAgent(t, "yolo", m)
			a.Watchdog = Watchdog{Tokens: 100, Idle: -1}
			a.Run(bg, "say hi")
			st := rec.text(EvStatus)
			var tok int
			if _, err := fmt.Sscanf(st[strings.Index(st, "~"):], "~%d tokens", &tok); err != nil || tok < 100 || tok > 100+c.bytes/4 {
				t.Fatalf("tripped at %d tokens (%v): %q", tok, err, st)
			}
			if a.Stats().Watchdog != 1 || !strings.Contains(rec.text(EvText), "done") {
				t.Fatalf("stats %+v text %q", a.Stats(), rec.text(EvText))
			}
		})
	}
}

// Issue #2: seconds trip a step that streams reasoning slowly, and one that
// sends nothing at all (a long prefill), with the token limit off.
func TestWatchdogSeconds(t *testing.T) {
	slow := newFake(t, reply{reasoning: 100, gap: 50 * time.Millisecond, text: "never seen"}, reply{text: "done"})
	stall := newFake(t, reply{text: "never seen"}, reply{text: "done"})
	stall.delay = 3 * time.Second // every request; the retry isn't watchdogged (once per turn)
	for name, f := range map[string]*fakeLLM{"slow reasoning": slow, "nothing at all": stall} {
		t.Run(name, func(t *testing.T) {
			m := model(f.URL, "glm-5.3:cloud", 3, 1, 5)
			m.Reasoning = true
			a, rec := newAgent(t, "yolo", m)
			a.Watchdog = Watchdog{Tokens: -1, Idle: time.Second}
			t0 := time.Now()
			a.Run(bg, "say hi")
			if !strings.Contains(rec.text(EvStatus), "no text or tool call after") || a.Stats().Watchdog != 1 {
				t.Fatalf("status %q stats %+v", rec.text(EvStatus), a.Stats())
			}
			if el := time.Since(t0); name == "slow reasoning" && el > 4500*time.Millisecond {
				t.Fatalf("took %v; the 1 s limit should have cut a 5 s stream", el)
			}
		})
	}
}

// Under routing v2 a slow step is a failed step: the turn moves to another
// model instead of retrying the slow one.
func TestV2SlowStepEscalates(t *testing.T) {
	slow := newFake(t, reply{reasoning: 200, gap: 20 * time.Millisecond, text: "never seen"})
	fast := newFake(t, reply{text: "done fast"})
	q, g := localModel(slow.URL, "qwen3.6:latest", 3), cloudModel(fast.URL, "glm-5.3:cloud", 3)
	a, rec, sp := v2Agent(t, []*discover.Model{q}, q, g)
	for range 10 { // measured as very fast: the 1 s limit isn't raised for it
		sp.Observe(q.Key(), 100, 5000, 10*time.Millisecond, 10*time.Millisecond)
		sp.Observe(q.Key(), 20000, 0, time.Millisecond, 0)
	}
	a.Watchdog = Watchdog{Tokens: -1, Idle: time.Second}
	a.Run(bg, "say hi")
	if len(fast.requests()) != 1 || !strings.Contains(rec.text(EvText), "done fast") {
		t.Fatalf("fast %d text %q status %q", len(fast.requests()), rec.text(EvText), rec.text(EvStatus))
	}
}

// The limit follows routing v2's expectation: a model expected to be slow
// gets 3x its expected step time before it counts as stuck.
func TestWatchdogSecondsFollowExpectation(t *testing.T) {
	slow := newFake(t, reply{reasoning: 60, gap: 25 * time.Millisecond, text: "finished"})
	q := localModel(slow.URL, "qwen3.6:latest", 3)
	a, rec, _ := v2Agent(t, []*discover.Model{q}, q) // expected ≈ 3 s a step: limit 9 s
	a.Watchdog = Watchdog{Tokens: -1, Idle: time.Second}
	a.Run(bg, "say hi")
	if a.Stats().Watchdog != 0 || !strings.Contains(rec.text(EvText), "finished") {
		t.Fatalf("a 1.5 s step of a model expected at ~3 s was cut: %q %+v", rec.text(EvStatus), a.Stats())
	}
}

// limited is a provider that signals a usage limit: a status with a body, or
// an error inside a 200 stream (the shapes Ollama documents; no real
// exhaustion has been recorded).
func limited(t *testing.T, status int, retryAfter, body string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		if status == 200 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", body)
			return
		}
		http.Error(w, body, status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestV2QuotaSignals(t *testing.T) {
	for _, c := range []struct {
		name, retryAfter, body string
		status                 int
		cooldown               time.Duration // 0: not exhausted
	}{
		{"429 with Retry-After", "120", `{"error":"you have reached your weekly usage limit"}`, 429, 2 * time.Minute},
		{"429 without", "", `{"error":"too many requests"}`, 429, quotaCooldown},
		{"in-stream usage limit", "", `{"error":"you have reached your usage limit, upgrade for more"}`, 200, quotaCooldown},
		{"503 overloaded", "90", `{"error":"server overloaded"}`, 503, 0},
		{"402 out of credits", "", `{"error":"insufficient balance: out of credits"}`, 402, quotaCooldown}, // not retryable, still a quota
	} {
		t.Run(c.name, func(t *testing.T) {
			backup := newFake(t, reply{text: "done locally"})
			g := cloudModel(limited(t, c.status, c.retryAfter, c.body), "glm-5.3:cloud", 3)
			q := localModel(backup.URL, "qwen3.6:latest", 3)
			a, rec, _ := v2Agent(t, []*discover.Model{g}, g, q)
			t0 := time.Now()
			a.Run(bg, "say hi")
			if len(backup.requests()) == 0 || !strings.Contains(rec.text(EvStatus), "failing over") || rec.text(EvError) != "" {
				t.Fatalf("no failover: local %d, status %q, errors %q", len(backup.requests()), rec.text(EvStatus), rec.text(EvError))
			}
			until := a.Router.ExhaustedUntil(g.Key())
			switch {
			case c.cooldown == 0 && !until.IsZero():
				t.Fatalf("a transient error took the model out until %v", until)
			case c.cooldown > 0 && (until.Before(t0.Add(c.cooldown)) || until.After(time.Now().Add(c.cooldown))):
				t.Fatalf("out until %v, want ~%v from now", until.Sub(t0).Round(time.Second), c.cooldown)
			}
		})
	}
}
