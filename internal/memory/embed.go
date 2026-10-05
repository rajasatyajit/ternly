package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net/http"
	"strings"
	"time"
)

// Embedder turns texts into vectors. Only local models are used: memory text
// never leaves the machine for embedding.
type Embedder interface {
	Embed(ctx context.Context, texts []string, query bool) ([][]float32, error)
	Name() string
}

// Ollama embeds with a local Ollama embedding model (/api/embed).
type Ollama struct {
	Base  string // e.g. http://127.0.0.1:11434
	Model string
	HTTP  *http.Client
}

func (o *Ollama) Name() string { return "ollama/" + o.Model }

func (o *Ollama) Embed(ctx context.Context, texts []string, query bool) ([][]float32, error) {
	qp, dp := prefixes(o.Model)
	in := make([]string, len(texts))
	for i, t := range texts {
		if query {
			in[i] = qp + t
		} else {
			in[i] = dp + t
		}
	}
	body, _ := json.Marshal(map[string]any{"model": o.Model, "input": in})
	req, err := http.NewRequestWithContext(ctx, "POST", o.Base+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("embed: HTTP %d: %s", resp.StatusCode, b)
	}
	var out struct{ Embeddings [][]float32 }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

func (o *Ollama) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// prefixes are the query and document prefixes each model family was
// trained with (from their model cards); without them retrieval is worse.
func prefixes(model string) (query, doc string) {
	switch {
	case strings.Contains(model, "nomic"):
		return "search_query: ", "search_document: "
	case strings.Contains(model, "mxbai"):
		return "Represent this sentence for searching relevant passages: ", ""
	case strings.Contains(model, "qwen3-embedding"):
		return "Instruct: Given a question about a software project, retrieve notes that answer it\nQuery: ", ""
	case strings.Contains(model, "embeddinggemma"):
		return "task: search result | query: ", "title: none | text: "
	case strings.Contains(model, "snowflake-arctic"):
		return "Represent this sentence for searching relevant passages: ", ""
	}
	return "", "" // bge-m3, all-minilm: no prefixes
}

// embedPreference orders known local embedding models, best first.
var embedPreference = []string{"nomic-embed-text", "mxbai-embed-large", "snowflake-arctic-embed", "bge-m3", "bge-large", "embeddinggemma", "qwen3-embedding", "all-minilm"}

// FindOllama returns an embedder for a local (not cloud) embedding model
// served by Ollama at base, or nil if there is none.
func FindOllama(ctx context.Context, base string) *Ollama {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name       string `json:"name"`
			RemoteHost string `json:"remote_host"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return nil
	}
	best, rank := "", len(embedPreference)+1
	for _, m := range tags.Models {
		if m.RemoteHost != "" || !strings.Contains(m.Name, "embed") && !strings.Contains(m.Name, "minilm") && !strings.HasPrefix(m.Name, "bge") {
			continue
		}
		r := len(embedPreference)
		for i, p := range embedPreference {
			if strings.HasPrefix(m.Name, p) {
				r = i
				break
			}
		}
		if r < rank {
			best, rank = m.Name, r
		}
	}
	if best == "" {
		return nil
	}
	return &Ollama{Base: base, Model: best}
}

// quantize unit-normalises v and stores it as int8 with one scale, so a
// cosine is an int dot product times two scales (4× smaller than float32;
// ranking error measured in ADR 009).
func quantize(v []float32) ([]byte, float32) {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	if n == 0 {
		return nil, 0
	}
	var mx float64
	for _, x := range v {
		mx = max(mx, math.Abs(float64(x)/n))
	}
	scale := mx / 127
	out := make([]byte, len(v))
	for i, x := range v {
		out[i] = byte(int8(math.Round(float64(x) / n / scale)))
	}
	return out, float32(scale)
}

// cosine of two quantized unit vectors.
func cosine(a []byte, as float32, b []byte, bs float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot int32
	b = b[:len(a)]
	for i := range a {
		dot += int32(int8(a[i])) * int32(int8(b[i]))
	}
	return float32(dot) * as * bs
}

// signBits packs the signs of a quantized vector (1 bit per dimension). The
// Hamming distance between two of them tracks the angle between the vectors,
// so a full scan can compare bits (a few popcounts) and re-score only the
// nearest few with the int8 cosine.
func signBits(v []byte) []uint64 {
	out := make([]uint64, (len(v)+63)/64)
	for i, x := range v {
		if int8(x) > 0 {
			out[i/64] |= 1 << (i % 64)
		}
	}
	return out
}

func hamming(a, b []uint64) int {
	n := 0
	for i := range a {
		n += bits.OnesCount64(a[i] ^ b[i])
	}
	return n
}

// SetEmbedder enables vectors. New and edited items are embedded in the
// background, and items without a vector are back-filled.
func (m *Memory) SetEmbedder(e Embedder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.embed != nil || e == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.embed, m.queue, m.stop = e, make(chan struct{}, 1), cancel
	m.bg.Add(1)
	go m.embedLoop(ctx, e, m.queue)
	m.queue <- struct{}{}
}

// Embedder returns the embedding model in use (nil: none).
func (m *Memory) Embedder() Embedder { m.mu.Lock(); defer m.mu.Unlock(); return m.embed }

func (m *Memory) wakeEmbed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queue != nil && !m.closed {
		select {
		case m.queue <- struct{}{}:
		default:
		}
	}
}

func embedText(it *Item) string {
	t := it.Text
	if len(it.Keys) > 0 {
		t += " (" + strings.Join(it.Keys, ", ") + ")"
	}
	if it.Alt != "" && it.AltV == it.V {
		t += "\nAlso: " + it.Alt
	}
	return t
}

// needsVec: no vector for this version, or one made before Alt was written.
func needsVec(it *Item) bool {
	return it.Vec == nil || it.VecV != it.V || it.Alt != "" && it.AltV == it.V && !it.VecAlt
}

func (m *Memory) embedLoop(ctx context.Context, e Embedder, wake <-chan struct{}) {
	defer m.bg.Done()
	if !m.wait() {
		return
	}
	for range wake {
		for _, st := range []*Store{m.Project, m.User} {
			st.mu.RLock()
			var todo []Item
			for _, it := range st.items {
				if needsVec(it) {
					todo = append(todo, *it)
				}
			}
			st.mu.RUnlock()
			for i := 0; i < len(todo); i += 32 {
				batch := todo[i:min(i+32, len(todo))]
				texts := make([]string, len(batch))
				for j := range batch {
					texts[j] = embedText(&batch[j])
				}
				vs, err := e.Embed(ctx, texts, false)
				if err != nil {
					break // the model went away or the store closed: retried on the next wake
				}
				for j := range batch {
					n := batch[j]
					n.Vec, n.Scale = quantize(vs[j])
					n.VecV = n.V
					n.VecAlt = n.Alt != "" && n.AltV == n.V
					st.put(&n)
				}
			}
		}
	}
}
