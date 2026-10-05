package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/testutil"
	"github.com/rajasatyajit/ternly/internal/tools"
)

func msg(role, content string) agent.Record {
	return agent.Record{T: "msg", Msg: &llm.Message{Role: role, Content: content}, TS: time.Now().UnixMilli()}
}

func project(t testing.TB) *Project {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	p, err := OpenProject(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLogTornAndCorruptTails(t *testing.T) {
	p := project(t)
	s, _ := p.Create()
	for i := range 5 {
		s.Record(agent.Record{T: "turn", Prompt: fmt.Sprint("p", i), TS: time.Now().UnixMilli()})
		s.Record(msg("user", fmt.Sprint("p", i)))
	}
	_ = s.Close("paused")
	path := filepath.Join(p.sessionDir(s.ID), "events.log")
	good, _ := os.ReadFile(path)

	cases := map[string][]byte{
		"torn last line":       append(append([]byte{}, good...), []byte(`0badc0de {"t":"msg","msg":{"Role":"us`)...),
		"bad checksum":         append(append([]byte{}, good...), []byte("00000000 {\"t\":\"turn\",\"prompt\":\"x\"}\n")...),
		"garbage line":         append(append([]byte{}, good...), []byte("\x00\x00\x00\n")...),
		"truncated mid-record": good[:len(good)-7],
	}
	for name, content := range cases {
		_ = os.WriteFile(path, content, 0o600)
		s2, st, err := p.Open(s.ID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantTurns := 5
		if name == "truncated mid-record" {
			wantTurns = 5 // the cut hits the final status record, not a turn
		}
		if len(st.Turns) != wantTurns {
			t.Errorf("%s: %d turns, want %d", name, len(st.Turns), wantTurns)
		}
		s2.Record(msg("user", "after repair")) // appends land after the last good record
		_ = s2.Close("")
		recs, _ := p.Records(s.ID)
		if last := recs[len(recs)-1]; last.Msg == nil || last.Msg.Content != "after repair" {
			t.Errorf("%s: append after repair lost: %+v", name, last)
		}
	}
}

// A process killed while appending as fast as it can leaves a log that opens cleanly.
func TestKillDuringAppend(t *testing.T) {
	if dir := os.Getenv("SESSION_KILL_CHILD"); dir != "" {
		p := &Project{Dir: dir}
		s, _, err := p.Open(os.Getenv("SESSION_KILL_ID"))
		if err != nil {
			os.Exit(3)
		}
		big := strings.Repeat("x", 4000)
		for i := 0; ; i++ {
			s.Record(agent.Record{T: "turn", Prompt: big, TS: time.Now().UnixMilli()})
			if i%50 == 0 {
				s.Sync()
			}
		}
	}
	p := project(t)
	s, _ := p.Create()
	_ = s.Close("")
	for round := range 5 {
		c := exec.Command(os.Args[0], "-test.run=^TestKillDuringAppend$")
		c.Env = append(os.Environ(), "SESSION_KILL_CHILD="+p.Dir, "SESSION_KILL_ID="+s.ID)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(150+round*40) * time.Millisecond)
		_ = c.Process.Signal(syscall.SIGKILL)
		_ = c.Wait()
		s2, st, err := p.Open(s.ID)
		if err != nil {
			t.Fatalf("round %d: reopen after kill: %v", round, err)
		}
		t.Logf("round %d: %d turns survived the kill", round, len(st.Turns))
		_ = s2.Close("")
	}
}

func TestLocksListDelete(t *testing.T) {
	p := project(t)
	a, _ := p.Create()
	b, _ := p.Create()
	b.Record(agent.Record{T: "title", Text: "bee", TS: time.Now().Add(time.Second).UnixMilli()})
	time.Sleep(5 * time.Millisecond) // Close stamps Active with the current time: keep it after a's (List sorts by it)
	_ = b.Close("paused")
	if _, _, err := p.Open(a.ID); err != ErrLocked {
		t.Fatalf("second open of a live session: %v", err)
	}
	ms, _ := p.List()
	if len(ms) != 2 || ms[0].ID != b.ID || ms[0].Title != "bee" || ms[0].Locked || !ms[1].Locked {
		t.Fatalf("list: %+v", ms)
	}
	if err := p.Delete(a.ID); err != ErrLocked {
		t.Fatalf("deleted a live session: %v", err)
	}
	if err := p.Delete(b.ID); err != nil || p.Saved(b.ID) {
		t.Fatalf("delete: %v", err)
	}
	pick, locked, _ := Resumable([]Meta{{ID: "new", Locked: true, Active: time.Now()}, {ID: "old", Active: time.Now().Add(-time.Hour)}})
	if pick != nil || locked == nil || locked.ID != "new" {
		t.Fatal("auto-resume must not skip a newer session open elsewhere")
	}
	pick, _, stopped := Resumable([]Meta{{ID: "s", Status: "stopped", Active: time.Now()}, {ID: "p", Status: "paused", Active: time.Now().Add(-time.Hour)}})
	if pick != nil || stopped == nil || stopped.ID != "s" {
		t.Fatal("a stopped latest session starts a new one (and is reported), never an older one")
	}
	if pick, _, _ = Resumable([]Meta{{ID: "p", Status: "paused", Active: time.Now()}}); pick == nil || pick.ID != "p" {
		t.Fatal("the latest paused session is resumed")
	}
}

func TestMovedRepoAdoptsSessions(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	data := t.TempDir()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	a := filepath.Join(base, "proj")
	_ = os.MkdirAll(a, 0o755)
	git := func(dir string, args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git(a, "init", "-q")
	_ = os.WriteFile(filepath.Join(a, "f"), []byte("x"), 0o644)
	git(a, "add", "f")
	git(a, "commit", "-qm", "root")
	p, _ := OpenProject(data, a)
	s, _ := p.Create()
	_ = s.Close("paused")

	clone := filepath.Join(base, "clone")
	git(base, "clone", "-q", a, clone)
	if c, _ := OpenProject(data, clone); c.AdoptedKey != "" {
		t.Fatal("a clone took over the original's sessions while the original exists")
	}
	moved := filepath.Join(base, "renamed")
	if err := os.Rename(a, moved); err != nil {
		t.Fatal(err)
	}
	m, _ := OpenProject(data, moved)
	if ms, _ := m.List(); m.AdoptedKey != p.Key || len(ms) != 1 || ms[0].ID != s.ID {
		t.Fatalf("moved repo did not adopt its sessions: adopted=%q list=%+v", m.AdoptedKey, ms)
	}
}

func newManager(t testing.TB, p *Project) *Manager {
	reg, err := tools.NewRegistry(p.Path, tools.NewPolicy("ask", nil), tools.NewSandbox(false, false, nil), tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{Project: p, Agent: agent.New(reg, discover.NewRouter(), func(agent.Event) {}), Policy: reg.Policy}
}

// A switch to a target that fails to load keeps the current session current,
// journaling, and intact on disk.
func TestFailedSwitchKeepsCurrent(t *testing.T) {
	p := project(t)
	m := newManager(t, p)
	s, _ := p.Create()
	if _, err := m.Attach(s, agent.State{}, false); err != nil {
		t.Fatal(err)
	}
	m.Agent.Commit(agent.Record{T: "turn", Prompt: "one"})
	m.Agent.Commit(agent.Record{T: "msg", Msg: &llm.Message{Role: "user", Content: "one"}})
	other, _ := p.Create() // stays open (locked) for the whole test
	defer other.Close("")
	for _, target := range []string{"no-such-session", other.ID} {
		if _, err := m.Switch(context.Background(), target); err == nil {
			t.Fatalf("switch to %s should fail", target)
		}
		if m.Current() == nil || m.Current().ID != s.ID {
			t.Fatal("current session lost after a failed switch")
		}
	}
	m.Agent.Commit(agent.Record{T: "msg", Msg: &llm.Message{Role: "assistant", Content: "still journaling"}})
	_ = m.Close("paused")
	_, st, err := p.Open(s.ID)
	if err != nil || len(st.History) != 2 || st.History[1].Content != "still journaling" {
		t.Fatalf("history after failed switches: %v %+v", err, st.History)
	}
}

func BenchmarkRecord(b *testing.B) {
	p := project(b)
	s, _ := p.Create()
	defer s.Close("")
	r := msg("tool", strings.Repeat("output line\n", 170)) // ~2 KB tool result
	b.ReportAllocs()
	for b.Loop() {
		s.Record(r)
	}
	b.StopTimer()
	_ = s.Flush()
}

func BenchmarkFlush(b *testing.B) {
	p := project(b)
	s, _ := p.Create()
	defer s.Close("")
	r := msg("assistant", "short")
	for b.Loop() {
		s.Record(r)
		_ = s.Flush() // write + fsync
	}
}

// writeSession builds a session of n turns: prompt, tool call, ~2 KB result, answer, usage.
func writeSession(t testing.TB, p *Project, n int) string {
	s, _ := p.Create()
	out := strings.Repeat("some tool output line\n", 90)
	for i := range n {
		ts := time.Now().UnixMilli()
		s.Record(agent.Record{T: "turn", Prompt: fmt.Sprintf("turn %d: fix the thing", i), TS: ts})
		s.Record(msg("user", fmt.Sprintf("turn %d: fix the thing", i)))
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: fmt.Sprint("c", i), Name: "read_file", Args: `{"path":"a.go"}`}}}})
		s.Record(agent.Record{T: "msg", TS: ts, Msg: &llm.Message{Role: "tool", ToolCallID: fmt.Sprint("c", i), Content: out}})
		s.Record(msg("assistant", "Done; changed a.go. Unverified."))
		s.Record(agent.Record{T: "usage", Usage: &llm.Usage{In: 2000, Out: 200}, Cost: 0.001, TS: ts})
	}
	_ = s.Close("paused")
	return s.ID
}

