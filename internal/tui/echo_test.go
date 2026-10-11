package tui

import (
	"io"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// echoWriter is the terminal: it counts the bytes and signals each write.
type echoWriter struct {
	mu    sync.Mutex
	n     int
	wrote chan struct{}
}

func (w *echoWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.n += len(p)
	w.mu.Unlock()
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *echoWriter) bytes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// TestKeystrokeEcho (ADR 030, ADR 032): a typed key reaches the terminal
// within 8 ms at the median: the real Bubble Tea program with ternly's
// options, at 120×40 in truecolor with a 3k-block transcript, from the key's
// byte on the input to the frame's first byte on the output. Bubble Tea
// writes only on its frame ticker, so the echo is the wait for the next tick
// plus the frame. The development machine measures 6.0–7.0 ms at ternly's
// 120 fps and 9.1–10.0 ms at Bubble Tea's default 60, so the budget fails if
// the frame rate falls back. Each key's frame also stays small.
// Skipped under -race and -short, like the other timing budgets.
func TestKeystrokeEcho(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing: not under the race detector or -short (the pre-push tier); CI runs it in its own step")
	}
	const budget, bytesBudget = 8 * time.Millisecond, 32
	m := benchModel(t, 1000)
	m.App.Discover = func() ([]*discover.Model, []string) { return nil, nil }
	in, keys := io.Pipe()
	w := &echoWriter{wrote: make(chan struct{}, 1)}
	p := tea.NewProgram(m, append(ProgramOptions(), tea.WithInput(in), tea.WithOutput(w), tea.WithWindowSize(120, 40),
		tea.WithColorProfile(colorprofile.TrueColor), tea.WithoutSignals(),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}))...)
	done := make(chan struct{})
	go func() { _, _ = p.Run(); close(done) }()
	defer func() { p.Quit(); _ = keys.Close(); <-done }()

	settle := func() { // 300 ms without output
		for quiet := time.After(300 * time.Millisecond); ; {
			select {
			case <-w.wrote:
				quiet = time.After(300 * time.Millisecond)
				continue
			case <-time.After(10 * time.Second):
				t.Fatal("the screen never settled")
			case <-quiet:
			}
			break
		}
	}
	// Silence alone doesn't mean the program reads input: on a loaded machine
	// (CI runs packages in parallel) the event loop started 1.4 s after the
	// terminal setup was written. A warm-up key and its deletion must echo first.
	settle()
	for _, k := range []byte{'x', 0x7f} {
		if _, err := keys.Write([]byte{k}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.wrote:
		case <-time.After(10 * time.Second):
			t.Fatalf("warm-up key %q never echoed", k)
		}
	}
	settle()

	var ds []time.Duration
	var bs []int
	for i := range 120 {
		k := []byte{byte('a' + i%26)}
		if i%40 >= 20 { // type 20, delete 20: the line stays short
			k = []byte{0x7f}
		}
		select { // drop a stale signal
		case <-w.wrote:
		default:
		}
		before := w.bytes()
		t0 := time.Now()
		if _, err := keys.Write(k); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.wrote:
			ds = append(ds, time.Since(t0))
		case <-time.After(time.Second):
			t.Fatalf("key %d: nothing written within 1 s", i)
		}
		// the rest of the frame, then a gap spread over 50 ms (6 frames at 120 fps,
		// 3 at 60), so keys land anywhere in the frame period as typing does,
		// instead of right after a tick
		time.Sleep(5*time.Millisecond + time.Duration(rand.IntN(50_000))*time.Microsecond)
		bs = append(bs, w.bytes()-before)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	sort.Ints(bs)
	t.Logf("keystroke to echo: median %v, p95 %v; bytes per key: median %d, max %d",
		ds[len(ds)/2], ds[len(ds)*95/100], bs[len(bs)/2], bs[len(bs)-1])
	if ds[len(ds)/2] > budget {
		t.Errorf("echo median %v, budget %v", ds[len(ds)/2], budget)
	}
	if bs[len(bs)/2] > bytesBudget {
		t.Errorf("bytes per key: median %d, budget %d", bs[len(bs)/2], bytesBudget)
	}
}
