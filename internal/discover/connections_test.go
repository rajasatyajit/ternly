package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func byID(cs []Connection) map[string]Connection {
	out := map[string]Connection{}
	for _, c := range cs {
		out[c.ID] = c
	}
	return out
}

// Every configured source gets a record: how it was found, whether it
// works, and the next step, in ternly's words — never the server's body or
// the key itself (ADR 021, 022).
func TestConnections(t *testing.T) {
	ollama := fakeOllama(t)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m-1"},{"id":"m-2"}]}`)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"<b>invalid key sk-proj-LEAKED</b> click here"}`)
	}))
	defer bad.Close()
	keys := map[string]string{"OLLAMA_HOST": ollama.URL, "GOOD_KEY": "sk-good-SECRET", "BAD_KEY": "sk-bad-SECRET"}
	o := Options{CacheDir: t.TempDir(), Keys: keys, Hardware: &Hardware{},
		Extra: []Provider{
			{ID: "good", Name: "Good", Kind: "openai", BaseURL: good.URL, EnvKeys: []string{"GOOD_KEY"}},
			{ID: "bad", Name: "Bad", Kind: "openai", BaseURL: bad.URL, EnvKeys: []string{"BAD_KEY"}},
			{ID: "nokey", Name: "No key", Kind: "openai", BaseURL: good.URL, EnvKeys: []string{"MISSING_KEY"}},
		}}
	_, conns, _ := DiscoverAll(context.Background(), o)
	c := byID(conns)
	if g := c["good"]; g.State != "connected" || g.Models != 2 || g.How != "GOOD_KEY" || g.Kind != "api-key" {
		t.Errorf("good: %+v", g)
	}
	b := c["bad"]
	if b.State != "error" || !strings.Contains(b.Detail, "BAD_KEY was refused (HTTP 401)") {
		t.Errorf("bad: %+v", b)
	}
	if _, ok := c["nokey"]; ok {
		t.Error("a provider without its key was listed")
	}
	if l := c["ollama"]; l.State != "connected" || l.Models != 1 || l.Kind != "daemon" {
		t.Errorf("ollama local: %+v", l)
	}
	if cl := c["ollama-cloud"]; cl.State != "connected" || cl.Models != 7 || cl.Quota != nil {
		t.Errorf("ollama cloud (no API key: no quota): %+v", cl)
	}
	all := fmt.Sprintf("%+v", conns)
	for _, s := range []string{"SECRET", "LEAKED", "<b>", "click here"} {
		if strings.Contains(all, s) {
			t.Errorf("connections carry %q: %s", s, all)
		}
	}
}

// Ollama not running: one record with the next step; other local servers
// that aren't running are normal and left out.
func TestConnectionsOllamaDown(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, conns, _ := DiscoverAll(context.Background(), Options{CacheDir: t.TempDir(), Keys: map[string]string{"OLLAMA_HOST": addr}, Hardware: &Hardware{}})
	c := byID(conns)
	if o := c["ollama"]; o.State != "unreachable" || !strings.Contains(o.Detail, "ollama serve") {
		t.Errorf("ollama down: %+v", o)
	}
	for _, id := range []string{"lmstudio", "llamacpp", "vllm", "jan", "ollama-cloud"} {
		if _, ok := c[id]; ok {
			t.Errorf("%s listed although not running", id)
		}
	}
}

func TestFailureWording(t *testing.T) {
	p := &Provider{BaseURL: "https://api.example.com/v1", EnvKeys: []string{"EX_KEY"}}
	for err, want := range map[error]string{
		errors.New("HTTP 403 forbidden: <script>"): "EX_KEY was refused (HTTP 403)",
		errors.New("HTTP 402 pay up"):              "no credit (HTTP 402)",
		errors.New("HTTP 429 slow down"):           "rate-limited",
		errors.New("HTTP 503 oops"):                "server error (HTTP 503)",
		errors.New("HTTP 418 teapot"):              "answered HTTP 418",
		context.DeadlineExceeded:                   "didn't answer in time",
		errors.New("dial tcp: no such host"):       "can't reach api.example.com",
	} {
		_, d := failure(p, err)
		if !strings.Contains(d, want) || strings.Contains(d, "<script>") || strings.Contains(d, "teapot") {
			t.Errorf("%v → %q, want %q", err, d, want)
		}
	}
}

