package memory

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// evalFacts are true statements about this repository, each with a keyword
// query (shares its terms) and a paraphrase (avoids its distinctive words,
// as a user asking weeks later would).
var evalFacts = []struct {
	kind, text string
	keys       []string
	kw, para   string
}{
	{"convention", "Session logs fsync at most every 3 seconds while there are unsynced writes (logstore.GroupSync), plus at every turn boundary.", []string{"internal/logstore/logstore.go"}, "how often do session logs fsync", "how much of the conversation record can a power cut lose"},
	{"decision", "The sandbox masks ~/.config/ternly, ~/.cache/ternly and ~/.local/share/ternly so model-run commands cannot read keys, checkpoints or sessions.", []string{"internal/tools/security.go", "main.go"}, "which directories does the sandbox mask", "can a shell command started by the model read my API keys"},
	{"decision", "Checkpoints are one shared shadow git repository per workspace identity with per-session refs; GC keeps unreferenced objects for a grace period.", []string{"internal/checkpoint/checkpoint.go"}, "how are checkpoints stored per session", "where do undo snapshots live on disk"},
	{"convention", "The checkpoint disk cap defaults to 2048 MB (checkpoint_cap_mb); refs of the oldest sessions are pruned first.", []string{"main.go"}, "checkpoint cap default size", "what limits how much space the undo history takes"},
	{"decision", "The code graph is built with go list -export -deps inside the sandbox, then type-checked with go/types using a shared export-data importer.", []string{"internal/graph/build.go"}, "how is the code graph built", "what produces the symbol index for Go code"},
	{"note", "Graph caches live at ~/.cache/ternly/graphs as per-package gob shards plus an atomic manifest; dependency tables go under graphs/go/<module>@<version>.", []string{"internal/graph/store.go"}, "where are graph caches stored", "which folder holds the persisted symbol data"},
	{"decision", "On a first visit the graph serves an approximate syntax-only pass with name-matched references while the typed build runs off the lock.", []string{"internal/graph/build.go"}, "approximate first pass graph", "why are early answers about callers sometimes inexact"},
	{"decision", "After an incremental update changes an exported API the graph is marked inexact, and a full typed rebuild runs once queries pause for 2 minutes.", []string{"internal/graph/store.go"}, "idle full rebuild after incremental update", "when does the index get rebuilt from scratch in the background"},
	{"decision", "Graph builds set GOMEMLIMIT to half of MemAvailable; workers = (budget - 3 GB) / 64 MB, capped at GOMAXPROCS.", []string{"internal/graph/mem.go"}, "graph build memory limit workers", "how does ternly avoid running out of RAM on huge repositories"},
	{"decision", "Ollama Cloud models are detected by remote_host in /api/tags; they are not Local, not Free, priced as cloud quota, and excluded by --local-only.", []string{"internal/discover/discover.go"}, "how are ollama cloud models detected", "which hosted models behind the local endpoint count as remote"},
	{"decision", "The router ranks free local models first, then Ollama Cloud quota models (price 0.01), then pay-per-token models.", []string{"internal/discover/router.go"}, "router ranking cloud quota price", "in what order are models chosen when cost matters"},
	{"fix", "Tier matching normalises Ollama colon names (qwen3-coder:480b-cloud becomes qwen3-coder-480b), and 'mini' only matches as a name segment, not inside minimax.", []string{"internal/discover/discover.go"}, "tier matching colon names mini minimax", "why was a big model wrongly capped as small"},
	{"decision", "Escalation to a stronger model happens only after two verification failures or a no-progress loop, never upfront.", []string{"internal/agent/agent.go"}, "when does the agent escalate the model", "when do we pay for a more expensive model"},
	{"decision", "A success claim without a passing build or test this turn is challenged once; if still unbacked it is flagged to the user.", []string{"internal/agent/guard.go"}, "unbacked success claim challenged", "what happens if it says it's done but never ran the tests"},
	{"decision", "Tool output is framed as untrusted data with a per-session random nonce, and instruction-like text is flagged as possible prompt injection.", []string{"internal/tools/guard.go"}, "tool output framed untrusted nonce injection", "how do we stop file contents from hijacking the model"},
	{"convention", "Per-turn limits default to 60 steps, 30 minutes and $2; the session budget is a hard cap.", []string{"internal/agent/guard.go"}, "default per-turn limits steps time", "how long can one request run before it is stopped"},
	{"decision", "Context compaction summarises old turns with the cheapest model once history passes 55% of the window or 120k tokens.", []string{"internal/agent/agent.go"}, "context compaction threshold", "what happens when the conversation gets too long"},
	{"decision", "The system prompt must stay byte-stable for prompt caching, so per-turn notes go into the user message.", []string{"internal/agent/agent.go"}, "system prompt byte-stable caching", "why aren't memory notes put at the top of the instructions"},
	{"convention", "The most recent session in the directory auto-resumes unless --new or a headless -p prompt is used; a stopped session is not auto-resumed.", []string{"main.go", "internal/session/manager.go"}, "session auto-resume rules", "why did ternly open my previous conversation"},
	{"decision", "Each session holds an exclusive flock on its lock file; a second ternly on the same session gets ErrLocked.", []string{"internal/session/store.go"}, "session lock flock ErrLocked", "what stops two terminals from writing the same conversation"},
	{"decision", "Absolute symlinks in the workspace are rejected with an explanation and a suggestion to use a relative link.", []string{"internal/tools/tools.go"}, "absolute symlink error", "why can't the agent follow a link pointing outside the folder"},
	{"decision", "All file access goes through os.Root, so paths cannot escape the workspace even if directories are swapped during a walk.", []string{"internal/tools/tools.go"}, "os.Root file access escape", "how is reading outside the project prevented"},
	{"fix", "ripgrep results whose path looks like a secret file are dropped entirely, path included.", []string{"internal/tools/tools.go"}, "rg filter drops secret file results", "can a search leak the name of a .env file"},
	{"convention", "CI runs gofmt, vet, go test -race and a static CGO_ENABLED=0 build on Linux plus a macOS job; sandbox tests require TERNLY_REQUIRE_SANDBOX=1 there.", []string{".github/workflows/ci.yml"}, "CI jobs and TERNLY_REQUIRE_SANDBOX", "what must pass before merging"},
	{"note", "The kubernetes benchmark clone lives at ~/scratch-ternly-bench/kubernetes; run benchmarks with TMPDIR=$HOME/scratch-ternly-bench/tmp.", []string{"docs/adr/007-code-graph.md"}, "kubernetes benchmark clone location", "where is the big repository used for timing tests"},
	{"decision", "Store bake-off at 100k items: ternly's log beat bbolt (46x slower writes) and Pebble (slower fsync and reads), so memory uses the log.", []string{"docs/adr/009-memory.md"}, "store benchmark bbolt pebble", "why didn't we use an embedded database library"},
	{"fix", "A GOCACHE under /tmp is bind-mounted into the sandbox, because the sandbox replaces /tmp with a private tmpfs.", []string{"main.go", "internal/tools/security.go"}, "GOCACHE tmp bind mount sandbox", "why did go builds in the sandbox recompile everything"},
	{"decision", "The bubbletea v2 migration is deferred; it removes the 5 second startup stall in terminals that ignore status queries.", []string{"docs/backlog.md"}, "bubbletea v2 migration", "why does the UI sometimes take seconds to appear"},
	{"note", "glm-5.1:cloud returned HTTP 410 retired; dropping retired models from routing is on the backlog.", []string{"docs/backlog.md"}, "glm-5.1 retired 410", "a hosted model stopped working with a gone error"},
	{"convention", "The verification command is auto-detected from the project (go test ./... for Go) and can be changed with /verify or disabled with off.", []string{"internal/agent/agent.go"}, "verify command auto-detected", "which check runs after the agent edits code"},
	{"note", "Generics in implementations and calls through function values are known code-graph gaps, scheduled before M7.", []string{"docs/backlog.md"}, "graph gaps generics function values", "which call edges does the index miss"},
	{"convention", "Memory injects at most 600 tokens of notes per turn by default (memory_budget).", []string{"internal/memory/memory.go"}, "memory budget tokens", "how much of the prompt can past notes take"},
	{"decision", "Memory refuses secrets rather than storing them redacted, because notes are replayed into future prompts.", []string{"internal/memory/guard.go"}, "memory refuses secrets", "what if I tell it my password"},
	{"pref", "Use table-driven tests in Go packages.", nil, "table-driven tests", "how should new test cases be organised"},
	{"pref", "Prefer the standard library over new dependencies; add a library only when measurements show it clearly wins.", nil, "prefer standard library dependencies", "should I pull in a package for this"},
	{"convention", "Every milestone gets an ADR in docs/adr with measured numbers, and stops for review.", []string{"docs/adr"}, "ADR per milestone", "where are design decisions written down"},
	{"convention", "Headless -p mode never auto-resumes a session and prints the answer to stdout.", []string{"main.go"}, "headless -p mode", "running a single question from a script"},
	{"decision", "MCP servers from ./.mcp.json only start with --project-mcp, because repository config is untrusted.", []string{"main.go"}, "project mcp json untrusted", "why didn't the repo's tool servers load"},
	{"convention", "Permission modes are ask, edits and yolo; read-only tools never prompt, and dangerous shell commands always ask.", []string{"internal/tools/security.go"}, "permission modes ask edits yolo", "when will it ask before running something"},
	{"note", "Model discovery caches the catalog under ~/.cache/ternly and re-discovers with /refresh.", []string{"internal/discover/discover.go"}, "model discovery cache refresh", "a newly started local server isn't listed"},
}

