package memory

import (
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// index is a BM25 inverted index over item text and keys, plus a key index
// for structural matching. Removal is lazy (slots go nil) with a rebuild once
// half the slots are dead.
type index struct {
	docs  []*Item
	slot  map[*Item]uint32
	dl    []uint16 // document length in tokens, by slot
	post  map[string][]posting
	keys  map[string][]uint32 // normalised key (and "~"+its last segment) → slots
	live  int
	total int               // sum of live document lengths
	nvec  int               // live documents with a current vector
	sign  []uint64          // sign bits of each slot's vector, stride words (derived, for the full scan)
	words int               // uint64s per vector in sign (0 until the first vector)
	hasV  []bool            // slot has a current vector
	tf    map[string]uint16 // scratch for add
}

type posting struct {
	slot uint32
	tf   uint16
}

func newIndex() *index {
	return &index{post: map[string][]posting{}, keys: map[string][]uint32{}, slot: map[*Item]uint32{}}
}

// nearest returns about k slots whose sign bits are closest to q's.
func (x *index) nearest(q []uint64, k int) []uint32 {
	if x.nvec == 0 || len(q) != x.words {
		return nil
	}
	var hist [1025]int
	ds := make([]uint16, len(x.docs)) // hamming+1; 0: no vector
	w := x.words
	for s := range x.docs {
		if !x.hasV[s] || (s+1)*w > len(x.sign) {
			continue
		}
		d := min(hamming(q, x.sign[s*w:(s+1)*w]), 1024)
		ds[s] = uint16(d + 1)
		hist[d]++
	}
	cut, n := 0, 0
	for ; cut < 1024 && n+hist[cut] < k; cut++ {
		n += hist[cut]
	}
	out := make([]uint32, 0, k+hist[cut])
	for s, d := range ds {
		if d != 0 && int(d-1) <= cut {
			out = append(out, uint32(s))
		}
	}
	return out
}

// docTokens are an item's indexed terms: its text, then its keys.
func docTokens(it *Item) []string {
	ts := tokens(it.Text)
	for _, k := range it.Keys {
		ts = append(ts, tokens(k)...)
	}
	if it.Alt != "" && it.AltV == it.V {
		for _, t := range tokens(it.Alt) {
			ts = append(ts, altPrefix+t)
		}
	}
	return ts
}

func (x *index) add(it *Item, toks []string) {
	if x == nil {
		return
	}
	slot := uint32(len(x.docs))
	x.slot[it] = slot
	x.docs = append(x.docs, it)
	if x.tf == nil {
		x.tf = map[string]uint16{}
	}
	tf := x.tf
	clear(tf)
	if toks == nil {
		toks = docTokens(it)
	}
	for _, t := range toks {
		tf[t]++
	}
	n := len(toks)
	for _, k := range it.Keys {
		for _, nk := range keyForms(k) {
			x.keys[nk] = append(x.keys[nk], slot)
		}
	}
	for t, c := range tf {
		x.post[t] = append(x.post[t], posting{slot, c})
	}
	if it.Kind == "pref" {
		x.keys["kind:pref"] = append(x.keys["kind:pref"], slot)
	}
	x.dl = append(x.dl, uint16(min(n, math.MaxUint16)))
	x.live++
	x.hasV = append(x.hasV, false)
	if it.Vec != nil && it.VecV == it.V {
		b := signBits(it.Vec)
		if x.words == 0 {
			x.words = len(b)
		}
		if len(b) == x.words { // a different model's dimension is ignored until re-embedded
			x.nvec++
			x.hasV[slot] = true
			x.sign = append(x.sign, make([]uint64, int(slot)*x.words-len(x.sign))...)
			x.sign = append(x.sign, b...)
		}
	}
	x.total += n
}

func (x *index) remove(it *Item) {
	if x == nil {
		return
	}
	slot, ok := x.slot[it]
	if !ok {
		return
	}
	delete(x.slot, it)
	x.docs[slot] = nil
	x.live--
	if x.hasV[slot] {
		x.nvec--
		x.hasV[slot] = false
	}
	x.total -= int(x.dl[slot])
	if dead := len(x.docs) - x.live; dead > 1024 && dead > x.live {
		x.rebuild()
	}
}

func (x *index) rebuild() {
	old := x.docs
	*x = *newIndex()
	for _, it := range old {
		if it != nil {
			x.add(it, nil)
		}
	}
}

// keyForms are the index keys of a file or symbol key: the whole key and its
// last segment ("internal/agent/agent.go" → also "~agent.go"; "Agent.setModel"
// → also "~setmodel").
func keyForms(k string) []string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return nil
	}
	out := []string{k}
	tail := k
	if isFile(k) {
		tail = k[strings.LastIndexByte(k, '/')+1:]
	} else if i := strings.LastIndexByte(k, '.'); i >= 0 {
		tail = k[i+1:]
	}
	if tail != k && tail != "" {
		out = append(out, "~"+tail)
	}
	return out
}

