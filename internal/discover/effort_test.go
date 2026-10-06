package discover

import (
	"strings"
	"testing"
)

func TestEffortLabel(t *testing.T) {
	ollama := &Provider{Kind: "openai"}
	anth := &Provider{Kind: "anthropic"}
	m := func(id string, p *Provider, reasoning bool) *Model {
		return &Model{ID: id, Provider: p, Reasoning: reasoning}
	}
	user := []EffortRule{{Match: `^qwen3`, Verified: true}, {Match: `kimi`, Map: map[string]string{"medium": "low"}, Why: "my measurement"}}
	for _, c := range []struct {
		m           *Model
		want, label string
		noted       bool
	}{
		{m("glm-5.3:cloud", ollama, true), "low", "low", false},
		{m("glm-5.3:cloud", ollama, true), "medium", "high", true}, // ADR 015 evidence
		{m("glm-5.3:cloud", ollama, true), "high", "high", false},
		{m("gpt-oss:120b-cloud", ollama, true), "medium", "medium", false}, // documented
		{m("openai/o4-mini", ollama, true), "medium", "medium", false},
		{m("gemini-2.5-pro", ollama, true), "medium", "medium", false},
		{m("claude-sonnet-5", anth, true), "medium", "medium", false},          // token budgets
		{m("deepseek-v4.1-flash:cloud", ollama, true), "medium", "high", true}, // unverified: never medium
		{m("deepseek-v4.1-flash:cloud", ollama, true), "low", "low", false},
		{m("qwen3.6:latest", ollama, true), "medium", "medium", false}, // the user verified it
		{m("kimi-k2.7-code:cloud", ollama, true), "medium", "low", true},
		{m("llama3.1:8b", ollama, false), "medium", "", false}, // doesn't reason: nothing sent
	} {
		label, note := c.m.EffortLabel(c.want, user)
		if label != c.label || (note != "") != c.noted {
			t.Errorf("%s %s: sent %q (note %q), want %q (noted %v)", c.m.ID, c.want, label, note, c.label, c.noted)
		}
		if c.noted && !strings.Contains(note, "asked "+c.want) {
			t.Errorf("note %q", note)
		}
	}
}
