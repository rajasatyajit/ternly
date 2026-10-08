package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTUIPerHunkReview (ADR 021 amendment 1): through the real binary, an
// edit in ask mode is shown hunk by hunk; declining the second hunk writes
// only the first and third, and the model is told which hunk was declined.
func TestTUIPerHunkReview(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	old := strings.Join(lines, "\n") + "\n"
	lines[2], lines[20], lines[37] = "ONE", "TWO", "THREE"
	nw := strings.Join(lines, "\n") + "\n"
	args, _ := json.Marshal(map[string]string{"path": "a.txt", "content": nw})
	f := newProvider(t, step{call: [2]string{"write_file", string(args)}}, step{text: "review-turn-done"})
	home := testHome(t, f.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "a.txt"), []byte(old), 0o644)

	c := ternly(t, home, "-model", "fake/m1", "-mode", "ask", "-C", ws)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 140, 50)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait(); _ = tty.Close() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(80 * time.Millisecond) }

	scr.waitFor(t, "fake")
	send("change the file\r")
	scr.waitFor(t, "Edit a.txt?")
	scr.waitFor(t, "hunk 1 of 3")
	send("\x1b[B") // ↓ to hunk 2 (the renderer redraws only the changed digit, so the stream can't show "hunk 2 of 3")
	send(" ")      // decline it
	send("y")
	scr.waitFor(t, "applied 2 of 3 hunks to a.txt")
	scr.waitFor(t, "review-turn-done")

	b, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	got := string(b)
	if !strings.Contains(got, "ONE") || strings.Contains(got, "TWO") || !strings.Contains(got, "THREE") || !strings.Contains(got, "line 20") {
		t.Fatalf("a.txt:\n%s", got)
	}
	toolResults, _ := f.seen()
	if len(toolResults) == 0 || !strings.Contains(strings.Join(toolResults, "\n"), "declined hunk(s) 2") {
		t.Fatalf("the model wasn't told which hunk was declined: %q", toolResults)
	}
}
