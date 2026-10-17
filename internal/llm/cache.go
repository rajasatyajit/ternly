package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Cache answers identical deterministic requests (temperature 0 and a seed)
// from disk, so a rerun of the same task gets the same responses: outcome
// reproducibility where providers aren't bit-identical (ADR 029). Only
// complete, successful streams are stored; errors are never replayed.
type Cache struct {
	Dir          string
	Hits, Misses atomic.Int64
}

// Wrap returns c's client for ep: requests that aren't deterministic pass
// through untouched.
func (c *Cache) Wrap(cl Client, ep Endpoint) Client {
	if c == nil || c.Dir == "" {
		return cl
	}
	return &cachedClient{c: c, cl: cl, ep: ep}
}

type cachedClient struct {
	c  *Cache
	cl Client
	ep Endpoint
}

// cachedEvent is an Event without its error (errors are never cached).
type cachedEvent struct {
	Kind      EventKind
	Text      string          `json:",omitempty"`
	Call      ToolCall        `json:",omitzero"`
	Usage     Usage           `json:",omitzero"`
	Raw       json.RawMessage `json:",omitempty"`
	Reasoning bool            `json:",omitempty"`
	N         int             `json:",omitempty"`
	Stop      string          `json:",omitempty"`
}

// key identifies a request: the endpoint (not its key) and every field
// of the request.
func (cc *cachedClient) key(r Request) string {
	b, _ := json.Marshal(struct {
		Kind, URL string
		Req       Request
	}{cc.ep.Kind, cc.ep.BaseURL, r})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (cc *cachedClient) Stream(ctx context.Context, r Request) <-chan Event {
	if !r.Deterministic() {
		return cc.cl.Stream(ctx, r)
	}
	path := filepath.Join(cc.c.Dir, cc.key(r)+".json")
	if b, err := os.ReadFile(path); err == nil {
		var evs []cachedEvent
		if json.Unmarshal(b, &evs) == nil && len(evs) > 0 {
			cc.c.Hits.Add(1)
			ch := make(chan Event, len(evs))
			for _, e := range evs {
				ch <- Event{Kind: e.Kind, Text: e.Text, Call: e.Call, Usage: e.Usage, Raw: e.Raw, Reasoning: e.Reasoning, N: e.N, Stop: e.Stop}
			}
			close(ch)
			return ch
		}
	}
	cc.c.Misses.Add(1)
	in := cc.cl.Stream(ctx, r)
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		var evs []cachedEvent
		ok, done := true, false
		for e := range in {
			out <- e
			switch e.Kind {
			case EvError:
				ok = false
			case EvDone:
				done = true
			}
			evs = append(evs, cachedEvent{Kind: e.Kind, Text: e.Text, Call: e.Call, Usage: e.Usage, Raw: e.Raw, Reasoning: e.Reasoning, N: e.N, Stop: e.Stop})
		}
		if !ok || !done || ctx.Err() != nil {
			return
		}
		if b, err := json.Marshal(evs); err == nil && os.MkdirAll(cc.c.Dir, 0o700) == nil {
			tmp := path + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}()
	return out
}
