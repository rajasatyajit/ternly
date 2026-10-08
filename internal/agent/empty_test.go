package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// turns are the requests of the turn itself (ternly's system prompt), not
// utility calls such as the session title, which race it.
func turns(f *fakeLLM) [][]map[string]any {
	var out [][]map[string]any
	for _, r := range f.requests() {
		if len(r) > 0 && strings.HasPrefix(fmt.Sprint(r[0]["content"]), "You are ternly") {
			out = append(out, r)
		}
	}
	return out
}

func lastUser(msgs []map[string]any) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			return fmt.Sprint(msgs[i]["content"])
		}
	}
	return ""
}

// ADR 028: an empty answer is asked again once, on the same model, with a
// nudge; the empty reply itself isn't kept in the conversation.
func TestEmptyAnswerRetriedOnce(t *testing.T) {
	f := newFake(t, reply{text: ""}, reply{text: "the answer is 42"})
	a, rec := newAgent(t, "ask", model(f.URL, "m", 3, 0, 0))
	a.Run(bg, "what is the answer")
	reqs := turns(f)
	if len(reqs) != 2 || !strings.Contains(lastUser(reqs[1]), "Your last reply was empty") {
		t.Fatalf("%d requests; second ends with %q", len(reqs), lastUser(reqs[len(reqs)-1]))
	}
	for _, m := range reqs[1] {
		if m["role"] == "assistant" && strings.TrimSpace(fmt.Sprint(m["content"])) == "" && m["tool_calls"] == nil {
			t.Fatalf("the empty reply was sent back: %v", reqs[1])
		}
	}
	if s := a.Stats(); s.EmptyRetry != 1 || s.Failovers != 0 {
		t.Fatalf("stats %+v", s)
	}
	if !strings.Contains(rec.text(EvStatus), "asking it once more") || rec.text(EvError) != "" {
		t.Fatalf("status %q errors %q", rec.text(EvStatus), rec.text(EvError))
	}
}

// Empty again after the nudge: fail over (ADR 018's path), counted.
func TestEmptyAnswerTwiceFailsOver(t *testing.T) {
	empty := newFake(t, reply{text: ""})
	backup := newFake(t, reply{text: "done locally"})
	g, q := cloudModel(empty.URL, "glm-5.3:cloud", 3), localModel(backup.URL, "qwen3.6:latest", 3)
	a, rec, _ := v2Agent(t, []*discover.Model{g}, g, q)
	a.Run(bg, "say hi")
	if len(turns(empty)) != 2 || len(turns(backup)) != 1 {
		t.Fatalf("empty model %d turn requests, backup %d", len(turns(empty)), len(turns(backup)))
	}
	if s := a.Stats(); s.EmptyRetry != 1 || s.Failovers != 1 {
		t.Fatalf("stats %+v", s)
	}
	if !strings.Contains(rec.text(EvStatus), "failing over to "+q.Key()) {
		t.Fatalf("status %q", rec.text(EvStatus))
	}
}

// Bounded: one retry and one failover a turn, whatever the models do.
func TestEmptyAnswerBounded(t *testing.T) {
	one := newFake(t, reply{text: ""})
	a, _ := newAgent(t, "ask", model(one.URL, "m", 3, 0, 0))
	a.Run(bg, "hello")
	if n := len(turns(one)); n != 2 {
		t.Fatalf("a lone empty model got %d requests, want 2 (the turn, one retry)", n)
	}
	e1, e2 := newFake(t, reply{text: ""}), newFake(t, reply{text: ""})
	g, q := cloudModel(e1.URL, "glm-5.3:cloud", 3), localModel(e2.URL, "qwen3.6:latest", 3)
	b, rec, _ := v2Agent(t, []*discover.Model{g}, g, q)
	b.Run(bg, "hello")
	if n1, n2 := len(turns(e1)), len(turns(e2)); n1 != 2 || n2 != 1 {
		t.Fatalf("both empty: %d and %d requests, want 2 and 1", n1, n2)
	}
	if s := b.Stats(); s.EmptyRetry != 1 || s.Failovers != 1 {
		t.Fatalf("stats %+v", s)
	}
	if rec.text(EvError) != "" {
		t.Fatalf("errors %q", rec.text(EvError))
	}
	// three empty models: still one failover, not a walk through all of them
	f1, f2, f3 := newFake(t, reply{text: ""}), newFake(t, reply{text: ""}), newFake(t, reply{text: ""})
	m1, m2, m3 := cloudModel(f1.URL, "glm-5.3:cloud", 3), localModel(f2.URL, "qwen3.6:latest", 3), localModel(f3.URL, "llama3.1:8b", 3)
	c, _, _ := v2Agent(t, []*discover.Model{m1}, m1, m2, m3)
	c.Run(bg, "hello")
	total := len(turns(f1)) + len(turns(f2)) + len(turns(f3))
	if s := c.Stats(); total != 3 || s.Failovers != 1 || s.EmptyRetry != 1 {
		t.Fatalf("three empty models: %d turn requests (%d/%d/%d), stats %+v; want 3 and one failover", total, len(turns(f1)), len(turns(f2)), len(turns(f3)), s)
	}
}
