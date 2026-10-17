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

// fakeOllama answers /api/ps with ps and /api/chat with reply (NDJSON
// lines), keeping each chat request's body.
func fakeOllama(t *testing.T, ps string, reply ...string) (string, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			fmt.Fprint(w, ps)
		case "/api/chat":
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			bodies = append(bodies, m)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/x-ndjson")
			for _, l := range reply {
				fmt.Fprintln(w, l)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }
}

func resetOllamaCtx() {
	ollamaMu.Lock()
	ollamaCtx = map[string]int{}
	ollamaMu.Unlock()
}

func TestOllamaStream(t *testing.T) {
	resetOllamaCtx()
	base, bodies := fakeOllama(t, `{"models":[]}`,
		`{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`,
		`{"message":{"role":"assistant","content":"Hello "},"done":false}`,
		`{"message":{"role":"assistant","content":"there."},"done":false}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.go"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":120,"eval_count":7}`)
	req := Request{Model: "m:latest", System: "sys", Ctx: 131072, Effort: "low",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "grep", Args: `{"q":"x"}`}}},
			{Role: "tool", ToolCallID: "c1", Content: "no match"},
		},
		Tools: []ToolSpec{{Name: "read_file", Description: "read", Schema: json.RawMessage(`{"type":"object"}`)}}}
	var text string
	var calls []ToolCall
	var u Usage
	var stop string
	reasoning := 0
	for ev := range New(Endpoint{Kind: "ollama", BaseURL: base}).Stream(context.Background(), req) {
		switch ev.Kind {
		case EvText:
			text += ev.Text
		case EvToolCall:
			calls = append(calls, ev.Call)
		case EvUsage:
			u = ev.Usage
		case EvDone:
			stop = ev.Stop
		case EvError:
			t.Fatal(ev.Err)
		case EvProgress:
			if ev.Reasoning {
				reasoning += ev.N
			}
		}
	}
	if text != "Hello there." || len(calls) != 1 || calls[0].Name != "read_file" || calls[0].Args != `{"path":"a.go"}` || calls[0].ID == "" {
		t.Fatalf("text %q calls %+v", text, calls)
	}
	if u.In != 120 || u.Out != 7 || stop != "tool_calls" || reasoning != 3 {
		t.Fatalf("usage %+v stop %q reasoning %d", u, stop, reasoning)
	}
	b := bodies()[0]
	opts, _ := b["options"].(map[string]any)
	// num_ctx: a few tokens of prompt plus the reply's 8192, rounded up.
	if opts["num_ctx"] != float64(16384) || b["think"] != "low" || b["stream"] != true {
		t.Fatalf("body: %v", b)
	}
	msgs := b["messages"].([]any)
	if r := msgs[0].(map[string]any); r["role"] != "system" {
		t.Fatalf("first message: %v", r)
	}
	tc := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if args, ok := tc["arguments"].(map[string]any); !ok || args["q"] != "x" {
		t.Fatalf("a call's arguments go as an object: %v", tc)
	}
	if res := msgs[3].(map[string]any); res["role"] != "tool" || res["tool_name"] != "grep" {
		t.Fatalf("tool result: %v", res)
	}
	if got := OllamaAsked(base, "m:latest"); got != 16384 {
		t.Fatalf("OllamaAsked = %d", got)
	}
}

// The context asked for follows the request's size, in powers of two,
// capped at the window routing assumed, and never shrinks for a model (each
// change reloads it).
func TestOllamaCtxFor(t *testing.T) {
	resetOllamaCtx()
	small := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, Ctx: 131072}
	big := small
	big.Messages = []Message{{Role: "user", Content: strings.Repeat("x", 100_000)}} // ~33k tokens + reply
	huge := small
	huge.Messages = []Message{{Role: "user", Content: strings.Repeat("x", 1_000_000)}}
	for i, c := range []struct {
		r            Request
		loaded, want int
	}{
		{small, 0, 16384}, // a few tokens plus the reply's 8192
		{big, 0, 65536},
		{small, 0, 65536},  // never shrinks
		{huge, 0, 131072},  // capped at the assumed window
		{small, 0, 131072}, // still
	} {
		if got := ctxFor("k", c.r, c.loaded); got != c.want {
			t.Fatalf("step %d: ctxFor = %d, want %d", i, got, c.want)
		}
	}
	// A model the server already runs with enough context keeps it.
	if got := ctxFor("other", small, 32768); got != 32768 {
		t.Fatalf("loaded with 32768: %d", got)
	}
}

// The first request for a model reads /api/ps: a model loaded with enough
// context is asked for that size, so Ollama doesn't reload it.
func TestOllamaKeepsLoadedContext(t *testing.T) {
	resetOllamaCtx()
	base, bodies := fakeOllama(t, `{"models":[{"name":"m:latest","model":"m:latest","context_length":131072}]}`,
		`{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`)
	_, _, err := Collect(New(Endpoint{Kind: "ollama", BaseURL: base}).Stream(context.Background(),
		Request{Model: "m:latest", Messages: []Message{{Role: "user", Content: "hi"}}, Ctx: 131072}))
	if err != nil {
		t.Fatal(err)
	}
	if n := bodies()[0]["options"].(map[string]any)["num_ctx"]; n != float64(131072) {
		t.Fatalf("num_ctx = %v, want the loaded 131072", n)
	}
}

func TestOllamaErrors(t *testing.T) {
	resetOllamaCtx()
	base, _ := fakeOllama(t, `{"models":[]}`, `{"message":{"content":"par"},"done":false}`, `{"error":"model runner has unexpectedly stopped"}`)
	_, _, err := Collect(New(Endpoint{Kind: "ollama", BaseURL: base}).Stream(context.Background(), Request{Model: "m"}))
	if ae, ok := err.(*APIError); !ok || !ae.InStream || !strings.Contains(ae.Body, "unexpectedly stopped") {
		t.Fatalf("in-stream error: %v", err)
	}
	// A model that doesn't reason: the request is sent again without think.
	var mu sync.Mutex
	var thinks []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		thinks = append(thinks, b["think"])
		mu.Unlock()
		if b["think"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"\"m\" does not support thinking"}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`)
	}))
	defer srv.Close()
	out, _, err := Collect(New(Endpoint{Kind: "ollama", BaseURL: srv.URL}).Stream(context.Background(), Request{Model: "m", Effort: "high"}))
	if err != nil || out != "ok" || len(thinks) != 2 || thinks[1] != nil {
		t.Fatalf("out %q err %v thinks %v", out, err, thinks)
	}
}
