package llm

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Provider streams are untrusted input (a hostile or broken endpoint, a
// proxy). Run: go test -fuzz FuzzSSE ./internal/llm (and the Stream targets).

func FuzzSSE(f *testing.F) {
	f.Add([]byte("event: x\ndata: {\"a\":1}\n\ndata: [DONE]\n\n"))
	f.Add([]byte("data: a\ndata: b\n\n\n\nevent:\ndata:\n"))
	f.Add([]byte("data:" + strings.Repeat("x", 70000) + "\n\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		n := 0
		_ = sse(bytes.NewReader(b), func(event, data string) bool {
			n++
			if data == "" {
				t.Fatalf("empty data dispatched (event %q)", event)
			}
			return n < 1000
		})
	})
}

// streamServer serves whatever body the current fuzz input is.
type streamServer struct {
	*httptest.Server
	mu   sync.Mutex
	body []byte
}

func newStreamServer(f *testing.F) *streamServer {
	s := &streamServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		b := s.body
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(b)
	}))
	f.Cleanup(s.Close)
	return s
}

// drain checks what any stream must guarantee: it ends, every tool call it
// emits has a name and an ID, and usage is never negative.
func drain(t *testing.T, c Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ev := range c.Stream(ctx, Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}) {
		if ev.Kind == EvToolCall && (ev.Call.Name == "" || ev.Call.ID == "") {
			t.Fatalf("tool call without a name or ID: %+v", ev.Call)
		}
		if u := ev.Usage; ev.Kind == EvUsage && (u.In < 0 || u.Out < 0 || u.CacheRead < 0 || u.CacheWrite < 0) {
			t.Fatalf("negative usage (a negative cost): %+v", u)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("the stream didn't end")
	}
}

func FuzzOpenAIStream(f *testing.F) {
	for _, d := range []string{
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"grep","arguments":"{\"p"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":-5,"function":{"arguments":"x"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2147483647,"id":"b","function":{"name":"x"}}]}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":-1,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`,
	} {
		f.Add([]byte("data: " + d + "\n\ndata: [DONE]\n\n"))
	}
	s := newStreamServer(f)
	c := New(Endpoint{Kind: "openai", BaseURL: s.URL})
	f.Fuzz(func(t *testing.T, b []byte) {
		s.mu.Lock()
		s.body = b
		s.mu.Unlock()
		drain(t, c)
	})
}

func FuzzAnthropicStream(f *testing.F) {
	for _, d := range []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":900}}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"bash"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":7,"delta":{"type":"input_json_delta","partial_json":"x"}}`,
		`{"type":"content_block_stop","index":-1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`,
	} {
		f.Add([]byte("event: x\ndata: " + d + "\n\n"))
	}
	s := newStreamServer(f)
	c := New(Endpoint{Kind: "anthropic", BaseURL: s.URL})
	f.Fuzz(func(t *testing.T, b []byte) {
		s.mu.Lock()
		s.body = b
		s.mu.Unlock()
		drain(t, c)
	})
}