type evalResult struct{ hit5, mrr float64 }

func evalRun(m *Memory, qv func(q string) ([]byte, float32), ids []string, para bool) evalResult {
	var r evalResult
	for i, f := range evalFacts {
		q := f.kw
		if para {
			q = f.para
		}
		v, s := qv(q)
		hits := m.search(Query{Text: q, Limit: 5}, v, s)
		for rank, h := range hits {
			if h.Item.ID == ids[i] {
				r.hit5++
				r.mrr += 1 / float64(rank+1)
				break
			}
		}
	}
	n := float64(len(evalFacts))
	return evalResult{r.hit5 / n, r.mrr / n}
}

// TestEval: recall@5 and MRR on the labelled set, lexical only, then with a
// real local embedding model (TERNLY_MEM_OLLAMA=http://127.0.0.1:11434), at
// 40 items and with 10,000 distractors drawn from this repository's vocabulary.
// It also compares tokens injected with tokens to rediscover by reading.
func TestEval(t *testing.T) {
	m := open(t, t.TempDir())
	defer m.Close()
	ids := make([]string, len(evalFacts))
	for i, f := range evalFacts {
		it, err := m.Add(Item{Kind: f.kind, Text: f.text, Keys: f.keys, Source: "model"})
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		ids[i] = it.ID
	}
	none := func(string) ([]byte, float32) { return nil, 0 }
	kw, para := evalRun(m, none, ids, false), evalRun(m, none, ids, true)
	t.Logf("40 items, lexical+recency:  keyword recall@5 %.2f (MRR %.2f) · paraphrase recall@5 %.2f (MRR %.2f)", kw.hit5, kw.mrr, para.hit5, para.mrr)
	if kw.hit5 < 0.9 {
		t.Errorf("keyword recall@5 %.2f < 0.90", kw.hit5)
	}

	// Tokens: the injected notes for each keyword query against reading the
	// files the fact is about (what a model without memory would open).
	var inj, reread []int
	for _, f := range evalFacts {
		hits := m.search(Query{Text: f.kw, Limit: 30}, nil, 0)
		notes, _, toks := m.format(hits, DefaultBudget)
		_ = notes
		n := 0
		for _, k := range f.keys {
			if b, err := os.ReadFile(filepath.Join("../..", k)); err == nil {
				n += EstTokens(string(b))
			}
		}
		if n > 0 {
			inj, reread = append(inj, toks), append(reread, n)
		}
	}
	sort.Ints(inj)
	sort.Ints(reread)
	sumI, sumR := 0, 0
	for i := range inj {
		sumI += inj[i]
		sumR += reread[i]
	}
	t.Logf("tokens (%d facts with files): injected median %d (max %d) vs reading the cited files median %d (total %d vs %d: %.0fx)",
		len(inj), inj[len(inj)/2], inj[len(inj)-1], reread[len(reread)/2], sumI, sumR, float64(sumR)/float64(sumI))

	base := os.Getenv("TERNLY_MEM_OLLAMA")
	var e *Ollama
	if base != "" {
		if e = FindOllama(context.Background(), base); e == nil {
			t.Fatalf("no local embedding model at %s", base)
		}
	}
	if e != nil {
		m.SetEmbedder(e)
		waitEmbedded(t, m)
		cache := map[string][]float32{}
		var embedTime []time.Duration
		qvec := func(q string) ([]float32, error) {
			if v, ok := cache[q]; ok {
				return v, nil
			}
			t0 := time.Now()
			vs, err := e.Embed(context.Background(), []string{q}, true)
			if err != nil {
				return nil, err
			}
			embedTime = append(embedTime, time.Since(t0))
			cache[q] = vs[0]
			return vs[0], nil
		}
		qv := func(q string) ([]byte, float32) {
			v, err := qvec(q)
			if err != nil {
				t.Fatal(err)
			}
			return quantize(v)
		}
		evalRun(m, qv, ids, true) // warm the query cache
		t.Logf("vectors: %s · query embedding p50 %v", e.Name(), pct(embedTime, .5))
		evalFusion(t, m, ids, qv, "40 items")
		evalRepo(t, m, ids, qv)
		if os.Getenv("TERNLY_MEM_EVAL_SOUP") != "" {
			evalScale(t, m, ids, qv, e)
		}
		return
	}
	evalRepo(t, m, ids, nil)
}

