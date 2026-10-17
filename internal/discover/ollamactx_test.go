package discover

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// /doctor warns when Ollama runs a model with less context than ternly asked
// for, and stays quiet when it runs it with enough or ternly hasn't used it.
func TestOllamaContextWarnings(t *testing.T) {
	runs := 4096
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			fmt.Fprintf(w, `{"models":[{"name":"big:latest","model":"big:latest","context_length":%d},{"name":"idle:latest","model":"idle:latest","context_length":2048}]}`, runs)
		case "/api/chat":
			fmt.Fprintln(w, `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer srv.Close()
	p := &Provider{ID: "ollama", Kind: "ollama", BaseURL: srv.URL + "/v1", Local: true}
	big := &Model{Provider: p, ProvID: "ollama", ID: "big:latest", Ctx: 131072, Tools: true}
	idle := &Model{Provider: p, ProvID: "ollama", ID: "idle:latest", Ctx: 131072, Tools: true}
	ms := []*Model{big, idle}
	// ternly asks for big's context by using it (the reply's 8192 and the prompt: 16384).
	if _, _, err := llm.Collect(llm.New(p.Endpoint()).Stream(context.Background(), llm.Request{Model: big.ID, Ctx: big.Ctx,
		Messages: []llm.Message{{Role: "user", Content: "hi"}}})); err != nil {
		t.Fatal(err)
	}
	ws := OllamaContextWarnings(context.Background(), ms)
	if len(ws) != 1 || !strings.Contains(ws[0], "big:latest with 4096 tokens") || !strings.Contains(ws[0], "asked for 16384") {
		t.Fatalf("warnings: %q", ws)
	}
	runs = 16384 // Ollama honoured num_ctx
	if ws := OllamaContextWarnings(context.Background(), ms); len(ws) != 0 {
		t.Fatalf("warned with enough context: %q", ws)
	}
}
