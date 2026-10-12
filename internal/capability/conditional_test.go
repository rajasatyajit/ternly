package capability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// source is a fake catalog source: a marketplace.json whose body, ETag and
// Last-Modified the test controls.
type source struct {
	*httptest.Server
	mu        sync.Mutex
	body      string
	etag      string
	modified  string
	requests  int
	full      int // 200s sent
	condition []string
}

func newSource(t *testing.T, plugins int, validators bool) *source {
	s := &source{}
	s.setPlugins(plugins)
	if validators {
		s.etag, s.modified = `"v1"`, "Sat, 10 Oct 2026 09:00:00 GMT"
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++
		s.condition = append(s.condition, r.Header.Get("If-None-Match")+"|"+r.Header.Get("If-Modified-Since"))
		if s.etag != "" {
			w.Header().Set("ETag", s.etag)
			w.Header().Set("Last-Modified", s.modified)
			if r.Header.Get("If-None-Match") == s.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		s.full++
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *source) setPlugins(n int) {
	b := `{"name":"fake-market","plugins":[`
	for i := range n {
		if i > 0 {
			b += ","
		}
		b += fmt.Sprintf(`{"name":"p%d","description":"postgres helper %d"}`, i, i)
	}
	s.body = b + `]}`
}

func (s *source) stats() (requests, full int, cond []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.full, append([]string(nil), s.condition...)
}

func logSize(t *testing.T, c *Catalog) int64 {
	if err := c.store.Flush(); err != nil { // Sync only asks; Flush waits for the writer
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(c.Dir, "catalog.log"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// ADR 031: a source with validators is asked conditionally; a 304 is no
// download and no re-index; a change is downloaded and indexed again.
func TestConditionalFetch(t *testing.T) {
	src := newSource(t, 50, true)
	dir := t.TempDir()
	c := &Catalog{Dir: dir, Sources: Sources{Marketplaces: []string{src.URL + "/m.json"}}}
	defer c.Close()
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 50 {
		t.Fatalf("%d entries", c.Len())
	}
	_, first, _ := c.LastRefresh()
	size := logSize(t, c)

	// a second start: a new Catalog on the same directory
	c.Close()
	c = &Catalog{Dir: dir, Sources: c.Sources}
	defer c.Close()
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	req, full, cond := src.stats()
	if req != 2 || full != 1 || cond[1] != `"v1"|Sat, 10 Oct 2026 09:00:00 GMT` {
		t.Fatalf("requests %d, full bodies %d, conditions %q", req, full, cond)
	}
	if got := logSize(t, c); got != size {
		t.Fatalf("unchanged source re-indexed: log %d → %d bytes", size, got)
	}
	_, second, unchanged := c.LastRefresh()
	if unchanged != 1 || second >= first || second > 1024 {
		t.Fatalf("second refresh received %d bytes (first %d), unchanged %d", second, first, unchanged)
	}
	t.Logf("first refresh %d bytes on the wire, unchanged second %d", first, second)

	// the source changes: downloaded and indexed again
	src.mu.Lock()
	src.setPlugins(60)
	src.etag = `"v2"`
	src.mu.Unlock()
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, full, _ := src.stats(); full != 2 || c.Len() != 60 {
		t.Fatalf("changed source: %d full bodies, %d entries", full, c.Len())
	}
}

// A source without validators is downloaded each time, but an identical
// body isn't indexed again (its hash); a changed one is.
func TestNoValidatorsHashSkip(t *testing.T) {
	src := newSource(t, 40, false)
	c := &Catalog{Dir: t.TempDir(), Sources: Sources{Marketplaces: []string{src.URL + "/m.json"}}}
	defer c.Close()
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	size := logSize(t, c)
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, full, cond := src.stats(); full != 2 || cond[1] != "|" {
		t.Fatalf("full bodies %d, conditions %q (none expected: no validators)", full, cond)
	}
	if got := logSize(t, c); got != size {
		t.Fatalf("identical body re-indexed: log %d → %d", size, got)
	}
	if _, _, unchanged := c.LastRefresh(); unchanged != 1 {
		t.Fatalf("unchanged %d", unchanged)
	}
	src.mu.Lock()
	src.setPlugins(41)
	src.mu.Unlock()
	if err := c.Refresh(context.Background(), nil); err != nil || c.Len() != 41 {
		t.Fatalf("changed body: %v, %d entries", err, c.Len())
	}
}

// Validators are kept only once their entries are indexed: a body that
// fails to decode leaves none, so the next refresh fetches in full.
func TestValidatorsOnlyAfterIndexing(t *testing.T) {
	src := newSource(t, 5, true)
	src.body = `{"plugins": [`    // broken
	good := newSource(t, 3, true) // another source: the index isn't empty
	good.body = strings.Replace(good.body, "fake-market", "other-market", 1)
	c := &Catalog{Dir: t.TempDir(), Sources: Sources{Marketplaces: []string{good.URL + "/m.json", src.URL + "/m.json"}}}
	defer c.Close()
	if err := c.Refresh(context.Background(), nil); err == nil {
		t.Fatal("no error for a broken body")
	}
	src.mu.Lock()
	src.setPlugins(5)
	src.mu.Unlock()
	if err := c.Refresh(context.Background(), nil); err != nil || c.Len() != 8 {
		t.Fatalf("%v, %d entries", err, c.Len())
	}
	if _, _, cond := src.stats(); cond[1] != "|" {
		t.Fatalf("second request was conditional (%q) though nothing was indexed", cond[1])
	}
	// an emptied index (catalog.log removed) fetches everything again
	c.Close()
	_ = os.Remove(filepath.Join(c.Dir, "catalog.log"))
	c = &Catalog{Dir: c.Dir, Sources: c.Sources}
	defer c.Close()
	if err := c.Refresh(context.Background(), nil); err != nil || c.Len() != 8 {
		t.Fatalf("after the index was removed: %v, %d entries", err, c.Len())
	}
}

// The idle refresh waits before each request while not allowed, and goes
// on when allowed; a refresh the user asks for doesn't wait.
func TestRefreshIdleWaits(t *testing.T) {
	old := idlePoll
	idlePoll = time.Millisecond
	defer func() { idlePoll = old }()
	a, b := newSource(t, 3, true), newSource(t, 4, true)
	c := &Catalog{Dir: t.TempDir(), Sources: Sources{Marketplaces: []string{a.URL + "/m.json", b.URL + "/m.json"}}}
	defer c.Close()
	var allowed atomic.Bool
	done := make(chan error, 1)
	go func() { done <- c.RefreshIdle(context.Background(), allowed.Load) }()
	time.Sleep(30 * time.Millisecond)
	if r, _, _ := a.stats(); r != 0 {
		t.Fatal("fetched while not idle")
	}
	allowed.Store(true)
	if err := <-done; err != nil || c.Len() != 4 { // both sources list p0…p3 under one name
		t.Fatalf("%v, %d entries", err, c.Len())
	}
	// the user's own refresh ignores idleness (and an idle one waiting stops waiting)
	allowed.Store(false)
	go func() { done <- c.RefreshIdle(context.Background(), allowed.Load) }()
	time.Sleep(10 * time.Millisecond)
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// a cancelled idle refresh isn't a completed one: still due
	c2 := &Catalog{Dir: t.TempDir(), Sources: c.Sources}
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	allowed.Store(false)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := c2.RefreshIdle(ctx, allowed.Load); err == nil {
		t.Fatal("cancelled refresh reported success")
	}
	if !c2.Due(24 * time.Hour) {
		t.Fatal("not due after a cancelled refresh")
	}
}

// An interrupted full registry crawl goes on from its saved cursor.
func TestRegistryCrawlResumes(t *testing.T) {
	var mu sync.Mutex
	var cursors []string
	fail := 3 // the request for this page fails once (ternly quit)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		mu.Lock()
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		if p == fail {
			fail = -1
			mu.Unlock()
			http.Error(w, "gone", http.StatusBadGateway)
			return
		}
		mu.Unlock()
		next := ""
		if p+1 < 6 {
			next = strconv.Itoa(p + 1)
		}
		fmt.Fprintf(w, `{"servers":[{"server":{"name":"io.example/s%d","description":"d","version":"1.0.0","packages":[{"registryType":"npm","identifier":"s%d","version":"1.0.0"}]}}],"metadata":{"nextCursor":%q}}`, p, p, next)
	}))
	defer reg.Close()
	dir := t.TempDir()
	c := &Catalog{Dir: dir, Sources: Sources{MCPRegistry: reg.URL}}
	if err := c.Refresh(context.Background(), nil); err == nil {
		t.Fatal("no error")
	}
	c.Close()
	c = &Catalog{Dir: dir, Sources: Sources{MCPRegistry: reg.URL}}
	defer c.Close()
	if !c.Due(24 * time.Hour) {
		t.Fatal("not due after an interrupted crawl")
	}
	if err := c.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(cursors) != "[ 1 2 3 3 4 5]" || c.Len() != 6 {
		t.Fatalf("cursors %q, %d entries", cursors, c.Len())
	}
	if c.Due(24 * time.Hour) {
		t.Fatal("due right after a completed crawl")
	}
}

// Due reads the state alone: the index isn't opened at startup.
func TestDueDoesNotOpenStore(t *testing.T) {
	c := &Catalog{Dir: t.TempDir()}
	if !c.Due(24 * time.Hour) {
		t.Fatal("a new catalog isn't due")
	}
	if c.store != nil {
		t.Fatal("Due opened the store")
	}
}
