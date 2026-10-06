// Package session persists ternly sessions: one crash-safe append-only log
// per session (Log), grouped by project (a workspace identified by its path
// and git root commit), with per-session locks, listing, fork, delete and
// export, plus in-place switching (Manager).
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/logstore"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// ErrLocked: another ternly process has the session open.
var ErrLocked = errors.New("session is open in another ternly process")

// Project is one workspace's session directory.
type Project struct {
	Key        string `json:"-"`
	Dir        string `json:"-"`
	Path       string `json:"path"`
	RootCommit string `json:"root_commit,omitempty"`
	AdoptedKey string `json:"-"` // set when a moved workspace took over an old project (its old key)
}

// Meta summarises a session for listings; it is a cache of the log.
type Meta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
	Active  time.Time `json:"active"`
	Turns   int       `json:"turns"`
	Cost    float64   `json:"cost"`
	Locked  bool      `json:"-"`
}

// Key derives a project key from a workspace path.
func Key(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:8])
}

// rootCommit returns the repository's first commit ("" when not a git repo or empty).
func rootCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		return ""
	}
	ids := strings.Fields(string(out))
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// OpenProject finds or creates root's project under dataDir. A workspace that
// was moved or renamed takes over its old project when the git root commit
// matches and the old path no longer exists (so two live clones never share).
func OpenProject(dataDir, root string) (*Project, error) {
	base := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	p := &Project{Key: Key(root), Path: root, RootCommit: rootCommit(root)}
	p.Dir = filepath.Join(base, p.Key)
	if _, err := os.Stat(p.Dir); err != nil && p.RootCommit != "" {
		ents, _ := os.ReadDir(base)
		for _, e := range ents {
			var old Project
			b, err := os.ReadFile(filepath.Join(base, e.Name(), "project.json"))
			if err != nil || json.Unmarshal(b, &old) != nil || old.RootCommit != p.RootCommit {
				continue
			}
			if _, err := os.Stat(old.Path); err == nil {
				continue // still there: a different clone
			}
			if os.Rename(filepath.Join(base, e.Name()), p.Dir) == nil {
				p.AdoptedKey = e.Name()
			}
			break
		}
	}
	if err := os.MkdirAll(filepath.Join(p.Dir, "sessions"), 0o700); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(p)
	return p, writeAtomic(filepath.Join(p.Dir, "project.json"), b)
}

func (p *Project) sessionDir(id string) string { return filepath.Join(p.Dir, "sessions", id) }

// NewID returns a sortable, unique session id.
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\. \x00")
}

// Session is an open, locked session: an agent.Journal backed by its Log.
type Session struct {
	ID   string
	dir  string
	log  *logstore.Log
	lock *os.File
	mu   sync.Mutex
	meta Meta
}

// Create starts a new session.
func (p *Project) Create() (*Session, error) {
	id := NewID()
	if err := os.MkdirAll(p.sessionDir(id), 0o700); err != nil {
		return nil, err
	}
	s, _, err := p.open(id)
	if err != nil {
		return nil, err
	}
	s.meta.Created, s.meta.Active, s.meta.Status = time.Now(), time.Now(), "active"
	s.Record(agent.Record{T: "meta", Text: p.Path, TS: time.Now().UnixMilli()})
	return s, s.Flush()
}

// Open locks and loads a session. The state comes back repaired: dangling
// tool calls answered, so it is valid for every provider.
func (p *Project) Open(id string) (*Session, agent.State, error) {
	if !validID(id) {
		return nil, agent.State{}, fmt.Errorf("invalid session id %q", id)
	}
	if _, err := os.Stat(p.sessionDir(id)); err != nil {
		return nil, agent.State{}, fmt.Errorf("no session %s", id)
	}
	s, recs, err := p.open(id)
	if err != nil {
		return nil, agent.State{}, err
	}
	var st agent.State
	for _, r := range recs {
		st.Apply(r)
		s.observe(r)
	}
	if st.Repair() > 0 { // persist the synthetic results (they may sit mid-history, so as a snapshot)
		s.Record(agent.Record{T: "snapshot", State: ptr(st.Clone()), TS: time.Now().UnixMilli()})
	}
	if len(recs) > 0 && recs[0].T == "meta" {
		s.meta.Created = time.UnixMilli(recs[0].TS)
	}
	return s, st, s.Flush()
}

func (p *Project) open(id string) (*Session, []agent.Record, error) {
	dir := p.sessionDir(id)
	lock, err := lockFile(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, nil, ErrLocked
	}
	l, payloads, _, err := logstore.Open(filepath.Join(dir, "events.log"))
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	recs := decodeRecords(payloads)
	return &Session{ID: id, dir: dir, log: l, lock: lock, meta: Meta{ID: id}}, recs, nil
}

func ptr[T any](v T) *T { return &v }

func lockFile(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Record implements agent.Journal.
func (s *Session) Record(r agent.Record) {
	s.observe(r)
	b, _ := json.Marshal(r)
	s.log.Append(append([]byte(nil), b...)) // deliberate regression (ADR 017 gate proof): one extra copy, one extra allocation
}

// Sync implements agent.Journal: fsync in the background and refresh the listing cache.
func (s *Session) Sync() {
	s.log.Sync()
	s.writeMeta()
}

// Flush makes everything durable now.
func (s *Session) Flush() error {
	err := s.log.Flush()
	s.writeMeta()
	return err
}

// Close records the final status, flushes, closes and unlocks.
func (s *Session) Close(status string) error {
	if status != "" {
		s.Record(agent.Record{T: "status", Text: status, TS: time.Now().UnixMilli()})
	}
	err := s.Flush()
	if rerr := s.release(); err == nil {
		err = rerr
	}
	return err
}

// release closes the log and drops the lock (callers flush first).
func (s *Session) release() error {
	err := s.log.Close()
	s.lock.Close()
	return err
}

func (s *Session) Meta() Meta { s.mu.Lock(); defer s.mu.Unlock(); return s.meta }

func (s *Session) observe(r agent.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.TS != 0 {
		s.meta.Active = time.UnixMilli(r.TS)
	}
	switch r.T {
	case "turn":
		s.meta.Turns++
	case "usage":
		s.meta.Cost += r.Cost
	case "title":
		s.meta.Title = r.Text
	case "status":
		s.meta.Status = r.Text
	case "snapshot":
		if r.State != nil {
			s.meta.Turns, s.meta.Cost, s.meta.Title = r.State.Ledger.Turns, r.State.Ledger.Cost, r.State.Title
		}
	}
}

func (s *Session) writeMeta() {
	m := s.Meta()
	b, _ := json.Marshal(m)
	_ = writeAtomic(filepath.Join(s.dir, "meta.json"), b)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// List returns the project's sessions, most recently active first.
func (p *Project) List() ([]Meta, error) {
	ents, err := os.ReadDir(filepath.Join(p.Dir, "sessions"))
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range ents {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		var m Meta
		b, err := os.ReadFile(filepath.Join(p.sessionDir(e.Name()), "meta.json"))
		if err != nil || json.Unmarshal(b, &m) != nil {
			m = Meta{ID: e.Name(), Status: "unknown"}
		}
		m.ID = e.Name()
		if f, err := lockFile(filepath.Join(p.sessionDir(e.Name()), "lock")); err == nil {
			f.Close()
		} else {
			m.Locked = true
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Active.After(out[j].Active) })
	return out, nil
}

// Saved reports whether a session exists (used to decide what checkpoints GC keeps).
func (p *Project) Saved(id string) bool {
	_, err := os.Stat(filepath.Join(p.sessionDir(id), "events.log"))
	return validID(id) && err == nil
}

// Delete removes a session that no process has open.
func (p *Project) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	dir := p.sessionDir(id)
	f, err := lockFile(filepath.Join(dir, "lock"))
	if err != nil {
		return ErrLocked
	}
	defer f.Close()
	return os.RemoveAll(dir)
}

// Fork writes a new session whose history is st (one snapshot record).
func (p *Project) Fork(st agent.State, title string) (*Session, error) {
	s, err := p.Create()
	if err != nil {
		return nil, err
	}
	st.Title, st.Status = title, "active"
	s.Record(agent.Record{T: "snapshot", State: &st, TS: time.Now().UnixMilli()})
	return s, s.Flush()
}

// Export writes a session as markdown or as its JSON records.
func Export(st agent.State, recs []agent.Record, format string) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(recs, "", "  ")
	case "md", "markdown", "":
	default:
		return nil, fmt.Errorf("unknown format %q (md or json)", format)
	}
	var b strings.Builder
	title := st.Title
	if title == "" {
		title = "ternly session"
	}
	fmt.Fprintf(&b, "# %s\n\n%d turns · $%.4f\n", title, st.Ledger.Turns, st.Ledger.Cost)
	for _, m := range st.History {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "\n## User\n\n%s\n", m.Content)
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				fmt.Fprintf(&b, "\n## Assistant\n\n%s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "\n**%s** `%s`\n", tc.Name, tools.Cap(tc.Args, 300))
			}
		case "tool":
			fmt.Fprintf(&b, "\n```\n%s\n```\n", tools.Cap(tools.Unframe(m.Content), 2000))
		}
	}
	return []byte(b.String()), nil
}

// Records reads a session's records without opening (locking) it.
func (p *Project) Records(id string) ([]agent.Record, error) {
	f, err := os.Open(filepath.Join(p.sessionDir(id), "events.log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	payloads, _, err := logstore.ReadAll(f)
	return decodeRecords(payloads), err
}

func decodeRecords(payloads [][]byte) []agent.Record {
	recs := make([]agent.Record, 0, len(payloads))
	for _, p := range payloads {
		var r agent.Record
		if json.Unmarshal(p, &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs
}

var _ agent.Journal = (*Session)(nil)
