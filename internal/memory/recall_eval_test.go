package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const reformulatePrompt = `You are a coding agent. Before answering the user, you search notes saved from earlier sessions on this software project with a tool, recall(query). It is keyword search (BM25 over words and code identifiers, plus semantic similarity). Write the query you would pass to recall: key terms, likely synonyms and identifiers, at most 15 words. Output only the query.`

const retryPrompt = `These are the top results of your query. If one answers the user's question, reply exactly "FOUND <n>". Otherwise reply with a different recall query (only the query).`

// TestModelRecall: model-driven recall on the paraphrase set. A real model
// writes the recall query for each paraphrased question; if the target isn't
// in the top 5 it sees those results and may query once more.
//
//	TERNLY_MEM_MODEL_RECALL=qwen3.6:latest,deepseek-v4-flash:cloud TERNLY_MEM_OLLAMA=http://127.0.0.1:11434 \
//	TERNLY_MEM_ENRICH=qwen3.6:latest go test -run TestModelRecall -v -timeout 2h ./internal/memory
func TestModelRecall(t *testing.T) {
	base, models := os.Getenv("TERNLY_MEM_OLLAMA"), os.Getenv("TERNLY_MEM_MODEL_RECALL")
	if base == "" || models == "" {
		t.Skip("TERNLY_MEM_MODEL_RECALL and TERNLY_MEM_OLLAMA not set")
	}
	alts := map[string]string{}
	if em := os.Getenv("TERNLY_MEM_ENRICH"); em != "" {
		b, _ := os.ReadFile(filepath.Join(os.TempDir(), "ternly-enrich-"+strings.ReplaceAll(em, ":", "_")+".json"))
		_ = json.Unmarshal(b, &alts)
	}
	m := open(t, t.TempDir())
	defer m.Close()
	m.Suspicious = nil
	ids := make([]string, len(evalFacts))
	add := func(kind, text string, keys []string) string {
		it, err := m.Add(Item{Kind: kind, Text: text, Keys: keys, Source: "model"})
		if err != nil {
			t.Fatal(err)
		}
		if a := alts[text]; a != "" {
			n := *it
			n.Alt, n.AltV = a, n.V
			m.Project.put(&n)
		}
		return it.ID
	}
	for i, f := range evalFacts {
		ids[i] = add(f.kind, f.text, f.keys)
	}
	corpus, version := recallCorpus(t)
	for _, s := range corpus {
		add("note", s, nil)
	}
	e := &Ollama{Base: base, Model: "nomic-embed-text"}
	embedAll(t, m, e)
	vec := func(q string) ([]byte, float32) {
		vs, err := e.Embed(context.Background(), []string{q}, true)
		if err != nil {
			t.Fatal(err)
		}
		return quantize(vs[0])
	}
	none := func(string) ([]byte, float32) { return nil, 0 }
	t.Logf("store: %d notes (corpus %s), enrichment %v", m.Project.Len(), version, len(alts) > 0)

	only := os.Getenv("TERNLY_MEM_RECALL_MODES") // "lexical", "vectors" or both (default)
	for _, model := range strings.Split(models, ",") {
		chat := OllamaChat(base, model)
		for _, mode := range []struct {
			name, key string
			qv        func(string) ([]byte, float32)
		}{{"lexical", "lexical", none}, {"+ vectors", "vectors", vec}} {
			if only != "" && !strings.Contains(only, mode.key) {
				continue
			}
			var first, second, found, wrongFound int
			var calls int
			t0 := time.Now()
			for i, f := range evalFacts {
				ctx := context.Background()
				q, err := chat(ctx, reformulatePrompt, "User: "+f.para)
				calls++
				if err != nil {
					t.Fatalf("%s: %v", model, err)
				}
				q = cleanQuery(q)
				v, s := mode.qv(q)
				hits := m.search(Query{Text: q, Limit: 5}, v, s)
				if rank := rankOf(hits, ids[i]); rank >= 0 {
					first++
					second++
					continue
				}
				var list strings.Builder
				for k, h := range hits {
					fmt.Fprintf(&list, "%d. %s\n", k+1, h.Item.Text)
				}
				reply, err := chat(ctx, reformulatePrompt+"\n\n"+retryPrompt, "User: "+f.para+"\n\nYour query: "+q+"\nResults:\n"+list.String())
				calls++
				if err != nil {
					t.Fatalf("%s: %v", model, err)
				}
				reply = strings.TrimSpace(reply)
				if strings.HasPrefix(reply, "FOUND") {
					wrongFound++ // the target wasn't among them
					continue
				}
				q2 := cleanQuery(reply)
				v, s = mode.qv(q2)
				if rankOf(m.search(Query{Text: q2, Limit: 5}, v, s), ids[i]) >= 0 {
					second++
					found++
				}
			}
			n := float64(len(evalFacts))
			t.Logf("| %-28s | %-9s | %.2f | %.2f | %d | %d calls, %v |", model, mode.name, float64(first)/n, float64(second)/n, wrongFound, calls, time.Since(t0).Round(time.Second))
			fmt.Printf("E2E-METRIC corpus=%s mode=%s first=%.3f within2=%.3f wrong_found=%d calls=%d\n", version, mode.key, float64(first)/n, float64(second)/n, wrongFound, calls) // read by bench/run.sh e2e
		}
	}
}

func rankOf(hits []Hit, id string) int {
	for k, h := range hits {
		if h.Item.ID == id {
			return k
		}
	}
	return -1
}

// cleanQuery strips quotes, a "recall(" wrapper and thinking residue.
func cleanQuery(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "recall("), ")")
	s = strings.Trim(s, "\"'` \n")
	if l, _, ok := strings.Cut(s, "\n"); ok {
		s = l
	}
	return s
}

// recallCorpus is the pinned distractor set for TestModelRecall: repository
// sentences snapshotted into testdata/recall_corpus.txt, so the eval doesn't
// drift as the docs change. Its version (sha256 prefix) is pinned in
// bench/e2e_checks.txt; regenerate deliberately with
// TERNLY_MEM_WRITE_CORPUS=1 go test -run TestWriteRecallCorpus ./internal/memory
// and re-derive the threshold.
func recallCorpus(t *testing.T) ([]string, string) {
	b, err := os.ReadFile(filepath.Join("testdata", "recall_corpus.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n"), hex.EncodeToString(sum[:])[:12]
}

func TestWriteRecallCorpus(t *testing.T) {
	if os.Getenv("TERNLY_MEM_WRITE_CORPUS") == "" {
		t.Skip("TERNLY_MEM_WRITE_CORPUS not set")
	}
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile(filepath.Join("testdata", "recall_corpus.txt"), []byte(strings.Join(repoSentences(t), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, v := recallCorpus(t)
	t.Logf("wrote testdata/recall_corpus.txt, version %s", v)
}
