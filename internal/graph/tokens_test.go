package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// tok estimates tokens the way the agent budgets them (~3.6 bytes/token).
func tok(s string) int { return len(s) * 10 / 36 }

// Token cost of answering structural questions with the graph tools versus
// the cheapest sensible grep + read_file sequence, on sampled real symbols.
// Set TERNLY_TOKEN_BENCH to a Go repository (graph cache is built if needed).
func TestTokenReduction(t *testing.T) {
	root := os.Getenv("TERNLY_TOKEN_BENCH")
	if root == "" {
		t.Skip("TERNLY_TOKEN_BENCH not set")
	}
	cache := os.Getenv("TERNLY_TOKEN_CACHE")
	if cache == "" {
		cache = t.TempDir()
	}
	svc := NewService(root, cache, "bench", localRun)
	svc.Start(context.Background())
	waitBuilt(t, svc) // measure the typed graph, not the approximate first pass
	g, _, err := svc.Graph(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(root, tools.NewPolicy("yolo", nil), tools.NewSandbox(false, false, nil), tools.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range Tools(svc) {
		reg.Add(tl)
	}
	call := func(name string, args map[string]any) string {
		b, _ := json.Marshal(args)
		return reg.Call(context.Background(), llm.ToolCall{ID: "x", Name: name, Args: string(b)}).Out
	}
	reLine := regexp.MustCompile(`(?m)^([^:\n]+):(\d+):`)
	// readAround reads 30 lines around the first n grep hits (to learn the enclosing function).
	readAround := func(grepOut string, n int) (out string) {
		for _, m := range reLine.FindAllStringSubmatch(grepOut, n) {
			var line int
			fmt.Sscan(m[2], &line)
			out += call("read_file", map[string]any{"path": m[1], "offset": max(1, line-15), "limit": 30})
		}
		return out
	}

	// sample: exported, non-test functions/methods with 3+ callers; interfaces with implementations
	var funcs, ifaces []*Symbol
	g.mu.RLock()
	for _, s := range g.byID {
		if s.Test || !token.IsExported(s.Name) {
			continue
		}
		switch {
		case (s.Kind == KFunc || s.Kind == KMethod) && countCalls(g.refsTo[s.ID]) >= 3:
			funcs = append(funcs, s)
		case s.Kind == KInterface && len(s.Methods) > 0:
			ifaces = append(ifaces, s)
		}
	}
	g.mu.RUnlock()
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].ID < funcs[j].ID })
	sort.Slice(ifaces, func(i, j int) bool { return ifaces[i].ID < ifaces[j].ID })
	rng := rand.New(rand.NewSource(1))
	rng.Shuffle(len(funcs), func(i, j int) { funcs[i], funcs[j] = funcs[j], funcs[i] })
	rng.Shuffle(len(ifaces), func(i, j int) { ifaces[i], ifaces[j] = ifaces[j], ifaces[i] })
	n := 20

	type tally struct{ grep, graph int }
	res := map[string]*tally{"definition": {}, "callers": {}, "implementations": {}}
	for _, s := range funcs[:min(n, len(funcs))] {
		def := call("grep", map[string]any{"pattern": `func (\([^)]*\) )?` + s.Name + `[\[(]`})
		res["definition"].grep += tok(def) + tok(readAround(def, 1))
		res["definition"].graph += tok(call("find_symbol", map[string]any{"query": s.Name, "limit": 10}))

		uses := call("grep", map[string]any{"pattern": `\b` + s.Name + `\(`})
		res["callers"].grep += tok(uses) + tok(readAround(uses, 5))
		res["callers"].graph += tok(call("callers", map[string]any{"symbol": s.ID}))
	}
	found := 0
	for _, s := range ifaces {
		if found == n {
			break
		}
		if len(g.Implementations(s.ID)) == 0 {
			continue
		}
		found++
		m := strings.SplitN(s.Methods[0], "(", 2)[0]
		hits := call("grep", map[string]any{"pattern": `func \([^)]*\) ` + m + `\(`})
		res["implementations"].grep += tok(hits) + tok(readAround(hits, 5))
		res["implementations"].graph += tok(call("implementations", map[string]any{"symbol": s.ID}))
	}
	for _, q := range []string{"definition", "callers", "implementations"} {
		r := res[q]
		samples := min(n, len(funcs))
		if q == "implementations" {
			samples = found
		}
		if samples == 0 {
			continue
		}
		t.Logf("%-16s %2d questions: grep+read %6d tokens (%5d avg), graph %6d tokens (%4d avg) → %.1f× fewer",
			q, samples, r.grep, r.grep/samples, r.graph, r.graph/samples, float64(r.grep)/float64(max(1, r.graph)))
	}
}

func countCalls(rs []*Ref) (n int) {
	for _, r := range rs {
		if r.Call {
			n++
		}
	}
	return n
}
