package plugins

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Surface is what a plugin would execute, in a form a person can review:
// one line per hook and MCP server, and a hash of every file (scripts the
// commands call can live anywhere in the plugin).
type Surface struct {
	Lines []string          `json:"lines"`
	Files map[string]string `json:"files"` // path → sha256
	Tree  string            `json:"tree"`
}

// SurfaceOf computes a manifest's surface.
func SurfaceOf(m *Manifest) (Surface, error) {
	s := Surface{Files: map[string]string{}}
	for _, c := range m.Executable() {
		switch {
		case c.Hook != nil:
			cmd := c.Hook.Command
			if len(c.Hook.Args) > 0 {
				cmd = strings.Join(append([]string{c.Hook.Command}, c.Hook.Args...), " ")
			}
			s.Lines = append(s.Lines, fmt.Sprintf("hook %s %s: %s", c.Hook.Event, orStr(c.Hook.Matcher, "*"), cmd))
		case c.MCP != nil:
			var env []string
			for k := range c.MCP.Env {
				env = append(env, k)
			}
			sort.Strings(env)
			line := fmt.Sprintf("mcp %s: %s", c.MCP.Server, strings.Join(append([]string{c.MCP.Command}, c.MCP.Args...), " "))
			if len(env) > 0 {
				line += " (env " + strings.Join(env, ", ") + ")"
			}
			s.Lines = append(s.Lines, line)
		}
	}
	sort.Strings(s.Lines)
	if m.Format == "local" || m.Format == "codex-config" {
		return s, nil // not a plugin tree: the lines are the surface
	}
	h := sha256.New()
	err := filepath.WalkDir(m.Dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(m.Dir, p)
		if e.Type()&fs.ModeSymlink != 0 {
			s.Files[rel] = "symlink"
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		fh := sha256.New()
		_, err = io.Copy(fh, f)
		f.Close()
		if err != nil {
			return err
		}
		sum := hex.EncodeToString(fh.Sum(nil))
		if fi, err := e.Info(); err == nil && fi.Mode()&0o111 != 0 {
			sum += "+x"
		}
		s.Files[filepath.ToSlash(rel)] = sum
		return nil
	})
	paths := make([]string, 0, len(s.Files))
	for p := range s.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(h, "%s %s\n", p, s.Files[p])
	}
	for _, l := range s.Lines {
		fmt.Fprintln(h, l)
	}
	s.Tree = hex.EncodeToString(h.Sum(nil))
	return s, err
}

// Diff is how a new surface differs from the approved one.
type Diff struct {
	Added, Removed []string // surface lines
	Files          []string // changed files: "+ new", "- gone", "~ changed"
	Executes       bool     // what runs changed: commands, or a non-text file (scripts, binaries)
}

func (d Diff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Files) == 0 }

