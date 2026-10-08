package agent

import (
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Deterministic mode pins sampling on every request; off, nothing is sent.
func TestDeterministicPinsSampling(t *testing.T) {
	f := newFake(t, reply{text: "hello"})
	a, _ := newAgent(t, "edits", model(f.URL, "m", 3, 1, 5))
	a.Run(bg, "hi")
	a.Deterministic = true
	a.Run(bg, "hi again")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pins[0] != "" || f.pins[1] != "t=0,s=20261008" {
		t.Fatalf("pins = %q", f.pins)
	}
}

// With a response cache, a rerun of the same deterministic conversation is
// answered from disk.
func TestDeterministicRerunCached(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, reply{text: "the answer"})
	run := func() (*fakeLLM, *llm.Cache) {
		a, _ := newAgent(t, "edits", model(f.URL, "m", 3, 1, 5))
		a.Deterministic, a.Responses = true, &llm.Cache{Dir: dir}
		a.system = "fixed system prompt" // newAgent's workspace path differs per run
		a.Run(bg, "same question")
		return f, a.Responses
	}
	_, c1 := run()
	n := len(f.requests())
	_, c2 := run()
	if c1.Misses.Load() == 0 || c2.Hits.Load() == 0 || c2.Misses.Load() != 0 || len(f.requests()) != n {
		t.Fatalf("first: %d misses; rerun: %d hits, %d misses, %d new requests to the server", c1.Misses.Load(), c2.Hits.Load(), c2.Misses.Load(), len(f.requests())-n)
	}
}

// The prompt version ignores per-run lines and changes with the tools.
func TestPromptVersion(t *testing.T) {
	f := newFake(t, reply{text: "x"})
	a, _ := newAgent(t, "edits", model(f.URL, "m", 3, 1, 5))
	a.system = "You are ternly.\nEnvironment: linux/amd64, workspace /tmp/a, date 2026-10-08.\nGit branch: main\nRules."
	v1 := a.PromptVersion()
	a.system = "You are ternly.\nEnvironment: linux/amd64, workspace /tmp/b, date 2026-10-09.\nGit branch: dev\nRules."
	if v2 := a.PromptVersion(); v2 != v1 || len(v1) != 12 {
		t.Fatalf("per-run lines changed the version: %s vs %s", v1, v2)
	}
	a.system = "You are ternly.\nRules, changed."
	if a.PromptVersion() == v1 {
		t.Fatal("a changed prompt kept its version")
	}
	a.system = "You are ternly.\nEnvironment: x\nRules."
	v3 := a.PromptVersion()
	a.Reg.Add(&tools.Tool{Kind: tools.ReadOnly, Spec: llm.ToolSpec{Name: "extra_tool", Description: "new"}})
	a.Reg.Commit()
	if a.PromptVersion() == v3 {
		t.Fatal("a new tool kept the prompt version")
	}
}
