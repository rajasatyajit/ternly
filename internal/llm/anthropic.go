package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type anthropic struct{ ep Endpoint }

type aBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	CacheControl *cacheCtl       `json:"cache_control,omitempty"`
}
type cacheCtl struct {
	Type string `json:"type"`
}
type aMsg struct {
	Role    string   `json:"role"`
	Content []aBlock `json:"content"`
}

var ephemeral = &cacheCtl{Type: "ephemeral"}

// body builds the request with up to 3 cache breakpoints: system, tools, last message.
// Keeping system+tools byte-identical across turns is what makes the cache hit.
func (c *anthropic) body(r Request) map[string]any {
	var msgs []aMsg
	push := func(role string, blocks ...aBlock) {
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, aMsg{Role: role, Content: blocks})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "user":
			push("user", aBlock{Type: "text", Text: nonEmpty(m.Content)})
		case "tool":
			push("user", aBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: nonEmpty(m.Content)})
		case "assistant":
			var bs []aBlock
			if strings.TrimSpace(m.Content) != "" {
				bs = append(bs, aBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				bs = append(bs, aBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: validJSON(tc.Args)})
			}
			if len(bs) == 0 {
				bs = append(bs, aBlock{Type: "text", Text: "(no content)"})
			}
			push("assistant", bs...)
		}
	}
	if n := len(msgs); n > 0 {
		last := &msgs[n-1].Content[len(msgs[n-1].Content)-1]
		last.CacheControl = ephemeral
	}
	maxTok := r.MaxTokens
	if maxTok == 0 {
		maxTok = 8192
	}
	b := map[string]any{"model": r.Model, "max_tokens": maxTok, "messages": msgs, "stream": true}
	if r.System != "" {
		b["system"] = []aBlock{{Type: "text", Text: r.System, CacheControl: ephemeral}}
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema}
		}
		tools[len(tools)-1]["cache_control"] = ephemeral
		b["tools"] = tools
	}
	return b
}

func nonEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

type aEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage aUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type, ID, Name string
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *aUsage `json:"usage"`
	Error *struct {
		Type, Message string
	} `json:"error"`
}
type aUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (c *anthropic) Stream(ctx context.Context, r Request) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		hdr := map[string]string{"x-api-key": c.ep.Key, "anthropic-version": "2023-06-01"}
		for k, v := range c.ep.Headers {
			hdr[k] = v
		}
		resp, err := post(ctx, strings.TrimRight(c.ep.BaseURL, "/")+"/v1/messages", hdr, c.body(r))
		if err != nil {
			ch <- Event{Kind: EvError, Err: err}
			return
		}
		defer resp.Body.Close()

		type tool struct {
			id, name string
			args     strings.Builder
		}
		tools := map[int]*tool{}
		var u Usage
		var stop string
		var streamErr error
		perr := sse(resp.Body, func(_, data string) bool {
			var ev aEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				return true
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil {
					mu := ev.Message.Usage
					u.In, u.CacheRead, u.CacheWrite = mu.InputTokens, mu.CacheReadInputTokens, mu.CacheCreationInputTokens
				}
			case "content_block_start":
				if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
					tools[ev.Index] = &tool{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				}
			case "content_block_delta":
				if ev.Delta == nil {
					break
				}
				switch ev.Delta.Type {
				case "text_delta":
					ch <- Event{Kind: EvText, Text: ev.Delta.Text}
				case "input_json_delta":
					if t := tools[ev.Index]; t != nil {
						t.args.WriteString(ev.Delta.PartialJSON)
					}
					ch <- Event{Kind: EvProgress}
				default: // thinking, signatures
					ch <- Event{Kind: EvProgress}
				}
			case "content_block_stop":
				if t := tools[ev.Index]; t != nil {
					ch <- Event{Kind: EvToolCall, Call: ToolCall{ID: t.id, Name: t.name, Args: t.args.String()}}
					delete(tools, ev.Index)
				}
			case "message_delta":
				if ev.Delta != nil && ev.Delta.StopReason != "" {
					stop = ev.Delta.StopReason
				}
				if ev.Usage != nil {
					u.Out = ev.Usage.OutputTokens
				}
			case "message_stop":
				return false
			case "error":
				if ev.Error != nil {
					streamErr = &APIError{Status: 529, Body: ev.Error.Type + ": " + ev.Error.Message,
						Retryable: ev.Error.Type == "overloaded_error"}
				}
				return false
			}
			return true
		})
		switch {
		case ctx.Err() != nil:
			ch <- Event{Kind: EvError, Err: ctx.Err()}
		case streamErr != nil:
			ch <- Event{Kind: EvError, Err: streamErr}
		case perr != nil:
			ch <- Event{Kind: EvError, Err: fmt.Errorf("stream: %w", perr)}
		default:
			ch <- Event{Kind: EvUsage, Usage: u.clamped()}
			ch <- Event{Kind: EvDone, Stop: stop}
		}
	}()
	return ch
}
