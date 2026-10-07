package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

var reNoReasoning = regexp.MustCompile(`(?i)reasoning|thinking`)

type openAI struct {
	ep          Endpoint
	noUsageOpts atomic.Bool // some local servers reject stream_options
}

type oaFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type oaTC struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function oaFn   `json:"function"`
}
type oaMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCalls  []oaTC `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

func (c *openAI) body(r Request, usage bool) map[string]any {
	msgs := make([]oaMsg, 0, len(r.Messages)+1)
	if r.System != "" {
		msgs = append(msgs, oaMsg{Role: "system", Content: r.System})
	}
	for _, m := range r.Messages {
		om := oaMsg{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, oaTC{ID: tc.ID, Type: "function",
				Function: oaFn{Name: tc.Name, Arguments: string(validJSON(tc.Args))}})
		}
		msgs = append(msgs, om)
	}
	b := map[string]any{"model": r.Model, "messages": msgs, "stream": true}
	if r.Effort != "" {
		b["reasoning_effort"] = r.Effort // OpenAI, Ollama, Gemini, Groq…
	}
	if usage {
		b["stream_options"] = map[string]any{"include_usage": true}
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

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`         // Ollama
			ReasoningContent string `json:"reasoning_content"` // DeepSeek, vLLM, …
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function oaFn   `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (c *openAI) Stream(ctx context.Context, r Request) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		hdr := map[string]string{}
		if c.ep.Key != "" {
			hdr["Authorization"] = "Bearer " + c.ep.Key
		}
		for k, v := range c.ep.Headers {
			hdr[k] = v
		}
		url := strings.TrimRight(c.ep.BaseURL, "/") + "/chat/completions"
		resp, err := post(ctx, url, hdr, c.body(r, !c.noUsageOpts.Load()))
		if ae, ok := err.(*APIError); ok && ae.Status == 400 && strings.Contains(ae.Body, "stream_options") {
			c.noUsageOpts.Store(true)
			resp, err = post(ctx, url, hdr, c.body(r, false))
		}
		// A model that doesn't take a reasoning budget ("does not support
		// thinking", "Unsupported parameter: 'reasoning_effort'"): without it.
		if ae, ok := err.(*APIError); ok && ae.Status == 400 && r.Effort != "" && reNoReasoning.MatchString(ae.Body) {
			r.Effort = ""
			resp, err = post(ctx, url, hdr, c.body(r, !c.noUsageOpts.Load()))
		}
		if err != nil {
			ch <- Event{Kind: EvError, Err: err}
			return
		}
		defer resp.Body.Close()

		type acc struct {
			id, name string
			args     strings.Builder
		}
		calls := map[int]*acc{}
		var stop string
		var usage *Usage
		perr := sse(resp.Body, func(_, data string) bool {
			if data == "[DONE]" {
				return false
			}
			var ck oaChunk
			if json.Unmarshal([]byte(data), &ck) != nil {
				return true
			}
			for _, chc := range ck.Choices {
				switch {
				case chc.Delta.Content != "":
					ch <- Event{Kind: EvText, Text: chc.Delta.Content}
				case chc.Delta.Reasoning != "" || chc.Delta.ReasoningContent != "":
					ch <- Event{Kind: EvProgress, Reasoning: true, N: len(chc.Delta.Reasoning) + len(chc.Delta.ReasoningContent)}
				default:
					ch <- Event{Kind: EvProgress}
				}
				for _, tc := range chc.Delta.ToolCalls {
					a := calls[tc.Index]
					if a == nil {
						a = &acc{}
						calls[tc.Index] = a
					}
					if tc.ID != "" {
						a.id = tc.ID
					}
					if tc.Function.Name != "" {
						a.name += tc.Function.Name
					}
					a.args.WriteString(tc.Function.Arguments)
				}
				if chc.FinishReason != nil {
					stop = *chc.FinishReason
				}
			}
			if ck.Usage != nil {
				u := Usage{In: ck.Usage.PromptTokens, Out: ck.Usage.CompletionTokens}
				if d := ck.Usage.PromptTokensDetails; d != nil {
					u.CacheRead = d.CachedTokens
					u.In -= d.CachedTokens
				}
				usage = &u
			}
			return true
		})
		if perr != nil && ctx.Err() == nil {
			ch <- Event{Kind: EvError, Err: fmt.Errorf("stream: %w", perr)}
			return
		}
		if ctx.Err() != nil {
			ch <- Event{Kind: EvError, Err: ctx.Err()}
			return
		}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for n, i := range idx {
			a := calls[i]
			if a.name == "" { // a fragment without a function name isn't a call
				continue
			}
			if a.id == "" { // some local servers omit ids
				a.id = fmt.Sprintf("call_%d", n)
			}
			ch <- Event{Kind: EvToolCall, Call: ToolCall{ID: a.id, Name: a.name, Args: a.args.String()}}
		}
		if usage != nil {
			ch <- Event{Kind: EvUsage, Usage: usage.clamped()}
		}
		ch <- Event{Kind: EvDone, Stop: stop}
	}()
	return ch
}
