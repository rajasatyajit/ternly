package bgeval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	busy  atomic.Bool
	clock time.Time
	err   error
	block chan struct{} // Run waits on it (or ctx) when set
	done  int
}

func newFake(t *testing.T, caps Caps, ms ...*discover.Model) *fake {
	f := &fake{clock: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
	f.s = &Scheduler{Caps: caps, Ledger: OpenLedger(filepath.Join(t.TempDir(), "background-eval.json")),
		Models: func() []*discover.Model { return ms }, Busy: f.busy.Load, Now: func() time.Time { return f.clock },
		Run: func(ctx context.Context, m *discover.Model, budget float64) error {
			f.runs = append(f.runs, m.Key())
			if f.block != nil {
				select {
				case <-f.block:
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
	caps := DefaultCaps
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

func TestYieldsToATurn(t *testing.T) {
	m := cloud("a:cloud")
	f := newFake(t, DefaultCaps, m)
	f.block = make(chan struct{})
	go func() { time.Sleep(300 * time.Millisecond); f.busy.Store(true) }()
	if out := f.s.Step(context.Background()); out != Yielded {
		t.Fatalf("outcome %q", out)
	}
	if f.s.Step(context.Background()) != "" {
		t.Fatal("started while a turn runs")
	}
	f.busy.Store(false)
	f.block = nil
	if out := f.s.Step(context.Background()); out != Measured || len(f.runs) != 2 {
		t.Fatalf("after the turn: %q runs %v (a yielded run must not count)", out, f.runs)
	}
}

func TestTimeoutAndFailure(t *testing.T) {
	caps := DefaultCaps
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
	f := newFake(t, DefaultCaps, api, measured, noTools)
	if f.s.Step(context.Background()) != "" || len(f.runs) != 0 {
		t.Fatalf("ran %v", f.runs)
	}
	if st := f.s.Status(api); !strings.Contains(st, "paid API") {
		t.Fatalf("paid status %q", st)
	}
	// with a weekly budget, the paid model runs, with that budget
	caps := DefaultCaps
	caps.PaidUSD = 2
	g := newFake(t, caps, api)
	var got float64
	g.s.Run = func(_ context.Context, _ *discover.Model, b float64) error { got = b; return nil }
	if g.s.Step(context.Background()) != Measured || got != 2 {
		t.Fatalf("paid budget %v", got)
	}
	// --local-only: no cloud evaluation; off: nothing
	caps = DefaultCaps
	caps.LocalOnly = true
	if h := newFake(t, caps, cloud("a:cloud")); h.s.Step(context.Background()) != "" {
		t.Fatal("cloud evaluated under --local-only")
	}
	caps = DefaultCaps
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
