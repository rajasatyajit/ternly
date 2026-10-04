// Package discover finds every usable model — paid APIs (via keys), free tiers,
// and local servers (Ollama, LM Studio, llama.cpp, vLLM, Jan) — concurrently,
// then enriches them with live pricing / context / tool-support data.
package discover

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

type Provider struct {
	ID, Name string
	Kind     string // openai | anthropic
	BaseURL  string
	EnvKeys  []string
	Local    bool
	Key      string            `json:"-"`
	Headers  map[string]string `json:"-"`
}

func (p *Provider) Endpoint() llm.Endpoint {
	return llm.Endpoint{Kind: p.Kind, BaseURL: p.BaseURL, Key: p.Key, Headers: p.Headers}
}

type Model struct {
	Provider *Provider `json:"-"`
	ProvID   string    `json:"provider"`
	ID       string    `json:"id"`
	Ctx      int       `json:"ctx"`
	In, Out  float64   `json:"-"` // USD per 1M tokens
	CacheIn  float64   `json:"-"` // USD per 1M cached input tokens
	Tier     int       `json:"tier"`
	Tools    bool      `json:"tools"`
	Priced   bool      `json:"-"`
}

func (m *Model) Key() string      { return m.ProvID + "/" + m.ID }
func (m *Model) Local() bool      { return m.Provider != nil && m.Provider.Local }
func (m *Model) Free() bool       { return m.Local() || (m.Priced && m.In == 0 && m.Out == 0) }
func (m *Model) Blended() float64 { return 0.8*m.In + 0.2*m.Out } // agents are input-heavy

// Cost of a usage record in USD.
func (m *Model) Cost(u llm.Usage) float64 {
	cache := m.CacheIn
	if cache == 0 {
		cache = m.In * 0.1
	}
	return (float64(u.In)*m.In + float64(u.CacheWrite)*m.In*1.25 + float64(u.CacheRead)*cache + float64(u.Out)*m.Out) / 1e6
}

