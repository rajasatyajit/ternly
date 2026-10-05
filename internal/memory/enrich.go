package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Enricher writes other wordings of a note (synonyms, paraphrases), once
// per note version. They are indexed and embedded so a question phrased
// differently still finds the note; they are never injected.
type Enricher interface {
	Enrich(ctx context.Context, text string) (string, error)
	Name() string
}

// EnrichPrompt is the instruction given to the enriching model.
const EnrichPrompt = `You index notes about a software project for search. Given one note, write 12 to 20 other words and short phrases a developer might use when asking about it later: synonyms, everyday paraphrases, the question it answers, related concepts. Do not repeat the note. Output one line, comma-separated, nothing else.`

// LLMEnricher enriches with any chat completion function.
type LLMEnricher struct {
	Model    string
	Complete func(ctx context.Context, system, user string) (string, error)
}

func (e *LLMEnricher) Name() string { return e.Model }
func (e *LLMEnricher) Enrich(ctx context.Context, text string) (string, error) {
	return e.Complete(ctx, EnrichPrompt, text)
}

// OllamaChat completes with a model served by Ollama (/api/chat).
func OllamaChat(base, model string) func(ctx context.Context, system, user string) (string, error) {
	return func(ctx context.Context, system, user string) (string, error) {
		body, _ := json.Marshal(map[string]any{"model": model, "stream": false, "think": false,
			"options":  map[string]any{"temperature": 0.2, "num_predict": 160},
			"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/chat", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return "", fmt.Errorf("chat: HTTP %d: %s", resp.StatusCode, b)
		}
		var out struct{ Message struct{ Content string } }
		err = json.NewDecoder(resp.Body).Decode(&out)
		return out.Message.Content, err
	}
}

// cleanAlt keeps the first line of an enrichment, bounded; it is dropped
// if it looks like a secret or instructions (it is indexed, not injected,
// but it is still stored).
func (m *Memory) cleanAlt(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	s = clean(strings.Trim(s, "\"'`"), 400)
	if SecretReason(s) != "" || m.Suspicious != nil && m.Suspicious(s) {
		return ""
	}
	return s
}

// SetEnricher enables write-time enrichment in the background.
func (m *Memory) SetEnricher(e Enricher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.enrich != nil || e == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.enrich, m.enrichQ, m.enrichStop = e, make(chan struct{}, 1), cancel
	m.bg.Add(1)
	go m.enrichLoop(ctx, e, m.enrichQ)
	m.enrichQ <- struct{}{}
}

func (m *Memory) wakeEnrich() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enrichQ != nil && !m.closed {
		select {
		case m.enrichQ <- struct{}{}:
		default:
		}
	}
}

func (m *Memory) enrichLoop(ctx context.Context, e Enricher, wake <-chan struct{}) {
	defer m.bg.Done()
	if !m.wait() {
		return
	}
	for range wake {
		for _, st := range []*Store{m.Project, m.User} {
			st.mu.RLock()
			var todo []Item
			for _, it := range st.items {
				if it.AltV != it.V && it.Kind != "turn" { // turn summaries are many and short-lived
					todo = append(todo, *it)
				}
			}
			st.mu.RUnlock()
			for _, it := range todo {
				out, err := e.Enrich(ctx, it.Text)
				if err != nil {
					break // model gone or store closing: retried on the next wake
				}
				it.Alt, it.AltV = m.cleanAlt(out), it.V
				st.put(&it)
			}
		}
		m.wakeEmbed() // re-embed with the new wordings
	}
}
