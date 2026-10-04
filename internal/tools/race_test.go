package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/testutil"
)

// A hostile process swaps workspace paths between real entries and symlinks
// to outside the workspace while the tools run. Path checks done before the
// open (TOCTOU) lose this race; file access through os.Root must not.
func TestSymlinkSwapRace(t *testing.T) {
	t.Run("ripgrep", func(t *testing.T) {
		_, err := lookRG("rg")
		testutil.Require(t, "ripgrep", err == nil)
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

// Nested swaps: /dev/fd/3 pins only the directory grep starts from; a search
// tool that re-opens deeper paths by name can be redirected by swapping a
// nested directory for a symlink in the middle of the walk.
func TestNestedSymlinkSwapDuringGrep(t *testing.T) {
	t.Run("ripgrep", func(t *testing.T) {
		_, err := lookRG("rg")
		testutil.Require(t, "ripgrep", err == nil)
		nestedSwap(t)
	})
	t.Run("go-fallback", func(t *testing.T) {
		old := lookRG
		lookRG = func(string) (string, error) { return "", os.ErrNotExist }
		defer func() { lookRG = old }()
		nestedSwap(t)
	})
}

func nestedSwap(t *testing.T) {
	r := newReg(t, "yolo")
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOPSECRET\n"), 0o600)
	base := filepath.Join(r.Root, "a", "b")
	_ = os.MkdirAll(base, 0o755)
	for i := range 300 { // a longer walk widens the race window
		_ = os.WriteFile(filepath.Join(base, fmt.Sprintf("f%03d.txt", i)), []byte("benign\n"), 0o644)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for k := range 10 {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = os.RemoveAll(n)
				if i%2 == 0 {
					_ = os.Mkdir(n, 0o755)
					_ = os.WriteFile(filepath.Join(n, "secret.txt"), []byte("benign\n"), 0o644)
				} else {
					_ = os.Symlink(outside, n)
				}
			}
		}(filepath.Join(base, fmt.Sprintf("n%d", k)))
	}
	var leaks, n int
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		args := []string{`{"pattern":"TOPSECRET"}`, `{"pattern":"TOPSECRET","path":"a"}`}[n%2]
		n++
		if res := r.Call(context.Background(), llm.ToolCall{ID: "1", Name: "grep", Args: args}); strings.Contains(res.Out, "TOPSECRET") {
			leaks++
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("%d grep calls while nested dirs are swapped: %d leaks", n, leaks)
	if leaks > 0 {
		t.Fatalf("grep followed a nested symlink swap out of the workspace %d times", leaks)
	}
}

// Set TERNLY_BIG_TREE to time grep's two paths on a large tree.
func TestGrepTimingBigTree(t *testing.T) {
	dir := os.Getenv("TERNLY_BIG_TREE")
	if dir == "" {
		t.Skip("TERNLY_BIG_TREE not set")
	}
	r, err := NewRegistry(dir, NewPolicy("yolo", nil), NewSandbox(false, false, nil), NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, pat := range []string{"ErrUnexpectedEOF", "func "} {
		args := fmt.Sprintf(`{"pattern":%q}`, pat)
		timeIt := func() time.Duration {
			best := time.Hour
			for range 5 {
				t0 := time.Now()
				r.Call(context.Background(), llm.ToolCall{ID: "1", Name: "grep", Args: args})
				best = min(best, time.Since(t0))
			}
			return best.Round(time.Millisecond)
		}
		withRG := timeIt()
		old := lookRG
		lookRG = func(string) (string, error) { return "", os.ErrNotExist }
		fallback := timeIt()
		lookRG = old
		t.Logf("grep %q: ripgrep+confirm %v, Go fallback %v (best of 5)", pat, withRG, fallback)
	}
}
