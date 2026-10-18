package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/eval"
)

func write(t *testing.T, p string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMerge(t *testing.T) {
	dir := t.TempDir()
	defs := filepath.Join(dir, "defaults.json")
	write(t, defs, []eval.Record{{Model: "ollama/a", Version: eval.Version, Runs: 1}, {Model: "ollama/b", Version: eval.Version, Runs: 1}})
	rec := filepath.Join(dir, "a.json")
	write(t, rec, eval.Record{Model: "ollama/a", Version: eval.Version, Runs: 3})
	if err := run(defs, aliases{"ollama/a=ollama/a2"}, []string{rec}); err != nil {
		t.Fatal(err)
	}
	var got []eval.Record
	b, _ := os.ReadFile(defs)
	_ = json.Unmarshal(b, &got)
	runs := map[string]int{}
	for _, r := range got {
		runs[r.Model] = r.Runs
	}
	if len(got) != 3 || runs["ollama/a"] != 3 || runs["ollama/b"] != 1 || runs["ollama/a2"] != 3 {
		t.Fatalf("merged: %v", runs)
	}
	old := filepath.Join(dir, "old.json")
	write(t, old, eval.Record{Model: "ollama/c", Version: "3", Runs: 1})
	if err := run(defs, nil, []string{old}); err == nil {
		t.Fatal("a record of another eval version was merged")
	}
	if err := run(defs, aliases{"ollama/x=ollama/y"}, []string{rec}); err == nil {
		t.Fatal("an alias of a model not among the records was accepted")
	}
}

// The shipped defaults carry the counts, not the answers: an answer can quote
// the measuring machine's paths (gemma4's did: its home directory).
func TestMergeDropsOutcomes(t *testing.T) {
	dir := t.TempDir()
	defs := filepath.Join(dir, "defaults.json")
	write(t, defs, []eval.Record{})
	rec := filepath.Join(dir, "a.json")
	write(t, rec, eval.Record{Model: "ollama/a", Version: eval.Version, Runs: 1,
		Outcomes: []eval.Outcome{{Trap: "x", Answer: "no Go files in /home/someone/.cache/x"}}})
	if err := run(defs, aliases{"ollama/a=ollama/a2"}, []string{rec}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(defs)
	if strings.Contains(string(b), "/home/") || strings.Contains(string(b), `"outcomes"`) {
		t.Fatalf("the defaults carry the outcomes:\n%s", b)
	}
}
