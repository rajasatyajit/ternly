package memory

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// corpusWords is a Zipf-ish vocabulary taken from this repository's own Go
// source (identifiers and comment words), so synthetic items have realistic
// term statistics.
func corpusWords(t testing.TB) ([]string, []string) {
	root := "../.."
	freq := map[string]int{}
	var files []string
	re := regexp.MustCompile(`[A-Za-z][A-Za-z0-9]{2,}`)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".md") {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
			b, _ := os.ReadFile(p)
			for _, w := range re.FindAllString(string(b), -1) {
				if SecretReason(w) == "" { // the tests' fake keys
					freq[w]++
				}
			}
		}
		return nil
	})
	words := make([]string, 0, len(freq))
	for w := range freq {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool { return freq[words[i]] > freq[words[j]] })
	if len(words) < 1000 {
		t.Fatalf("vocabulary too small: %d", len(words))
	}
	return words, files
}

func zipf(r *rand.Rand, words []string) string {
	// P(rank k) ∝ 1/k: sample k = n^u.
	k := int(float64(len(words))*r.Float64()*r.Float64()) % len(words)
	return words[k]
}

func synthItem(r *rand.Rand, words, files []string, i int) Item {
	var b strings.Builder
	for b.Len() < 360 {
		b.WriteString(zipf(r, words))
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "#%d", i) // unique: no exact duplicates
	kinds := []string{"turn", "fix", "decision", "convention", "note"}
	scopes := []Scope{Session, Project, Project, Project, Project}
	k := r.IntN(len(kinds))
	return Item{Scope: scopes[k], Kind: kinds[k], Text: b.String(), Keys: []string{files[r.IntN(len(files))]}, Source: "auto"}
}

func pct(ds []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

// TestScale measures the store and retrieval at TERNLY_MEM_SCALE items
// (e.g. 100000): adds through the full write path (secret check, dedupe),
// reopen, point reads, and ranked retrieval with and without vectors.
func TestScale(t *testing.T) {
	n := 0
	fmt.Sscan(os.Getenv("TERNLY_MEM_SCALE"), &n)
	if n == 0 {
		t.Skip("TERNLY_MEM_SCALE not set")
	}
	words, files := corpusWords(t)
	r := rand.New(rand.NewPCG(1, 2))
	dir := t.TempDir()
	m := open(t, dir)
	m.Suspicious = nil // word soup from this repo's tests trips the injection check
	items := make([]Item, n)
	for i := range items {
		items[i] = synthItem(r, words, files, i)
	}
	t0 := time.Now()
	var addLat []time.Duration
	ids := make([]string, n)
	refused := 0
	for i := range items {
		a := time.Now()
		it, err := m.Add(items[i])
		if err != nil { // word soup can look like a secret ("Bearer" + an identifier with digits)
			refused++
			items[i] = items[i-1]
			ids[i] = ids[i-1]
			continue
		}
		addLat = append(addLat, time.Since(a))
		ids[i] = it.ID
	}
	addAll := time.Since(t0)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "project", "memory.log"))

	t0 = time.Now()
	m = open(t, dir)
	load := time.Since(t0)
	defer m.Close()
	if got := m.Project.Len(); got != n-refused {
		t.Fatalf("reloaded %d of %d", got, n)
	}

	var get []time.Duration
	for range 100000 {
		id := ids[r.IntN(n)]
		a := time.Now()
		if _, ok := m.Project.Get(id); !ok {
			t.Fatal("missing")
		}
		get = append(get, time.Since(a))
	}

	queries := make([]Query, 1000)
	for i := range queries {
		src := items[r.IntN(n)]
		ws := strings.Fields(src.Text)
		var q []string
		for range 5 {
			q = append(q, ws[r.IntN(len(ws)-1)])
		}
		queries[i] = Query{Text: strings.Join(q, " "), Near: map[string]float32{src.Keys[0]: 1}, Limit: 30}
	}
	run := func(label string, qv func(i int) ([]byte, float32)) {
		var lat []time.Duration
		var hits int
		for i, q := range queries {
			v, s := qv(i)
			a := time.Now()
			h := m.search(q, v, s)
			lat = append(lat, time.Since(a))
			hits += len(h)
		}
		t.Logf("%-44s p50 %8v  p99 %8v  (avg %.1f hits)", label, pct(lat, .5), pct(lat, .99), float64(hits)/float64(len(queries)))
	}
	t.Logf("(%d synthetic items refused as secret-like)", refused)
	t.Logf("%d items: add (full write path) total %v, p50 %v p99 %v; file %.1f MB; reopen+load %v", n, addAll.Round(time.Millisecond), pct(addLat, .5), pct(addLat, .99), float64(fi.Size())/1e6, load.Round(time.Millisecond))
	t.Logf("%-44s p50 %8v  p99 %8v", "point read (Get)", pct(get, .5), pct(get, .99))
	none := func(int) ([]byte, float32) { return nil, 0 }
	run("ranked: BM25 + structure + recency", none)

	// Vectors: synthetic 768-d vectors on every item (a real model's are
	// measured in TestEval); the query vector is random.
	vec := func() ([]byte, float32) {
		v := make([]float32, 768)
		for i := range v {
			v[i] = float32(r.NormFloat64())
		}
		return quantize(v)
	}
	m.Project.mu.Lock()
	for _, it := range m.Project.items {
		it.Vec, it.Scale = vec()
		it.VecV = it.V
	}
	m.Project.ix.rebuild() // recount vectors
	m.Project.mu.Unlock()
	qvs := make([][]byte, len(queries))
	qss := make([]float32, len(queries))
	for i := range qvs {
		qvs[i], qss[i] = vec()
	}
	withVec := func(i int) ([]byte, float32) { return qvs[i], qss[i] }
	old := VecScanMax
	VecScanMax = 0
	run("ranked + vector re-rank of candidates", withVec)
	VecScanMax = n
	run(fmt.Sprintf("ranked + full vector scan (%d vectors)", n), withVec)
	VecScanMax = old

	t0 = time.Now()
	var lat []time.Duration
	for _, q := range queries[:200] {
		a := time.Now()
		hits := m.search(q, nil, 0)
		_, _, _ = m.format(hits, DefaultBudget)
		lat = append(lat, time.Since(a))
	}
	t.Logf("%-44s p50 %8v  p99 %8v", "recall (rank + format under budget)", pct(lat, .5), pct(lat, .99))
}

func BenchmarkSearch10k(b *testing.B) {
	words, files := corpusWords(b)
	r := rand.New(rand.NewPCG(3, 4))
	m, err := Open(filepath.Join(b.TempDir(), "p"), filepath.Join(b.TempDir(), "u"))
	if err != nil {
		b.Fatal(err)
	}
	defer m.Close()
	var items []Item
	for i := range 10000 {
		it := synthItem(r, words, files, i)
		items = append(items, it)
		if _, err := m.Add(it); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := items[i%len(items)]
		ws := strings.Fields(src.Text)
		m.search(Query{Text: ws[1] + " " + ws[3] + " " + ws[5], Near: map[string]float32{src.Keys[0]: 1}}, nil, 0)
	}
}

func BenchmarkCosine768(b *testing.B) {
	v := make([]float32, 768)
	for i := range v {
		v[i] = float32(i%7) - 3
	}
	x, s := quantize(v)
	for i := 0; i < b.N; i++ {
		_ = cosine(x, s, x, s)
	}
	_ = context.Background
}