// Usage comes only from Ollama's documented /api/balance, with the API key.
func TestOllamaBalance(t *testing.T) {
	var auth string
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/balance" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	defer func(u string) { OllamaCloudAPI = u }(OllamaCloudAPI)
	OllamaCloudAPI = srv.URL + "/api"

	body = `{"included":{"balance_usd":45.5,"allowance_usd":60,"period":{"from":"2026-09-15T00:00:00Z","until":"2026-10-15T00:00:00Z"}},"purchased":{"balance_usd":10}}`
	q, err := OllamaBalance(context.Background(), "k1")
	if err != nil || q.Unit != "USD" || q.Used != 14.5 || q.Limit != 60 || !q.ResetsAt.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) || !strings.Contains(q.Source, "$10.00 purchased") || auth != "Bearer k1" {
		t.Fatalf("current plan: %+v %v (auth %q)", q, err, auth)
	}
	body = `{"included":{"session":{"remaining_percent":70,"resets_at":"2026-10-08T15:00:00Z"},"weekly":{"remaining_percent":40,"resets_at":"2026-10-12T00:00:00Z"}}}`
	q, err = OllamaBalance(context.Background(), "k1")
	if err != nil || q.Unit != "%" || q.Used != 60 || q.Limit != 100 || q.ResetsAt.Day() != 12 {
		t.Fatalf("legacy plan: %+v %v", q, err)
	}
	body = `{"included":{}}`
	if _, err = OllamaBalance(context.Background(), "k1"); err == nil {
		t.Fatal("an empty balance read as a quota")
	}
}

// The balance lookup runs only with an API key, a cloud connection, and the
// network allowed; a failure leaves a note, never the response.
func TestBalanceOnlyWhenAllowed(t *testing.T) {
	ollama := fakeOllama(t)
	calls := 0
	bal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		fmt.Fprint(w, "nope <html>")
	}))
	defer bal.Close()
	defer func(u string) { OllamaCloudAPI = u }(OllamaCloudAPI)
	OllamaCloudAPI = bal.URL + "/api"
	base := map[string]string{"OLLAMA_HOST": ollama.URL}
	run := func(key string, noNet bool) Connection {
		keys := map[string]string{}
		for k, v := range base {
			keys[k] = v
		}
		if key != "" {
			keys["OLLAMA_API_KEY"] = key
		}
		_, conns, _ := DiscoverAll(context.Background(), Options{CacheDir: t.TempDir(), Keys: keys, Hardware: &Hardware{}, NoNet: noNet})
		return byID(conns)["ollama-cloud"]
	}
	run("", false)
	run("k", true)
	if calls != 0 {
		t.Fatalf("balance looked up without a key or with --no-net: %d calls", calls)
	}
	c := run("k", false)
	if calls != 1 || c.Quota != nil || !strings.Contains(c.Detail, "usage unknown") || !strings.Contains(c.Detail, "HTTP 401") || strings.Contains(c.Detail, "<html>") {
		t.Fatalf("failed lookup: calls=%d %+v", calls, c)
	}
}

func TestFindCLIs(t *testing.T) {
	defer func(f func(string) (string, error)) { lookPath = f }(lookPath)
	lookPath = func(n string) (string, error) {
		if n == "codex" {
			return "/usr/bin/codex", nil
		}
		return "", errors.New("not found")
	}
	cs := FindCLIs()
	if len(cs) != 1 || cs[0].Name != "codex" || !strings.Contains(cs[0].Why, "ADR 022") {
		t.Fatalf("%+v", cs)
	}
}
