package agent

import (
	"encoding/json"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// Record is one persisted change to a session's state. The live agent and
// replay both go through State.Apply, so a resumed session equals the live one.
type Record struct {
	T        string       `json:"t"`
	TS       int64        `json:"ts,omitempty"` // unix ms
	Msg      *llm.Message `json:"msg,omitempty"`
	Prompt   string       `json:"prompt,omitempty"`
	N        int          `json:"n,omitempty"` // turn number (cp, rewind, fork)
	Tree     string       `json:"tree,omitempty"`
	Mode     string       `json:"mode,omitempty"` // rewind mode
	Cut      int          `json:"cut,omitempty"`  // compact: history index kept from
	Text     string       `json:"text,omitempty"` // compact summary, title, status, model
	Usage    *llm.Usage   `json:"usage,omitempty"`
	Cost     float64      `json:"cost,omitempty"`
	Stats    *Stats       `json:"stats,omitempty"`
	Settings *Settings    `json:"settings,omitempty"`
	State    *State       `json:"state,omitempty"` // snapshot (fork)
}

// Journal persists records. Record and Sync must not block on disk.
type Journal interface {
	Record(Record)
	Sync() // make everything recorded so far durable (fsync), asynchronously
}

// Settings that resume restores (CLI flags win over them).
type Settings struct {
	Mode   string  `json:"mode,omitempty"`
	Pin    string  `json:"pin,omitempty"`
	Verify *string `json:"verify,omitempty"`
	Limits Limits  `json:"limits"`
	Budget float64 `json:"budget,omitempty"`
}

// Turn marks where a user turn starts, for rewind.
type Turn struct {
	Prompt string    `json:"prompt"`
	At     time.Time `json:"at"`
	Hist   int       `json:"hist"`           // len(History) before the prompt
	Tree   string    `json:"tree,omitempty"` // workspace before the turn's first mutation ("" = none)
}

// State is everything a session persists.
type State struct {
	History  []llm.Message `json:"history"`
	Turns    []Turn        `json:"turns"`
	Ledger   Ledger        `json:"ledger"`
	Stats    Stats         `json:"stats"`
	Settings *Settings     `json:"settings,omitempty"`
	Title    string        `json:"title,omitempty"`
	Status   string        `json:"status,omitempty"` // active | paused | stopped
	Tree     string        `json:"tree,omitempty"`   // workspace at the end of the last mutating turn (drift baseline)
	Model    string        `json:"model,omitempty"`  // last routed model
	Active   time.Time     `json:"active"`
}

// Apply changes s by one record.
func (s *State) Apply(r Record) {
	if r.TS != 0 {
		s.Active = time.UnixMilli(r.TS)
	}
	switch r.T {
	case "turn":
		s.Turns = append(s.Turns, Turn{Prompt: r.Prompt, At: s.Active, Hist: len(s.History)})
		s.Ledger.Turns++
	case "msg":
		if r.Msg != nil {
			s.History = append(s.History, *r.Msg)
		}
	case "usage":
		if r.Usage != nil {
			s.Ledger.Usage.Add(*r.Usage)
		}
		s.Ledger.Cost += r.Cost
	case "cp":
		if r.N >= 1 && r.N <= len(s.Turns) {
			s.Turns[r.N-1].Tree = r.Tree
		}
	case "tree":
		s.Tree = r.Tree
	case "model":
		s.Model = r.Text
	case "compact":
		if r.Cut < 1 || r.Cut > len(s.History) {
			return
		}
		s.History = append([]llm.Message{{Role: "user", Content: "[Summary of earlier conversation]\n" + r.Text}}, s.History[r.Cut:]...)
		// Turns before the cut can no longer be rewound to; later ones shift down.
		kept := s.Turns[:0]
		for _, t := range s.Turns {
			if t.Hist >= r.Cut {
				t.Hist -= r.Cut - 1
				kept = append(kept, t)
			}
		}
		s.Turns = kept
	case "rewind":
		if r.Mode != RewindCode && r.N >= 1 && r.N <= len(s.Turns) {
			s.History = s.History[:min(max(s.Turns[r.N-1].Hist, 0), len(s.History))]
			s.Turns = s.Turns[:r.N-1]
		}
	case "reset":
		s.History, s.Turns = nil, nil
	case "settings":
		s.Settings = r.Settings
	case "stats":
		if r.Stats != nil {
			s.Stats = *r.Stats
		}
	case "title":
		s.Title = r.Text
	case "status":
		s.Status = r.Text
	case "snapshot":
		if r.State != nil {
			*s = r.State.Clone()
		}
	}
}

// Clone deep-copies s (messages and turns are copied; message strings are immutable).
func (s State) Clone() State {
	c := s
	c.History = append([]llm.Message(nil), s.History...)
	for i := range c.History {
		c.History[i].ToolCalls = append([]llm.ToolCall(nil), c.History[i].ToolCalls...)
	}
	c.Turns = append([]Turn(nil), s.Turns...)
	if s.Settings != nil {
		st := *s.Settings
		c.Settings = &st
	}
	return c
}

// Repair makes the history valid for every provider after a crash or a bad
// log: every tool call gets a result (the process stopped mid-call), results
// that answer no earlier call are dropped (a compaction cut between them),
// and turn indices stay inside the history. It returns how many changes it
// made.
func (s *State) Repair() int {
	n := 0
	called := map[string]bool{}
	var dropped []int // indices of removed results, ascending
	kept := s.History[:0]
	for i, m := range s.History {
		if m.Role == "assistant" { // only an assistant message's calls are calls
			for _, tc := range m.ToolCalls {
				called[tc.ID] = true
			}
		}
		if m.Role == "tool" && !called[m.ToolCallID] {
			dropped = append(dropped, i)
			continue
		}
		kept = append(kept, m)
	}
	s.History = kept
	for k := range s.Turns { // a turn starts as many messages earlier as were dropped before it
		before := 0
		for _, d := range dropped {
			if d < s.Turns[k].Hist {
				before++
			}
		}
		s.Turns[k].Hist -= before
	}
	n += len(dropped)
	answered := map[string]bool{}
	for _, m := range s.History {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for i := len(s.History) - 1; i >= 0; i-- {
		m := s.History[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		var missing []llm.Message
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				answered[tc.ID] = true // a repeated ID gets one result
				missing = append(missing, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: "cancelled: the session ended before this tool call finished; its effects, if any, are unknown — check before relying on them."})
			}
		}
		if len(missing) == 0 {
			continue
		}
		// results go right after the calls they answer (and before any later message)
		j := i + 1
		for j < len(s.History) && s.History[j].Role == "tool" {
			j++
		}
		s.History = append(s.History[:j], append(missing, s.History[j:]...)...)
		for k := range s.Turns {
			if s.Turns[k].Hist >= j {
				s.Turns[k].Hist += len(missing)
			}
		}
		n += len(missing)
	}
	for k := range s.Turns {
		if h := min(max(s.Turns[k].Hist, 0), len(s.History)); h != s.Turns[k].Hist {
			s.Turns[k].Hist = h
			n++
		}
	}
	return n
}

// redacted returns r with secrets removed from every free-text field.
func (r Record) redacted(apply func(string) string) Record {
	r.Prompt, r.Text = apply(r.Prompt), apply(r.Text)
	if r.Msg != nil {
		m := *r.Msg
		m.Content = apply(m.Content)
		m.ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
		for i := range m.ToolCalls {
			m.ToolCalls[i].Args = apply(m.ToolCalls[i].Args)
		}
		r.Msg = &m
	}
	if r.State != nil {
		b, _ := json.Marshal(r.State)
		var st State
		_ = json.Unmarshal([]byte(apply(string(b))), &st)
		r.State = &st
	}
	return r
}