// isFile tells a file key from a symbol key (pkg.Func, Type.Method).
func isFile(k string) bool {
	if strings.ContainsRune(k, '/') {
		return true
	}
	switch strings.ToLower(k[strings.LastIndexByte(k, '.')+1:]) {
	case "go", "py", "ts", "tsx", "js", "jsx", "rs", "md", "json", "yaml", "yml", "toml", "c", "h", "cc", "cpp", "java", "kt", "rb", "sh", "mod", "sum", "txt", "lock", "sql", "proto":
		return strings.ContainsRune(k, '.')
	}
	return false
}

// rarest returns up to k of q's terms with the fewest postings (a duplicate
// shares its rare words; common ones only cost time).
func (x *index) rarest(q []string, k int) []string {
	if len(q) <= k {
		return q
	}
	r := append([]string(nil), q...)
	sort.SliceStable(r, func(i, j int) bool { return len(x.post[r[i]]) < len(x.post[r[j]]) })
	return r[:k]
}

// scratch holds per-query accumulators (pooled: queries run concurrently).
type scratch struct {
	score     []float32
	orig, alt []uint64 // query terms matched in the text / only in other wordings (bit per term)
	mark      []uint32 // mark[slot] == epoch: slot touched by this query
	epoch     uint32
	touched   []uint32
}

