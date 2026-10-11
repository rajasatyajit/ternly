package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/gitenv"
	"github.com/rajasatyajit/ternly/internal/llm"
)

func editArgs(t *testing.T, path, old, new string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "old_string": old, "new_string": new})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An edit shows its change under its tool line: up to 6 lines, the rest on
// Ctrl+O; what the model wrote is shown as text.
func TestEditShowsDiff(t *testing.T) {
	m := sized(t, 120, 40)
	m.onAgent(agent.Event{Kind: agent.EvToolStart, ToolID: "e1", Tool: "edit_file", Text: "lru.go  (-1 +1 lines)",
		Args: editArgs(t, "lru.go", "c.cap = n; c.evictOne()", "c.cap = n; c.evictUntilFits()")})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, ToolID: "e1", Tool: "edit_file", OK: true})
	m.refresh(true)
	v := stripANSI(m.View().Content)
	for _, want := range []string{"- c.cap = n; c.evictOne()", "+ c.cap = n; c.evictUntilFits()"} {
		if !strings.Contains(v, want) {
			t.Errorf("the edit's change lacks %q:\n%s", want, v)
		}
	}

	var old, new []string
	for i := range 10 {
		old, new = append(old, fmt.Sprintf("old line %d", i)), append(new, fmt.Sprintf("new line %d \x1b[2J", i))
	}
	m.onAgent(agent.Event{Kind: agent.EvToolStart, ToolID: "e2", Tool: "edit_file", Text: "big.go",
		Args: editArgs(t, "big.go", strings.Join(old, "\n"), strings.Join(new, "\n"))})
	m.refresh(true)
	v = stripANSI(m.View().Content)
	if !strings.Contains(v, "… +14 lines · ctrl+o expands") || strings.Contains(v, "new line 9") {
		t.Errorf("a long change should show 6 lines and a count:\n%s", v)
	}
	key(m, "ctrl+o")
	m.refresh(true)
	if v = stripANSI(m.View().Content); !strings.Contains(v, "new line 9") {
		t.Errorf("Ctrl+O should show the whole change:\n%s", v)
	}
	if strings.Contains(m.View().Content, "\x1b[2J") || !strings.Contains(v, `new line 9 \x1b[2J`) {
		t.Error("the model's text must reach the screen as text, escaped")
	}
}

// The routing card says why the model was chosen.
func TestRouteCard(t *testing.T) {
	m := sized(t, 200, 50)
	md := &discover.Model{Provider: &discover.Provider{ID: "ollama", Local: true}, ProvID: "ollama", ID: "gemma4:latest", Tools: true, Tier: 1}
	for reason, want := range map[string]string{
		"T1 task → est. 28 s, free, p≥0.30":    "chosen as the cheapest capable model: T1 task → est. 28 s, free, p≥0.30",
		"failover":                             "failover: the previous model failed",
		"escalated: the model stopped working": "escalated: the model stopped working",
	} {
		m.onAgent(agent.Event{Kind: agent.EvModel, Model: md, Reason: reason})
		m.refresh(true)
		if v := stripANSI(m.View().Content); !strings.Contains(v, "ollama/gemma4:latest") || !strings.Contains(v, want) {
			t.Errorf("reason %q: the card lacks %q", reason, want)
		}
	}
}

// Every finished turn leaves a receipt: tokens, cost, time, the verdict and
// the files changed.
func TestReceipt(t *testing.T) {
	m := sized(t, 200, 50)
	m.onAgent(agent.Event{Kind: agent.EvModel, Model: &discover.Model{Provider: &discover.Provider{ID: "x"}, ProvID: "x", ID: "m1", Priced: true, In: 1, Out: 1}, Reason: "T1"})
	m.busy, m.turnStart = true, time.Now().Add(-12*time.Second)
	m.turnUsage0, m.turnFiles = llm.Usage{In: 100}, map[string]bool{}
	m.onAgent(agent.Event{Kind: agent.EvToolStart, ToolID: "e1", Tool: "edit_file", Args: editArgs(t, "lru.go", "a", "b")})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, ToolID: "e1", Tool: "edit_file", OK: true})
	m.onAgent(agent.Event{Kind: agent.EvToolStart, ToolID: "w1", Tool: "write_file", Args: `{"path":"new.go","content":"x"}`})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, ToolID: "w1", Tool: "write_file", OK: false}) // failed: not a change
	m.onAgent(agent.Event{Kind: agent.EvVerify, Text: "go build"})
	m.onAgent(agent.Event{Kind: agent.EvToolEnd, ToolID: "verify", Tool: "verify", OK: true, Verdict: agent.VerdictVerified})
	m.onAgent(agent.Event{Kind: agent.EvDone, Ledger: agent.Ledger{Usage: llm.Usage{In: 2300, Out: 120}, Cost: 0.0042}})
	m.refresh(true)
	v := stripANSI(m.View().Content)
	for _, want := range []string{"↳ turn 2.2k↑ 120↓", "$0.0042", "12s", "verified ✓", "1 file changed (lru.go)"} {
		if !strings.Contains(v, want) {
			t.Errorf("the receipt lacks %q:\n%s", want, v)
		}
	}
}

