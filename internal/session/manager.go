package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/checkpoint"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// CrashHook, when set (tests only), is called between the phases of a switch.
var CrashHook func(phase string)

func phase(p string) {
	if CrashHook != nil {
		CrashHook(p)
	}
}

// Manager owns the current session and moves the agent between sessions in place.
type Manager struct {
	Project *Project
	Agent   *agent.Agent
	Repo    *checkpoint.Repo // nil: checkpoints off
	Policy  *tools.Policy
	Router  *discover.Router

	mu  sync.Mutex
	cur *Session
}

// Current is the open session.
func (m *Manager) Current() *Session { m.mu.Lock(); defer m.mu.Unlock(); return m.cur }

// Attach makes s (with state st) current: the agent's state, journal and
// checkpoint handle are swapped together. With restore, st's saved settings
// are applied; the pinned model is returned for the caller to pin once
// models are discovered.
func (m *Manager) Attach(s *Session, st agent.State, restore bool) (pin string, err error) {
	if err := m.Agent.Load(st); err != nil {
		return "", err
	}
	m.Agent.SetJournal(s)
	m.Agent.CP = nil
	if m.Repo != nil {
		if m.Agent.CP, err = m.Repo.Session(s.ID); err != nil {
			return "", err
		}
	}
	m.cur = s
	if restore && st.Settings != nil {
		pin = m.apply(*st.Settings)
	}
	if st.Status != "active" {
		m.Agent.Commit(agent.Record{T: "status", Text: "active"})
	}
	return pin, nil
}

func (m *Manager) apply(s agent.Settings) string {
	if s.Mode != "" && m.Policy != nil {
		m.Policy.SetMode(s.Mode)
	}
	if s.Verify != nil {
		m.Agent.SetVerify(*s.Verify)
	}
	m.Agent.SetCaps(s.Limits, s.Budget)
	return s.Pin
}

// SaveSettings records the current mode, pinned model, verify command, limits and budget.
func (m *Manager) SaveSettings() {
	lim, budget := m.Agent.Caps()
	v := m.Agent.VerifyCmd()
	s := agent.Settings{Verify: &v, Limits: lim, Budget: budget}
	if m.Policy != nil {
		s.Mode = m.Policy.Mode()
	}
	if m.Router != nil {
		if p := m.Router.Pinned(); p != nil {
			s.Pin = p.Key()
		}
	}
	m.Agent.Commit(agent.Record{T: "settings", Settings: &s})
}

