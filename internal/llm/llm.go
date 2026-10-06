// Package llm is a minimal, dependency-free streaming client for the two wire
// protocols that cover ~every model today: OpenAI-compatible chat completions
// (OpenAI, OpenRouter, Gemini, Groq, DeepSeek, Mistral, xAI, Together, Ollama,
// LM Studio, llama.cpp, vLLM, …) and Anthropic Messages (prompt caching).
package llm

import (
	ssepkg "github.com/rajasatyajit/ternly/internal/sse"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	Role       string // user | assistant | tool
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// Thinking holds a provider's reasoning blocks verbatim (Anthropic's
	// thinking and redacted_thinking, with signatures), sent back with the
	// message as the provider requires during tool use.
	Thinking []json.RawMessage `json:",omitempty"`
}

type ToolCall struct {
	ID, Name string
	Args     string // raw JSON
}

type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
	// Effort is the reasoning budget the router chose: "low", "medium",
	// "high", or "" to send nothing (the model's default; ADR 015). Only set
	// for models known to reason.
	Effort string
}

type Usage struct{ In, Out, CacheRead, CacheWrite int }

// clamped zeroes negative counts: providers report them (cached tokens
// above prompt tokens, or plain garbage), and a negative count is a negative
// cost, which would let spend slip under a budget.
func (u Usage) clamped() Usage {
	u.In, u.Out, u.CacheRead, u.CacheWrite = max(u.In, 0), max(u.Out, 0), max(u.CacheRead, 0), max(u.CacheWrite, 0)
	return u
}

func (u *Usage) Add(o Usage) {
	u.In += o.In
	u.Out += o.Out
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

type EventKind int

const (
	EvText EventKind = iota
	EvToolCall
	EvUsage
	EvDone
	EvError
	EvProgress // the stream is producing something other than text (tool arguments, reasoning)
	EvThinking // a complete reasoning block to keep with the message (Raw)
)

type Event struct {
	Kind  EventKind
	Text  string
	Call  ToolCall
	Usage Usage
	Raw   json.RawMessage
	// Reasoning marks an EvProgress chunk as reasoning (thinking), not tool
	// arguments: what the reasoning watchdog counts (ADR 016).
	Reasoning bool
	Stop      string
	Err       error
}

// Endpoint describes where and how to talk to a provider.
type Endpoint struct {
	Kind    string // "openai" | "anthropic"
	BaseURL string
	Key     string
	Headers map[string]string
}

type Client interface {
	Stream(ctx context.Context, r Request) <-chan Event
}

func New(ep Endpoint) Client {
	if ep.Kind == "anthropic" {
		return &anthropic{ep}
	}
	return &openAI{ep: ep}
}

// One tuned transport for the whole process: keep-alive + HTTP/2 + tight dial timeouts.
var HTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 180 * time.Second,
}}

// APIError is returned for non-2xx responses; Retryable marks 429/5xx/overload.
type APIError struct {
	Status    int
	Body      string
	Retryable bool
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 400 {
		b = b[:400] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(b))
}

// post sends JSON with retry/backoff on 429/5xx (honouring Retry-After) before the stream starts.
func post(ctx context.Context, url string, hdr map[string]string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
		} else if resp.StatusCode/100 == 2 {
			return resp, nil
		} else {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			ae := &APIError{Status: resp.StatusCode, Body: string(b),
				Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500 || resp.StatusCode == 529}
			if !ae.Retryable {
				return nil, ae
			}
			last = ae
			if s, _ := strconv.Atoi(resp.Header.Get("Retry-After")); s > 0 && s < 60 {
				if !sleep(ctx, time.Duration(s)*time.Second) {
					return nil, ctx.Err()
				}
				continue
			}
		}
		if !sleep(ctx, time.Duration(1<<attempt)*750*time.Millisecond) {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sse parses a Server-Sent-Events stream, calling fn(event, data) per message.
func sse(r io.Reader, fn func(event, data string) bool) error { return ssepkg.Read(r, fn) }

// Collect drains a stream into text + usage (used for cheap utility calls).
func Collect(ch <-chan Event) (string, Usage, error) {
	var sb strings.Builder
	var u Usage
	for ev := range ch {
		switch ev.Kind {
		case EvText:
			sb.WriteString(ev.Text)
		case EvUsage:
			u.Add(ev.Usage)
		case EvError:
			return sb.String(), u, ev.Err
		}
	}
	return sb.String(), u, nil
}

func IsRetryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable
	}
	return !errors.Is(err, context.Canceled)
}

func validJSON(s string) json.RawMessage {
	if s = strings.TrimSpace(s); s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}
