// Package deps checks that packages and versions an agent adds actually
// exist (requirement 7): dependencies added to go.mod, package.json,
// requirements*.txt or Cargo.toml, and packages named in go get, npm install,
// pip install and cargo add, looked up in their registries. A failure goes
// back to the model with the tool result.
package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Dep is one package at a version ("" = any: the package must exist).
type Dep struct {
	Eco     string // go, npm, pypi, cargo
	Name    string
	Version string
}

func (d Dep) String() string {
	if d.Version == "" {
		return d.Name
	}
	return d.Name + "@" + d.Version
}

// Manifest reports whether a workspace path is a dependency manifest.
func Manifest(path string) bool {
	b := filepath.Base(path)
	return b == "go.mod" || b == "package.json" || b == "Cargo.toml" || strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")
}

// Added lists dependencies in after that weren't in before.
func Added(path string, before, after []byte) []Dep {
	old := map[Dep]bool{}
	for _, d := range parse(path, before) {
		old[d] = true
	}
	var out []Dep
	for _, d := range parse(path, after) {
		if !old[d] {
			out = append(out, d)
		}
	}
	return out
}

var (
	reGoReq    = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([\w.~-]+(?:/[\w.~-]+)+)\s+(v[\w.+-]+)`)
	reReq      = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*==\s*([\w.!+-]+)`)
	reCargoDep = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*=\s*(?:"([^"]+)"|\{[^}]*version\s*=\s*"([^"]+)")`)
)

func parse(path string, b []byte) []Dep {
	var out []Dep
	switch base := filepath.Base(path); {
	case base == "go.mod":
		for _, m := range reGoReq.FindAllStringSubmatch(string(b), -1) {
			if m[1] != "module" && !strings.HasPrefix(strings.TrimSpace(m[0]), "module") {
				out = append(out, Dep{"go", m[1], m[2]})
			}
		}
	case base == "package.json":
		var pj struct {
			Deps    map[string]string `json:"dependencies"`
			DevDeps map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(b, &pj) == nil {
			for _, m := range []map[string]string{pj.Deps, pj.DevDeps} {
				for n, v := range m {
					out = append(out, Dep{"npm", n, npmVersion(v)})
				}
			}
		}
	case base == "Cargo.toml":
		in := false
		for _, l := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "[") {
				in = strings.HasSuffix(t, "dependencies]")
				continue
			}
			if m := reCargoDep.FindStringSubmatch(l); in && m != nil {
				out = append(out, Dep{"cargo", m[1], cargoVersion(m[2] + m[3])})
			}
		}
	default: // requirements*.txt
		for _, m := range reReq.FindAllStringSubmatch(string(b), -1) {
			out = append(out, Dep{"pypi", m[1], m[2]})
		}
	}
	return out
}

// npmVersion keeps an exact version and drops ranges and tags (the package
// must then merely exist): "^1.2.3" → "", "1.2.3" → "1.2.3".
func npmVersion(v string) string {
	if regexp.MustCompile(`^\d+\.\d+\.\d+(-[\w.]+)?$`).MatchString(v) {
		return v
	}
	return ""
}

func cargoVersion(v string) string {
	v = strings.TrimPrefix(v, "=")
	if regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return v
	}
	return ""
}

var (
	reGoGet   = regexp.MustCompile(`\bgo\s+get\s+((?:-\S+\s+)*)(.+)`)
	reNpmAdd  = regexp.MustCompile(`\b(?:npm\s+(?:install|i|add)|pnpm\s+add|yarn\s+add)\s+(.+)`)
	rePipAdd  = regexp.MustCompile(`\bpip3?\s+install\s+(.+)`)
	reCargoAd = regexp.MustCompile(`\bcargo\s+add\s+(.+)`)
)

// FromCommand lists packages a shell command installs.
func FromCommand(cmd string) []Dep {
	var out []Dep
	for _, part := range regexp.MustCompile(`&&|\|\||;|\|`).Split(cmd, -1) {
		args := func(s string) []string {
			var a []string
			for _, f := range strings.Fields(s) {
				if !strings.HasPrefix(f, "-") && !strings.ContainsAny(f, "<>$`") {
					a = append(a, f)
				}
			}
			return a
		}
		switch {
		case reGoGet.MatchString(part):
			for _, a := range args(reGoGet.FindStringSubmatch(part)[2]) {
				n, v, _ := strings.Cut(a, "@")
				if strings.Contains(n, ".") && strings.Contains(n, "/") && v != "latest" && v != "none" {
					out = append(out, Dep{"go", n, v})
				}
			}
		case reNpmAdd.MatchString(part):
			for _, a := range args(reNpmAdd.FindStringSubmatch(part)[1]) {
				n, v := a, ""
				if i := strings.LastIndex(a, "@"); i > 0 {
					n, v = a[:i], npmVersion(a[i+1:])
				}
				if !strings.ContainsAny(n, "./:") || strings.HasPrefix(n, "@") {
					out = append(out, Dep{"npm", n, v})
				}
			}
		case rePipAdd.MatchString(part):
			for _, a := range args(rePipAdd.FindStringSubmatch(part)[1]) {
				n, v, _ := strings.Cut(a, "==")
				if !strings.ContainsAny(n, "./:") {
					out = append(out, Dep{"pypi", n, v})
				}
			}
		case reCargoAd.MatchString(part):
			for _, a := range args(reCargoAd.FindStringSubmatch(part)[1]) {
				n, v, _ := strings.Cut(a, "@")
				out = append(out, Dep{"cargo", n, cargoVersion(v)})
			}
		}
	}
	return out
}

