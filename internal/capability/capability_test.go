package capability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/memory"
)

// The labelled set: prompts that need an external integration (with the
// system), and prompts that only mention one while working on code.
var needing = []struct{ prompt, key string }{
	{"Query the orders table in our Postgres database and tell me how many orders shipped last week", "postgres"},
	{"Create a Jira ticket for the login bug we just fixed", "jira"},
	{"Extract the tables from invoice.pdf into a CSV", "pdf"},
	{"Pull the latest designs from Figma for the settings screen", "figma"},
	{"Check why the payments pod keeps restarting in the staging Kubernetes cluster", "kubernetes"},
	{"Post to the #eng Slack channel a summary of today's changes", "slack"},
	{"Open the app in a headless browser and screenshot the dashboard", "browser"},
	{"Read the Notion page with our API conventions and apply them here", "notion"},
	{"List the errors Sentry reported for this release", "sentry"},
	{"Apply the terraform plan for the new storage bucket", "terraform"},
	{"Look up the customer in Stripe and refund the last charge", "stripe"},
	{"Fill in budget.xlsx with the new numbers from the report", "excel"},
	{"Find the failed GitHub Actions runs on main from today", "github"},
	{"Fetch the Redis keys that start with session:", "redis"},
	{"Search our Confluence space for the onboarding doc", "confluence"},
}

var notNeeding = []string{
	"Refactor the parser to use a table-driven approach",
	"Fix the failing test in internal/memory",
	"Write a blog post about why we chose Postgres over MySQL",
	"Explain how our Kubernetes deployment manifests are structured",
	"Rename the slackNotifier function to notifier",
	"Add a unit test for the Stripe webhook handler",
	"Why is the Redis client reconnecting in a loop? Look at internal/cache/redis.go",
	"Update the README to mention Terraform support",
	"Bump the version and tag a release",
	"Convert this callback code to async/await",
	"What does the k8s readiness probe in deploy.yaml do?",
	"Make the PDF export use A4 by default",
	"Speed up the jira-sync job",
	"Write a migration that adds an index to the users table",
	"Document the Slack integration settings in docs/slack.md",
}

func TestDetectorCheapSignals(t *testing.T) {
	d := &Detector{Covered: CoveredBy(func() []string { return []string{"read_file Read a file", "mcp__plugin_db_sqlite__query Query SQLite"} })}
	ctx := context.Background()
	hit := 0
	for _, c := range needing {
		n, ok := d.Detect(ctx, Turn{Prompt: c.prompt})
		if ok && n.Key == c.key {
			hit++
		} else {
			t.Logf("missed %q → %+v %v", c.prompt, n, ok)
		}
	}
	fp := 0
	for _, p := range notNeeding {
		if n, ok := d.Detect(ctx, Turn{Prompt: p}); ok {
			fp++
			t.Errorf("false suggestion for %q: %+v", p, n)
		}
	}
	t.Logf("cheap signals only: %d/%d needs found, %d/%d false", hit, len(needing), fp, len(notNeeding))
	if hit < 13 {
		t.Errorf("found %d/%d", hit, len(needing))
	}
	// Covered systems are not gaps.
	if _, ok := d.Detect(ctx, Turn{Prompt: "Query the users table in the sqlite database"}); ok {
		t.Error("covered system reported")
	}
	// Other signals.
	if n, ok := d.Detect(ctx, Turn{Prompt: "why is staging down", ToolOutputs: []string{"bash: kubectl: command not found"}}); !ok || n.Key != "kubernetes" {
		t.Errorf("missing CLI: %+v %v", n, ok)
	}
	if n, ok := d.Detect(ctx, Turn{Prompt: "summarise the incident", Answer: "I don't have access to Datadog from here, so I can't see the logs."}); !ok || n.Key != "datadog" {
		t.Errorf("no-access answer: %+v %v", n, ok)
	}
	cmds := []string{"curl -s https://api.notion.com/v1/pages/1", "curl -s https://api.notion.com/v1/blocks/2", "curl -s https://api.notion.com/v1/blocks/3"}
	if n, ok := d.Detect(ctx, Turn{Prompt: "tidy up the docs", Commands: cmds}); !ok || n.Key != "notion" {
		t.Errorf("workaround: %+v %v", n, ok)
	}
}

