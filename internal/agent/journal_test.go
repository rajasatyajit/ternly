package agent

import (
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// Found by FuzzReplay: a log whose snapshot has a turn index past the history
// made a later rewind slice out of range (a crash on resume), and a compaction
// cut between a call and its result left a result that answers nothing.
func TestReplayBadIndicesAndOrphans(t *testing.T) {
	var s State
	s.Apply(Record{T: "snapshot", State: &State{History: []llm.Message{{Role: "user", Content: "a"}}, Turns: []Turn{{Hist: 1000}}}})
	s.Apply(Record{T: "rewind", N: 1, Mode: "both"}) // must not panic
	s = State{}
	for _, r := range []Record{
		{T: "turn"},
		{T: "msg", Msg: &llm.Message{Role: "user", Content: "go"}},
		{T: "msg", Msg: &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash"}}}},
		{T: "msg", Msg: &llm.Message{Role: "tool", ToolCallID: "c1", Content: "r"}},
		{T: "turn"},
		{T: "msg", Msg: &llm.Message{Role: "user", Content: "next"}},
		{T: "compact", Cut: 3, Text: "summary"}, // keeps from the tool result: an orphan
	} {
		s.Apply(r)
	}
	s.Turns = append(s.Turns, Turn{Hist: 99})
	if n := s.Repair(); n == 0 {
		t.Fatal("nothing repaired")
	}
	for i, m := range s.History {
		if m.Role == "tool" {
			t.Fatalf("history[%d] is a result with no call: %+v", i, s.History)
		}
	}
	if len(s.History) != 2 || s.Turns[0].Hist != 1 || s.Turns[1].Hist != 2 {
		t.Fatalf("history %+v turns %+v", s.History, s.Turns)
	}
}
