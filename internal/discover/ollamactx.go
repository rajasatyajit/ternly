package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// OllamaContextWarnings compares, for each local Ollama model ternly has used
// in this process, the context Ollama runs it with (/api/ps) with the context
// ternly asked for (num_ctx), for /doctor. Fewer tokens than asked means the
// window routing assumed for the model doesn't hold, and Ollama drops what
// doesn't fit without a word. A model ternly hasn't used yet isn't reported:
// its first request asks for what it needs.
func OllamaContextWarnings(ctx context.Context, models []*Model) []string {
	byBase := map[string][]*Model{}
	for _, m := range models {
		if m.Provider == nil || m.Provider.Kind != "ollama" || m.Cloud {
			continue
		}
		base := strings.TrimSuffix(strings.TrimRight(m.Provider.BaseURL, "/"), "/v1")
		byBase[base] = append(byBase[base], m)
	}
	var out []string
	for base, ms := range byBase {
		loaded := ollamaLoaded(ctx, base)
		for _, m := range ms {
			asked := llm.OllamaAsked(base, m.ID)
			runs, ok := loaded[m.ID]
			if asked == 0 || !ok || runs >= asked {
				continue
			}
			out = append(out, fmt.Sprintf("Ollama runs %s with %d tokens of context; ternly asked for %d (routing assumes up to %d), so Ollama drops what doesn't fit. Check OLLAMA_CONTEXT_LENGTH and the server's memory, or use a smaller model.", m.ID, runs, asked, m.Ctx))
		}
	}
	sort.Strings(out)
	return out
}

// ollamaLoaded maps each model Ollama at base has loaded to its context size.
func ollamaLoaded(ctx context.Context, base string) map[string]int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := map[string]int{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/ps", nil)
	resp, err := llm.HTTP.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var ps llm.OllamaPS
	if json.NewDecoder(resp.Body).Decode(&ps) != nil {
		return out
	}
	for _, m := range ps.Models {
		out[m.Name] = m.ContextLength
		out[m.Model] = m.ContextLength
	}
	return out
}
