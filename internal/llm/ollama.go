package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ollama talks to Ollama through its native /api/chat, not the
// OpenAI-compatible /v1: only the native API takes a context size per request
// (options.num_ctx). Without one, Ollama runs a model at its server default
// (4096 tokens unless OLLAMA_CONTEXT_LENGTH says otherwise) and silently drops
// whatever doesn't fit, while ternly budgets for the model's trained context
// (Phase C, 2026-10-13: /v1 ignored num_ctx, /api/chat honoured it).
type ollama struct{ ep Endpoint }

// Context sizes: the smallest asked for, and the step they grow by (a power
// of two at least the need). Each change of size reloads the model, so a
// model's size only grows within a process (see ctxFor).
const (
	minOllamaCtx  = 8192
	replyReserve  = 8192 // the reply's room when the request names no MaxTokens
	bytesPerToken = 3    // a deliberate overestimate: code runs 3.5–4 bytes a token
)

var (
	ollamaMu  sync.Mutex
	ollamaCtx = map[string]int{} // base + "\x00" + model → the context size last asked for
)

// OllamaAsked returns the context size ternly last asked Ollama at base to
// run model with (0: none yet), for /doctor to compare with what it runs.
func OllamaAsked(base, model string) int {
	ollamaMu.Lock()
	defer ollamaMu.Unlock()
	return ollamaCtx[ollamaBase(base)+"\x00"+model]
}

// ollamaBase is the server's root: an Ollama provider's BaseURL ends in /v1.
func ollamaBase(u string) string { return strings.TrimSuffix(strings.TrimRight(u, "/"), "/v1") }

// estimate is the request's size in tokens, overestimated, plus its reply.
func estimate(r Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		n += len(m.Content) + 16
		for _, c := range m.ToolCalls {
			n += len(c.Name) + len(c.Args) + 16
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema) + 16
	}
	reply := r.MaxTokens
	if reply <= 0 {
		reply = replyReserve
	}
	return n/bytesPerToken + reply
}

// ctxFor chooses num_ctx for r: the request's estimate rounded up to a power
// of two (at least minOllamaCtx), capped at the window routing assumed
// (r.Ctx). It never shrinks for a model, and it keeps the size the model is
// already loaded with when that is enough (loaded, from /api/ps): either way
// Ollama doesn't reload it.
func ctxFor(key string, r Request, loaded int) int {
	need := estimate(r)
	want := minOllamaCtx
	for want < need {
		want *= 2
	}
	if r.Ctx > 0 && want > r.Ctx {
		want = r.Ctx
	}
	ollamaMu.Lock()
	defer ollamaMu.Unlock()
	if had := ollamaCtx[key]; had >= want {
		return had
	}
	if loaded >= want {
		want = loaded
	}
	ollamaCtx[key] = want
	return want
}

// loadedCtx asks Ollama the context size model is loaded with (0: not loaded,
// or no answer within a second).
func loadedCtx(ctx context.Context, base, model string) int {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/ps", nil)
	resp, err := HTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var ps OllamaPS
	if json.NewDecoder(resp.Body).Decode(&ps) != nil {
		return 0
	}
	for _, m := range ps.Models {
		if m.Name == model || m.Model == model {
			return m.ContextLength
		}
	}
	return 0
}

// OllamaPS is Ollama's /api/ps: the models it has loaded.
type OllamaPS struct {
	Models []struct {
		Name          string `json:"name"`
		Model         string `json:"model"`
		ContextLength int    `json:"context_length"`
	} `json:"models"`
}

type olFn struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type olTC struct {
	ID       string `json:"id,omitempty"`
	Function olFn   `json:"function"`
}
type olMsg struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls []olTC `json:"tool_calls,omitempty"`
	ToolName  string `json:"tool_name,omitempty"` // a tool result: the tool it answers
}