// evalFusion reports recall with each way of fusing vectors into the score.
func evalFusion(t *testing.T, m *Memory, ids []string, qv func(string) ([]byte, float32), label string) {
	old := Fusion
	defer func() { Fusion = old }()
	for _, f := range []string{"add", "norm", "rrf"} {
		Fusion = f
		kw, para := evalRun(m, qv, ids, false), evalRun(m, qv, ids, true)
		t.Logf("%s, vectors fused by %-4s: keyword recall@5 %.2f (MRR %.2f) · paraphrase recall@5 %.2f (MRR %.2f)", label, f, kw.hit5, kw.mrr, para.hit5, para.mrr)
	}
}

// repoSentences are realistic distractors: sentences from this repository's
// comments and docs (excluding the memory package and ADR 009, which state
// the facts themselves, and anything too close to a fact).
func repoSentences(t *testing.T) []string {
	var out []string
	seen := map[string]bool{}
	var facts [][]string
	for _, f := range evalFacts {
		facts = append(facts, tokens(f.text))
	}
	addPara := func(p string) {
		p = strings.Join(strings.Fields(p), " ")
		for _, sn := range strings.SplitAfter(p, ". ") {
			sn = strings.TrimSpace(sn)
			if len(sn) < 40 || len(sn) > 300 || seen[sn] || SecretReason(sn) != "" {
				continue
			}
			tk := tokens(sn)
			close := false
			for _, f := range facts {
				if jaccard(tk, f) >= 0.4 {
					close = true
					break
				}
			}
			if !close {
				seen[sn] = true
				out = append(out, sn)
			}
		}
	}
	_ = filepath.WalkDir("../..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() && (d.Name() == ".git" || d.Name() == "memory" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, "009-memory.md") || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		switch {
		case strings.HasSuffix(p, ".go"):
			var para []string
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if c, ok := strings.CutPrefix(l, "//"); ok && !strings.HasPrefix(c, "go:") {
					para = append(para, c)
					continue
				}
				if len(para) > 0 {
					addPara(strings.Join(para, " "))
					para = nil
				}
			}
		case strings.HasSuffix(p, ".md"):
			inCode := false
			for _, para := range strings.Split(string(b), "\n\n") {
				if strings.Count(para, "```")%2 == 1 {
					inCode = !inCode
					continue
				}
				if inCode || strings.HasPrefix(strings.TrimSpace(para), "|") || strings.HasPrefix(strings.TrimSpace(para), "#") {
					continue
				}
				addPara(strings.ReplaceAll(para, "\n", " "))
			}
		}
		return nil
	})
	return out
}