// Builtins: every major OpenAI-compatible endpoint + Anthropic + local servers.
var Builtins = []Provider{
	{ID: "anthropic", Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com", EnvKeys: []string{"ANTHROPIC_API_KEY"}},
	{ID: "openai", Name: "OpenAI", Kind: "openai", BaseURL: "https://api.openai.com/v1", EnvKeys: []string{"OPENAI_API_KEY"}},
	{ID: "openrouter", Name: "OpenRouter", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1", EnvKeys: []string{"OPENROUTER_API_KEY"}},
	{ID: "gemini", Name: "Google Gemini", Kind: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", EnvKeys: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	{ID: "deepseek", Name: "DeepSeek", Kind: "openai", BaseURL: "https://api.deepseek.com/v1", EnvKeys: []string{"DEEPSEEK_API_KEY"}},
	{ID: "groq", Name: "Groq", Kind: "openai", BaseURL: "https://api.groq.com/openai/v1", EnvKeys: []string{"GROQ_API_KEY"}},
	{ID: "mistral", Name: "Mistral", Kind: "openai", BaseURL: "https://api.mistral.ai/v1", EnvKeys: []string{"MISTRAL_API_KEY"}},
	{ID: "xai", Name: "xAI", Kind: "openai", BaseURL: "https://api.x.ai/v1", EnvKeys: []string{"XAI_API_KEY"}},
	{ID: "together", Name: "Together", Kind: "openai", BaseURL: "https://api.together.xyz/v1", EnvKeys: []string{"TOGETHER_API_KEY"}},
	{ID: "fireworks", Name: "Fireworks", Kind: "openai", BaseURL: "https://api.fireworks.ai/inference/v1", EnvKeys: []string{"FIREWORKS_API_KEY"}},
	{ID: "cerebras", Name: "Cerebras", Kind: "openai", BaseURL: "https://api.cerebras.ai/v1", EnvKeys: []string{"CEREBRAS_API_KEY"}},
	{ID: "moonshot", Name: "Moonshot", Kind: "openai", BaseURL: "https://api.moonshot.ai/v1", EnvKeys: []string{"MOONSHOT_API_KEY"}},
	{ID: "qwen", Name: "Alibaba Qwen", Kind: "openai", BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", EnvKeys: []string{"DASHSCOPE_API_KEY"}},
	{ID: "zai", Name: "Z.ai GLM", Kind: "openai", BaseURL: "https://api.z.ai/api/paas/v4", EnvKeys: []string{"ZAI_API_KEY"}},
	{ID: "ollama", Name: "Ollama", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1", Local: true},
	{ID: "lmstudio", Name: "LM Studio", Kind: "openai", BaseURL: "http://127.0.0.1:1234/v1", Local: true},
	{ID: "llamacpp", Name: "llama.cpp", Kind: "openai", BaseURL: "http://127.0.0.1:8080/v1", Local: true},
	{ID: "vllm", Name: "vLLM", Kind: "openai", BaseURL: "http://127.0.0.1:8000/v1", Local: true},
	{ID: "jan", Name: "Jan", Kind: "openai", BaseURL: "http://127.0.0.1:1337/v1", Local: true},
}

// ─────────────────────────── keys ───────────────────────────

// LoadKeys merges ~/.config/ternly/keys.env (must be 0600) into a map; env vars win.
func LoadKeys(cfgDir string) (map[string]string, []string) {
	keys := map[string]string{}
	var warn []string
	path := filepath.Join(cfgDir, "keys.env")
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			warn = append(warn, fmt.Sprintf("%s is readable by others (mode %o) — ignored; run: chmod 600 %s", path, fi.Mode().Perm(), path))
		} else if f, err := os.Open(path); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				ln := strings.TrimSpace(sc.Text())
				if ln == "" || strings.HasPrefix(ln, "#") {
					continue
				}
				ln = strings.TrimPrefix(ln, "export ")
				if k, v, ok := strings.Cut(ln, "="); ok {
					keys[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
			f.Close()
		}
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			keys[k] = v
		}
	}
	return keys, warn
}

// ─────────────────────────── discovery ───────────────────────────

type Options struct {
	CacheDir  string
	Keys      map[string]string
	Extra     []Provider // from config
	NoLocal   bool
	LocalOnly bool
	Overrides map[string]int // model key → tier
}

var skipModel = regexp.MustCompile(`(?i)embed|whisper|tts|dall-?e|image|moderation|rerank|audio|realtime|transcri|guard|speech|vision-preview|search-preview|omni-moderation|sora|veo|imagen|lyria|aqa`)

func Discover(ctx context.Context, o Options) ([]*Model, []string) {
	provs := make([]*Provider, 0, len(Builtins)+len(o.Extra))
	for _, p := range append(append([]Provider{}, Builtins...), o.Extra...) {
		p := p
		if p.Local && o.NoLocal || !p.Local && o.LocalOnly {
			continue
		}
		if p.ID == "ollama" {
			if h := o.Keys["OLLAMA_HOST"]; h != "" {
				if !strings.HasPrefix(h, "http") {
					h = "http://" + h
				}
				p.BaseURL = strings.TrimRight(h, "/") + "/v1"
			}
		}
		if !p.Local {
			for _, k := range p.EnvKeys {
				if v := o.Keys[k]; v != "" {
					p.Key = v
					break
				}
			}
			if p.Key == "" {
				continue
			}
		}
		if p.ID == "openrouter" {
			p.Headers = map[string]string{"HTTP-Referer": "https://github.com/rajasatyajit/ternly", "X-Title": "ternly"}
		}
		provs = append(provs, &p)
	}

	var (
		mu     sync.Mutex
		models []*Model
		warn   []string
		wg     sync.WaitGroup
	)
	var cat catalog
	wg.Add(1)
	go func() { defer wg.Done(); cat = loadCatalog(ctx, o.CacheDir) }()
	for _, p := range provs {
		wg.Add(1)
		go func(p *Provider) {
			defer wg.Done()
			ms, err := listModels(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !p.Local { // local servers simply not running is normal
					warn = append(warn, fmt.Sprintf("%s: %v", p.Name, err))
				}
				return
			}
			models = append(models, ms...)
		}(p)
	}
	wg.Wait()

	out := models[:0]
	for _, m := range models {
		if skipModel.MatchString(m.ID) {
			continue
		}
		cat.enrich(m)
		m.Tier = tierOf(m)
		if t, ok := o.Overrides[m.Key()]; ok {
			m.Tier = t
		}
		if m.Ctx == 0 {
			m.Ctx = 32000
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tier != out[j].Tier {
			return out[i].Tier > out[j].Tier
		}
		return out[i].Blended() < out[j].Blended()
	})
	return out, warn
}

func listModels(ctx context.Context, p *Provider) ([]*Model, error) {
	timeout := 6 * time.Second
	if p.Local {
		timeout = 600 * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := strings.TrimRight(p.BaseURL, "/") + "/models"
	if p.Kind == "anthropic" {
		u = strings.TrimRight(p.BaseURL, "/") + "/v1/models?limit=100"
	}
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if p.Kind == "anthropic" {
		req.Header.Set("x-api-key", p.Key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	resp, err := llm.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var d struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Pricing       *struct {
				Prompt, Completion string
			} `json:"pricing"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&d); err != nil {
		return nil, err
	}
	var out []*Model
	for _, x := range d.Data {
		m := &Model{Provider: p, ProvID: p.ID, ID: strings.TrimPrefix(x.ID, "models/"), Ctx: x.ContextLength, Tools: true}
		if x.Pricing != nil { // OpenRouter gives live per-token prices
			in, e1 := strconv.ParseFloat(x.Pricing.Prompt, 64)
			outp, e2 := strconv.ParseFloat(x.Pricing.Completion, 64)
			if e1 == nil && e2 == nil && in >= 0 && outp >= 0 {
				m.In, m.Out, m.Priced = in*1e6, outp*1e6, true
			}
			m.Tools = contains(x.SupportedParameters, "tools")
		}
		out = append(out, m)
	}
	if p.ID == "ollama" {
		ollamaCaps(ctx, p, out)
	}
	return out, nil
}

// ollamaCaps asks Ollama which local models really support tool calling + their context size.
func ollamaCaps(ctx context.Context, p *Provider, ms []*Model) {
	base := strings.TrimSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1")
	var wg sync.WaitGroup
	for _, m := range ms {
		wg.Add(1)
		go func(m *Model) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			body := strings.NewReader(fmt.Sprintf(`{"model":%q}`, m.ID))
			req, _ := http.NewRequestWithContext(cctx, http.MethodPost, base+"/api/show", body)
			resp, err := llm.HTTP.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var d struct {
				Capabilities []string       `json:"capabilities"`
				ModelInfo    map[string]any `json:"model_info"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d) != nil {
				return
			}
			if len(d.Capabilities) > 0 {
				m.Tools = contains(d.Capabilities, "tools")
			}
			for k, v := range d.ModelInfo {
				if strings.HasSuffix(k, ".context_length") {
					if f, ok := v.(float64); ok {
						m.Ctx = int(f)
					}
				}
			}
		}(m)
	}
	wg.Wait()
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ─────────────────────────── pricing catalog ───────────────────────────

type catEntry struct {
	In, Out, CacheIn float64
	Ctx              int
	Tools            bool
}
type catalog map[string]catEntry

var reDate = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2}|\d{4}|latest|preview|exp)$`)

func norm(id string) string {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.TrimSuffix(id, ":free")
	for {
		n := reDate.ReplaceAllString(id, "")
		if n == id {
			break
		}
		id = n
	}
	return strings.ReplaceAll(id, ".", "-")
}

// loadCatalog uses OpenRouter's public model list (no key needed) as a live
// price/context/tool-support database, cached 24h.
func loadCatalog(ctx context.Context, cacheDir string) catalog {
	path := filepath.Join(cacheDir, "catalog.json")
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
		if b, err := os.ReadFile(path); err == nil {
			var c catalog
			if json.Unmarshal(b, &c) == nil && len(c) > 0 {
				return c
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	resp, err := llm.HTTP.Do(req)
	if err != nil {
		return stale(path)
	}
	defer resp.Body.Close()
	var d struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt, Completion string
				InputCacheRead     string `json:"input_cache_read"`
			} `json:"pricing"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&d) != nil {
		return stale(path)
	}
	c := catalog{}
	for _, x := range d.Data {
		in, _ := strconv.ParseFloat(x.Pricing.Prompt, 64)
		out, _ := strconv.ParseFloat(x.Pricing.Completion, 64)
		cr, _ := strconv.ParseFloat(x.Pricing.InputCacheRead, 64)
		if in < 0 || out < 0 || strings.HasSuffix(x.ID, ":free") {
			continue
		}
		k := norm(x.ID)
		if _, dup := c[k]; dup {
			continue
		}
		c[k] = catEntry{In: in * 1e6, Out: out * 1e6, CacheIn: cr * 1e6, Ctx: x.ContextLength, Tools: contains(x.SupportedParameters, "tools")}
	}
	if b, err := json.Marshal(c); err == nil {
		_ = os.MkdirAll(cacheDir, 0o700)
		_ = os.WriteFile(path, b, 0o600)
	}
	return c
}

