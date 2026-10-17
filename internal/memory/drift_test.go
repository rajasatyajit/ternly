package memory

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// driftStore is a store whose notes were revised (same ID, new version)
// rounds times: its index carries the dead postings of every old version.
func driftStore(t *testing.T, n, rounds int) (*Store, []*Item) {
	t.Helper()
	words, files := corpusWords(t)
	r := rand.New(rand.NewPCG(11, 12))
	s, err := OpenStore(filepath.Join(t.TempDir(), "drift.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	items := make([]*Item, 0, n)
	for i := range n {
		it := synthItem(r, words, files, i)
		it.ID = "n" + strconv.Itoa(i)
		items = append(items, &it)
		s.Upsert(&it)
	}
	for range rounds {
		for i := 0; i < n; i += 2 { // revise half the notes: a few words change, as an edit does
			old := items[i]
			ws := strings.Fields(old.Text)
			for k := 0; k < 4; k++ {
				ws[r.IntN(len(ws)-1)] = zipf(r, words)
			}
			nv := *old
			nv.V, nv.Text = old.V+1, strings.Join(ws, " ")
			items[i] = &nv
			s.Upsert(&nv)
		}
	}
	return s, items
}

// TestBM25Drift (opt-in, TERNLY_BM25_DRIFT=1) measures how far a store
// with revision history ranks from the same store with an exact index.
func TestBM25Drift(t *testing.T) {
	if os.Getenv("TERNLY_BM25_DRIFT") == "" {
		t.Skip("set TERNLY_BM25_DRIFT=1 to measure (ADR 028's open finding)")
	}
	n, rounds := 800, 2
	if v := os.Getenv("TERNLY_BM25_DRIFT"); strings.Contains(v, "x") { // "NxR": n notes, R revision rounds
		a, b, _ := strings.Cut(v, "x")
		n, _ = strconv.Atoi(a)
		rounds, _ = strconv.Atoi(b)
	}
	s, items := driftStore(t, n, rounds)
	r := rand.New(rand.NewPCG(5, 6))
	type q struct{ text, src string }
	var qs []q
	for range 300 {
		it := items[r.IntN(len(items))]
		ws := strings.Fields(it.Text)
		qs = append(qs, q{ws[r.IntN(len(ws)-1)] + " " + ws[r.IntN(len(ws)-1)] + " " + ws[r.IntN(len(ws)-1)], it.ID})
	}
	run := func() (top [][]string) {
		for _, x := range qs {
			var ids []string
			for _, h := range s.SearchText(x.text, 10) {
				ids = append(ids, h.Item.ID)
			}
			top = append(top, ids)
		}
		return top
	}
	s.mu.RLock()
	dead, live := len(s.ix.docs)-s.ix.live, s.ix.live
	neg := 0
	N := float64(s.ix.live)
	for _, p := range s.ix.post {
		if float64(len(p)) > N { // more postings than live documents: idf below 0 for this term
			neg++
		}
	}
	s.mu.RUnlock()
	drifted := run()
	s.mu.Lock()
	s.ix.rebuild()
	s.mu.Unlock()
	exact := run()
	top1, overlap, srcDrift, srcExact := 0, 0.0, 0, 0
	for i := range qs {
		if len(drifted[i]) > 0 && len(exact[i]) > 0 && drifted[i][0] == exact[i][0] {
			top1++
		}
		inter := 0
		for _, a := range drifted[i] {
			for _, b := range exact[i] {
				if a == b {
					inter++
				}
			}
		}
		overlap += float64(inter) / float64(max(len(exact[i]), 1))
		in5 := func(ids []string) bool {
			for k, id := range ids {
				if k < 5 && id == qs[i].src {
					return true
				}
			}
			return false
		}
		if in5(drifted[i]) {
			srcDrift++
		}
		if in5(exact[i]) {
			srcExact++
		}
	}
	t.Logf("store: %d live, %d dead slots (no compaction below 1024); %d terms with more postings than live docs (negative idf)", live, dead, neg)
	t.Logf("drifted vs exact over %d queries: top-1 agreement %.1f%%, mean top-10 overlap %.1f%%; source note in top 5: drifted %d, exact %d",
		len(qs), 100*float64(top1)/float64(len(qs)), 100*overlap/float64(len(qs)), srcDrift, srcExact)
}

// A store with heavy revision history ranks exactly as a freshly rebuilt
// index does (ADR 029): idf uses each term's live document frequency.
func TestBM25NoDriftAfterRevisions(t *testing.T) {
	s, items := driftStore(t, 120, 16) // 960 dead slots: below the compaction threshold
	r := rand.New(rand.NewPCG(5, 6))
	var qs []string
	for range 200 {
		ws := strings.Fields(items[r.IntN(len(items))].Text)
		qs = append(qs, ws[r.IntN(len(ws)-1)]+" "+ws[r.IntN(len(ws)-1)]+" "+ws[r.IntN(len(ws)-1)])
	}
	run := func() []string {
		var out []string
		for _, q := range qs {
			var ids []string
			for _, h := range s.SearchText(q, 10) {
				ids = append(ids, h.Item.ID)
			}
			out = append(out, strings.Join(ids, ","))
		}
		return out
	}
	drifted := run()
	s.mu.Lock()
	s.ix.rebuild()
	s.mu.Unlock()
	exact := run()
	for i := range qs {
		if drifted[i] != exact[i] {
			t.Fatalf("query %q: with history %s, rebuilt %s", qs[i], drifted[i], exact[i])
		}
	}
}
