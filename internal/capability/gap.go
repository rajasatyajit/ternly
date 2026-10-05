package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// System is an external system or format a task can need an integration for.
type System struct {
	Key, Label string
	Words      []string // how prompts name it (lowercase, matched as words)
	CLIs       []string // command-line tools whose absence shows the gap
	Hosts      []string // API hosts a workaround would call
	Exts       []string // file extensions that name it (invoice.pdf)
	Query      string   // catalog search terms
}

// Systems is the curated list cheap detection looks for.
var Systems = []System{
	{Key: "postgres", Label: "PostgreSQL", Words: []string{"postgres", "postgresql", "psql", "pg_dump"}, CLIs: []string{"psql", "pg_dump"}, Query: "postgres postgresql database sql"},
	{Key: "mysql", Label: "MySQL", Words: []string{"mysql", "mariadb"}, CLIs: []string{"mysql"}, Query: "mysql mariadb database sql"},
	{Key: "sqlite", Label: "SQLite", Words: []string{"sqlite", "sqlite3"}, CLIs: []string{"sqlite3"}, Query: "sqlite database"},
	{Key: "mongodb", Label: "MongoDB", Words: []string{"mongodb", "mongo", "mongosh"}, CLIs: []string{"mongosh", "mongo"}, Query: "mongodb database"},
	{Key: "redis", Label: "Redis", Words: []string{"redis"}, CLIs: []string{"redis-cli"}, Query: "redis"},
	{Key: "jira", Label: "Jira", Words: []string{"jira"}, Hosts: []string{"atlassian.net"}, Query: "jira atlassian issues tickets"},
	{Key: "confluence", Label: "Confluence", Words: []string{"confluence"}, Hosts: []string{"atlassian.net/wiki"}, Query: "confluence atlassian wiki"},
	{Key: "linear", Label: "Linear", Words: []string{"linear issue", "linear ticket", "linear project", "linear app", "in linear"}, Hosts: []string{"api.linear.app"}, Query: "linear issues project management"},
	{Key: "github", Label: "GitHub", Words: []string{"github issue", "github issues", "pull request", "github pr", "github actions", "github repo"}, CLIs: []string{"gh"}, Hosts: []string{"api.github.com"}, Query: "github issues pull requests"},
	{Key: "gitlab", Label: "GitLab", Words: []string{"gitlab"}, CLIs: []string{"glab"}, Hosts: []string{"gitlab.com/api"}, Query: "gitlab merge requests issues"},
	{Key: "figma", Label: "Figma", Words: []string{"figma"}, Hosts: []string{"api.figma.com"}, Query: "figma design"},
	{Key: "terraform", Label: "Terraform", Words: []string{"terraform", "tfstate", "hcl"}, CLIs: []string{"terraform"}, Query: "terraform infrastructure as code"},
	{Key: "kubernetes", Label: "Kubernetes", Words: []string{"kubernetes", "kubectl", "k8s", "helm chart", "helm"}, CLIs: []string{"kubectl", "helm"}, Query: "kubernetes kubectl cluster"},
	{Key: "aws", Label: "AWS", Words: []string{"aws", "s3 bucket", "lambda function", "cloudwatch", "dynamodb", "ec2"}, CLIs: []string{"aws"}, Hosts: []string{"amazonaws.com"}, Query: "aws amazon web services"},
	{Key: "gcp", Label: "Google Cloud", Words: []string{"google cloud", "gcp", "bigquery", "gcs bucket", "cloud run"}, CLIs: []string{"gcloud", "bq"}, Hosts: []string{"googleapis.com"}, Query: "google cloud gcp bigquery"},
	{Key: "azure", Label: "Azure", Words: []string{"azure"}, CLIs: []string{"az"}, Hosts: []string{"management.azure.com"}, Query: "azure microsoft cloud"},
	{Key: "slack", Label: "Slack", Words: []string{"slack"}, Hosts: []string{"slack.com/api"}, Query: "slack messages channels"},
	{Key: "notion", Label: "Notion", Words: []string{"notion"}, Hosts: []string{"api.notion.com"}, Query: "notion pages databases"},
	{Key: "sentry", Label: "Sentry", Words: []string{"sentry"}, Hosts: []string{"sentry.io/api"}, Query: "sentry errors monitoring"},
	{Key: "datadog", Label: "Datadog", Words: []string{"datadog"}, Hosts: []string{"api.datadoghq.com"}, Query: "datadog monitoring metrics logs"},
	{Key: "stripe", Label: "Stripe", Words: []string{"stripe"}, Hosts: []string{"api.stripe.com"}, Query: "stripe payments"},
	{Key: "pdf", Label: "PDF", Words: []string{"pdf", "pdfs"}, CLIs: []string{"pdftotext"}, Exts: []string{"pdf"}, Query: "pdf documents extract text"},
	{Key: "excel", Label: "Excel spreadsheets", Words: []string{"xlsx", "excel", "spreadsheet"}, Exts: []string{"xlsx", "xls"}, Query: "excel xlsx spreadsheet"},
	{Key: "docx", Label: "Word documents", Words: []string{"docx", "word document"}, Exts: []string{"docx"}, Query: "docx word document"},
	{Key: "pptx", Label: "PowerPoint", Words: []string{"pptx", "powerpoint", "slide deck"}, Exts: []string{"pptx"}, Query: "pptx powerpoint slides presentation"},
	{Key: "browser", Label: "a web browser", Words: []string{"playwright", "puppeteer", "headless browser", "browser automation", "click through the site", "screenshot of the page", "in the browser"}, Query: "browser automation playwright web testing"},
	{Key: "gdrive", Label: "Google Drive", Words: []string{"google drive", "google docs", "google sheets"}, Query: "google drive docs sheets"},
	{Key: "gmail", Label: "Gmail", Words: []string{"gmail"}, Query: "gmail email"},
	{Key: "supabase", Label: "Supabase", Words: []string{"supabase"}, Query: "supabase"},
	{Key: "firebase", Label: "Firebase", Words: []string{"firebase", "firestore"}, CLIs: []string{"firebase"}, Query: "firebase firestore"},
	{Key: "snowflake", Label: "Snowflake", Words: []string{"snowflake"}, Query: "snowflake data warehouse"},
	{Key: "elasticsearch", Label: "Elasticsearch", Words: []string{"elasticsearch", "opensearch", "kibana"}, Query: "elasticsearch search"},
	{Key: "grafana", Label: "Grafana", Words: []string{"grafana", "prometheus"}, Query: "grafana prometheus monitoring"},
}

