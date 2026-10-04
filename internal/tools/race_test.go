package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// A hostile process swaps workspace paths between real entries and symlinks
// to outside the workspace while the tools run. Path checks done before the
// open (TOCTOU) lose this race; file access through os.Root must not.
func TestSymlinkSwapRace(t *testing.T) {
	t.Run("ripgrep", func(t *testing.T) {
		if _, err := lookRG("rg"); err != nil {
			t.Skip("rg not installed")
		}
		symlinkSwapRace(t)
	})
	t.Run("go-fallback", func(t *testing.T) {
		old := lookRG
		lookRG = func(string) (string, error) { return "", os.ErrNotExist }
		defer func() { lookRG = old }()
		symlinkSwapRace(t)
	})
}

func symlinkSwapRace(t *testing.T) {
	r := newReg(t, "yolo")
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("TOPSECRET"), 0o600)
	inFile, inDir := filepath.Join(r.Root, "f"), filepath.Join(r.Root, "d")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the attacker
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.RemoveAll(inFile)
			_ = os.RemoveAll(inDir)
			if i%2 == 0 {
				_ = os.WriteFile(inFile, []byte("benign"), 0o644)
				_ = os.Mkdir(inDir, 0o755)
				_ = os.WriteFile(filepath.Join(inDir, "secret"), []byte("benign"), 0o644)
			} else {
				_ = os.Symlink(filepath.Join(outside, "secret"), inFile)
				_ = os.Symlink(outside, inDir)
			}
		}
	}()

	var leaks, writes atomic.Int64
	deadline := time.Now().Add(1500 * time.Millisecond)
	calls := []struct{ tool, args string }{
		{"read_file", `{"path":"f"}`},
		{"read_file", `{"path":"d/secret"}`},
		{"grep", `{"pattern":"TOPSECRET","path":"d"}`},
		{"edit_file", `{"path":"d/secret","old_string":"TOPSECRET","new_string":"pwned"}`},
		{"write_file", `{"path":"d/planted","content":"pwned"}`},
	}
	var n int
	for time.Now().Before(deadline) {
		c := calls[n%len(calls)]
		n++
		res := r.Call(context.Background(), llm.ToolCall{ID: "1", Name: c.tool, Args: c.args})
		if strings.Contains(res.Out, "TOPSECRET") && c.tool != "edit_file" {
			leaks.Add(1)
		}
		if c.tool == "write_file" && !res.IsErr {
			writes.Add(1)
		}
	}
	close(stop)
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(outside, "secret"))
	_, planted := os.Stat(filepath.Join(outside, "planted"))
	t.Logf("%d tool calls under a symlink-swap race: %d leaks, %d successful in-workspace writes", n, leaks.Load(), writes.Load())
	if leaks.Load() > 0 || string(b) != "TOPSECRET" || planted == nil {
		t.Fatalf("escaped the workspace: %d reads leaked, outside secret=%q, planted file outside=%v", leaks.Load(), b, planted == nil)
	}
}
