package bgeval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

var (
	ollama = &discover.Provider{ID: "ollama", Local: true}
	paid   = &discover.Provider{ID: "openai"}
)

func cloud(id string) *discover.Model {
	return &discover.Model{Provider: ollama, ProvID: "ollama", ID: id, Tools: true, Cloud: true}
}

type fake struct {
	s     *Scheduler
	runs  []string
	busy  atomic.Bool // the session isn't idle
	quota atomic.Bool // the quota is spent
	pause []bool      // what Run was sent
	clock time.Time
	err   error
	block chan struct{} // Run waits on it (or ctx) when set
	done  int
	mu    sync.Mutex
}

func newFake(t *testing.T, caps Caps, ms ...*discover.Model) *fake {
	f := &fake{clock: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
	f.s = &Scheduler{Tick: 5 * time.Millisecond, Caps: caps, Ledger: OpenLedger(filepath.Join(t.TempDir(), "background-eval.json")),
		Models: func() []*discover.Model { return ms }, Idle: func() (bool, string) { return !f.busy.Load(), "busy" },
		Quota: func(*discover.Model) (bool, string) { return !f.quota.Load(), "quota exhausted (429 on 2026-10-10)" }, Now: func() time.Time { return f.clock },
		Run: func(ctx context.Context, m *discover.Model, budget float64, pause <-chan bool) error {
			f.runs = append(f.runs, m.Key())
			for f.block != nil {
				select {
				case <-f.block:
					return f.err
				case p := <-pause:
					f.mu.Lock()
					f.pause = append(f.pause, p)
					f.mu.Unlock()
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return f.err
		},
		Done: func() { f.done++ }}
	return f
}

func TestWeeklyCapAndOncePerModel(t *testing.T) {
	ms := []*discover.Model{cloud("a:cloud"), cloud("b:cloud"), cloud("c:cloud"), cloud("d:cloud")}
	caps := on()
	caps.PerWeek = 2
	f := newFake(t, caps, ms...)
	for range 5 {
		f.s.Step(context.Background())
	}
	if strings.Join(f.runs, " ") != "ollama/a:cloud ollama/b:cloud" || f.done != 2 {
		t.Fatalf("runs %v done %d", f.runs, f.done)
	}
	if st := f.s.Status(ms[2]); !strings.Contains(st, "2 evaluations this week") {
		t.Fatalf("status %q", st)
	}
	if st := f.s.Status(ms[0]); !strings.Contains(st, "evaluation measured") {
		t.Fatalf("status %q", st)
	}
	f.clock = f.clock.Add(8 * 24 * time.Hour) // a week later: the next two, never a again
	for range 5 {
		f.s.Step(context.Background())
	}
	if strings.Join(f.runs, " ") != "ollama/a:cloud ollama/b:cloud ollama/c:cloud ollama/d:cloud" {
		t.Fatalf("runs %v", f.runs)
	}
}

// A turn starting (or the user typing) pauses the evaluation instead of
// stopping it; it resumes, not restarts, when the session is idle again, and
// the paused time doesn't count against the time cap (ADR 031).
func TestPausesAndResumes(t *testing.T) {
	m := cloud("a:cloud")
	caps := on()
	caps.PerModel = 150 * time.Millisecond
	f := newFake(t, caps, m)
	f.block = make(chan struct{})
	res := make(chan string, 1)
	go func() { res <- f.s.Step(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	f.busy.Store(true)
	time.Sleep(300 * time.Millisecond) // paused for twice the cap: no timeout
	if st := f.s.Status(m); st != "evaluation paused (resumes when idle)" {
		t.Errorf("status while paused %q", st)
	}
	f.busy.Store(false)
	time.Sleep(30 * time.Millisecond)
	close(f.block) // the evaluation finishes
	if out := <-res; out != Measured {
		t.Fatalf("outcome %q", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) != 1 || len(f.pause) != 2 || !f.pause[0] || f.pause[1] {
		t.Fatalf("runs %v pause signals %v: want one run, paused then resumed", f.runs, f.pause)
	}
}

// Nothing starts while the session isn't idle.
func TestNothingStartsUnlessIdle(t *testing.T) {
	f := newFake(t, on(), cloud("a:cloud"))
	f.busy.Store(true)
	if f.s.Step(context.Background()) != "" || len(f.runs) != 0 {
		t.Fatal("started while not idle")
	}
	if st := f.s.Status(cloud("a:cloud")); !strings.Contains(st, "(now: busy)") {
		t.Fatalf("status %q", st)
	}
}

// An exhausted quota schedules nothing; once it has room, the model runs.
func TestQuotaExhaustedSchedulesNothing(t *testing.T) {
	m := cloud("a:cloud")
	f := newFake(t, on(), m)
	f.quota.Store(true)
	for range 3 {
		if f.s.Step(context.Background()) != "" {
			t.Fatal("scheduled on an exhausted quota")
		}
	}
	if len(f.runs) != 0 {
		t.Fatalf("ran %v", f.runs)
	}
	if st := f.s.Status(m); !strings.Contains(st, "quota exhausted") {
		t.Fatalf("status %q", st)
	}
	f.quota.Store(false)
	if out := f.s.Step(context.Background()); out != Measured || len(f.runs) != 1 {
		t.Fatalf("quota available: %q %v", out, f.runs)
	}
	// local models draw on no quota: never asked
	local := &discover.Model{Provider: ollama, ProvID: "ollama", ID: "qwen3.6", Tools: true}
	g := newFake(t, on(), local)
	g.s.Quota = func(*discover.Model) (bool, string) { t.Fatal("quota checked for a local model"); return false, "" }
	if g.s.Step(context.Background()) != Measured {
		t.Fatal("local model not evaluated")
	}
}

// A quota hit during an evaluation stops it: Run reports ErrQuota (its trap
// runs recorded the 429), or the quota check sees the mark first. Neither
// counts against the caps, and the model is retried once the quota has room.
func TestQuotaHitStopsEvaluation(t *testing.T) {
	m := cloud("a:cloud")
	f := newFake(t, on(), m)
	f.err = fmt.Errorf("trap 3: %w", ErrQuota)
	if out := f.s.Step(context.Background()); out != QuotaStop {
		t.Fatalf("outcome %q", out)
	}
	if n, _ := f.s.weekly(); n != 0 || f.s.last(m.Key()) != nil {
		t.Fatalf("a quota stop counted: %d this week", n)
	}
	old := quotaEvery
	quotaEvery = 10 * time.Millisecond
	defer func() { quotaEvery = old }()
	f.err, f.block = nil, make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); f.quota.Store(true) }()
	if out := f.s.Step(context.Background()); out != QuotaStop {
		t.Fatalf("outcome when the quota check fires %q", out)
	}
	f.quota.Store(false)
	f.block = nil
	if out := f.s.Step(context.Background()); out != Measured || len(f.runs) != 3 {
		t.Fatalf("after the reset: %q %v", out, f.runs)
	}
}

func TestTimeoutAndFailure(t *testing.T) {
	caps := on()
	caps.PerModel = 100 * time.Millisecond
	f := newFake(t, caps, cloud("a:cloud"), cloud("b:cloud"))
	f.block = make(chan struct{})
	if out := f.s.Step(context.Background()); out != Timeout {
		t.Fatalf("outcome %q", out)
	}
	f.block, f.err = nil, errors.New("eval failed")
	if out := f.s.Step(context.Background()); out != Failed || f.done != 0 {
		t.Fatalf("outcome %q done %d", out, f.done)
	}
}

func TestWhatIsNeverEvaluated(t *testing.T) {
	api := &discover.Model{Provider: paid, ProvID: "openai", ID: "gpt-x", Tools: true, Priced: true, In: 1, Out: 4}
	measured := cloud("m:cloud")
	measured.Measure = &discover.Measurement{Tier: 3, Runs: 3}
	noTools := cloud("embed:cloud")
	noTools.Tools = false
	f := newFake(t, on(), api, measured, noTools)
	if f.s.Step(context.Background()) != "" || len(f.runs) != 0 {
		t.Fatalf("ran %v", f.runs)
	}
	if st := f.s.Status(api); !strings.Contains(st, "paid API") {
		t.Fatalf("paid status %q", st)
	}
	// with a weekly budget, the paid model runs, with that budget
	caps := on()
	caps.PaidUSD = 2
	g := newFake(t, caps, api)
	var got float64
	g.s.Run = func(_ context.Context, _ *discover.Model, b float64, _ <-chan bool) error { got = b; return nil }
	if g.s.Step(context.Background()) != Measured || got != 2 {
		t.Fatalf("paid budget %v", got)
	}
	// --local-only: no cloud evaluation; off: nothing
	caps = on()
	caps.LocalOnly = true
	if h := newFake(t, caps, cloud("a:cloud")); h.s.Step(context.Background()) != "" {
		t.Fatal("cloud evaluated under --local-only")
	}
	caps = on()
	caps.Enabled = false
	if h := newFake(t, caps, cloud("a:cloud")); h.s.Step(context.Background()) != "" || !strings.Contains(h.s.Status(cloud("a:cloud")), "off") {
		t.Fatal("ran while off")
	}
}

func TestLedgerPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "background-eval.json")
	l := OpenLedger(p)
	_ = l.add(Entry{Model: "ollama/a:cloud", Outcome: Measured, Started: time.Now(), Finished: time.Now()})
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if got := OpenLedger(p).Entries; len(got) != 1 || got[0].Model != "ollama/a:cloud" {
		t.Fatalf("%+v", got)
	}
	_ = os.WriteFile(p, []byte(`{"version":9,"entries":[{"model":"x"}]}`), 0o600)
	if got := OpenLedger(p).Entries; len(got) != 0 {
		t.Fatal("another version was read")
	}
}

// on is DefaultCaps, opted in.
func on() Caps {
	c := DefaultCaps
	c.Enabled = true
	return c
}

// Off unless the user opted in (ADR 031).
func TestOffByDefault(t *testing.T) {
	f := newFake(t, DefaultCaps, cloud("a:cloud"))
	if f.s.Step(context.Background()) != "" || len(f.runs) != 0 {
		t.Fatal("evaluated without opting in")
	}
}