// An error is framed with ternly's cause and the next step, and counted on
// the status line; ASCII mode frames it in ASCII.
func TestErrorBox(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for _, w := range []int{80, 120} {
			m := sized(t, w, 30)
			m.App.Access.ASCII = ascii
			m.onAgent(agent.Event{Kind: agent.EvError, Text: `openai: HTTP 500: {"error":{"message":"SCRIPTED-FAILURE: the upstream model is overloaded"}}`})
			m.refresh(true)
			v := strings.Join(fits(t, m.View().Content, w), "\n")
			for _, want := range []string{"the provider failed on its side", "SCRIPTED-FAILURE", "next: ask again"} {
				if !strings.Contains(v, want) {
					t.Errorf("w=%d ascii=%v: the error lacks %q:\n%s", w, ascii, want, v)
				}
			}
			if bar := stripANSI(m.statusBar()); !strings.Contains(bar, "1 error") && !strings.Contains(bar, "⚠1") && !strings.Contains(bar, "!1") {
				t.Errorf("w=%d: the status doesn't count the error: %q", w, bar)
			}
			if box := stripANSI(m.errorBox(m.blocks[len(m.blocks)-1])); ascii && (strings.ContainsAny(box, "╭╮╰╯│─") || !strings.Contains(box, "+-")) {
				t.Errorf("ASCII mode should frame in ASCII:\n%s", box)
			}
		}
	}
	for text, want := range map[string]string{
		"HTTP 429 Too Many Requests":                   "rate limit or quota",
		"401 Unauthorized: invalid api key":            "refused the credentials",
		"context deadline exceeded":                    "took too long",
		"dial tcp 127.0.0.1:11434: connection refused": "can't be reached",
		"maximum context length is 8192 tokens":        "no longer fits",
		"something nobody has seen":                    "the turn failed",
	} {
		if c, _ := errorCause(text); !strings.Contains(c, want) {
			t.Errorf("%q: cause %q, want %q", text, c, want)
		}
	}
}

// The header shows the branch (and * when tracked files changed); it gives
// way after the title and before the path, and a hostile branch name is
// shown as text.
func TestHeaderBranch(t *testing.T) {
	long := "/home/someone/work/" + strings.Repeat("a-very-long-directory-name/", 4) + "project-x"
	for _, s := range [][2]int{{200, 50}, {120, 40}, {80, 24}, {40, 24}} {
		m := sized(t, s[0], s[1])
		m.App.Reg.Root = long
		m.App.Agent.Commit(agent.Record{T: "title", Text: "Fix the resize eviction in the LRU cache"})
		m.Update(gitMsg{branch: "fix-resize-branch", dirty: true})
		h := fits(t, m.header(), s[0])[0]
		modes := m.App.Reg.Sandbox.Mode() + " · " + m.App.Reg.Policy.Mode()
		if !strings.HasSuffix(h, modes) {
			t.Errorf("%d: the header lost the modes: %q", s[0], h)
		}
		if s[0] >= 80 && !strings.Contains(h, "fix-resize") {
			t.Errorf("%d: the header lacks the branch: %q", s[0], h)
		}
		if s[0] >= 120 && !strings.Contains(h, "fix-resize-branch*") {
			t.Errorf("%d: the branch should be whole, with the dirty mark: %q", s[0], h)
		}
	}
	m := sized(t, 120, 40)
	m.Update(gitMsg{branch: "evil\x1b]0;pwned\x07"})
	if h := m.header(); strings.Contains(h, "\x1b]0;") {
		t.Errorf("a branch name reached the terminal as a sequence: %q", h)
	}
}

// gitInfo reads a real repository's branch and dirty state, and nothing
// outside one.
func TestGitInfo(t *testing.T) {
	if _, err := gitenv.Command(context.Background(), "--version").Output(); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		c := gitenv.Command(context.Background(), append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if msg := gitInfo(dir)().(gitMsg); msg.branch != "" {
		t.Fatalf("outside a repository: %+v", msg)
	}
	git("init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "a")
	git("checkout", "-q", "-b", "feature/x")
	if msg := gitInfo(dir)().(gitMsg); msg.branch != "feature/x" || msg.dirty {
		t.Fatalf("clean: %+v", msg)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := gitInfo(dir)().(gitMsg); !msg.dirty {
		t.Fatalf("a tracked change: %+v", msg)
	}
}

// yieldsGit runs a command (and any batch it returns) and reports whether
// one of its messages is the git refresh.
func yieldsGit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(3 * time.Second): // a command that blocks (a ticker) isn't it
		return false
	}
	switch msg := msg.(type) {
	case gitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if yieldsGit(c) {
				return true
			}
		}
	}
	return false
}

// The branch is read at startup and again after every turn (a turn can
// switch or dirty it).
func TestGitRefreshed(t *testing.T) {
	m := sized(t, 120, 40)
	m.App.Discover = func() ([]*discover.Model, []string) { return nil, nil }
	if !yieldsGit(m.Init()) {
		t.Error("Init doesn't read the branch")
	}
	_, cmd := m.Update(agentMsg(agent.Event{Kind: agent.EvDone}))
	if !yieldsGit(cmd) {
		t.Error("a finished turn doesn't re-read the branch")
	}
	_, cmd = m.Update(agentMsg(agent.Event{Kind: agent.EvText, Text: "x"}))
	if yieldsGit(cmd) {
		t.Error("only a finished turn re-reads the branch")
	}
}

// renderDiff escapes what the model wrote by itself, not only through the
// frame's last check (defence in depth, ADR 023).
func TestRenderDiffEscapes(t *testing.T) {
	m := sized(t, 120, 40)
	out := m.renderDiff([]string{"+evil \x1b]0;pwned\x07 \x1b[2J"})
	if strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\x1b[2J") || !strings.Contains(stripANSI(out), `\x1b[2J`) {
		t.Errorf("renderDiff passed a control sequence through: %q", out)
	}
}
