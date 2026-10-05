package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func drainEvents(ch <-chan Event) (evs []Event) {
	for e := range ch {
		evs = append(evs, e)
	}
	return evs
}

// reasoning_effort is sent when set; a model that rejects it ("does not
// support thinking", as Ollama answers for llama3.1) is asked again without.
func TestOpenAIEffort(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		if _, ok := b["reasoning_effort"]; ok && b["model"] == "plain" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"\"plain\" does not support thinking"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Endpoint{Kind: "openai", BaseURL: srv.URL})
	drainEvents(c.Stream(context.Background(), Request{Model: "thinker", Effort: "low", Messages: []Message{{Role: "user", Content: "x"}}}))
	evs := drainEvents(c.Stream(context.Background(), Request{Model: "plain", Effort: "high", Messages: []Message{{Role: "user", Content: "x"}}}))
	drainEvents(c.Stream(context.Background(), Request{Model: "thinker", Messages: []Message{{Role: "user", Content: "x"}}}))
	if bodies[0]["reasoning_effort"] != "low" || len(bodies) != 4 {
		t.Fatalf("bodies %v", bodies)
	}
	if _, ok := bodies[2]["reasoning_effort"]; ok || evs[0].Kind != EvText {
		t.Fatalf("no retry without the budget: %v %v", bodies[2], evs)
	}
	if _, ok := bodies[3]["reasoning_effort"]; ok {
		t.Fatal("sent a budget nobody asked for")
	}
}

func TestAnthropicThinking(t *testing.T) {
	c := &anthropic{}
	user := Message{Role: "user", Content: "fix it"}
	thought := json.RawMessage(`{"type":"thinking","thinking":"hmm","signature":"sig"}`)
	withThought := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Args: `{}`}}, Thinking: []json.RawMessage{thought}}
	without := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Args: `{}`}}}
	result := Message{Role: "tool", ToolCallID: "t1", Content: "x"}
	for _, c2 := range []struct {
		name   string
		effort string
		msgs   []Message
		budget int
	}{
		{"low: no thinking", "low", []Message{user}, 0},
		{"medium", "medium", []Message{user}, 4096},
		{"high, continuing a thought", "high", []Message{user, withThought, result}, 16384},
		{"high, but the exchange began without one", "high", []Message{user, without, result}, 0},
	} {
		b := c.body(Request{Model: "claude", Effort: c2.effort, Messages: c2.msgs})
		th, _ := b["thinking"].(map[string]any)
		got := 0
		if th != nil {
			got = th["budget_tokens"].(int)
			if b["max_tokens"].(int) <= got {
				t.Errorf("%s: max_tokens %v not above the budget", c2.name, b["max_tokens"])
			}
		}
		if got != c2.budget {
			t.Errorf("%s: budget %d, want %d", c2.name, got, c2.budget)
		}
	}
	// The thinking block goes back first, verbatim.
	raw, _ := json.Marshal(c.body(Request{Model: "claude", Effort: "high", Messages: []Message{user, withThought, result}})["messages"])
	if !strings.Contains(string(raw), `"content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"tool_use"`) {
		t.Fatalf("thinking block not replayed first: %s", raw)
	}
}

// A streamed thinking block (with its signature) comes back whole.
func TestAnthropicThinkingCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step one. "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step two."}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"ENC"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", d)
		}
	}))
	defer srv.Close()
	var thoughts []string
	for _, e := range drainEvents(New(Endpoint{Kind: "anthropic", BaseURL: srv.URL}).Stream(context.Background(), Request{Model: "claude", Effort: "high", Messages: []Message{{Role: "user", Content: "x"}}})) {
		if e.Kind == EvThinking {
			thoughts = append(thoughts, string(e.Raw))
		}
	}
	if len(thoughts) != 2 || thoughts[0] != `{"type":"thinking","thinking":"step one. step two.","signature":"SIG"}` || thoughts[1] != `{"type":"redacted_thinking","data":"ENC"}` {
		t.Fatalf("%q", thoughts)
	}
}
