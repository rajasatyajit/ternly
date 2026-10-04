package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIStreamToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"choices":[{"delta":{"content":"hi "}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"grep","arguments":"{\"pat"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"tern\":\"x\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", d)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	var text string
	var call ToolCall
	var u Usage
	for ev := range New(Endpoint{Kind: "openai", BaseURL: srv.URL}).Stream(context.Background(), Request{Model: "m"}) {
		switch ev.Kind {
		case EvText:
			text += ev.Text
		case EvToolCall:
			call = ev.Call
		case EvUsage:
			u = ev.Usage
		case EvError:
			t.Fatal(ev.Err)
		}
	}
	if text != "hi " || call.Name != "grep" || call.Args != `{"pattern":"x"}` || u.CacheRead != 80 || u.In != 20 {
		t.Fatalf("text=%q call=%+v usage=%+v", text, call, u)
	}
}

func TestAnthropicStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version")
		}
		for _, d := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":900}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"bash"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", d)
		}
	}))
	defer srv.Close()
	var got []Event
	for ev := range New(Endpoint{Kind: "anthropic", BaseURL: srv.URL, Key: "k"}).Stream(context.Background(), Request{Model: "m", System: "s",
		Messages: []Message{{Role: "user", Content: "hi"}}}) {
		got = append(got, ev)
	}
	var call ToolCall
	var u Usage
	for _, e := range got {
		if e.Kind == EvToolCall {
			call = e.Call
		}
		if e.Kind == EvUsage {
			u = e.Usage
		}
		if e.Kind == EvError {
			t.Fatal(e.Err)
		}
	}
	if call.Name != "bash" || call.Args != `{"command":"ls"}` || u.CacheRead != 900 || u.Out != 7 {
		t.Fatalf("call=%+v usage=%+v", call, u)
	}
}
