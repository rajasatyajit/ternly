package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Perf-gate benchmarks (ADR 017): keystroke-to-frame and redraw cost with a
// long transcript (~10k lines: 1,000 turns of prompt, tool call, answer).

func benchModel(b *testing.B, turns int) *Model {
	b.Helper()
	reg, err := tools.NewRegistry(b.TempDir(), tools.NewPolicy("ask", nil), tools.NewSandbox(false, false, nil), tools.NewRedactor(nil))
	if err != nil {
		b.Fatal(err)
	}
	r := discover.NewRouter()
	m := New(&App{Agent: agent.New(reg, r, func(agent.Event) {}), Router: r, Reg: reg, Theme: "dark"}, true)
	for i := range turns {
		m.blocks = append(m.blocks,
			&block{kind: bUser, text: fmt.Sprintf("turn %d: fix the failing test in pkg/a", i)},
			&block{kind: bTool, tool: "read_file", text: "pkg/a/a.go", state: 1, detail: "120 lines"},
			&block{kind: bAssistant, text: "Fixed `Parse`: it now rejects empty input.\n\n- added a test\n- ran `go test ./...`\n\n✓ verified"})
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// BenchmarkKeystroke: one typed character, through Update, to a rendered frame.
func BenchmarkKeystroke(b *testing.B) {
	m := benchModel(b, 1000)
	k := tea.KeyPressMsg{Code: 'a', Text: "a"}
	b.ReportAllocs()
	for b.Loop() {
		m.Update(k)
		_ = m.View()
	}
}

// BenchmarkRedraw10k: a transcript redraw (new output arriving), blocks already rendered.
func BenchmarkRedraw10k(b *testing.B) {
	m := benchModel(b, 1000)
	if n := strings.Count(m.vp.GetContent(), "\n"); n < 10000 {
		b.Fatalf("transcript is %d lines, want ≥ 10k", n)
	}
	b.ReportAllocs()
	for b.Loop() {
		m.refresh(true)
		_ = m.View()
	}
}

// BenchmarkRenderAnswer: markdown rendering of one new assistant answer.
func BenchmarkRenderAnswer(b *testing.B) {
	m := benchModel(b, 0)
	blk := &block{kind: bAssistant, text: strings.Repeat("Some **markdown** with `code` and a list:\n\n- one\n- two\n\n```go\nfunc f() int { return 1 }\n```\n\n", 4)}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderBlock(blk)
	}
}
