package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// sseServer answers every chat request with one text chunk ("reply N", N
// counting requests); fail makes it answer 500 instead.
func sseServer(t *testing.T, fail *atomic.Bool, bodies *[]string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if bodies != nil {
			*bodies = append(*bodies, string(b))
		}
		k := n.Add(1)
		if fail != nil && fail.Load() {
			http.Error(w, "boom", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply %d\"}}]}\n\n", k)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func det() (*float64, *int) { z, s := 0.0, 7; return &z, &s }

func TestCacheDeterministicOnly(t *testing.T) {
	srv, n := sseServer(t, nil, nil)
	ep := Endpoint{Kind: "openai", BaseURL: srv.URL}
	c := &Cache{Dir: t.TempDir()}
	cl := c.Wrap(New(ep), ep)
	temp, seed := det()
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, Temperature: temp, Seed: seed}
	a, _, err := Collect(cl.Stream(context.Background(), req))
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := Collect(cl.Stream(context.Background(), req))
	if a != "reply 1" || b != "reply 1" || n.Load() != 1 || c.Hits.Load() != 1 || c.Misses.Load() != 1 {
		t.Fatalf("a=%q b=%q server=%d hits=%d misses=%d", a, b, n.Load(), c.Hits.Load(), c.Misses.Load())
	}
	other := req
	other.Messages = []Message{{Role: "user", Content: "different"}}
	if x, _, _ := Collect(cl.Stream(context.Background(), other)); x != "reply 2" {
		t.Fatalf("a different request was answered from the cache: %q", x)
	}
	free := Request{Model: "m", Messages: req.Messages} // not deterministic
	x, _, _ := Collect(cl.Stream(context.Background(), free))
	y, _, _ := Collect(cl.Stream(context.Background(), free))
	if x == y {
		t.Fatalf("a non-deterministic request was cached: %q %q", x, y)
	}
}

func TestCacheNeverStoresErrors(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv, n := sseServer(t, &fail, nil)
	ep := Endpoint{Kind: "openai", BaseURL: srv.URL}
	c := &Cache{Dir: t.TempDir()}
	cl := c.Wrap(New(ep), ep)
	temp, seed := det()
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, Temperature: temp, Seed: seed}
	if _, _, err := Collect(cl.Stream(context.Background(), req)); err == nil {
		t.Fatal("want the server's error")
	}
	fail.Store(false)
	if s, _, err := Collect(cl.Stream(context.Background(), req)); err != nil || !strings.HasPrefix(s, "reply") || n.Load() != 2 {
		t.Fatalf("an error was replayed from the cache: %q %v (server %d)", s, err, n.Load())
	}
}

func TestBodyPinsSampling(t *testing.T) {
	var bodies []string
	srv, _ := sseServer(t, nil, &bodies)
	temp, seed := det()
	_, _, _ = Collect(New(Endpoint{Kind: "openai", BaseURL: srv.URL}).Stream(context.Background(), Request{Model: "m", Temperature: temp, Seed: seed}))
	_, _, _ = Collect(New(Endpoint{Kind: "openai", BaseURL: srv.URL}).Stream(context.Background(), Request{Model: "m"}))
	if !strings.Contains(bodies[0], `"temperature":0`) || !strings.Contains(bodies[0], `"seed":7`) {
		t.Fatalf("pinned body: %s", bodies[0])
	}
	if strings.Contains(bodies[1], "temperature") || strings.Contains(bodies[1], "seed") {
		t.Fatalf("unpinned body: %s", bodies[1])
	}
}

// scripted is a Client that plays a fixed event sequence.
type scripted struct {
	evs   []Event
	calls atomic.Int64
}

func (s *scripted) Stream(context.Context, Request) <-chan Event {
	s.calls.Add(1)
	ch := make(chan Event, len(s.evs))
	for _, e := range s.evs {
		ch <- e
	}
	close(ch)
	return ch
}

// An error inside a stream that still reports done is not cached either.
func TestCacheSkipsInStreamErrors(t *testing.T) {
	cl := &scripted{evs: []Event{{Kind: EvText, Text: "partial"}, {Kind: EvError, Err: fmt.Errorf("upstream failed")}, {Kind: EvDone}}}
	ep := Endpoint{Kind: "openai", BaseURL: "http://x"}
	c := &Cache{Dir: t.TempDir()}
	w := c.Wrap(cl, ep)
	temp, seed := det()
	req := Request{Model: "m", Temperature: temp, Seed: seed}
	_, _, _ = Collect(w.Stream(context.Background(), req))
	_, _, _ = Collect(w.Stream(context.Background(), req))
	if cl.calls.Load() != 2 || c.Hits.Load() != 0 {
		t.Fatalf("an errored stream was cached: provider calls %d, hits %d", cl.calls.Load(), c.Hits.Load())
	}
}