var scratchPool = sync.Pool{New: func() any { return &scratch{} }}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// lexical scores documents for query terms q: BM25, and coverage (the share
// of the query's IDF mass the document contains). Terms are taken rarest
// first. Once the terms left can't lift an unseen document to gate coverage
// on their own, they only update documents already seen (and the seeds),
// found by binary search when that beats a scan: the long posting lists of
// common words are rarely walked. visit sees every touched document.
func (x *index) lexical(q []string, seeds []uint32, gate float32, visit func(slot uint32, bm25, cover float32, nterm, nalt int)) {
	if x.live == 0 {
		return
	}
	if len(q) > 64 {
		q = q[:64]
	}
	sc := scratchPool.Get().(*scratch)
	defer scratchPool.Put(sc)
	if n := len(x.docs); len(sc.score) < n {
		sc.score, sc.orig, sc.alt, sc.mark, sc.epoch = make([]float32, n+n/4), make([]uint64, n+n/4), make([]uint64, n+n/4), make([]uint32, n+n/4), 0
	}
	sc.epoch++
	sc.touched = sc.touched[:0]
	touch := func(s uint32) {
		if sc.mark[s] != sc.epoch {
			sc.mark[s] = sc.epoch
			sc.score[s], sc.orig[s], sc.alt[s] = 0, 0, 0
			sc.touched = append(sc.touched, s)
		}
	}
	for _, s := range seeds {
		if int(s) < len(x.docs) && x.docs[s] != nil {
			touch(s)
		}
	}
	N := float64(x.live)
	avg := float64(x.total) / N
	type term struct {
		post  []posting
		idf   float64
		share float32 // upper bound of what this term adds to coverage
		bit   uint64  // the query term's bit
		alt   bool    // matches in other wordings (Alt), weighted down
	}
	ts := make([]term, 0, 2*len(q))
	shares := make([]float32, len(q))
	var mass float64
	for _, t := range q {
		df := float64(len(x.post[t]))
		mass += math.Log(1 + (N-max(df, 1)+0.5)/(max(df, 1)+0.5)) // an unseen word weighs as a rare one, not more
	}
	idf := func(df float64) float64 { return math.Log(1 + (N-df+0.5)/(df+0.5)) }
	for i, t := range q {
		p := x.post[t]
		shares[i] = float32(idf(max(float64(len(p)), 1)) / mass)
		if len(p) > 0 {
			ts = append(ts, term{post: p, idf: idf(float64(len(p))), share: shares[i], bit: 1 << i})
		}
		if pa := x.post[altPrefix+t]; len(pa) > 0 {
			ts = append(ts, term{post: pa, idf: altWeight * idf(float64(len(pa))), share: altWeight * shares[i], bit: 1 << i, alt: true})
		}
	}
	sort.Slice(ts, func(i, j int) bool { return len(ts[i].post) < len(ts[j].post) })
	var remaining float32
	for i := range ts {
		remaining += ts[i].share
	}
	add := func(t *term, p posting) {
		tf := float64(p.tf)
		sc.score[p.slot] += float32(t.idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(x.dl[p.slot])/avg)))
		if t.alt {
			sc.alt[p.slot] |= t.bit
		} else {
			sc.orig[p.slot] |= t.bit
		}
	}
	for i := range ts {
		t := &ts[i]
		switch {
		case remaining >= gate: // an unseen document could still qualify: scan, adding
			for _, p := range t.post {
				if x.docs[p.slot] != nil {
					touch(p.slot)
					add(t, p)
				}
			}
		case len(sc.touched)*bitsLen(len(t.post)) < len(t.post): // probe the few seen
			for _, s := range sc.touched {
				j := sort.Search(len(t.post), func(k int) bool { return t.post[k].slot >= s })
				if j < len(t.post) && t.post[j].slot == s {
					add(t, t.post[j])
				}
			}
		default: // scan, updating only the seen
			for _, p := range t.post {
				if sc.mark[p.slot] == sc.epoch {
					add(t, p)
				}
			}
		}
		remaining -= t.share
	}
	for _, s := range sc.touched {
		o, a := sc.orig[s], sc.alt[s]&^sc.orig[s]
		var cover float32
		for i := range q {
			switch {
			case o&(1<<i) != 0:
				cover += shares[i]
			case a&(1<<i) != 0:
				cover += altWeight * shares[i]
			}
		}
		visit(s, sc.score[s], cover, bits.OnesCount64(o), bits.OnesCount64(a))
	}
}

// Other wordings (Item.Alt) are indexed as altPrefix+term and count
// altWeight of a match in the item's own text.
const (
	altPrefix = "\x01"
	altWeight = 0.6
)

func bitsLen(n int) int {
	b := 1
	for ; n > 1; n >>= 1 {
		b++
	}
	return b
}

// ─────────────────────────── tokenizer ───────────────────────────

// tokens lowercases, splits code identifiers (parseHTTPRequest → parse, http,
// request; the whole identifier is kept too), drops stopwords and applies a
// light suffix stemmer so "commits", "committed" and "commit" meet. ASCII
// words take an allocation-light path (tokens are substrings when already
// lower case).
func tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf && !wordByte(c) {
			i++
			continue
		}
		j, ascii := i, true
		for j < len(s) {
			c := s[j]
			if c >= utf8.RuneSelf {
				r, n := utf8.DecodeRuneInString(s[j:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				ascii = false
				j += n
				continue
			}
			if !wordByte(c) {
				break
			}
			j++
		}
		if j == i { // a non-word rune
			_, n := utf8.DecodeRuneInString(s[i:])
			i += n
			continue
		}
		if ascii {
			out = asciiWord(out, s[i:j])
		} else {
			out = unicodeWord(out, s[i:j])
		}
		i = j
	}
	return out
}

func wordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

func isUp(c byte) bool  { return 'A' <= c && c <= 'Z' }
func isLow(c byte) bool { return 'a' <= c && c <= 'z' }
func isDig(c byte) bool { return '0' <= c && c <= '9' }

func lowerASCII(w string) string {
	for i := 0; i < len(w); i++ {
		if isUp(w[i]) {
			return strings.ToLower(w)
		}
	}
	return w
}

func asciiWord(out []string, w string) []string {
	parts, b := 0, 0
	emit := func(p string) {
		if strings.Trim(p, "_") == "" {
			return
		}
		parts++
		if len(p) < 2 {
			return
		}
		p = lowerASCII(p)
		if !stop[p] {
			out = append(out, stem(p))
		}
	}
	first := len(out)
	for i := 1; i <= len(w); i++ {
		if i == len(w) {
			if i > b {
				emit(w[b:i])
			}
			break
		}
		p, c := w[i-1], w[i]
		if c == '_' {
			if i > b {
				emit(w[b:i])
			}
			b = i + 1
			continue
		}
		if p == '_' {
			continue
		}
		if isLow(p) && isUp(c) || isUp(p) && isUp(c) && i+1 < len(w) && isLow(w[i+1]) || isDig(p) != isDig(c) {
			emit(w[b:i])
			b = i
		}
	}
	if parts > 1 && len(w) >= 2 { // the whole identifier too, first
		out = append(out, "")
		copy(out[first+1:], out[first:])
		out[first] = lowerASCII(w)
	}
	return out
}

func unicodeWord(out []string, w string) []string {
	parts := splitIdent(w)
	if len(parts) > 1 {
		if t := strings.ToLower(w); len(t) >= 2 {
			out = append(out, t)
		}
	}
	for _, p := range parts {
		p = strings.ToLower(p)
		if len([]rune(p)) < 2 || stop[p] {
			continue
		}
		out = append(out, stem(p))
	}
	return out
}

// splitIdent splits on underscores, lower→Upper, Upper→Upper+lower and letter↔digit.
func splitIdent(w string) []string {
	rs := []rune(w)
	var parts []string
	b := 0
	for i := 1; i <= len(rs); i++ {
		if i == len(rs) {
			parts = append(parts, string(rs[b:i]))
			break
		}
		p, c := rs[i-1], rs[i]
		split := c == '_' ||
			unicode.IsLower(p) && unicode.IsUpper(c) ||
			unicode.IsUpper(p) && unicode.IsUpper(c) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) ||
			unicode.IsLetter(p) != unicode.IsLetter(c) && (unicode.IsDigit(p) || unicode.IsDigit(c))
		if p == '_' {
			b = i
			continue
		}
		if split {
			if i > b {
				parts = append(parts, string(rs[b:i]))
			}
			b = i
			if c == '_' {
				b = i + 1
			}
		}
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" && p != "_" {
			out = append(out, p)
		}
	}
	return out
}

func stem(w string) string {
	n := len(w)
	switch {
	case n > 4 && strings.HasSuffix(w, "ies"):
		return w[:n-3] + "y"
	case n > 5 && strings.HasSuffix(w, "ing"):
		return undouble(w[:n-3])
	case n > 4 && strings.HasSuffix(w, "ed"):
		return undouble(w[:n-2])
	case n > 4 && (strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "sses")):
		return w[:n-2]
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:n-1]
	}
	return w
}

func undouble(w string) string {
	if n := len(w); n >= 3 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiouls", rune(w[n-1])) {
		return w[:n-1]
	}
	return w
}

var stop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an the and or but of to in on for with is are was were be been being it its this that these those as at by from
		how what why when where which who whom do does did done we i you he she they me my our your their us them can could should would will shall may might must
		not no if then else so there here into about up out over under than too very just also only any all some such each other more most own same both
		have has had having am get got let lets please`) {
		m[w] = true
	}
	return m
}()