func stale(path string) catalog {
	var c catalog
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (c catalog) enrich(m *Model) {
	if m.Local() {
		m.In, m.Out, m.Priced = 0, 0, true
		return
	}
	e, ok := c[norm(m.ID)]
	if !ok {
		return
	}
	if !m.Priced {
		m.In, m.Out, m.Priced = e.In, e.Out, true
		m.Tools = e.Tools
	}
	m.CacheIn = e.CacheIn
	if m.Ctx == 0 {
		m.Ctx = e.Ctx
	}
}

// ─────────────────────────── capability tiers ───────────────────────────
// Tier 3 = frontier coding models, 2 = strong/fast, 1 = small/cheap.
// Name rules go stale; price is the fallback signal, and config overrides win.

var (
	reT1      = regexp.MustCompile(`(?i)nano|lite|tiny|smol|gemma|phi-?\d|[:\-_](0\.5|1|1\.5|2|3|4|7|8|9)b\b|llama-?3\.2`)
	reT3      = regexp.MustCompile(`(?i)opus|sonnet|gpt-5(\.\d+)?($|-codex|-pro|-\d)|^o3($|-pro)|gemini-[\d.]+-pro|grok-4|grok-code|kimi-k2|qwen3-coder-(480|plus)|qwen3-max|deepseek-(v3|r1|chat|reasoner)|glm-(4\.[5-9]|5)|devstral-medium|mistral-large|codex|minimax-m2`)
	reCap2    = regexp.MustCompile(`(?i)mini|small|flash|haiku|lite|turbo|instant`)
	reT2      = regexp.MustCompile(`(?i)haiku|mini|flash|small|medium|devstral|codestral|coder|llama.*70b|gpt-4o|gpt-4\.1|grok-3|mistral|qwen|deepseek|command-r`)
	reParamsB = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)b\b`)
)

func tierOf(m *Model) int {
	id := m.ID
	switch {
	case reT1.MatchString(id):
		return 1
	case reT3.MatchString(id) && !reCap2.MatchString(id):
		return 3
	case reT2.MatchString(id):
		if m.Local() {
			if p := params(id); p > 0 && p < 25 {
				return 1
			}
		}
		return 2
	}
	if m.Local() {
		if p := params(id); p >= 25 {
			return 2
		}
		return 1
	}
	switch {
	case !m.Priced:
		return 2
	case m.Out >= 8:
		return 3
	case m.Out >= 1:
		return 2
	}
	return 1
}

func params(id string) float64 {
	if s := reParamsB.FindStringSubmatch(id); s != nil {
		f, _ := strconv.ParseFloat(s[1], 64)
		return f
	}
	return 0
}

func Price(m *Model) string {
	switch {
	case m.Local():
		return "local"
	case !m.Priced:
		return "?"
	case m.In == 0 && m.Out == 0:
		return "free"
	}
	return fmt.Sprintf("$%s/$%s", trim(m.In), trim(m.Out))
}

func trim(f float64) string {
	if f >= 10 || f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}