// Need is a detected gap.
type Need struct {
	Key, Label string
	Query      string
	Why        string // the signal, for the suggestion's first line
}

// Turn is what a finished turn left for detection.
type Turn struct {
	Prompt      string
	Answer      string   // the model's final text
	Commands    []string // shell commands the model ran
	ToolOutputs []string // their outputs
}

// Detector finds gaps with cheap signals first and the cheapest model last.
type Detector struct {
	// Covered reports whether an available tool, skill or MCP server already
	// covers a system (names and descriptions mention one of its words).
	Covered func(s System) bool
	// Classify asks the cheapest model (tight token cap); nil: no model.
	Classify func(ctx context.Context, prompt string) (string, error)

	mu    sync.Mutex
	cache map[string]string // prompt hash → classification
	stats DetectStats
}

// DetectStats counts what detection did (for measurement).
type DetectStats struct {
	Turns, Cheap, Classified, CacheHits int
}

var (
	reAction = regexp.MustCompile(`(?i)\b(query|queries|connect to|fetch|read|list|create|open|update|post (to|in|on)|send|deploy|run|check|look ?up|search|download|upload|convert|extract|parse|fill in|scrape|screenshot|file a|comment on|close|merge|migrate|export|import|sync|show me|get|pull|summari[sz]e|inspect|debug|apply|provision|scale|restart|roll ?back|navigate|click|log ?in|analy[sz]e|refund|find)\b`)
	// reCodeObject: the action is on code or docs about the system, not on the system.
	reCodeObject = regexp.MustCompile(`(?i)\b(readme|docs?|documentation|function|method|class|struct|module|handler|client|wrapper|tests?|unit test|code|comment|variable|config(uration)? file|settings?|option|flag|parameter|default|button|endpoint|feature|support|blog post|article|example|probe|manifests?)\b|\.(go|py|ts|js|rs|java|md|ya?ml|json|toml)\b`)
	reNoAcces    = regexp.MustCompile(`(?i)\b(i (don'?t|do not|can'?t|cannot|am (not able|unable) to)|there is no|no) (have )?(access|connect|reach|query|open|read|tool|way|integration)\b`)
	reNotFound   = regexp.MustCompile(`(?m)(?:^|\s)([\w.-]+): (?:command )?not found|command not found: ([\w.-]+)`)
)

var (
	reNonWord  = regexp.MustCompile(`[^\pL\pN.-]+`)
	reSentence = regexp.MustCompile(`[.!?\n]+\s`)
)

func words(s string) string { return " " + strings.ToLower(reNonWord.ReplaceAllString(s, " ")) + " " }

func mentions(text string, s System) bool {
	t := words(text)
	for _, w := range s.Words {
		if strings.Contains(t, " "+w+" ") {
			return true
		}
	}
	for _, e := range s.Exts {
		if strings.Contains(t, "."+e+" ") {
			return true
		}
	}
	return false
}