func (c *ollama) body(r Request, numCtx int) map[string]any {
	msgs := make([]olMsg, 0, len(r.Messages)+1)
	if r.System != "" {
		msgs = append(msgs, olMsg{Role: "system", Content: r.System})
	}
	names := map[string]string{} // tool call ID → tool name, for the results
	for _, m := range r.Messages {
		om := olMsg{Role: m.Role, Content: m.Content}
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Name
			om.ToolCalls = append(om.ToolCalls, olTC{ID: tc.ID, Function: olFn{Name: tc.Name, Arguments: validJSON(tc.Args)}})
		}
		if m.Role == "tool" {
			om.ToolName = names[m.ToolCallID]
		}
		msgs = append(msgs, om)
	}
	opts := map[string]any{}
	if numCtx > 0 {
		opts["num_ctx"] = numCtx
	}
	if r.MaxTokens > 0 {
		opts["num_predict"] = r.MaxTokens
	}
	b := map[string]any{"model": r.Model, "messages": msgs, "stream": true, "options": opts}
	if r.Effort != "" {
		b["think"] = r.Effort // "low" | "medium" | "high"
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema}}
		}
		b["tools"] = tools
	}
	return b
}

type olChunk struct {
	Message struct {
		Content   string `json:"content"`
		Thinking  string `json:"thinking"`
		ToolCalls []olTC `json:"tool_calls"`
	} `json:"message"`
	Done            bool            `json:"done"`
	DoneReason      string          `json:"done_reason"`
	PromptEvalCount int             `json:"prompt_eval_count"`
	EvalCount       int             `json:"eval_count"`
	Error           json.RawMessage `json:"error"`
}

func (c *ollama) Stream(ctx context.Context, r Request) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		base := ollamaBase(c.ep.BaseURL)
		hdr := map[string]string{}
		if c.ep.Key != "" {
			hdr["Authorization"] = "Bearer " + c.ep.Key
		}
		for k, v := range c.ep.Headers {
			hdr[k] = v
		}
		key := base + "\x00" + r.Model
		loaded := 0
		if OllamaAsked(base, r.Model) == 0 {
			loaded = loadedCtx(ctx, base, r.Model)
		}
		numCtx := ctxFor(key, r, loaded)
		url := base + "/api/chat"
		resp, err := post(ctx, url, hdr, c.body(r, numCtx))
		// A model that doesn't reason ("does not support thinking"): without it.
		if ae, ok := err.(*APIError); ok && ae.Status == 400 && r.Effort != "" && reNoReasoning.MatchString(ae.Body) {
			r.Effort = ""
			resp, err = post(ctx, url, hdr, c.body(r, numCtx))
		}
		if err != nil {
			ch <- Event{Kind: EvError, Err: err}
			return
		}
		defer resp.Body.Close()

		var calls []ToolCall
		var stop string
		var usage *Usage
		var streamErr error
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var ck olChunk
			if json.Unmarshal(line, &ck) != nil {
				continue
			}
			if e := streamError(ck.Error); e != nil {
				streamErr = e
				break
			}
			switch {
			case ck.Message.Content != "":
				ch <- Event{Kind: EvText, Text: ck.Message.Content}
			case ck.Message.Thinking != "":
				ch <- Event{Kind: EvProgress, Reasoning: true, N: len(ck.Message.Thinking)}
			case !ck.Done:
				ch <- Event{Kind: EvProgress}
			}
			for _, tc := range ck.Message.ToolCalls { // Ollama sends each call whole
				if tc.Function.Name == "" {
					continue
				}
				args := strings.TrimSpace(string(tc.Function.Arguments))
				if args == "" || args == "null" {
					args = "{}"
				}
				calls = append(calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
			}
			if ck.Done {
				stop = ck.DoneReason
				usage = &Usage{In: ck.PromptEvalCount, Out: ck.EvalCount}
				break
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			ch <- Event{Kind: EvError, Err: fmt.Errorf("stream: %w", err)}
			return
		}
		if streamErr != nil {
			ch <- Event{Kind: EvError, Err: streamErr}
			return
		}
		if ctx.Err() != nil {
			ch <- Event{Kind: EvError, Err: ctx.Err()}
			return
		}
		for n, c := range calls {
			if c.ID == "" {
				c.ID = fmt.Sprintf("call_%d", n)
			}
			ch <- Event{Kind: EvToolCall, Call: c}
		}
		if len(calls) > 0 && (stop == "" || stop == "stop") {
			stop = "tool_calls" // as the OpenAI-compatible API reports it
		}
		if usage != nil {
			ch <- Event{Kind: EvUsage, Usage: usage.clamped()}
		}
		ch <- Event{Kind: EvDone, Stop: stop}
	}()
	return ch
}
