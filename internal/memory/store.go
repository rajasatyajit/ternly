// Package memory keeps what ternly learned in earlier turns and sessions, and
// recalls it under a token budget whichever model is active (ADR 009).
//
// A Store is one append-only log (internal/logstore, shared between processes)
// with the live items and their indexes in memory. A Memory combines the
// project store and the user store, ranks with BM25 + structure + recency
// (+ optional local-embedding vectors), and writes at turn boundaries.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/logstore"
)

// Scope is an item's tier (the working tier is the context window: never stored).
type Scope string

const (
	Session Scope = "session" // expires SessionTTL after last use
	Project Scope = "project"
	User    Scope = "user"
)

// SessionTTL is how long session-tier items live after their last update.
var SessionTTL = 30 * 24 * time.Hour

// Item is one remembered fact with its provenance.
type Item struct {
	ID      string   `json:"id"`
	V       int      `json:"v"` // version: a near-duplicate rewrite bumps it, keeping the ID
	Scope   Scope    `json:"scope"`
	Kind    string   `json:"kind"` // turn, fix, pref, decision, convention, note
	Text    string   `json:"text"`
	Keys    []string `json:"keys,omitempty"` // files and symbols the item is about
	Source  string   `json:"src"`            // user, auto, model
	Session string   `json:"session,omitempty"`
	Turn    int      `json:"turn,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Created int64    `json:"created"` // unix ms
	Updated int64    `json:"updated"`
	Expires int64    `json:"expires,omitempty"`
	Prev    []string `json:"prev,omitempty"` // earlier versions' text, newest first
	Vec     []byte   `json:"vec,omitempty"`  // int8 embedding (see quantize)
	Scale   float32  `json:"scale,omitempty"`
	VecV    int      `json:"vecv,omitempty"`   // the version Vec was computed for
	VecAlt  bool     `json:"vecalt,omitempty"` // Vec includes Alt
	// Alt holds other wordings of the item (synonyms, paraphrases) written
	// once per version by the cheapest model, so a question phrased
	// differently still matches. Indexed and embedded; never injected.
	Alt  string `json:"alt,omitempty"`
	AltV int    `json:"altv,omitempty"` // the version Alt was written for
	// Meta is free-form data for stores that aren't memory (the capability
	// catalog keeps publisher, source, version, … here).
	Meta map[string]string `json:"meta,omitempty"`
}

// Stored items are immutable: a change stores a new *Item. So search can
// read them after releasing the store's lock. Mutable per-item state (last
// use, index slot) lives in the Store and index.

// Expired reports whether the item is past its expiry at now (unix ms).
func (it *Item) Expired(now int64) bool { return it.Expires > 0 && now >= it.Expires }

// rec is one log line. put carries a whole item; del and use name an ID.
type rec struct {
	Op   string `json:"op"` // put, del, use
	Item *Item  `json:"item,omitempty"`
	ID   string `json:"id,omitempty"`
	V    int    `json:"v,omitempty"`
	At   int64  `json:"at,omitempty"`
}

// Store is one memory file: the project's or the user's.
type Store struct {
	mu    sync.RWMutex
	log   *logstore.Log
	items map[string]*Item
	dead  map[string]int   // deleted IDs → version (a put must beat it to revive)
	used  map[string]int64 // ID → last recalled (unix ms), from "use" records
	ix    *index
	recs  int // records in the log (for compaction stats)
}

// OpenStore opens or creates the store at path. Expired and superseded
// records are dropped when no other process has it open.
func OpenStore(path string) (*Store, error) {
	s := &Store{items: map[string]*Item{}, dead: map[string]int{}, used: map[string]int64{}, ix: newIndex()}
	l, recs, err := logstore.OpenShared(path, compactor)
	if err != nil {
		return nil, err
	}
	s.log = l
	s.mu.Lock()
	s.applyAll(recs)
	s.mu.Unlock()
	return s, nil
}

// compactor keeps one put per live item (its latest version, last use folded
// in) and drops deletions and expired items, once the log is 2× the live set.
func compactor(raw [][]byte) [][]byte {
	s := &Store{items: map[string]*Item{}, dead: map[string]int{}, used: map[string]int64{}} // no index: only the live set matters
	s.applyAll(raw)
	now := time.Now().UnixMilli()
	live := 0
	for _, it := range s.items {
		if !it.Expired(now) {
			live++
		}
	}
	if len(raw) < 64 || len(raw) < 2*live {
		return raw
	}
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.items[ids[i]].Created < s.items[ids[j]].Created })
	out := make([][]byte, 0, live)
	for _, id := range ids {
		it := s.items[id]
		if it.Expired(now) {
			continue
		}
		b, _ := json.Marshal(rec{Op: "put", Item: it})
		out = append(out, b)
		if u := s.used[id]; u > it.Updated {
			b, _ = json.Marshal(rec{Op: "use", ID: id, At: u})
			out = append(out, b)
		}
	}
	return out
}

// applyAll decodes (and, for the index, tokenizes) records on all cores,
// then applies them in order.
func (s *Store) applyAll(raw [][]byte) {
	recs := make([]rec, len(raw))
	toks := make([][]string, len(raw))
	ok := make([]bool, len(raw))
	workers := min(runtime.GOMAXPROCS(0), 1+len(raw)/512)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < len(raw); i += workers {
				if json.Unmarshal(raw[i], &recs[i]) != nil {
					continue
				}
				ok[i] = true
				if it := recs[i].Item; it != nil && s.ix != nil {
					toks[i] = docTokens(it)
				}
			}
		}()
	}
	wg.Wait()
	for i := range recs {
		if ok[i] {
			s.apply(recs[i], toks[i])
		}
	}
	s.recs += len(raw)
}

// apply is idempotent: replaying any suffix of the log converges to the same
// state. toks are the item's pre-computed index terms (nil: computed here).
func (s *Store) apply(r rec, toks []string) {
	switch r.Op {
	case "put":
		it := r.Item
		if it == nil || it.ID == "" {
			return
		}
		if v, ok := s.dead[it.ID]; ok && it.V <= v {
			return
		}
		cur := s.items[it.ID]
		if cur != nil && (cur.V > it.V || cur.V == it.V && cur.Updated == it.Updated && cur.VecV == it.VecV && cur.VecAlt == it.VecAlt && cur.AltV == it.AltV) {
			return // newer, or this very write (replayed by refresh)
		}
		if cur != nil {
			s.ix.remove(cur)
		}
		delete(s.dead, it.ID)
		s.items[it.ID] = it
		s.ix.add(it, toks)
	case "del":
		if cur := s.items[r.ID]; cur != nil && cur.V <= r.V {
			s.ix.remove(cur)
			delete(s.items, r.ID)
			delete(s.used, r.ID)
		}
		s.dead[r.ID] = max(s.dead[r.ID], r.V)
	case "use":
		if s.items[r.ID] != nil {
			s.used[r.ID] = max(s.used[r.ID], r.At)
		}
	}
}

// refresh applies records other processes appended since the last refresh.
func (s *Store) refresh() {
	recs, reset, err := s.log.Tail()
	if err != nil || len(recs) == 0 && !reset {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reset { // another process compacted: rebuild from the new file
		s.items, s.dead, s.used, s.ix, s.recs = map[string]*Item{}, map[string]int{}, map[string]int64{}, newIndex(), 0
	}
	s.applyAll(recs)
}

func (s *Store) write(r rec) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.log.Append(b) // the log's own writer goroutine does the I/O
	s.mu.Lock()
	s.recs++
	s.mu.Unlock()
}

// put stores it (a new item or a new version) and returns it.
func (s *Store) put(it *Item) *Item {
	s.mu.Lock()
	s.apply(rec{Op: "put", Item: it}, nil)
	s.mu.Unlock()
	s.write(rec{Op: "put", Item: it})
	return it
}

// Get returns a copy of the live item with this ID (or a unique ID prefix).
func (s *Store) Get(id string) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if it := s.find(id); it != nil {
		return *it, true
	}
	return Item{}, false
}

func (s *Store) find(id string) *Item {
	if it := s.items[id]; it != nil {
		return it
	}
	if len(id) < 4 {
		return nil
	}
	var hit *Item
	for k, it := range s.items {
		if strings.HasPrefix(k, id) {
			if hit != nil {
				return nil
			}
			hit = it
		}
	}
	return hit
}

// ErrNotFound is returned for an unknown item ID.
var ErrNotFound = errors.New("no memory item with that id")

// Forget deletes an item.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	it := s.find(id)
	if it == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	r := rec{Op: "del", ID: it.ID, V: it.V}
	s.apply(r, nil)
	s.mu.Unlock()
	s.write(r)
	return nil
}

// touch records that items were recalled (recency counts from last use).
func (s *Store) touch(ids []string, at int64) {
	for _, id := range ids {
		s.mu.Lock()
		s.apply(rec{Op: "use", ID: id, At: at}, nil)
		s.mu.Unlock()
		s.write(rec{Op: "use", ID: id, At: at})
	}
}

// List returns live, unexpired items, newest first.
func (s *Store) List() []Item {
	now := time.Now().UnixMilli()
	s.mu.RLock()
	out := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		if !it.Expired(now) {
			out = append(out, *it)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

// Len is the number of live items.
func (s *Store) Len() int { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.items) }

// Sync makes writes so far durable soon (turn boundary); Flush waits for it.
func (s *Store) Sync()        { s.log.Sync() }
func (s *Store) Flush() error { return s.log.Flush() }
func (s *Store) Close() error { return s.log.Close() }

func newID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Upsert stores it as is (no dedupe, secret check or versioning: for
// stores that aren't memory, such as the capability catalog). A put with
// the same ID replaces the previous item.
func (s *Store) Upsert(it *Item) {
	s.mu.Lock()
	if cur := s.items[it.ID]; cur != nil {
		it.V = cur.V + 1
	} else if it.V == 0 {
		it.V = 1
	}
	s.apply(rec{Op: "put", Item: it}, nil)
	s.mu.Unlock()
	s.write(rec{Op: "put", Item: it})
}

// Scored is a search result: BM25, and the share of the query's words found.
type Scored struct {
	Item        Item
	BM25, Cover float32
}

// SearchText ranks items by words alone (BM25 with coverage), best first.
func (s *Store) SearchText(q string, limit int) []Scored {
	qt := uniq(tokens(q))
	s.mu.RLock()
	var out []Scored
	s.ix.lexical(qt, nil, 0, func(slot uint32, bm, cover float32, nterm, nalt int) {
		out = append(out, Scored{Item: *s.ix.docs[slot], BM25: bm, Cover: cover})
	})
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].BM25*(0.5+out[i].Cover), out[j].BM25*(0.5+out[j].Cover)
		if a != b {
			return a > b
		}
		return out[i].Item.ID < out[j].Item.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