// Detect looks at one finished turn.
func (d *Detector) Detect(ctx context.Context, t Turn) (Need, bool) {
	d.mu.Lock()
	d.stats.Turns++
	d.mu.Unlock()
	covered := func(s System) bool { return d.Covered != nil && d.Covered(s) }
	// 1. A tool failed because a CLI is missing.
	for _, out := range t.ToolOutputs {
		for _, m := range reNotFound.FindAllStringSubmatch(out, -1) {
			cli := m[1] + m[2]
			for _, s := range Systems {
				if containsStr(s.CLIs, cli) && !covered(s) {
					return d.cheap(Need{Key: s.Key, Label: s.Label, Query: s.Query, Why: "`" + cli + "` isn't installed, and no tool covers " + s.Label}), true
				}
			}
		}
	}
	// 2. The model says it lacks access.
	if reNoAcces.MatchString(t.Answer) {
		for _, s := range Systems {
			if mentions(t.Answer, s) && !covered(s) {
				return d.cheap(Need{Key: s.Key, Label: s.Label, Query: s.Query, Why: "the model said it can't reach " + s.Label}), true
			}
		}
	}
	// 3. The model keeps working around it (3+ calls to its API from the shell).
	for _, s := range Systems {
		n := 0
		for _, c := range t.Commands {
			for _, h := range s.Hosts {
				if strings.Contains(c, h) {
					n++
				}
			}
		}
		if n >= 3 && !covered(s) {
			return d.cheap(Need{Key: s.Key, Label: s.Label, Query: s.Query, Why: "the model called the " + s.Label + " API by hand " + itoa(n) + " times"}), true
		}
	}
	// 4. The prompt asks to act on a system no tool covers.
	var ambiguous *System
	for _, sent := range reSentence.Split(t.Prompt+" ", -1) {
		for i := range Systems {
			s := &Systems[i]
			if !mentions(sent, *s) || covered(*s) {
				continue
			}
			if reAction.MatchString(sent) && !reCodeObject.MatchString(sent) {
				return d.cheap(Need{Key: s.Key, Label: s.Label, Query: s.Query, Why: "the task works with " + s.Label + " and no tool covers it"}), true
			}
			if ambiguous == nil {
				ambiguous = s
			}
		}
	}
	// 5. Only now, and only for an ambiguous mention: ask the cheapest model.
	if ambiguous != nil && d.Classify != nil {
		if d.classify(ctx, t.Prompt, ambiguous.Key) {
			return Need{Key: ambiguous.Key, Label: ambiguous.Label, Query: ambiguous.Query, Why: "the task needs " + ambiguous.Label + " (judged by the cheapest model)"}, true
		}
	}
	return Need{}, false
}

func (d *Detector) cheap(n Need) Need {
	d.mu.Lock()
	d.stats.Cheap++
	d.mu.Unlock()
	return n
}

// ClassifyPrompt is the instruction for ambiguous mentions (answer capped at a few tokens).
const ClassifyPrompt = "Does carrying out this coding-agent request require live access to the named external system (to read from or act on it), rather than just discussing it or editing code about it? Answer only yes or no."

func (d *Detector) classify(ctx context.Context, prompt, key string) bool {
	h := sha256.Sum256([]byte(key + "\x00" + prompt))
	k := hex.EncodeToString(h[:8])
	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[string]string{}
	}
	if v, ok := d.cache[k]; ok {
		d.stats.CacheHits++
		d.mu.Unlock()
		return v == "yes"
	}
	d.mu.Unlock()
	if len(prompt) > 1500 {
		prompt = prompt[:1500]
	}
	out, err := d.Classify(ctx, "System: "+key+"\nRequest: "+prompt)
	v := "no"
	if err == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(out)), "yes") {
		v = "yes"
	}
	d.mu.Lock()
	d.cache[k] = v
	d.stats.Classified++
	d.mu.Unlock()
	return v == "yes"
}

// Stats returns detection counters.
func (d *Detector) Stats() DetectStats { d.mu.Lock(); defer d.mu.Unlock(); return d.stats }

// CoveredBy builds a Covered function from the text of available tools and
// skills (names and descriptions).
func CoveredBy(available func() []string) func(System) bool {
	return func(s System) bool {
		for _, t := range available() {
			lt := words(strings.ReplaceAll(strings.ReplaceAll(t, "_", " "), ":", " "))
			for _, w := range append([]string{s.Key}, s.Words...) {
				if strings.Contains(lt, " "+w+" ") {
					return true
				}
			}
		}
		return false
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }
