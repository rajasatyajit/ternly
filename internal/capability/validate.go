package capability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Validator confirms a candidate's artifact exists before it is shown: the
// npm version (with an executable), the PyPI version, or the git repository.
type Validator struct {
	HTTP     *http.Client
	Registry string // npm registry base (tests); default registry.npmjs.org
	PyPI     string // PyPI base (tests); default pypi.org
	Git      func(ctx context.Context, url string) error

	mu    sync.Mutex
	cache map[string]valid
}

type valid struct {
	err error
	at  time.Time
}

// Check returns nil when the entry's artifact exists. Results are cached for a day.
func (v *Validator) Check(ctx context.Context, e Entry) error {
	key := e.ID + "@" + e.Version + "|" + e.Install
	v.mu.Lock()
	if v.cache == nil {
		v.cache = map[string]valid{}
	}
	if c, ok := v.cache[key]; ok && time.Since(c.at) < 24*time.Hour {
		v.mu.Unlock()
		return c.err
	}
	v.mu.Unlock()
	err := v.check(ctx, e)
	if ctx.Err() == nil { // a timeout says nothing about the artifact
		v.mu.Lock()
		v.cache[key] = valid{err, time.Now()}
		v.mu.Unlock()
	}
	return err
}

func (v *Validator) check(ctx context.Context, e Entry) error {
	switch e.Kind {
	case "mcp", "npm":
		kind, pkg, ok := strings.Cut(e.Install, ":")
		if !ok {
			return errors.New("nothing to install")
		}
		switch kind {
		case "npm":
			name, ver := splitVersion(pkg, "@")
			var doc struct {
				Bin json.RawMessage `json:"bin"`
			}
			if err := v.get(ctx, orStr(v.Registry, "https://registry.npmjs.org")+"/"+name+"/"+orStr(ver, "latest"), &doc); err != nil {
				return fmt.Errorf("npm %s@%s: %w", name, ver, err)
			}
			if len(doc.Bin) == 0 || string(doc.Bin) == "{}" || string(doc.Bin) == "null" {
				return fmt.Errorf("npm %s@%s has no executable to run", name, ver)
			}
		case "pypi":
			name, ver := splitVersion(pkg, "==")
			var doc map[string]any
			if err := v.get(ctx, orStr(v.PyPI, "https://pypi.org")+"/pypi/"+name+"/"+ver+"/json", &doc); err != nil {
				return fmt.Errorf("pypi %s==%s: %w", name, ver, err)
			}
		}
	case "plugin":
		_, repo, _ := strings.Cut(e.Install, " ")
		if repo == "" {
			return errors.New("no marketplace repository")
		}
		if !strings.Contains(repo, "://") && !filepath.IsAbs(repo) {
			repo = "https://github.com/" + repo
		}
		if filepath.IsAbs(repo) {
			_, err := os.Stat(repo)
			return err
		}
		return v.git(ctx, repo)
	case "extension":
		return v.git(ctx, e.Install)
	}
	return nil
}

// splitVersion splits name@version (keeping a leading @scope) or name==version.
func splitVersion(s, sep string) (string, string) {
	i := strings.LastIndex(s, sep)
	if i <= 0 {
		return s, ""
	}
	return s[:i], s[i+len(sep):]
}

func (v *Validator) get(ctx context.Context, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	cl := v.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (v *Validator) git(ctx context.Context, url string) error {
	if v.Git != nil {
		return v.Git(ctx, url)
	}
	if strings.HasPrefix(url, "-") {
		return errors.New("invalid URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", "-c", "protocol.ext.allow=never", "ls-remote", "--exit-code", url, "HEAD")
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git repository %s isn't reachable: %s", url, strings.TrimSpace(string(out)))
	}
	return nil
}

// ─────────────────────────── outcomes ───────────────────────────

// Outcomes is a local-only log of what happened to suggestions (shown,
// accepted, installed, failed, invalid, declined, dismissed). It is never
// sent anywhere; it demotes entries that failed and measures real precision.
type Outcomes struct {
	File string

	mu     sync.Mutex
	loaded bool
	fails  map[string]int
	oks    map[string]int
}

// Event is one line of the log.
type Event struct {
	Time   time.Time `json:"time"`
	Need   string    `json:"need"`
	Entry  string    `json:"entry,omitempty"`
	Event  string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
}

func (o *Outcomes) load() {
	if o.loaded {
		return
	}
	o.loaded, o.fails, o.oks = true, map[string]int{}, map[string]int{}
	f, err := os.Open(o.File)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			o.count(e)
		}
	}
}

func (o *Outcomes) count(e Event) {
	switch e.Event {
	case "failed", "invalid":
		o.fails[e.Entry]++
	case "installed":
		o.oks[e.Entry]++
	}
}

// Record appends an event.
func (o *Outcomes) Record(e Event) {
	if o == nil || o.File == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load()
	e.Time = time.Now()
	o.count(e)
	_ = os.MkdirAll(filepath.Dir(o.File), 0o700)
	f, err := os.OpenFile(o.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, _ = f.Write(append(b, '\n'))
}

// Penalty is the score factor for an entry's history: each failure more than
// its successes halves it.
func (o *Outcomes) Penalty(id string) float64 {
	if o == nil {
		return 1
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load()
	f := 1.0
	for i := o.oks[id]; i < o.fails[id]; i++ {
		f /= 2
	}
	return f
}

// Report summarises the log: suggestions shown and what became of them.
func (o *Outcomes) Report() map[string]int {
	out := map[string]int{}
	f, err := os.Open(o.File)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out[e.Event]++
		}
	}
	return out
}
