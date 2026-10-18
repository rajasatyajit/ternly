package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A rerun skips what results.jsonl already holds for the same binary, keeps
// rows written before rows carried a hash (attributed to <out>/ternly), and
// re-runs everything for a different binary.
func TestResumeSkipsCompleted(t *testing.T) {
	dir := t.TempDir()
	rows := []Result{
		{Task: "a", Model: "auto", Arm: "x", Run: 0, Binary: "B1"},
		{Task: "b", Model: "auto", Arm: "x", Run: 0},               // legacy: no hash
		{Task: "c", Model: "auto", Arm: "y", Run: 1, Binary: "B2"}, // another binary
		{Task: "d", Model: "auto", Arm: "x", Run: 0, Binary: "B1", Outcome: "infra"},
	}
	var b []byte
	for _, r := range rows {
		j, _ := json.Marshal(r)
		b = append(append(b, j...), '\n')
	}
	b = append(b, []byte("{not json\n")...)
	p := filepath.Join(dir, "results.jsonl")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	done := completed(p, "B1")
	for _, c := range []struct {
		key  string
		want bool
	}{
		{resumeKey("a", "auto", "x", 0, "B1"), true},
		{resumeKey("b", "auto", "x", 0, "B1"), true},  // legacy row, same binary
		{resumeKey("b", "auto", "x", 0, "B3"), false}, // legacy row, another binary
		{resumeKey("a", "auto", "x", 1, "B1"), false}, // another run
		{resumeKey("a", "auto", "z", 0, "B1"), false}, // another arm
		{resumeKey("c", "auto", "y", 1, "B1"), false}, // measured on B2
		{resumeKey("c", "auto", "y", 1, "B2"), true},
		{resumeKey("d", "auto", "x", 0, "B1"), false}, // no model ran: run again
	} {
		if done[c.key] != c.want {
			t.Errorf("%s: done=%v, want %v", c.key, done[c.key], c.want)
		}
	}
	if len(completed(filepath.Join(dir, "missing.jsonl"), "B1")) != 0 {
		t.Error("a missing file has completed jobs")
	}
}

func TestStateSavedAtomically(t *testing.T) {
	dir := t.TempDir()
	st := &runState{Out: dir, Total: 3, Done: 1, Started: time.Now()}
	st.save()
	var got runState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || json.Unmarshal(b, &got) != nil || got.Total != 3 || got.Done != 1 || got.Updated.IsZero() {
		t.Fatalf("state.json: %s %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); err == nil {
		t.Error("temp file left behind")
	}
}

func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("abc"), 0o644)
	if h, _ := fileSHA256(p); h != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256(abc) = %s", h)
	}
}

func TestStatusLine(t *testing.T) {
	dir := t.TempDir()
	st := &runState{Out: dir, Arm: "control", Total: 40, Skipped: 19, Done: 2, Running: []string{"x auto run 1"}, Last: "y auto run 1: pass"}
	st.save()
	var b strings.Builder
	if err := status(&b, []string{filepath.Join(dir, "state.json")}); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "arm=control 21/40 done") || !strings.Contains(got, "last: y auto run 1: pass") {
		t.Fatalf("status line: %q", got)
	}
}