func TestResume1000Turns(t *testing.T) {
	p := project(t)
	id := writeSession(t, p, 1000)
	fi, _ := os.Stat(filepath.Join(p.sessionDir(id), "events.log"))
	m := newManager(t, p)
	best := time.Hour
	for range 5 {
		t0 := time.Now()
		s, st, err := p.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Attach(s, st, true); err != nil {
			t.Fatal(err)
		}
		best = min(best, time.Since(t0))
		if len(st.Turns) != 1000 || len(st.History) != 4000 {
			t.Fatalf("turns %d history %d", len(st.Turns), len(st.History))
		}
		_ = m.Close("paused")
	}
	t.Logf("resume of a 1000-turn session (%s log): open+replay+attach %v (best of 5)", humanBytes(fi.Size()), best)
	if best > 100*time.Millisecond && !raceEnabled { // the race detector slows this several-fold
		t.Errorf("resume took %v, target < 100ms", best)
	}
}

func humanBytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// A process killed while a tool ran leaves a call without a result; the
// session must open with a valid history, and stay valid when reopened.
func TestOpenRepairsInterruptedToolCall(t *testing.T) {
	p := project(t)
	s, _ := p.Create()
	s.Record(agent.Record{T: "turn", Prompt: "do it", TS: time.Now().UnixMilli()})
	s.Record(msg("user", "do it"))
	s.Record(agent.Record{T: "msg", TS: time.Now().UnixMilli(), Msg: &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "bash", Args: `{"command":"make"}`}}}})
	_ = s.Close("") // the kill: no tool result was ever written
	for range 2 {
		s2, st, err := p.Open(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(st.History); n != 3 || st.History[2].Role != "tool" || !strings.Contains(st.History[2].Content, "cancelled") {
			t.Fatalf("history not repaired: %+v", st.History)
		}
		_ = s2.Close("")
	}
}
