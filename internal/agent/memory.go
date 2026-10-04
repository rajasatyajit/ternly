package agent

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// Memory is long-term memory across turns and sessions (internal/memory).
type Memory interface {
	// Recall returns notes relevant to the prompt, framed for the user
	// message ("" if none), and how many items they hold.
	Recall(ctx context.Context, prompt string) (notes string, n int)
	// Learn records a finished turn. It returns at once; writes happen in the background.
	Learn(t Learned)
	// Rewound forgets what was learned automatically from turns n and later
	// of the current session: their summaries and fixes describe changes
	// that were undone.
	Rewound(n int)
}

// Learned is what a finished turn leaves for memory.
type Learned struct {
	Turn    int
	Prompt  string
	Answer  string   // the model's final text
	Changed []string // files the turn changed (from the checkpoint diff)
	Fix     *Fix     // a check that failed and later passed in this turn
	Model   string
}

// Fix is a verified fix: the first failure of a check, which later passed.
type Fix struct{ Cmd, Error string }

var (
	reErrPos  = regexp.MustCompile(`^\S+\.\w+:\d+(:\d+)?:\s*\S`) // file.go:12:3: message
	reErrWord = regexp.MustCompile(`(?i)\b(error|panic|undefined|cannot|failed|mismatch)\b|^--- FAIL`)
)

// firstError is the most telling line of a failed check's output: the first
// file:line diagnostic, else the first line naming an error.
func firstError(out string) string {
	var word string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<<<") || strings.HasPrefix(l, "[exit") {
			continue
		}
		if reErrPos.MatchString(l) {
			return clip(l, 200)
		}
		if word == "" && reErrWord.MatchString(l) {
			word = l
		}
	}
	return clip(word, 200)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// recall injects memory notes ahead of the prompt.
func (a *Agent) recall(ctx context.Context, prompt string) string {
	if a.Mem == nil {
		return ""
	}
	notes, n := a.Mem.Recall(ctx, prompt)
	if n > 0 {
		a.Emit(Event{Kind: EvStatus, Text: "memory: " + plural(n, "note") + " recalled (/memory to review)"})
	}
	return notes
}

// learn hands the finished turn to memory.
func (a *Agent) learn(st *turnState, changed []string) {
	if a.Mem == nil {
		return
	}
	a.mu.Lock()
	t := Learned{Turn: len(a.state.Turns), Prompt: st.prompt, Answer: st.answer, Changed: changed, Fix: st.fix}
	if a.current != nil {
		t.Model = a.current.Key()
	}
	a.mu.Unlock()
	a.Mem.Learn(t)
}

// checkResult tracks a failing check that later passes (a verified fix).
func (st *turnState) checkResult(cmd, out string, ok bool) {
	switch {
	case !ok && st.failErr == "":
		if e := firstError(out); e != "" {
			st.failCmd, st.failErr = cmd, e
		}
	case ok && st.failErr != "" && st.fix == nil:
		st.fix = &Fix{Cmd: st.failCmd, Error: st.failErr}
	}
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}
