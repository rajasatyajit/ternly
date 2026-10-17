package agent

import (
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

func TestTextToolCall(t *testing.T) {
	specs := []llm.ToolSpec{{Name: "edit_file"}, {Name: "read_file"}, {Name: "find_symbol"}}
	for in, want := range map[string]string{
		// what llama3.1:8b wrote on the suite's bug reports
		"Let me run a diff on this file.\n\n{\"name\": \"edit_file\", \"parameters\": {\"file_path\":\"lru.go\",\"file_offset\":221}}": "edit_file",
		`{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}`:                                                                       "read_file",
		`I'll call {"function": {"name": "find_symbol"}, "arguments": {"query": "X"}}`:                                                 "find_symbol",
		"I used edit_file to change the code; it's done.":                                                                              "",
		`{"name": "deploy", "parameters": {}}`:                                                                                         "", // not a tool
		"```json\n{\"call_sites\": [\"a.go:3\"]}\n```":                                                                                 "", // a structured answer
		`{"name": "edit_file"}`: "", // no arguments: a mention, not a call
	} {
		if got := textToolCall(in, specs); got != want {
			t.Errorf("textToolCall(%q) = %q, want %q", in, got, want)
		}
	}
}

// A model that writes its tool call as text has failed the turn: it is
// escalated once (ADR 018/029) and the next model makes the edit.
func TestTextCallEscalates(t *testing.T) {
	for _, off := range []bool{false, true} {
		f := newFake(t,
			reply{text: "Let me fix it.\n{\"name\": \"write_file\", \"parameters\": {\"path\": \"a.txt\", \"content\": \"good\"}}"},
			reply{calls: [][2]string{call("write_file", `{"path":"a.txt","content":"good\n"}`)}}, reply{text: "done"},
		)
		weak, strong := model(f.URL, "weak", 1, 0.1, 0.1), model(f.URL, "strong", 3, 10, 10)
		a, rec := newAgent(t, "yolo", weak, strong)
		a.NoTextCallEscalation = off
		a.Run(bg, "rename foo") // a T1 prompt: routing picks the weak model
		var used []string
		for _, e := range rec.events {
			if e.Kind == EvModel {
				used = append(used, e.Model.ID)
			}
		}
		escalated := len(used) > 1 && used[len(used)-1] == "strong"
		if escalated == off || (a.Stats().TextCalls == 1) == off {
			t.Fatalf("off=%v: models %v, text calls %d", off, used, a.Stats().TextCalls)
		}
		if !off && read(a, "a.txt") != "good\n" {
			t.Fatal("the escalated model didn't make the edit")
		}
	}
}