// unrelated prompts: nothing stored is about them.
var unrelated = []string{
	"write a haiku about autumn leaves",
	"convert this CSV of invoices to JSON",
	"what is the capital of Australia",
	"add a dark mode toggle to the settings page",
	"explain how quicksort works",
	"rename the variable cnt to count in parser.py",
	"draft an email declining the meeting",
	"why is my React component re-rendering twice",
	"set up a Python virtualenv with poetry",
	"summarise the plot of Hamlet",
}

// falseInjections counts notes (other than preferences, which are always
// injected) that Recall would inject for unrelated prompts.
func falseInjections(m *Memory, qv func(string) ([]byte, float32)) (notes, prompts int) {
	for _, p := range unrelated {
		v, s := qv(p)
		hits := m.search(Query{Text: p, Near: m.Near(p), Limit: 30}, v, s)
		_, ids, _ := m.format(hits, DefaultBudget)
		n := 0
		for _, id := range ids {
			if it, ok := m.Project.Get(id); ok && it.Kind != "pref" {
				n++
			}
		}
		notes += n
		if n > 0 {
			prompts++
		}
	}
	return
}

// injected reports for how many queries the target fact is among the notes
// Recall would inject (under the default budget).
func injected(m *Memory, qv func(string) ([]byte, float32), ids []string, para bool) int {
	n := 0
	for i, f := range evalFacts {
		q := f.kw
		if para {
			q = f.para
		}
		v, s := qv(q)
		_, got, _ := m.format(m.search(Query{Text: q, Near: m.Near(q), Limit: 30}, v, s), DefaultBudget)
		if slices.Contains(got, ids[i]) {
			n++
		}
	}
	return n
}

