package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveSubagent (TERNLY_E2E_MODEL=<local model>, e.g. qwen3.6) runs the
// real binary headlessly against a real local model: the model must delegate
// to a project agent (.claude/agents) through `task`, the subagent must read
// the file with its own tool, and the answer must be right. A count of 23
// isn't something to guess.
func TestLiveSubagent(t *testing.T) {
	model := os.Getenv("TERNLY_E2E_MODEL")
	if model == "" {
		t.Skip("TERNLY_E2E_MODEL not set")
	}
	home := testHome(t, "http://127.0.0.1:1") // the fake provider is unreachable; only the local model is used
	_ = os.WriteFile(filepath.Join(home, ".config", "ternly", "config.json"), []byte(`{"suggestions":false}`), 0o600)
	ws := t.TempDir()
	var lines []string
	for i := range 23 {
		lines = append(lines, fmt.Sprintf("entry %c%d", 'a'+i%26, i*7%11))
	}
	_ = os.WriteFile(filepath.Join(ws, "data.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(ws, ".claude", "agents"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, ".claude", "agents", "linecounter.md"), []byte(`---
name: linecounter
description: Counts the lines in a file. Give it the file path.
tools: Read
---
You count lines. Read the file you are given with the read tool and reply with only the number of lines it has.
`), 0o644)

	c := ternly(t, home, "-C", ws, "--local-only", "--model", model, "--mode", "yolo", "--new",
		"-p", "Delegate to the linecounter agent (task tool) to count the lines in data.txt, then tell me the number.")
	t0 := time.Now()
	out, err := c.CombinedOutput()
	t.Logf("%s in %s:\n%s", model, time.Since(t0).Round(time.Second), out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"→ task linecounter", "subagent → read_file data.txt", "23"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q", want)
		}
	}
}
