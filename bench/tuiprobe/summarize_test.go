package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCell(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSummarize(t *testing.T) {
	dir := t.TempDir()
	for i, echo := range [][]float64{{2, 4}, {6, 8}, {10, 100}} {
		writeCell(t, dir, "x.base-120x40-truecolor-r"+string(rune('1'+i)), Result{Steps: []StepResult{
			{Step: "until 60000 Ready", OK: true, MS: float64(100 * (i + 1))},
			{Step: "keys ab", OK: true, EchoMS: echo, KeyBytes: []int{10, 20}},
			{Step: "idle 30", OK: true, CPUPercent: 0.5},
		}, Tech: Technique{TrueColor: 3, NonASCII: 7}})
	}
	writeCell(t, dir, "x.base-120x40-256", map[string]any{"outcome": "timeout"})
	writeCell(t, dir, "x.stream-120x40-r1", Result{Steps: []StepResult{{Step: "stream 8000 300000 3000", OK: true, Bytes: 5000, MS: 900}},
		Snaps: map[string]string{"answered": "It evicts the oldest entry."}})
	writeCell(t, dir, "x.stream-120x40-r2", Result{Steps: []StepResult{{Step: "stream 8000 300000 3000", OK: true, Bytes: 4000, MS: 700}},
		Snaps: map[string]string{"answered": "glob{pattern:simplelru/lru.go}"}})
	var b strings.Builder
	if err := summarize(&b, dir); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// echo over all 6 keys: p50 = 6, p95 = 100; ready p50 = 200; bytes/key p50 = 10
	for _, want := range []string{"| x | 3/3 | 200 | 6.0 / 100.0 | 10 | 0.50 | n/a |", "| timeout |", "24-bit", "| 700 | 1/2 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPctNaN(t *testing.T) {
	if f(pct(nil, 50), 1) != "n/a" {
		t.Fatal("no data must print n/a, never 0")
	}
}

func TestAnswerSeen(t *testing.T) {
	prompt := "> Explain what simplelru/lru.go does and how eviction works.\n"
	for screen, want := range map[string]bool{
		prompt + "runs glob\nglob{pattern:simplelru/lru.go}\n":                 false, // the prompt alone isn't an answer
		prompt + "When full, Add calls removeOldest, which evicts the tail.\n": true,
		prompt + "It drops the Least Recently Used entry.\n":                   true,
	} {
		if got := answerSeen(screen); got != want {
			t.Errorf("answerSeen(%q) = %v, want %v", screen, got, want)
		}
	}
}