// evalRepo adds the repository sentences as distractors and re-runs.
func evalRepo(t *testing.T, m *Memory, ids []string, qv func(string) ([]byte, float32)) {
	sents := repoSentences(t)
	m.Suspicious = nil
	for _, sn := range sents {
		if _, err := m.Add(Item{Kind: "note", Text: sn, Source: "auto"}); err != nil {
			t.Fatal(err)
		}
	}
	label := fmt.Sprintf("+%d repo sentences", len(sents))
	none := func(string) ([]byte, float32) { return nil, 0 }
	kw, para := evalRun(m, none, ids, false), evalRun(m, none, ids, true)
	t.Logf("%s, lexical: keyword recall@5 %.2f (MRR %.2f) · paraphrase recall@5 %.2f (MRR %.2f)", label, kw.hit5, kw.mrr, para.hit5, para.mrr)
	n, p := falseInjections(m, none)
	t.Logf("%s, lexical: target injected for %d/40 keyword and %d/40 paraphrase queries; %d unrelated prompts → %d non-preference notes injected (%d prompts got any)", label, injected(m, none, ids, false), injected(m, none, ids, true), len(unrelated), n, p)
	if qv == nil {
		return
	}
	defer func() {
		n, p := falseInjections(m, qv)
		t.Logf("%s, + vectors (rrf): target injected for %d/40 keyword and %d/40 paraphrase queries; %d unrelated prompts → %d non-preference notes injected (%d prompts got any)", label, injected(m, qv, ids, false), injected(m, qv, ids, true), len(unrelated), n, p)
	}()
	waitEmbedded(t, m)
	evalFusion(t, m, ids, qv, label)
	old := VecShortlist
	VecShortlist = 1 << 30
	kwE, paraE := evalRun(m, qv, ids, false), evalRun(m, qv, ids, true)
	VecShortlist = old
	t.Logf("%s, rrf, exact int8 scan instead of the sign-bit shortlist: keyword %.2f · paraphrase %.2f", label, kwE.hit5, paraE.hit5)
}

func waitEmbedded(t *testing.T, m *Memory) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		m.Project.mu.RLock()
		left := 0
		for _, it := range m.Project.items {
			if it.Vec == nil || it.VecV != it.V {
				left++
			}
		}
		m.Project.mu.RUnlock()
		if left == 0 {
			return
		}
		m.wakeEmbed()
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("embedding did not finish")
}

// evalScale adds 10,000 distractors (and embeds them when e is set) and re-runs.
func evalScale(t *testing.T, m *Memory, ids []string, qv func(string) ([]byte, float32), e Embedder) {
	words, files := corpusWords(t)
	r := rand.New(rand.NewPCG(7, 8))
	m.Suspicious = nil
	n := 10000
	if e != nil {
		if s := os.Getenv("TERNLY_MEM_EVAL_DISTRACTORS"); s != "" {
			fmt.Sscan(s, &n)
		}
	}
	for i := range n {
		it := synthItem(r, words, files, i)
		it.Text = it.Text[:min(len(it.Text), 200)] // shorter: embedding 10k long texts is slow
		if _, err := m.Add(it); err != nil {
			t.Fatal(err)
		}
	}
	none := func(string) ([]byte, float32) { return nil, 0 }
	kw, para := evalRun(m, none, ids, false), evalRun(m, none, ids, true)
	t.Logf("+%d distractors, lexical:  keyword recall@5 %.2f (MRR %.2f) · paraphrase recall@5 %.2f (MRR %.2f)", n, kw.hit5, kw.mrr, para.hit5, para.mrr)
	if qv == nil {
		return
	}
	t0 := time.Now()
	waitEmbedded(t, m)
	t.Logf("embedded %d distractors in %v", n, time.Since(t0).Round(time.Second))
	evalFusion(t, m, ids, qv, fmt.Sprintf("+%d word-soup distractors", n))

	// Sign-bit shortlist against an exact int8 scan of every vector.
	old := VecShortlist
	VecShortlist = 1 << 30
	kwE, paraE := evalRun(m, qv, ids, false), evalRun(m, qv, ids, true)
	VecShortlist = old
	t.Logf("same, exact int8 scan instead of the sign-bit shortlist: keyword %.2f · paraphrase %.2f", kwE.hit5, paraE.hit5)
	_ = strings.TrimSpace
}