// Text renders the diff for review.
func (d Diff) Text() string {
	var b strings.Builder
	for _, l := range d.Removed {
		b.WriteString("  - " + l + "\n")
	}
	for _, l := range d.Added {
		b.WriteString("  + " + l + "\n")
	}
	if len(d.Files) > 0 {
		b.WriteString(fmt.Sprintf("  files: %s\n", strings.Join(d.Files[:min(len(d.Files), 20)], ", ")))
		if len(d.Files) > 20 {
			b.WriteString(fmt.Sprintf("  … %d more\n", len(d.Files)-20))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var promptFile = regexp.MustCompile(`(?i)\.(md|mdc|txt|toml)$|(^|/)(LICENSE|NOTICE|README)[^/]*$`)

// DiffSurface compares an approved surface with a new one.
func DiffSurface(old, nw Surface) Diff {
	var d Diff
	had := map[string]bool{}
	for _, l := range old.Lines {
		had[l] = true
	}
	has := map[string]bool{}
	for _, l := range nw.Lines {
		has[l] = true
		if !had[l] {
			d.Added = append(d.Added, l)
		}
	}
	for _, l := range old.Lines {
		if !has[l] {
			d.Removed = append(d.Removed, l)
		}
	}
	for p, h := range nw.Files {
		switch o, ok := old.Files[p]; {
		case !ok:
			d.Files = append(d.Files, "+ "+p)
		case o != h:
			d.Files = append(d.Files, "~ "+p)
		default:
			continue
		}
		if !promptFile.MatchString(p) {
			d.Executes = true
		}
	}
	for p := range old.Files {
		if _, ok := nw.Files[p]; !ok {
			d.Files = append(d.Files, "- "+p)
		}
	}
	sort.Strings(d.Files)
	if len(d.Added)+len(d.Removed) > 0 {
		d.Executes = true
	}
	return d
}

// ─────────────────────────── trust ───────────────────────────

// Trust is a label computed only from where a plugin came from (which
// marketplace or registry listed it, and its repository host and owner),
// never from anything the plugin says about itself.
type Trust struct {
	Level string `json:"level"` // official, listed, local, unverified
	Why   string `json:"why"`
}

// officialMarketplaces are Anthropic's first-party marketplaces, as named
// in the Claude Code docs (plugins/anthropic-marketplaces), with the GitHub
// owner their repositories must belong to.
var officialMarketplaces = map[string]string{
	"claude-plugins-official": "anthropics/claude-plugins-official",
	"claude-community":        "anthropics/claude-plugins-community",
	"claude-code-plugins":     "anthropics/claude-code",
}

// TrustFor labels a source. A marketplace counts only if it was fetched
// from the repository it claims to be; "official" additionally needs the
// plugin itself to live in that repository or under github.com/anthropics.
func TrustFor(src Source) Trust {
	switch {
	case src.Kind == "local":
		return Trust{"local", "from a directory on this machine: " + src.URL}
	case src.Marketplace != "":
		repo, known := officialMarketplaces[src.Marketplace]
		fromRepo := known && sameRepo(src.MarketplaceURL, repo)
		mrepo := githubRepo(src.MarketplaceURL)
		byAnthropic := strings.HasPrefix(mrepo, "anthropics/") // the repository it was actually fetched from
		hostedByAnthropic := strings.HasPrefix(githubRepo(src.URL), "anthropics/")
		switch {
		case fromRepo && src.Marketplace == "claude-plugins-official" && hostedByAnthropic:
			return Trust{"official", "listed in Anthropic's official marketplace (" + repo + ") and hosted by Anthropic"}
		case byAnthropic && hostedByAnthropic && src.Marketplace != "claude-community":
			return Trust{"official", "from Anthropic's marketplace repository " + mrepo + ", hosted by Anthropic"}
		case fromRepo || byAnthropic:
			return Trust{"listed", "listed in " + orStr(mrepo, repo) + "; the plugin itself is from " + orStr(githubRepo(src.URL), src.URL)}
		case known:
			return Trust{"unverified", "claims the marketplace name " + src.Marketplace + " but was fetched from " + src.MarketplaceURL}
		}
		return Trust{"unverified", "listed in the third-party marketplace " + src.Marketplace + " (" + src.MarketplaceURL + ")"}
	}
	return Trust{"unverified", "installed directly from " + src.URL}
}

var reGitHub = regexp.MustCompile(`^(?:https?://|git@)github\.com[/:]([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

func githubRepo(u string) string {
	if m := reGitHub.FindStringSubmatch(u); m != nil {
		return strings.ToLower(m[1])
	}
	if strings.Count(u, "/") == 1 && !strings.Contains(u, ":") { // owner/repo shorthand
		return strings.ToLower(u)
	}
	return ""
}

func sameRepo(u, ownerRepo string) bool { return githubRepo(u) == strings.ToLower(ownerRepo) }

// ─────────────────────────── store ───────────────────────────

// Source is where a plugin comes from.
type Source struct {
	Kind           string `json:"kind"` // git, local, generated (from the capability catalog)
	URL            string `json:"url"`
	Ref            string `json:"ref,omitempty"`
	Path           string `json:"path,omitempty"` // subdirectory (git-subdir, marketplace relative path)
	Marketplace    string `json:"marketplace,omitempty"`
	MarketplaceURL string `json:"marketplace_url,omitempty"`
	// From a marketplace entry: its name (the plugin's name when it has no
	// plugin.json) and, for entries that list them, which skills it has.
	Name   string   `json:"name,omitempty"`
	Skills []string `json:"skills,omitempty"`
}

// Scope is what a plugin's code may touch.
type Scope struct {
	Workspace string `json:"workspace"` // none, ro, rw
	Network   bool   `json:"network"`
}

// Installed is one plugin in the lock file.
type Installed struct {
	Name     string            `json:"name"`
	Source   Source            `json:"source"`
	Commit   string            `json:"commit"` // pinned
	Dir      string            `json:"dir"`
	Enabled  bool              `json:"enabled"`
	Approved Surface           `json:"approved"` // what the user approved to run
	Hooks    Scope             `json:"hooks"`
	MCP      Scope             `json:"mcp"`
	Trust    Trust             `json:"trust"`
	Format   string            `json:"format"`
	Time     time.Time         `json:"time"`
	Env      map[string]string `json:"env,omitempty"` // values for ${VAR} in its MCP config (/plugin env); this file is 0600 in a masked directory
}

// SetEnv sets (or with value "" removes) a variable a plugin's MCP servers get.
func (s *Store) SetEnv(name, key, value string) error {
	return s.update(name, func(p *Installed) {
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		if value == "" {
			delete(p.Env, key)
		} else {
			p.Env[key] = value
		}
	})
}

// DefaultScopes: hooks read the workspace, offline; MCP servers read the
// workspace and may use the network (most talk to a service).
var (
	DefaultHookScope = Scope{Workspace: "ro"}
	DefaultMCPScope  = Scope{Workspace: "ro", Network: true}
)

// Store is the set of installed plugins: a lock file plus their files under
// dir (ternly's data directory, masked from every sandbox).
type Store struct {
	Dir  string
	mu   sync.Mutex
	all  map[string]*Installed
	prev map[string]*Installed // previous versions awaiting Finish
}

// OpenStore reads dir/plugins.json.
func OpenStore(dir string) (*Store, error) {
	s := &Store{Dir: dir, all: map[string]*Installed{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "plugins.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Installed
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("plugins.json: %w", err)
	}
	for _, p := range list {
		s.all[p.Name] = p
	}
	return s, nil
}

func (s *Store) save() error {
	list := make([]*Installed, 0, len(s.all))
	for _, p := range s.all {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := filepath.Join(s.Dir, ".plugins.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Dir, "plugins.json"))
}

// List returns copies of the installed plugins, by name.
func (s *Store) List() []Installed {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Installed, 0, len(s.all))
	for _, p := range s.all {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a copy of one plugin.
func (s *Store) Get(name string) (Installed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[name]
	if !ok {
		return Installed{}, false
	}
	return *p, true
}

// SetEnabled enables or disables a plugin.
func (s *Store) SetEnabled(name string, on bool) error {
	return s.update(name, func(p *Installed) { p.Enabled = on })
}

// SetScopes changes what a plugin's code may touch.
func (s *Store) SetScopes(name string, hooks, mcp Scope) error {
	return s.update(name, func(p *Installed) { p.Hooks, p.MCP = hooks, mcp })
}

func (s *Store) update(name string, f func(*Installed)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.all[name]
	if !ok {
		return fmt.Errorf("no plugin %q", name)
	}
	f(p)
	return s.save()
}

// Remove uninstalls a plugin and deletes its files.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.all[name]; !ok {
		return fmt.Errorf("no plugin %q", name)
	}
	delete(s.all, name)
	if err := s.save(); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.Dir, name))
}

// ─────────────────────────── install and update ───────────────────────────

// Pending is a fetched plugin awaiting review: nothing in it is active.
type Pending struct {
	Manifest *Manifest
	Surface  Surface
	Source   Source
	Commit   string
	Trust    Trust
	Prev     *Installed // set for an update
	Diff     Diff       // against Prev's approved surface
	staging  string
}

// Fetch brings a plugin into a staging directory, pinned to one commit, and
// reads it. Git runs hardened: no hooks, no submodules, symlinks checked
// out as plain files (so a plugin can't point a "skill" at ~/.ssh), and the
// .git directory removed afterwards.
func (s *Store) Fetch(ctx context.Context, src Source) (*Pending, error) {
	staging := filepath.Join(s.Dir, ".staging", randHex())
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, err
	}
	fail := func(err error) (*Pending, error) { os.RemoveAll(staging); return nil, err }
	var commit string
	switch src.Kind {
	case "local":
		if err := copyTree(src.URL, staging); err != nil {
			return fail(err)
		}
		commit = "local"
	case "git":
		var err error
		if commit, err = gitFetch(ctx, src.URL, src.Ref, staging); err != nil {
			return fail(err)
		}
	default:
		return fail(fmt.Errorf("unknown source kind %q", src.Kind))
	}
	dir := staging
	if src.Path != "" {
		dir = filepath.Join(staging, filepath.Clean(src.Path))
		if rel, err := filepath.Rel(staging, dir); err != nil || strings.HasPrefix(rel, "..") {
			return fail(errors.New("plugin path leaves the repository"))
		}
	}
	m, err := loadPlugin(dir, orStr(src.Name, nameFromURL(src.URL)), src)
	if err != nil {
		return fail(err)
	}
	sf, err := SurfaceOf(m)
	if err != nil {
		return fail(err)
	}
	p := &Pending{Manifest: m, Surface: sf, Source: src, Commit: commit, Trust: TrustFor(src), staging: staging}
	if prev, ok := s.Get(m.Name); ok {
		p.Prev = &prev
		p.Diff = DiffSurface(prev.Approved, sf)
	}
	return p, nil
}

// Discard drops a pending plugin.
func (s *Store) Discard(p *Pending) { os.RemoveAll(p.staging) }

// Accept installs (or updates to) a reviewed plugin: its files move into
// place under <name>/<commit>, and the lock records the approved surface.
// The previous version's files are removed.
func (s *Store) Accept(p *Pending) (*Installed, error) {
	m := p.Manifest
	version := shortCommit(p.Commit)
	if p.Commit == "local" { // a local source has no commit: its content hash names the version
		version = "local-" + shortCommit(p.Surface.Tree)
	}
	dst := filepath.Join(s.Dir, m.Name, version)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(dst)
	src := m.Dir
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	os.RemoveAll(p.staging)
	m.Dir = dst
	if sf, err := SurfaceOf(m); err == nil { // paths are relative: unchanged by the move
		p.Surface = sf
	}
	in := &Installed{Name: m.Name, Source: p.Source, Commit: p.Commit, Dir: dst, Enabled: true, Approved: p.Surface,
		Hooks: DefaultHookScope, MCP: DefaultMCPScope, Trust: p.Trust, Format: m.Format, Time: time.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev := s.all[m.Name]; prev != nil {
		in.Hooks, in.MCP, in.Enabled, in.Env = prev.Hooks, prev.MCP, prev.Enabled, prev.Env
		if prev.Dir != dst {
			if s.prev == nil {
				s.prev = map[string]*Installed{}
			}
			s.prev[m.Name] = prev // kept until Finish: a failed validation rolls back to it
		}
	}
	s.all[m.Name] = in
	return in, s.save()
}

// Finish completes an install after validation: ok keeps it (and deletes
// the previous version's files); otherwise the previous version (or
// nothing) is restored, so a failed install changes nothing.
func (s *Store) Finish(name string, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, prev := s.all[name], s.prev[name]
	delete(s.prev, name)
	switch {
	case ok:
		if prev != nil && cur != nil && prev.Dir != cur.Dir {
			os.RemoveAll(prev.Dir)
		}
		return nil
	case prev != nil:
		s.all[name] = prev
	default:
		delete(s.all, name)
	}
	if cur != nil && (prev == nil || cur.Dir != prev.Dir) {
		os.RemoveAll(cur.Dir)
	}
	return s.save()
}

// loadPlugin reads a plugin directory, applying what its marketplace entry
// says when the entry is the manifest (no plugin.json): which skills it has.
func loadPlugin(dir, name string, src Source) (*Manifest, error) {
	m, err := Load(dir, name)
	if err != nil || len(src.Skills) == 0 || exists(filepath.Join(dir, ".claude-plugin", "plugin.json")) {
		return m, err
	}
	keep := map[string]bool{}
	for _, p := range src.Skills {
		keep[filepath.Clean(filepath.Join(dir, p))] = true
	}
	kept := m.Components[:0]
	for _, c := range m.Components {
		if c.Kind != KSkill || keep[filepath.Dir(c.Path)] {
			kept = append(kept, c)
		}
	}
	m.Components = kept
	return m, nil
}

// Verify re-reads an installed plugin from disk. ok is false when its files
// no longer match what was approved (edited or tampered with since): its
// executable parts must not run until approved again.
func (s *Store) Verify(p Installed) (*Manifest, Diff, bool, error) {
	m, err := loadPlugin(p.Dir, p.Name, p.Source)
	if err != nil {
		return nil, Diff{}, false, err
	}
	sf, err := SurfaceOf(m)
	if err != nil {
		return nil, Diff{}, false, err
	}
	d := DiffSurface(p.Approved, sf)
	return m, d, d.Empty(), nil
}

// Reapprove records the current on-disk surface as approved (after review).
func (s *Store) Reapprove(name string) error {
	p, ok := s.Get(name)
	if !ok {
		return fmt.Errorf("no plugin %q", name)
	}
	m, err := loadPlugin(p.Dir, p.Name, p.Source)
	if err != nil {
		return err
	}
	sf, err := SurfaceOf(m)
	if err != nil {
		return err
	}
	return s.update(name, func(p *Installed) { p.Approved = sf })
}

func gitFetch(ctx context.Context, url, ref, dir string) (string, error) {
	if strings.HasPrefix(url, "-") || strings.ContainsAny(ref, " \t\n") || strings.HasPrefix(ref, "-") {
		return "", errors.New("invalid git URL or ref")
	}
	git := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		c := exec.CommandContext(cctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.symlinks=false",
			"-c", "submodule.recurse=false", "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never"}, args...)...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		out, err := c.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git("init", "-q"); err != nil {
		return "", err
	}
	want := orStr(ref, "HEAD")
	if _, err := git("fetch", "-q", "--depth", "1", "--no-tags", "--no-recurse-submodules", url, want); err != nil {
		return "", err
	}
	if _, err := git("checkout", "-q", "FETCH_HEAD"); err != nil {
		return "", err
	}
	sha, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return sha, os.RemoveAll(filepath.Join(dir, ".git"))
}

// copyTree copies regular files (symlinks are skipped: they could point anywhere).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		t := filepath.Join(dst, rel)
		switch {
		case e.IsDir():
			return os.MkdirAll(t, 0o700)
		case !e.Type().IsRegular():
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, _ := e.Info()
		return os.WriteFile(t, b, fi.Mode().Perm()&0o755)
	})
}

func nameFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	return u[strings.LastIndexAny(u, "/:")+1:]
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
