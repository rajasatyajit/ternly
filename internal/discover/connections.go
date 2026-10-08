package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// Connection is one source of models as discovery found it (ADR 021, 022):
// how ternly reaches it, whether it works, and the next step if not. Detail
// is ternly's own wording, never a provider's error body (untrusted text),
// and nothing here is a secret: the env var's name, never its value.
type Connection struct {
	ID     string
	Label  string
	Kind   string // "daemon", "local-server", "api-key"
	How    string
	State  string // "connected", "unreachable", "error"
	Detail string
	Models int
	Quota  *Quota
}

// Quota is a usage limit from a provider's official signal.
type Quota struct {
	Used, Limit float64
	Unit        string // "USD", "%"
	ResetsAt    time.Time
	Source      string
}

var reHTTPStatus = regexp.MustCompile(`^HTTP (\d{3})\b`)

// failure describes a listing error for a person, without echoing what the
// server sent: a status class, or that the host can't be reached.
func failure(p *Provider, err error) (state, detail string) {
	host := p.BaseURL
	if u, e := url.Parse(p.BaseURL); e == nil && u.Host != "" {
		host = u.Host
	}
	if m := reHTTPStatus.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		key := strings.Join(p.EnvKeys, " or ")
		switch {
		case code == 401 || code == 403:
			return "error", fmt.Sprintf("the key in %s was refused (HTTP %d): check it, or create a new one", key, code)
		case code == 402:
			return "error", fmt.Sprintf("the account behind %s has no credit (HTTP 402)", key)
		case code == 429:
			return "error", "rate-limited right now (HTTP 429): it will be retried at the next start"
		case code >= 500:
			return "error", fmt.Sprintf("%s answered with a server error (HTTP %d): try again later", host, code)
		}
		return "error", fmt.Sprintf("%s answered HTTP %d to the model listing", host, code)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "unreachable", fmt.Sprintf("%s didn't answer in time", host)
	}
	if p.Local {
		return "unreachable", fmt.Sprintf("nothing is listening at %s", host)
	}
	return "unreachable", fmt.Sprintf("can't reach %s: check the network", host)
}

// connections turns per-provider listing results into Connection records.
// Local servers that aren't running are left out, except Ollama, the one
// ternly expects: it gets a next step.
func connections(provs []*Provider, results map[*Provider]error, models []*Model, keys map[string]string) []Connection {
	count := map[string]int{}
	cloud := 0
	for _, m := range models {
		if m.ProvID == "ollama" && m.Cloud {
			cloud++
			continue
		}
		count[m.ProvID]++
	}
	var out []Connection
	for _, p := range provs {
		c := Connection{ID: p.ID, Label: p.Name, Models: count[p.ID], State: "connected"}
		switch {
		case p.ID == "ollama":
			c.Label, c.Kind, c.How = "Ollama (local)", "daemon", "the Ollama daemon at "+hostOf(p.BaseURL)
		case p.Local:
			c.Label, c.Kind, c.How = p.Name+" (local)", "local-server", "a local server at "+hostOf(p.BaseURL)
		default:
			c.Kind = "api-key"
			for _, k := range p.EnvKeys {
				if keys[k] != "" {
					c.How = k
					break
				}
			}
		}
		if err := results[p]; err != nil {
			if p.Local && p.ID != "ollama" {
				continue
			}
			c.State, c.Detail = failure(p, err)
			if p.ID == "ollama" && c.State == "unreachable" {
				c.Detail += ": start it with `ollama serve` (or set OLLAMA_HOST)"
			}
		}
		out = append(out, c)
		if p.ID == "ollama" && cloud > 0 {
			out = append(out, Connection{ID: "ollama-cloud", Label: "Ollama Cloud", Kind: "daemon",
				How: "the signed-in Ollama daemon (`ollama signin`)", State: "connected", Models: cloud})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].State == "connected" && out[j].State != "connected" })
	return out
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// OllamaCloudAPI is ollama.com's API (a variable for tests).
var OllamaCloudAPI = "https://ollama.com/api"

// OllamaBalance reads the account's remaining usage from Ollama's documented
// endpoint (GET /api/balance with an API key; ADR 022). Current plans report
// a monthly dollar allowance; legacy plans session and weekly percentages,
// of which the tighter is returned. Only with OLLAMA_API_KEY: the signed-in
// daemon has no documented usage signal, and ternly scrapes nothing.
func OllamaBalance(ctx context.Context, key string) (*Quota, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, OllamaCloudAPI+"/balance", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := llm.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	type limit struct {
		Remaining *float64  `json:"remaining_percent"`
		ResetsAt  time.Time `json:"resets_at"`
	}
	var d struct {
		Included struct {
			Balance   *float64 `json:"balance_usd"`
			Allowance *float64 `json:"allowance_usd"`
			Period    struct {
				Until time.Time `json:"until"`
			} `json:"period"`
			Session *limit `json:"session"`
			Weekly  *limit `json:"weekly"`
		} `json:"included"`
		Purchased struct {
			Balance *float64 `json:"balance_usd"`
		} `json:"purchased"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&d); err != nil {
		return nil, err
	}
	in := d.Included
	if in.Balance != nil && in.Allowance != nil {
		q := &Quota{Used: *in.Allowance - *in.Balance, Limit: *in.Allowance, Unit: "USD", ResetsAt: in.Period.Until, Source: "ollama.com/api/balance (included allowance)"}
		if b := d.Purchased.Balance; b != nil && *b > 0 {
			q.Source += fmt.Sprintf("; $%.2f purchased credit besides", *b)
		}
		return q, nil
	}
	var tight *limit
	for _, l := range []*limit{in.Session, in.Weekly} {
		if l != nil && l.Remaining != nil && (tight == nil || *l.Remaining < *tight.Remaining) {
			tight = l
		}
	}
	if tight == nil {
		return nil, errors.New("no limit in the response")
	}
	return &Quota{Used: 100 - *tight.Remaining, Limit: 100, Unit: "%", ResetsAt: tight.ResetsAt, Source: "ollama.com/api/balance (legacy plan: the tighter of session and weekly)"}, nil
}

// CLI is a provider's official command-line tool found on PATH. ternly
// doesn't drive these as backends (ADR 022: decided against).
type CLI struct {
	Name, Path, Provider, Why string
}

// lookPath is exec.LookPath (a variable for tests).
var lookPath = exec.LookPath

// FindCLIs reports which official CLIs are installed, without running them.
func FindCLIs() []CLI {
	var out []CLI
	for _, c := range []CLI{
		{Name: "claude", Provider: "Anthropic (Claude subscription)", Why: "Anthropic's consumer terms bar automated access except by API key or where explicitly permitted; ternly doesn't drive the claude CLI with a subscription (ADR 022): use ANTHROPIC_API_KEY."},
		{Name: "codex", Provider: "OpenAI (ChatGPT plan)", Why: "ternly doesn't drive codex with a ChatGPT plan (ADR 022): use OPENAI_API_KEY; Sign in with ChatGPT is pending."},
	} {
		if p, err := lookPath(c.Name); err == nil {
			c.Path = p
			out = append(out, c)
		}
	}
	return out
}