// Checker looks dependencies up in their registries, caching answers for a
// day. Bases are overridable for tests.
type Checker struct {
	HTTP                       *http.Client
	GoProxy, NPM, PyPI, Crates string

	mu    sync.Mutex
	cache map[Dep]cached
}

type cached struct {
	err error
	at  time.Time
}

// errNotFound is a registry's definite "no".
type errNotFound struct{ what string }

func (e errNotFound) Error() string { return e.what }

// Check returns problems for deps that don't exist (definite answers only:
// a registry that can't be reached says nothing).
func (c *Checker) Check(ctx context.Context, ds []Dep) []string {
	var out []string
	for _, d := range ds {
		if err := c.check(ctx, d); err != nil {
			if _, ok := err.(errNotFound); ok {
				out = append(out, err.Error())
			}
		}
	}
	return out
}

func (c *Checker) check(ctx context.Context, d Dep) error {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[Dep]cached{}
	}
	if e, ok := c.cache[d]; ok && time.Since(e.at) < 24*time.Hour {
		c.mu.Unlock()
		return e.err
	}
	c.mu.Unlock()
	err := c.lookup(ctx, d)
	if _, definite := err.(errNotFound); definite || err == nil {
		c.mu.Lock()
		c.cache[d] = cached{err, time.Now()}
		c.mu.Unlock()
	}
	return err
}

func (c *Checker) lookup(ctx context.Context, d Dep) error {
	var u, latest string
	switch d.Eco {
	case "go":
		esc := goEscape(d.Name)
		base := or(c.GoProxy, "https://proxy.golang.org")
		u, latest = base+"/"+esc+"/@v/"+goEscape(d.Version)+".info", base+"/"+esc+"/@latest"
		if d.Version == "" {
			u = latest
		}
	case "npm":
		base := or(c.NPM, "https://registry.npmjs.org")
		name := strings.Replace(url.PathEscape(d.Name), "%40", "@", 1)
		u, latest = base+"/"+name+"/"+or(d.Version, "latest"), base+"/"+name+"/latest"
	case "pypi":
		base := or(c.PyPI, "https://pypi.org")
		u, latest = base+"/pypi/"+url.PathEscape(d.Name)+"/json", ""
		if d.Version != "" {
			u, latest = base+"/pypi/"+url.PathEscape(d.Name)+"/"+url.PathEscape(d.Version)+"/json", base+"/pypi/"+url.PathEscape(d.Name)+"/json"
		}
	case "cargo":
		base := or(c.Crates, "https://crates.io")
		u, latest = base+"/api/v1/crates/"+url.PathEscape(d.Name), ""
		if d.Version != "" {
			u, latest = u+"/"+url.PathEscape(d.Version), u
		}
	default:
		return nil
	}
	status, err := c.get(ctx, u)
	if err != nil || status == 200 {
		return err
	}
	if status != 404 && status != 410 {
		return fmt.Errorf("%s: HTTP %d", u, status)
	}
	// Not found: is it the version or the whole package?
	if d.Version != "" && latest != "" {
		if s, err := c.get(ctx, latest); err == nil && s == 200 {
			return errNotFound{fmt.Sprintf("%s %s: version %s doesn't exist (the package does) — check the published versions", d.Eco, d.Name, d.Version)}
		}
	}
	return errNotFound{fmt.Sprintf("%s %s: no such package in the registry", d.Eco, d.Name)}
}

func (c *Checker) get(ctx context.Context, u string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ternly (dependency check)") // crates.io requires one
	cl := c.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// goEscape is the module proxy's case encoding: an upper-case letter becomes
// "!" plus its lower case.
func goEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
