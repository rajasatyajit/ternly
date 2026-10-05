package storebench

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	bolt "go.etcd.io/bbolt"

	"storebench/logstore"
)

const N = 100_000

var words = strings.Fields("router session checkpoint graph symbol verify build test sandbox model token budget cache provider stream tool edit diff commit branch module package interface method field struct error retry timeout context file path workspace index memory recall fact decision convention failure fix preference user project")

type item struct {
	ID, Tier, Kind, Text, Session, Commit string
	Files                                 []string
	Created                               int64
}

func mkItem(rng *rand.Rand, i int) item {
	var b strings.Builder
	for b.Len() < 320 {
		b.WriteString(words[rng.Intn(len(words))] + " ")
	}
	return item{ID: fmt.Sprintf("m%07d", i), Tier: "project", Kind: "fact", Text: b.String(), Session: "20261005-000000-abcdef",
		Commit: "af1b8de3421be036826b323e651063e64f39446d", Files: []string{"internal/agent/agent.go"}, Created: time.Now().UnixMilli()}
}

func pct(d []time.Duration, p float64) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func size(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

type store interface {
	put(id string, v []byte, sync bool)
	get(id string) []byte
	loadAll() int // reopen and read everything; returns items
	close()
}

// ---- logstore: append-only log, live set in memory
type logS struct {
	path string
	l    *logstore.Log
	m    map[string][]byte
}

func newLog(dir string) *logS {
	p := filepath.Join(dir, "m.log")
	l, _, _, _ := logstore.Open(p)
	return &logS{path: p, l: l, m: map[string][]byte{}}
}
func (s *logS) put(id string, v []byte, sync bool) {
	s.m[id] = v
	s.l.Append(v)
	if sync {
		_ = s.l.Flush()
	}
}
func (s *logS) get(id string) []byte { return s.m[id] }
func (s *logS) loadAll() int {
	_ = s.l.Close()
	l, recs, _, _ := logstore.Open(s.path)
	s.l = l
	m := make(map[string][]byte, len(recs))
	for _, r := range recs {
		var it struct{ ID string }
		_ = json.Unmarshal(r, &it)
		m[it.ID] = r
	}
	s.m = m
	return len(m)
}
func (s *logS) close() { _ = s.l.Close() }

// ---- bbolt
type boltS struct {
	path string
	db   *bolt.DB
}

var bucket = []byte("m")

func newBolt(dir string) *boltS {
	p := filepath.Join(dir, "m.db")
	db, err := bolt.Open(p, 0o600, nil)
	if err != nil {
		panic(err)
	}
	db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(bucket); return err })
	return &boltS{path: p, db: db}
}
func (s *boltS) put(id string, v []byte, sync bool) {
	s.db.NoSync = !sync
	s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucket).Put([]byte(id), v) })
}
func (s *boltS) get(id string) (out []byte) {
	s.db.View(func(tx *bolt.Tx) error { out = append([]byte(nil), tx.Bucket(bucket).Get([]byte(id))...); return nil })
	return out
}
func (s *boltS) loadAll() int {
	s.db.Sync()
	s.db.Close()
	db, _ := bolt.Open(s.path, 0o600, nil)
	s.db = db
	n := 0
	db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(k, v []byte) error { var it item; _ = json.Unmarshal(v, &it); n++; return nil })
	})
	return n
}
func (s *boltS) close() { s.db.Close() }

// ---- pebble
type pebS struct {
	dir string
	db  *pebble.DB
}

func newPebble(dir string) *pebS {
	db, err := pebble.Open(filepath.Join(dir, "peb"), &pebble.Options{})
	if err != nil {
		panic(err)
	}
	return &pebS{dir: filepath.Join(dir, "peb"), db: db}
}
func (s *pebS) put(id string, v []byte, sync bool) {
	o := pebble.NoSync
	if sync {
		o = pebble.Sync
	}
	s.db.Set([]byte(id), v, o)
}
func (s *pebS) get(id string) []byte {
	v, c, err := s.db.Get([]byte(id))
	if err != nil {
		return nil
	}
	out := append([]byte(nil), v...)
	c.Close()
	return out
}
func (s *pebS) loadAll() int {
	s.db.Flush()
	s.db.Close()
	db, _ := pebble.Open(s.dir, &pebble.Options{})
	s.db = db
	it, _ := db.NewIter(nil)
	n := 0
	for it.First(); it.Valid(); it.Next() {
		var x item
		_ = json.Unmarshal(it.Value(), &x)
		n++
	}
	it.Close()
	return n
}
func (s *pebS) close() { s.db.Close() }

func TestStores(t *testing.T) {
	for _, c := range []struct {
		name string
		mk   func(string) store
	}{
		{"logstore", func(d string) store { return newLog(d) }},
		{"bbolt v1.5.0", func(d string) store { return newBolt(d) }},
		{"pebble v2.1.7", func(d string) store { return newPebble(d) }},
	} {
		dir := t.TempDir()
		s := c.mk(dir)
		rng := rand.New(rand.NewSource(1))
		vals := make([][]byte, N)
		for i := range N {
			vals[i], _ = json.Marshal(mkItem(rng, i))
		}
		var put []time.Duration
		t0 := time.Now()
		for i := range N {
			t1 := time.Now()
			s.put(fmt.Sprintf("m%07d", i), vals[i], false)
			put = append(put, time.Since(t1))
		}
		bulk := time.Since(t0)
		var sync []time.Duration
		for i := range 300 {
			t1 := time.Now()
			s.put(fmt.Sprintf("m%07d", i), vals[i], true)
			sync = append(sync, time.Since(t1))
		}
		t0 = time.Now()
		n := s.loadAll()
		load := time.Since(t0)
		var get []time.Duration
		for range 20000 {
			id := fmt.Sprintf("m%07d", rng.Intn(N))
			t1 := time.Now()
			if s.get(id) == nil {
				t.Fatal("miss")
			}
			get = append(get, time.Since(t1))
		}
		s.close()
		t.Logf("%-14s put(background) p50 %8v p99 %8v | put(fsync) p50 %8v p99 %8v | get p50 %7v p99 %7v | reopen+load %d %7v | 100k inserts %v | disk %d MB",
			c.name, pct(put, .5), pct(put, .99), pct(sync, .5), pct(sync, .99), pct(get, .5), pct(get, .99), n, load.Round(time.Millisecond), bulk.Round(time.Millisecond), size(dir)>>20)
	}
}
