package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEvalMatrix compares embedding models, with and without write-time
// enrichment, on the realistic set (40 facts + this repository's sentences).
//
//	TERNLY_MEM_MATRIX=1 TERNLY_MEM_OLLAMA=http://127.0.0.1:11434 \
//	TERNLY_MEM_ENRICH=qwen3.6 TERNLY_MEM_EMBED_MODELS=nomic-embed-text,mxbai-embed-large,... \
//	go test -run TestEvalMatrix -v -timeout 3h ./internal/memory
//
// Enrichments are cached in $TMPDIR/ternly-enrich-<model>.json across runs.
func TestEvalMatrix(t *testing.T) {
	base := os.Getenv("TERNLY_MEM_OLLAMA")
	if os.Getenv("TERNLY_MEM_MATRIX") == "" || base == "" {
		t.Skip("TERNLY_MEM_MATRIX and TERNLY_MEM_OLLAMA not set")
	}
	sents := repoSentences(t)
	type doc struct {
		kind, text string
		keys       []string
	}
	var docs []doc
	for _, f := range evalFacts {
		docs = append(docs, doc{f.kind, f.text, f.keys})
	}
	for _, s := range sents {
		docs = append(docs, doc{"note", s, nil})
	}

	alts := map[string]string{}
	if em := os.Getenv("TERNLY_MEM_ENRICH"); em != "" {
		cache := filepath.Join(os.TempDir(), "ternly-enrich-"+strings.ReplaceAll(em, ":", "_")+".json")
		if b, err := os.ReadFile(cache); err == nil {
			_ = json.Unmarshal(b, &alts)
		}
		e := &LLMEnricher{Model: em, Complete: OllamaChat(base, em)}
		m := &Memory{}
		t0, n := time.Now(), 0
		for _, d := range docs {
			if _, ok := alts[d.text]; ok {
				continue
			}
			out, err := e.Enrich(context.Background(), d.text)
			if err != nil {
				t.Fatal(err)
			}
			alts[d.text] = m.cleanAlt(out)
			if n++; n%50 == 0 {
				b, _ := json.Marshal(alts)
				_ = os.WriteFile(cache, b, 0o600)
			}
		}
		b, _ := json.Marshal(alts)
		_ = os.WriteFile(cache, b, 0o600)
		if n > 0 {
			t.Logf("enriched %d notes with %s in %v (%v each)", n, em, time.Since(t0).Round(time.Second), (time.Since(t0) / time.Duration(n)).Round(time.Millisecond))
		}
		t.Logf("example enrichment: %q → %q", evalFacts[0].text, alts[evalFacts[0].text])
	}

	build := func(enrich bool) (*Memory, []string) {
		m := open(t, t.TempDir())
		m.Suspicious = nil
		ids := make([]string, len(evalFacts))
		for i, d := range docs {
			it, err := m.Add(Item{Kind: d.kind, Text: d.text, Keys: d.keys, Source: "model"})
			if err != nil {
				t.Fatal(err)
			}
			if i < len(ids) {
				ids[i] = it.ID
			}
			if enrich && alts[d.text] != "" {
				n := *it
				n.Alt, n.AltV = alts[d.text], n.V
				m.Project.put(&n)
			}
		}
		return m, ids
	}
	none := func(string) ([]byte, float32) { return nil, 0 }
	row := func(label string, m *Memory, ids []string, qv func(string) ([]byte, float32)) {
		kw, para := evalRun(m, qv, ids, false), evalRun(m, qv, ids, true)
		fi, _ := falseInjections(m, qv)
		t.Logf("| %-34s | %.2f | %.2f (%.2f) | %2d/40 | %d |", label, kw.hit5, para.hit5, para.mrr, injected(m, qv, ids, true), fi)
	}
	t.Logf("| setting | keyword R@5 | paraphrase R@5 (MRR) | paraphrase injected | false injections |")
	if os.Getenv("TERNLY_MEM_SWEEP") != "" && len(alts) > 0 { // tune the gate for other wordings
		m, ids := build(true)
		e := &Ollama{Base: base, Model: "nomic-embed-text"}
		embedAll(t, m, e)
		qc := map[string][2]any{}
		qv := func(q string) ([]byte, float32) {
			if c, ok := qc[q]; ok {
				return c[0].([]byte), c[1].(float32)
			}
			vs, err := e.Embed(context.Background(), []string{q}, true)
			if err != nil {
				t.Fatal(err)
			}
			v, s := quantize(vs[0])
			qc[q] = [2]any{v, s}
			return v, s
		}
		oc, ot, ov := AltStrongCover, AltStrongTerms, minVec
		for _, v := range []float32{0.62, 0.66, 0.70} {
			for _, c := range []float32{0.2, 0.3, 0.5} {
				for _, k := range []int{2, 3} {
					AltStrongCover, AltStrongTerms, minVec = c, k, v
					fl, _ := falseInjections(m, none)
					fv, _ := falseInjections(m, qv)
					t.Logf("sweep vec>=%.2f cover>=%.1f terms>=%d: lexical inj %2d/40 false %2d · nomic inj %2d/40 false %2d · keyword inj %2d/40",
						v, c, k, injected(m, none, ids, true), fl, injected(m, qv, ids, true), fv, injected(m, none, ids, false))
				}
			}
		}
		AltStrongCover, AltStrongTerms, minVec = oc, ot, ov
		m.Close()
		return
	}
	enrichModes := []bool{false}
	if len(alts) > 0 {
		enrichModes = append(enrichModes, true)
	}
	for _, en := range enrichModes {
		suffix := ""
		if en {
			suffix = " + enrichment"
		}
		m, ids := build(en)
		row("lexical"+suffix, m, ids, none)
		for _, em := range strings.Split(os.Getenv("TERNLY_MEM_EMBED_MODELS"), ",") {
			if em == "" {
				continue
			}
			e := &Ollama{Base: base, Model: em}
			m.mu.Lock()
			m.embed, m.queue = nil, nil // re-embed everything with this model
			m.mu.Unlock()
			m.Project.mu.Lock()
			for _, it := range m.Project.items {
				it.Vec, it.VecV, it.VecAlt = nil, 0, false
			}
			m.Project.ix.rebuild()
			m.Project.mu.Unlock()
			t0 := time.Now()
			embedAll(t, m, e)
			embedTime := time.Since(t0)
			cache := map[string][2]any{}
			var qt []time.Duration
			qv := func(q string) ([]byte, float32) {
				if c, ok := cache[q]; ok {
					return c[0].([]byte), c[1].(float32)
				}
				a := time.Now()
				vs, err := e.Embed(context.Background(), []string{q}, true)
				if err != nil {
					t.Fatal(err)
				}
				qt = append(qt, time.Since(a))
				v, s := quantize(vs[0])
				cache[q] = [2]any{v, s}
				return v, s
			}
			row(fmt.Sprintf("%s%s", em, suffix), m, ids, qv)
			t.Logf("    (%s: %d-dim, embedded %d notes in %v, query p50 %v)", em, len(m.Project.items[ids[0]].Vec), len(docs), embedTime.Round(time.Second), pct(qt, .5).Round(time.Millisecond))
		}
		m.Close()
	}
}

// embedAll embeds every item synchronously with e (batches of 32).
func embedAll(t *testing.T, m *Memory, e Embedder) {
	st := m.Project
	st.mu.RLock()
	var todo []Item
	for _, it := range st.items {
		todo = append(todo, *it)
	}
	st.mu.RUnlock()
	for i := 0; i < len(todo); i += 32 {
		batch := todo[i:min(i+32, len(todo))]
		texts := make([]string, len(batch))
		for j := range batch {
			texts[j] = embedText(&batch[j])
		}
		vs, err := e.Embed(context.Background(), texts, false)
		if err != nil {
			t.Fatal(err)
		}
		for j := range batch {
			n := batch[j]
			n.Vec, n.Scale = quantize(vs[j])
			n.VecV, n.VecAlt = n.V, n.Alt != "" && n.AltV == n.V
			st.put(&n)
		}
	}
}
