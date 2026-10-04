package logstore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRoundTripAndTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	l, recs, _, err := Open(path)
	if err != nil || len(recs) != 0 {
		t.Fatal(err, len(recs))
	}
	for i := range 3 {
		l.Append([]byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`deadbeef {"n":`) // torn final line
	f.Close()
	l, recs, dropped, err := Open(path)
	if err != nil || len(recs) != 3 || string(recs[2]) != `{"n":2}` || dropped == 0 {
		t.Fatalf("recs=%q dropped=%d err=%v", recs, dropped, err)
	}
	l.Append([]byte(`{"n":3}`))
	_ = l.Close()
	if _, recs, _, _ := Open(path); len(recs) != 4 {
		t.Fatalf("append after repair: %d records", len(recs))
	}
}

// Unsynced writes are fsync'd within GroupSync even with no Sync call.
func TestGroupCommit(t *testing.T) {
	old := GroupSync
	GroupSync = 50 * time.Millisecond
	defer func() { GroupSync = old }()
	l, _, _, err := Open(filepath.Join(t.TempDir(), "g.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	before, _ := l.Stats()
	l.Append([]byte(`{"t":"long-running output"}`))
	time.Sleep(200 * time.Millisecond)
	after, dirty := l.Stats()
	if after <= before || dirty {
		t.Fatalf("no group commit: fsyncs %d→%d, dirty=%v", before, after, dirty)
	}
	time.Sleep(200 * time.Millisecond) // idle: no further fsyncs
	if idle, _ := l.Stats(); idle != after {
		t.Fatalf("fsync while clean: %d→%d", after, idle)
	}
}

// A long tool-heavy turn: a 2 KB record every 2 ms for TERNLY_LONG_TURN
// (e.g. 10s). Reports caller-side Append latency and the fsyncs it caused.
func TestLongTurnOverhead(t *testing.T) {
	d, _ := time.ParseDuration(os.Getenv("TERNLY_LONG_TURN"))
	if d == 0 {
		t.Skip("TERNLY_LONG_TURN not set")
	}
	for _, every := range []time.Duration{time.Hour, GroupSync} {
		old := GroupSync
		GroupSync = every
		l, _, _, err := Open(filepath.Join(t.TempDir(), "turn.log"))
		if err != nil {
			t.Fatal(err)
		}
		rec := []byte(`{"t":"msg","content":"` + strings.Repeat("output line ", 170) + `"}`)
		var lat []time.Duration
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
			t0 := time.Now()
			l.Append(rec)
			lat = append(lat, time.Since(t0))
		}
		n, _ := l.Stats()
		_ = l.Close()
		GroupSync = old
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		label := "turn-boundary syncs only"
		if every != time.Hour {
			label = "group commit every " + every.String()
		}
		t.Logf("%s: %d records, %d fsyncs during the turn, Append latency p50 %v p99 %v max %v", label, len(lat), n, lat[len(lat)/2], lat[len(lat)*99/100], lat[len(lat)-1])
	}
}

func tailAll(t *testing.T, l *Log) map[string]bool {
	t.Helper()
	recs, _, err := l.Tail()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, r := range recs {
		m[string(r)] = true
	}
	return m
}

// Two opens of one shared log (flocks are per open file, so this behaves like
// two processes): each sees the other's records, and the records survive.
func TestSharedTwoWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.log")
	a, _, err := OpenShared(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := OpenShared(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		a.Append([]byte(fmt.Sprintf(`{"a":%d}`, i)))
		b.Append([]byte(fmt.Sprintf(`{"b":%d}`, i)))
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := tailAll(t, a); !got[`{"b":199}`] || !got[`{"a":0}`] || len(got) != 400 {
		t.Fatalf("a's tail: %d records", len(got))
	}
	if got := tailAll(t, a); len(got) != 0 {
		t.Fatalf("second tail returned %d old records", len(got))
	}
	_ = a.Close()
	_ = b.Close()
	_, recs, err := OpenShared(path, nil)
	if err != nil || len(recs) != 400 {
		t.Fatalf("reopen: %d records, %v", len(recs), err)
	}
}

// A writer that died mid-line leaves a fragment; later records stay readable.
func TestSharedTornFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.log")
	a, _, _ := OpenShared(path, nil)
	b, _, _ := OpenShared(path, nil)
	a.Append([]byte(`{"n":1}`))
	_ = a.Flush()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`0badc0de {"torn":`) // a crashed process's partial write
	f.Close()
	b.Append([]byte(`{"n":2}`))
	_ = b.Flush()
	if got := tailAll(t, a); !got[`{"n":2}`] {
		t.Fatalf("record after a fragment lost: %v", got)
	}
	_ = a.Close()
	_ = b.Close()
	_, recs, _ := OpenShared(path, nil)
	if len(recs) != 2 {
		t.Fatalf("reopen: %q", recs)
	}
}

// Compaction runs only when no other process has the log open; a process that
// opened before a rewrite keeps appending to the live file.
func TestSharedCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.log")
	l, _, _ := OpenShared(path, nil)
	for i := range 10 {
		l.Append([]byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	_ = l.Close()
	keepLast := func(r [][]byte) [][]byte { return r[len(r)-1:] }

	held, _, _ := OpenShared(path, nil)
	second, recs, _ := OpenShared(path, keepLast) // not alone: no rewrite
	if len(recs) != 10 {
		t.Fatalf("compacted while shared: %d", len(recs))
	}
	_ = second.Close()
	_ = held.Close()

	old, _, _ := OpenShared(path, nil) // will see the file replaced under it
	_ = old.Close()
	stale, _, _ := OpenShared(path, nil)
	// Simulate a racing compactor (the presence lock makes this rare: see OpenShared).
	nf, _, err := rewrite(path, [][]byte{[]byte(`{"n":9}`)})
	if err != nil {
		t.Fatal(err)
	}
	nf.Close()
	stale.Append([]byte(`{"after":1}`))
	_ = stale.Flush()
	recs2, reset, _ := stale.Tail()
	if !reset || len(recs2) != 2 {
		t.Fatalf("after replace: reset=%v %q", reset, recs2)
	}
	_ = stale.Close()
	_, recs, _ = OpenShared(path, keepLast)
	if len(recs) != 1 || string(recs[0]) != `{"after":1}` {
		t.Fatalf("compacted: %q", recs)
	}
}

// Real processes: the test binary re-runs itself as 4 writers appending at once.
func TestSharedProcesses(t *testing.T) {
	if p := os.Getenv("TERNLY_LOGSTORE_CHILD"); p != "" {
		l, _, err := OpenShared(p, nil)
		if err != nil {
			os.Exit(3)
		}
		for i := range 500 {
			l.Append([]byte(fmt.Sprintf(`{"pid":%d,"i":%d,"pad":"%s"}`, os.Getpid(), i, strings.Repeat("x", i%300))))
			if i%50 == 0 {
				l.Sync()
			}
		}
		if l.Close() != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "m.log")
	var cmds []*exec.Cmd
	for range 4 {
		c := exec.Command(os.Args[0], "-test.run=^TestSharedProcesses$")
		c.Env = append(os.Environ(), "TERNLY_LOGSTORE_CHILD="+path)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	_, recs, err := OpenShared(path, nil)
	if err != nil || len(recs) != 2000 {
		t.Fatalf("%d records from 4 processes, want 2000 (%v)", len(recs), err)
	}
}