func TestClassifierOnlyForAmbiguous(t *testing.T) {
	calls := 0
	d := &Detector{Classify: func(ctx context.Context, p string) (string, error) { calls++; return "no", nil }}
	ctx := context.Background()
	d.Detect(ctx, Turn{Prompt: "Refactor the parser"})              // no mention: no call
	d.Detect(ctx, Turn{Prompt: "Create a Jira ticket for this"})    // strong: no call
	d.Detect(ctx, Turn{Prompt: "Explain our Kubernetes manifests"}) // ambiguous: one call
	d.Detect(ctx, Turn{Prompt: "Explain our Kubernetes manifests"}) // cached
	if calls != 1 || d.Stats().CacheHits != 1 {
		t.Fatalf("classifier calls %d, stats %+v", calls, d.Stats())
	}
}

func TestRankingAndSuggester(t *testing.T) {
	c := &Catalog{Dir: t.TempDir()}
	defer c.Close()
	_ = c.Put(
		Entry{ID: "plugin:pg@claude-plugins-official", Kind: "plugin", Name: "postgres-tools", Description: "Query PostgreSQL databases", Publisher: "Anthropic", Source: "marketplace:claude-plugins-official", Official: true, Coverage: 1, Tokens: 60, Runs: "an MCP server", Network: true},
		Entry{ID: "npm:pg-mcp", Kind: "npm", Name: "pg-mcp", Description: "postgres mcp server", Source: "npm", Coverage: 1, Tokens: 400, Runs: "npx -y pg-mcp@1.0.0", Network: true, Popularity: 50},
		Entry{ID: "mcp:io.github.x/pg", Kind: "mcp", Name: "io.github.x/pg", Description: "PostgreSQL MCP server", Source: "mcp-registry", Verified: true, Coverage: 1, Tokens: 400, Runs: "npx -y @x/pg@2.0.0", Network: true, Secrets: []string{"DATABASE_URL"}},
		Entry{ID: "mcp:io.github.y/pg-remote", Kind: "mcp", Name: "pg remote", Description: "PostgreSQL remote", Source: "mcp-registry", Verified: true, Coverage: 0},
		Entry{ID: "plugin:jira@x", Kind: "plugin", Name: "jira", Description: "Jira issues", Source: "marketplace:x", Coverage: 1},
	)
	n := Need{Key: "postgres", Label: "PostgreSQL", Query: "postgres postgresql database sql"}
	got := c.Search(n, 3)
	if len(got) != 3 || got[0].ID != "plugin:pg@claude-plugins-official" {
		t.Fatalf("ranking: %+v", got)
	}
	for _, cd := range got {
		if cd.Coverage == 0 || strings.Contains(cd.Name, "jira") {
			t.Fatalf("unloadable or irrelevant candidate ranked: %+v", cd)
		}
	}
	s := &Suggester{File: filepath.Join(t.TempDir(), "capability.json")}
	sg := Suggestion{Need: n, Candidates: got}
	if !s.Offer(sg) || s.Offer(sg) {
		t.Fatal("offered twice in one session")
	}
	if p := s.Take(); p == nil || s.Take() != nil {
		t.Fatal("take")
	}
	_ = s.Dismiss("postgres")
	s2 := &Suggester{File: s.File} // a later session in the same project
	if s2.Offer(sg) {
		t.Fatal("dismissed need suggested again")
	}
}

// TestClassifierEval: the labelled set with the ambiguous cases sent to a
// real cheap model (TERNLY_CAP_CLASSIFY=<ollama model>, TERNLY_MEM_OLLAMA=url).
func TestClassifierEval(t *testing.T) {
	model, base := os.Getenv("TERNLY_CAP_CLASSIFY"), os.Getenv("TERNLY_MEM_OLLAMA")
	if model == "" || base == "" {
		t.Skip("TERNLY_CAP_CLASSIFY and TERNLY_MEM_OLLAMA not set")
	}
	chat := memory.OllamaChat(base, model)
	var calls, inChars int
	d := &Detector{Classify: func(ctx context.Context, p string) (string, error) {
		calls++
		inChars += len(ClassifyPrompt) + len(p)
		return chat(ctx, ClassifyPrompt, p)
	}}
	ctx := context.Background()
	hit, fp := 0, 0
	t0 := time.Now()
	for _, c := range needing {
		if n, ok := d.Detect(ctx, Turn{Prompt: c.prompt}); ok && n.Key == c.key {
			hit++
		}
	}
	for _, p := range notNeeding {
		if n, ok := d.Detect(ctx, Turn{Prompt: p}); ok {
			fp++
			t.Logf("false suggestion for %q: %s", p, n.Why)
		}
	}
	t.Logf("cheap signals + %s for ambiguous ones: %d/%d needs found, %d/%d false; %d classifications (~%d input tokens each), %v total",
		model, hit, len(needing), fp, len(notNeeding), calls, inChars*10/36/max(calls, 1), time.Since(t0).Round(time.Millisecond))
}
