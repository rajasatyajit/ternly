package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// msgTextCall tells a model that its tool call, written as text, didn't run.
const msgTextCall = "Your last message contains a tool call written as text, so nothing ran. Tools run only when you call them through the tool-calling interface. Call the tool now (or answer in plain prose if you are done)."

// reJSONStart finds the start of JSON objects that may be tool calls written
// as text: {"name": … (OpenAI style, often with "parameters" or "arguments").
var reJSONStart = regexp.MustCompile(`\{\s*"(name|tool|function)"\s*:`)

// textToolCall reports the name of a registered tool that content calls in
// text: a JSON object whose "name" (or "tool", or "function"/"function.name")
// is a tool, with an arguments object ("parameters", "arguments", "input" or
// "args"). Prose that merely mentions a tool doesn't count.
func textToolCall(content string, specs []llm.ToolSpec) string {
	if !strings.Contains(content, "{") {
		return ""
	}
	tools := map[string]bool{}
	for _, s := range specs {
		tools[s.Name] = true
	}
	for _, loc := range reJSONStart.FindAllStringIndex(content, 8) {
		d := json.NewDecoder(strings.NewReader(content[loc[0]:]))
		var v map[string]json.RawMessage
		if d.Decode(&v) != nil {
			continue
		}
		name := ""
		for _, k := range []string{"name", "tool", "function"} {
			var s string
			if json.Unmarshal(v[k], &s) == nil && s != "" {
				name = s
				break
			}
			var fn struct{ Name string }
			if json.Unmarshal(v[k], &fn) == nil && fn.Name != "" {
				name = fn.Name
				break
			}
		}
		if !tools[name] {
			continue
		}
		for _, k := range []string{"parameters", "arguments", "input", "args"} {
			if raw, ok := v[k]; ok && len(raw) > 0 && (raw[0] == '{' || raw[0] == '"') {
				return name
			}
		}
	}
	return ""
}
