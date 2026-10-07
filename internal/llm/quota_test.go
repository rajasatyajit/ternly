package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Quota and rate limits, as providers signal them. No real Ollama Cloud
// quota response has been recorded (none has happened here, and Ollama
// documents only the shapes: a 429, {"error": "..."} bodies, errors inside
// a stream), so these responses are simulated from those shapes.

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for h, want := range map[string]time.Duration{
		"120": 120 * time.Second, " 5 ": 5 * time.Second, "": 0, "soon": 0, "-3": 0,
		"Wed, 07 Oct 2026 12:02:00 GMT": 2 * time.Minute, "Wed, 07 Oct 2026 11:00:00 GMT": 0,
	} {
		if got := retryAfter(h, now); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
}

// stream serves one chat completion stream of the given SSE data lines.
func stream(t *testing.T, status int, hdr map[string]string, lines ...string) (string, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		if status != 200 {
			w.WriteHeader(status)
			fmt.Fprint(w, lines[0])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func run(url string) (string, error) {
	text, _, err := Collect(New(Endpoint{Kind: "openai", BaseURL: url}).Stream(context.Background(), Request{Model: "m"}))
	return text, err
}

func TestInStreamErrors(t *testing.T) {
	for _, c := range []struct {
		name, line string
		quota      bool
	}{
		{"ollama string", `{"error":"you have reached your usage limit, please try again later"}`, true},
		{"openai object", `{"error":{"message":"Rate limit reached for requests","type":"rate_limit_error"}}`, true},
		{"credits", `{"error":"insufficient balance: out of credits"}`, true},
		{"other", `{"error":"the model failed to generate a response"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			url, _ := stream(t, 200, nil, `{"choices":[{"delta":{"content":"partial "}}]}`, c.line)
			_, err := run(url)
			hit, _ := QuotaHit(err)
			if err == nil || hit != c.quota || !IsRetryable(err) || !strings.Contains(err.Error(), "error in the stream") {
				t.Fatalf("err %v, quota %v (want %v)", err, hit, c.quota)
			}
		})
	}
	// before: such a chunk was dropped and the turn ended with an empty answer
	url, _ := stream(t, 200, nil, `{"choices":[{"delta":{"content":"fine"}}]}`, `{"error":null,"choices":[]}`)
	if text, err := run(url); err != nil || text != "fine" {
		t.Fatalf("\"error\": null isn't an error: %q %v", text, err)
	}
}

// A long Retry-After is honoured by giving up at once (the caller fails over),
// not by retrying four times.
func TestLongRetryAfterReturnsAtOnce(t *testing.T) {
	url, n := stream(t, 429, map[string]string{"Retry-After": "120"}, `{"error":"you have reached your weekly usage limit"}`)
	t0 := time.Now()
	_, err := run(url)
	hit, wait := QuotaHit(err)
	if !hit || wait != 120*time.Second || n.Load() != 1 || time.Since(t0) > 2*time.Second {
		t.Fatalf("hit %v wait %v requests %d after %v: %v", hit, wait, n.Load(), time.Since(t0), err)
	}
	// a 503 (an overloaded server, or a full concurrency queue) is retryable but no quota
	url, _ = stream(t, 503, map[string]string{"Retry-After": "90"}, `{"error":"server overloaded"}`)
	if _, err := run(url); err == nil || !IsRetryable(err) {
		t.Fatalf("503: %v", err)
	} else if hit, _ := QuotaHit(err); hit {
		t.Fatalf("a 503 read as a quota: %v", err)
	}
}