// Switch pauses and persists the current session, releases it, then loads
// and attaches session id. If the target can't be loaded, the current
// session is re-opened and stays current: nothing is lost either way.
func (m *Manager) Switch(ctx context.Context, id string) (agent.State, error) {
	if m.Agent.Running() {
		return agent.State{}, errors.New("a turn is running — pause it first")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.cur
	if old != nil && old.ID == id {
		return agent.State{}, fmt.Errorf("session %s is already current", id)
	}
	if err := m.release(old); err != nil {
		return agent.State{}, err
	}
	s, st, err := m.Project.Open(id)
	if err != nil {
		if old != nil {
			if back, _, berr := m.Project.Open(old.ID); berr == nil { // the agent still holds its state
				m.Agent.SetJournal(back)
				m.cur = back
				back.Record(agent.Record{T: "status", Text: "active", TS: time.Now().UnixMilli()})
			}
		}
		return agent.State{}, err
	}
	phase("loaded")
	_, err = m.Attach(s, st, true)
	phase("swapped")
	return st, err
}

// release pauses, persists and unlocks the current session (phases "persisted", "unlocked").
func (m *Manager) release(s *Session) error {
	if s == nil {
		return nil
	}
	m.SaveSettings()
	m.recordTree()
	s.Record(agent.Record{T: "status", Text: "paused", TS: time.Now().UnixMilli()})
	if err := s.Flush(); err != nil {
		return err
	}
	phase("persisted")
	m.Agent.SetJournal(nil)
	err := s.release()
	phase("unlocked")
	return err
}

// recordTree records the workspace as the session leaves it: the baseline
// for drift detection when it is resumed.
func (m *Manager) recordTree() {
	cp := m.Agent.CP
	if cp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if tree, err := cp.Snapshot(ctx); err == nil && cp.Keep(ctx, tree) == nil {
		m.Agent.Commit(agent.Record{T: "tree", Tree: tree})
	}
}

// New pauses the current session and starts an empty one.
func (m *Manager) New() error {
	if m.Agent.Running() {
		return errors.New("a turn is running — pause it first")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.cur
	if err := m.release(old); err != nil {
		return err
	}
	s, err := m.Project.Create()
	if err != nil {
		return err
	}
	_, err = m.Attach(s, agent.State{Status: "active"}, false)
	m.SaveSettings() // a new session starts with the current settings
	return err
}

// Fork branches a session after turn n (0 = latest) into a new session,
// which becomes current. from is "" for the current session, or any session
// id — even one open in another process: its log is read, not locked.
// Checkpoints are shared, not copied.
func (m *Manager) Fork(ctx context.Context, from string, n int) (string, error) {
	if m.Agent.Running() {
		return "", errors.New("a turn is running — pause it first")
	}
	if c := m.Current(); c != nil && from == "" {
		from = c.ID
	}
	var st agent.State
	if c := m.Current(); c != nil && c.ID == from {
		st = m.Agent.Export()
	} else {
		recs, err := m.Project.Records(from)
		if err != nil {
			return "", fmt.Errorf("no session %s", from)
		}
		for _, r := range recs {
			st.Apply(r)
		}
		st.Repair()
	}
	if n < 0 || n > len(st.Turns) {
		return "", fmt.Errorf("no turn %d (this conversation has %d)", n, len(st.Turns))
	}
	if n > 0 && n < len(st.Turns) {
		st.History, st.Turns = st.History[:st.Turns[n].Hist], st.Turns[:n]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	title := strings.TrimSpace(st.Title + " (fork)")
	if err := m.release(m.cur); err != nil {
		return "", err
	}
	s, err := m.Project.Fork(st, title)
	if err != nil {
		return "", err
	}
	if m.Repo != nil {
		if err := m.Repo.CopyRefs(ctx, from, s.ID); err != nil {
			return "", err
		}
	}
	st.Title = title
	_, err = m.Attach(s, st, false)
	return s.ID, err
}

// Delete removes another session and frees checkpoint data only it reached.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if c := m.Current(); c != nil && c.ID == id {
		return errors.New("can't delete the current session — switch away first")
	}
	if err := m.Project.Delete(id); err != nil {
		return err
	}
	if m.Repo != nil {
		go func() { _ = m.Repo.GC(context.WithoutCancel(ctx), m.Project.Saved) }()
	}
	return nil
}

// Rename sets the current session's title.
func (m *Manager) Rename(title string) {
	m.Agent.Commit(agent.Record{T: "title", Text: strings.TrimSpace(title)})
}

// Export renders the current session as markdown or JSON records.
func (m *Manager) Export(format string) ([]byte, error) {
	s := m.Current()
	if s == nil {
		return nil, errors.New("no session")
	}
	if err := s.Flush(); err != nil {
		return nil, err
	}
	recs, err := m.Project.Records(s.ID)
	if err != nil {
		return nil, err
	}
	return Export(m.Agent.Export(), recs, format)
}

// Close ends the current session with status (paused or stopped).
func (m *Manager) Close(status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	m.SaveSettings()
	m.recordTree()
	err := m.cur.Close(status)
	m.Agent.SetJournal(nil)
	m.cur = nil
	return err
}

// Drift reports files changed outside the session since it last ran and
// queues a note about them for the model's next prompt.
func (m *Manager) Drift(ctx context.Context, st agent.State) []checkpoint.Change {
	cp := m.Agent.CP
	if cp == nil || st.Tree == "" {
		return nil
	}
	now, err := cp.Snapshot(ctx)
	if err != nil {
		return nil
	}
	cs, err := cp.Diff(ctx, st.Tree, now) // fails if the baseline was pruned: no report
	if err != nil || len(cs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("[ternly] While this session was paused, these files changed outside it; re-read them before relying on what you saw earlier:")
	for i, c := range cs {
		if i == 20 {
			fmt.Fprintf(&b, " … and %d more", len(cs)-20)
			break
		}
		fmt.Fprintf(&b, " %s %s;", map[byte]string{'A': "added", 'D': "deleted", 'M': "modified", 'T': "type-changed"}[c.Status], c.Path)
	}
	m.Agent.SetNote(b.String())
	return cs
}

// AutoTitle names the session from its first prompt once, using the cheapest model.
func (m *Manager) AutoTitle(ctx context.Context) {
	st := m.Agent.Export()
	if st.Title != "" || len(st.Turns) == 0 {
		return
	}
	if t := m.Agent.MakeTitle(ctx, st.Turns[0].Prompt); t != "" {
		m.Rename(t)
	}
}

// Resumable decides what auto-resume does with the most recently active
// session: pick it, or — when it is open in another process (locked) or was
// ended with /stop (stopped) — start a new one and tell the user why. An
// older session is never resumed silently in its place.
func Resumable(list []Meta) (pick, locked, stopped *Meta) {
	if len(list) == 0 {
		return nil, nil, nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Active.After(list[j].Active) })
	switch m := &list[0]; {
	case m.Locked:
		return nil, m, nil
	case m.Status == "stopped":
		return nil, nil, m
	default:
		return m, nil, nil
	}
}
