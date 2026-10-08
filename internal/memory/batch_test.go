package memory

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// churn is a refresh's shape at its worst: few live items, each replaced
// many times (a registry that lists every version of a server).
func churn(n, times int) [][]*Item {
	var pages [][]*Item
	for r := range times {
		var page []*Item
		for i := range n {
			page = append(page, &Item{ID: fmt.Sprintf("mcp:s%d", i), Scope: Project, Kind: "catalog",
				Text: fmt.Sprintf("server %d for postgres queries v%d", i, r), Keys: []string{fmt.Sprintf("s%d", i)}, Updated: int64(r + 1)})
		}
		pages = append(pages, page)
	}
	return pages
}

// ADR 028: a held bulk update rebuilds the index at most once, at release,
// where the same updates unheld rebuild it each time half its slots die.
func TestHoldIndexRebuildsOnce(t *testing.T) {
	unheld, _ := OpenStore(filepath.Join(t.TempDir(), "a.log"))
	defer unheld.Close()
	for _, page := range churn(100, 30) {
		for _, it := range page {
			unheld.Upsert(it)
		}
	}
	if n := unheld.IndexRebuilds(); n < 2 {
		t.Fatalf("test premise: unheld churn rebuilt the index %d times, want ≥ 2", n)
	}
	held, _ := OpenStore(filepath.Join(t.TempDir(), "b.log"))
	defer held.Close()
	for refresh := 1; refresh <= 2; refresh++ {
		before := held.IndexRebuilds()
		release := held.HoldIndex()
		for _, page := range churn(100, 30) {
			held.UpsertAll(page)
			if got := held.SearchText("postgres", 3); len(got) != 3 { // searches stay correct while held
				t.Fatalf("search while held: %d results", len(got))
			}
		}
		if n := held.IndexRebuilds() - before; n != 0 {
			t.Fatalf("refresh %d: %d rebuilds while held", refresh, n)
		}
		release()
		release() // idempotent
		if n := held.IndexRebuilds() - before; n != 1 {
			t.Fatalf("refresh %d: %d rebuilds, want exactly 1 (at release)", refresh, n)
		}
	}
}

// After a held refresh the index is exact: every search returns what an
// index built fresh from the final items returns, same order, same scores.
// (Per-item upserts don't: dead postings inflate document frequencies until
// a rebuild, so scores drift; the premise is checked below.)
func TestUpsertAllGolden(t *testing.T) {
	one, _ := OpenStore(filepath.Join(t.TempDir(), "one.log"))
	defer one.Close()
	fresh, _ := OpenStore(filepath.Join(t.TempDir(), "fresh.log"))
	defer fresh.Close()
	all, _ := OpenStore(filepath.Join(t.TempDir(), "all.log"))
	defer all.Close()
	for _, page := range churn(300, 8) {
		for _, it := range page {
			c := *it
			one.Upsert(&c)
		}
		cp := make([]*Item, len(page))
		for i, it := range page {
			c := *it
			cp[i] = &c
		}
		release := all.HoldIndex()
		all.UpsertAll(cp)
		release()
	}
	for _, it := range churn(300, 8)[7] { // the final items, each once, with the versions the others reached
		c := *it
		c.V = 8
		fresh.Upsert(&c)
	}
	key := func(r []Scored) []string {
		var out []string
		for _, x := range r {
			out = append(out, fmt.Sprintf("%s %d %.4f %.4f", x.Item.ID, x.Item.V, x.BM25, x.Cover))
		}
		return out
	}
	drift := false
	for _, q := range []string{"postgres", "server 7", "queries v7", "s42", "nothing here"} {
		want, got := key(fresh.SearchText(q, 20)), key(all.SearchText(q, 20))
		if !slices.Equal(want, got) {
			t.Errorf("%q: fresh %v\nbatched %v", q, want, got)
		}
		drift = drift || !slices.Equal(want, key(one.SearchText(q, 20)))
	}
	if !drift {
		t.Fatal("test premise: per-item upserts should have drifted from the exact index")
	}
	if fresh.Len() != all.Len() || one.Len() != all.Len() {
		t.Fatalf("len %d / %d / %d", fresh.Len(), one.Len(), all.Len())
	}
}

func BenchmarkCatalogRefresh(b *testing.B) {
	pages := churn(1000, 8)
	b.ReportAllocs()
	for b.Loop() {
		s, _ := OpenStore(filepath.Join(b.TempDir(), "c.log"))
		release := s.HoldIndex()
		for _, page := range pages {
			cp := make([]*Item, len(page))
			for i, it := range page {
				c := *it
				cp[i] = &c
			}
			s.UpsertAll(cp)
		}
		release()
		_ = s.Close()
	}
}
