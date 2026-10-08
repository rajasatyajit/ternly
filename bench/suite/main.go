// Command suite runs ternly's Phase C task suite (ADR 025): coding tasks on
// pinned public repositories and on ternly itself, each judged by a hidden
// test oracle that the model never sees.
//
//	go run ./bench/suite fetch                       # clone the pinned repositories into the cache
//	go run ./bench/suite validate                    # every oracle fails on the task, passes on its reference
//	go run ./bench/suite run -models auto,ollama/kimi-k3:cloud -runs 2 -out DIR
//	go run ./bench/suite report DIR/*.jsonl          # pass@1 with Wilson intervals, per model and class
//	go run ./bench/suite repro DIR/*.jsonl           # outcome agreement and diff similarity per task
//
// Downloaded repositories are untrusted: they are fetched with no hooks and
// no checkout filters, and their code (the oracle's tests) runs only inside
// bubblewrap with no network and the user's secrets masked.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/eval"
	"github.com/rajasatyajit/ternly/internal/gitenv"
)

type edit struct {
	File   string `json:"file,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	Write  string `json:"write,omitempty"` // replace this file with From (relative to the suite dir)
	From   string `json:"from,omitempty"`
	Remove string `json:"remove,omitempty"`
}

type repo struct {
	URL    string `json:"url"` // "self": ternly's own repository at Commit
	Commit string `json:"commit"`
	Lang   string `json:"lang"`
	Prep   []edit `json:"prep,omitempty"`
}

type task struct {
	ID        string            `json:"id"`
	Repo      string            `json:"repo"`
	Class     string            `json:"class"` // bugfix | feature
	Prompt    string            `json:"prompt"`
	Seed      []edit            `json:"seed"`
	Reference []edit            `json:"reference,omitempty"` // a correct solution, applied after the seed (bugfix: the seed reversed)
	Oracle    map[string]string `json:"oracle"`              // workspace path ← suite file
	Cmd       []string          `json:"cmd"`
}

type suite struct {
	Version string          `json:"version"`
	Repos   map[string]repo `json:"repos"`
	Tasks   []task          `json:"tasks"`
	dir     string
	root    string // ternly's module root
}

// Result is one run: a task, a model (or "auto": routed) and its outcome.
type Result struct {
	Suite     string   `json:"suite_version"`
	Task      string   `json:"task"`
	Class     string   `json:"class"`
	Lang      string   `json:"lang"`
	Model     string   `json:"model"` // as asked: "auto" or a pinned key
	Arm       string   `json:"arm,omitempty"`
	Run       int      `json:"run"`
	Pass      bool     `json:"pass"`
	Outcome   string   `json:"outcome"` // pass | oracle-fail | timeout | error | no-change
	Seconds   float64  `json:"seconds"`
	CostUSD   float64  `json:"cost_usd"`
	TokensIn  int      `json:"tokens_in"`
	TokensOut int      `json:"tokens_out"`
	Models    []string `json:"models"`             // every model the turn used, in order
	Switches  []string `json:"switches,omitempty"` // routing reasons after the first (escalation, failover)
	Verified  string   `json:"verified,omitempty"` // ternly's own verify verdict: verified | failed | unverified | ""
	Quota     bool     `json:"quota,omitempty"`    // a usage limit was hit during the run
	Error     string   `json:"error,omitempty"`    // ternly's error line, if any
	Oracle    string   `json:"oracle,omitempty"`   // the oracle's last lines when it failed
	Diff      string   `json:"diff"`               // the model's change (git diff of the workspace)
	DiffLines int      `json:"diff_lines"`         // changed lines
	At        string   `json:"at"`
	Env       []string `json:"env_flags,omitempty"` // TERNLY_* toggles for this arm (levers)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: suite fetch|validate|run|report|repro …")
		os.Exit(2)
	}
	s, err := load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "fetch":
		err = s.fetchAll()
	case "validate":
		err = s.validateAll(args)
	case "run":
		err = s.runCmd(args)
	case "report":
		err = report(os.Stdout, args)
	case "repro":
		err = repro(os.Stdout, args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "suite:", err)
		os.Exit(1)
	}
}

func load() (*suite, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "bench", "suite")
	b, err := os.ReadFile(filepath.Join(dir, "suite.json"))
	if err != nil {
		return nil, err
	}
	var s suite
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("suite.json: %w", err)
	}
	s.dir, s.root = dir, root
	for _, t := range s.Tasks {
		if _, ok := s.Repos[t.Repo]; !ok {
			return nil, fmt.Errorf("task %s: unknown repo %q", t.ID, t.Repo)
		}
	}
	return &s, nil
}

func moduleRoot() (string, error) {
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "bench", "suite", "suite.json")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", errors.New("run from inside the ternly repository")
		}
		d = p
	}
}

// cacheDir holds the fetched repositories and the oracle's Go build cache.
func cacheDir() string {
	if d := os.Getenv("SUITE_CACHE"); d != "" {
		return d
	}
	d, _ := os.UserCacheDir()
	return filepath.Join(d, "ternly-suite")
}

// ─────────────────────────── fetch ───────────────────────────

func (s *suite) fetchAll() error {
	for name := range s.Repos {
		if err := s.fetch(name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Println("fetched", name)
	}
	return nil
}

func (s *suite) repoDir(name string) string {
	return filepath.Join(cacheDir(), "repos", name+"@"+s.Repos[name].Commit)
}

func (s *suite) fetch(name string) error {
	r, dst := s.Repos[name], s.repoDir(name)
	if _, err := os.Stat(filepath.Join(dst, ".fetched")); err == nil {
		return nil
	}
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ctx := context.Background()
	if r.URL == "self" { // ternly at a commit of its own history
		arch := gitenv.Command(ctx, "-C", s.root, "archive", "--format=tar", r.Commit)
		b, err := arch.Output()
		if err != nil {
			return fmt.Errorf("git archive %s: %v", r.Commit, err)
		}
		tar := exec.Command("tar", "-x", "-C", dst)
		tar.Stdin = bytes.NewReader(b)
		if out, err := tar.CombinedOutput(); err != nil {
			return fmt.Errorf("tar: %v %s", err, out)
		}
	} else {
		for _, args := range [][]string{
			{"init", "-q"},
			{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "fetch", "-q", "--depth", "1", r.URL, r.Commit},
			{"-c", "core.hooksPath=/dev/null", "-c", "core.symlinks=false", "-c", "filter.lfs.smudge=", "checkout", "-q", "FETCH_HEAD"},
		} {
			c := gitenv.Command(ctx, append([]string{"-C", dst}, args...)...)
			c.Env = append(c.Env, "GIT_TERMINAL_PROMPT=0")
			if out, err := c.CombinedOutput(); err != nil {
				return fmt.Errorf("git %s: %v %s", args[0], err, out)
			}
		}
		_ = os.RemoveAll(filepath.Join(dst, ".git"))
	}
	return os.WriteFile(filepath.Join(dst, ".fetched"), []byte(r.Commit+"\n"), 0o644)
}

// ─────────────────────────── workspaces ───────────────────────────

// prepare makes a fresh workspace for t at dst: the pinned repository, its
// prep, the task's seed, then extra edits; committed, so the model's change
// is the workspace's git diff.
func (s *suite) prepare(t task, dst string, extra []edit) error {
	if err := s.fetch(t.Repo); err != nil {
		return err
	}
	if err := copyTree(s.repoDir(t.Repo), dst); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dst, ".fetched"))
	for _, list := range [][]edit{s.Repos[t.Repo].Prep, t.Seed, extra} {
		if err := s.apply(dst, list); err != nil {
			return fmt.Errorf("%s: %w", t.ID, err)
		}
	}
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=suite", "-c", "user.email=suite@example.invalid", "commit", "-q", "-m", "task"}} {
		c := gitenv.Command(ctx, append(append([]string{"-C", dst}, untrustedGit...), args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v %s", args[0], err, out)
		}
	}
	return nil
}

func (s *suite) apply(dir string, list []edit) error {
	for _, e := range list {
		switch {
		case e.Remove != "":
			if err := os.RemoveAll(filepath.Join(dir, e.Remove)); err != nil {
				return err
			}
		case e.Write != "":
			b, err := os.ReadFile(filepath.Join(s.dir, e.From))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, e.Write), b, 0o644); err != nil {
				return err
			}
		default:
			p := filepath.Join(dir, e.File)
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if n := strings.Count(string(b), e.Old); n != 1 {
				return fmt.Errorf("edit of %s: the old text occurs %d times, want 1", e.File, n)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(string(b), e.Old, e.New, 1)), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// untrustedGit hardens git run on a workspace holding untrusted files: no
// hooks, no fsmonitor, no filters or diff drivers a repository's attributes
// could name.
var untrustedGit = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.symlinks=false", "-c", "diff.external=", "-c", "filter.lfs.smudge=", "-c", "filter.lfs.process="}

func reverse(list []edit) []edit {
	out := make([]edit, len(list))
	for i, e := range list {
		out[len(list)-1-i] = edit{File: e.File, Old: e.New, New: e.Old}
	}
	return out
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			return nil // no links into a workspace: the repository is untrusted
		case !d.Type().IsRegular():
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(out, b, info.Mode().Perm()|0o200)
	})
}

// ─────────────────────────── the oracle ───────────────────────────

// unsafeNoSandbox is the explicit opt-in (-unsafe-no-sandbox) to run oracles
// without bubblewrap. Never the default: an oracle runs the task
// repository's code, which is untrusted (ADR 016: no sandbox, no automatic
// execution of repository code).
var unsafeNoSandbox bool

// lookBwrap finds bubblewrap (a variable so tests can take it away).
var lookBwrap = func() (string, error) { return exec.LookPath("bwrap") }

// errNoSandbox: an oracle was refused because bubblewrap is missing.
var errNoSandbox = errors.New("bwrap not found: refusing to run an untrusted oracle unsandboxed (pass -unsafe-no-sandbox to override)")

// oracle copies the hidden tests into ws and runs the task's command in
// bubblewrap (no network; the user's secrets masked) with a scrubbed
// environment. It reports whether the tests passed and the last lines of
// their output; without bubblewrap it refuses (errNoSandbox).
func (s *suite) oracle(t task, ws string) (bool, string, error) {
	bw, err := lookBwrap()
	if err != nil && !unsafeNoSandbox {
		return false, "", errNoSandbox
	}
	for dst, src := range t.Oracle {
		b, err := os.ReadFile(filepath.Join(s.dir, src))
		if err != nil {
			return false, "", err
		}
		p := filepath.Join(ws, dst)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return false, "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_ = os.MkdirAll(filepath.Join(cacheDir(), "gocache"), 0o755)
	var c *exec.Cmd
	if bw != "" {
		c = exec.CommandContext(ctx, bw, append(sandboxArgs(ws), t.Cmd...)...)
	} else { // -unsafe-no-sandbox only
		c = exec.CommandContext(ctx, t.Cmd[0], t.Cmd[1:]...)
	}
	c.Dir = ws
	c.Env = oracleEnv(os.Environ())
	out, err := c.CombinedOutput()
	return err == nil, tail(string(out), 12), nil
}

// oracleEnv is the oracle's environment: base without secrets (*_API_KEY,
// *_TOKEN, ternly's own settings) and without any inherited git environment
// (ADR 024), plus offline toolchains.
func oracleEnv(base []string) []string {
	return append(scrub(base), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off",
		"GOCACHE="+filepath.Join(cacheDir(), "gocache"), "CARGO_NET_OFFLINE=true", "PYTHONDONTWRITEBYTECODE=1")
}

// scrub drops secrets, ternly's settings, HOME/XDG and every GIT_* variable
// (gitenv.Clean adds GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1).
func scrub(base []string) []string {
	var env []string
	for _, kv := range gitenv.Clean(base) {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasSuffix(k, "_API_KEY") || strings.HasSuffix(k, "_TOKEN") || strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_PASSWORD"):
		case strings.HasPrefix(k, "TERNLY_") || strings.HasPrefix(k, "XDG_") || k == "HOME" || k == "SSH_AUTH_SOCK" || k == "GH_TOKEN" || k == "GITHUB_TOKEN":
		case strings.HasPrefix(k, "GO") && (k == "GOFLAGS" || k == "GOTOOLCHAIN" || k == "GOPROXY" || k == "GOCACHE"):
		default:
			env = append(env, kv)
		}
	}
	return env
}

func sandboxArgs(ws string) []string {
	home, _ := os.UserHomeDir()
	a := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--bind", ws, ws, "--bind", filepath.Join(cacheDir(), "gocache"), filepath.Join(cacheDir(), "gocache"),
		"--unshare-net", "--unshare-pid", "--die-with-parent", "--chdir", ws}
	for _, d := range []string{".ssh", ".aws", ".gnupg", ".config", ".local/share/ternly", ".netrc", ".git-credentials", ".cargo/credentials.toml", ".npmrc"} {
		p := filepath.Join(home, d)
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				a = append(a, "--tmpfs", p)
			} else {
				a = append(a, "--ro-bind", "/dev/null", p)
			}
		}
	}
	// The user's runtime directory holds the SSH agent's and other sockets:
	// reachable through the read-only root even with no network.
	for _, d := range []string{os.Getenv("XDG_RUNTIME_DIR"), fmt.Sprintf("/run/user/%d", os.Getuid())} {
		if fi, err := os.Stat(d); d != "" && err == nil && fi.IsDir() {
			a = append(a, "--tmpfs", d)
		}
	}
	return append(a, "--")
}

func tail(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// ─────────────────────────── validate ───────────────────────────

// validateAll proves each oracle: it fails on the task as given and passes
// on the reference solution (a bugfix's reference is its seed reversed).
func (s *suite) validateAll(args []string) error {
	fl := flag.NewFlagSet("validate", flag.ExitOnError)
	only := fl.String("tasks", "", "regexp of task ids")
	fl.BoolVar(&unsafeNoSandbox, "unsafe-no-sandbox", false, "run oracles (untrusted repository code) without bubblewrap")
	_ = fl.Parse(args)
	re := regexp.MustCompile(*only)
	tmp, err := os.MkdirTemp("", "suite-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bad := 0
	for _, t := range s.Tasks {
		if !re.MatchString(t.ID) {
			continue
		}
		ref := t.Reference
		if len(ref) == 0 {
			ref = reverse(t.Seed)
		}
		seeded, solved := filepath.Join(tmp, t.ID+"-task"), filepath.Join(tmp, t.ID+"-ref")
		if err := s.prepare(t, seeded, nil); err != nil {
			return err
		}
		if err := s.prepare(t, solved, ref); err != nil {
			return err
		}
		p1, out1, err := s.oracle(t, seeded)
		if err != nil {
			return err
		}
		p2, out2, err := s.oracle(t, solved)
		if err != nil {
			return err
		}
		status := "ok"
		if p1 || !p2 {
			status, bad = "BAD", bad+1
		}
		fmt.Printf("%-24s task: %-4s reference: %-4s %s\n", t.ID, verdict(p1), verdict(p2), status)
		if p1 {
			fmt.Printf("    the oracle passes on the unsolved task:\n%s\n", indent(out1))
		}
		if !p2 {
			fmt.Printf("    the oracle fails on the reference:\n%s\n", indent(out2))
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d oracle(s) invalid", bad)
	}
	return nil
}

func verdict(pass bool) string {
	if pass {
		return "pass"
	}
	return "fail"
}

func indent(s string) string { return "      " + strings.ReplaceAll(s, "\n", "\n      ") }

// ─────────────────────────── run ───────────────────────────

type job struct {
	t     task
	model string
	run   int
	arm   string
	env   []string
}

func (s *suite) runCmd(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	models := fl.String("models", "auto", "comma-separated: auto (routed) or model keys to pin")
	runs := fl.Int("runs", 1, "runs of each task per model")
	only := fl.String("tasks", "", "regexp of task ids")
	out := fl.String("out", "", "output directory (results.jsonl is appended)")
	par := fl.Int("parallel", 2, "runs at once")
	timeout := fl.Duration("timeout", 12*time.Minute, "per run")
	arm := fl.String("arm", "", "a label for this configuration (lever A/B)")
	envs := fl.String("env", "", "comma-separated KEY=VALUE set for ternly in this arm (lever toggles)")
	bin := fl.String("bin", "", "ternly binary (default: built from this checkout)")
	extra := fl.String("args", "", "extra ternly arguments, space-separated")
	fl.BoolVar(&unsafeNoSandbox, "unsafe-no-sandbox", false, "run oracles (untrusted repository code) without bubblewrap")
	_ = fl.Parse(args)
	if _, err := lookBwrap(); err != nil && !unsafeNoSandbox {
		return errNoSandbox // before spending any model quota
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	if err := os.MkdirAll(filepath.Join(*out, "logs"), 0o755); err != nil {
		return err
	}
	if *bin == "" {
		*bin = filepath.Join(*out, "ternly")
		b := exec.Command("go", "build", "-o", *bin, ".")
		b.Dir, b.Env = s.root, append(gitenv.Clean(os.Environ()), "CGO_ENABLED=0")
		if o, err := b.CombinedOutput(); err != nil {
			return fmt.Errorf("build: %v %s", err, o)
		}
	}
	re := regexp.MustCompile(*only)
	var env []string
	if *envs != "" {
		env = strings.Split(*envs, ",")
	}
	var jobs []job
	for r := 0; r < *runs; r++ { // run-major: an interrupted run still covers every task
		for _, t := range s.Tasks {
			if !re.MatchString(t.ID) {
				continue
			}
			for _, m := range strings.Split(*models, ",") {
				jobs = append(jobs, job{t: t, model: strings.TrimSpace(m), run: r, arm: *arm, env: env})
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(*out, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var mu sync.Mutex
	ch := make(chan job)
	var wg sync.WaitGroup
	for range max(*par, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				res := s.one(j, *bin, *out, *timeout, strings.Fields(*extra))
				b, _ := json.Marshal(res)
				mu.Lock()
				_, _ = f.Write(append(b, '\n'))
				fmt.Printf("%-24s %-28s run %d  %-11s %6.0fs  %s\n", res.Task, res.Model, res.Run, res.Outcome, res.Seconds, strings.Join(res.Models, " → "))
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	return nil
}

var (
	reModel = regexp.MustCompile(`(?m)^◆ (\S+) \(T\d+, [^)]*\) — (.*)$`)
	reCost  = regexp.MustCompile(`(?m)^\$([0-9.]+) · [0-9]+% cache hits · ([0-9]+) tokens in, ([0-9]+) out$`)
	reError = regexp.MustCompile(`(?m)^error: (.*)$`)
	reQuota = regexp.MustCompile(`(?i)usage limit|quota|rate.?limit|too many requests|exhausted`)
)

func (s *suite) one(j job, bin, out string, timeout time.Duration, extra []string) Result {
	res := Result{Suite: s.Version, Task: j.t.ID, Class: j.t.Class, Lang: s.Repos[j.t.Repo].Lang, Model: j.model, Arm: j.arm, Run: j.run,
		At: time.Now().UTC().Format(time.RFC3339), Env: j.env}
	base := filepath.Join(out, "work", fmt.Sprintf("%s-%s-%s-%d-%d", j.t.ID, sanitize(j.model), sanitize(j.arm), j.run, time.Now().UnixNano()))
	ws, home := filepath.Join(base, "ws"), filepath.Join(base, "home")
	defer os.RemoveAll(base)
	if err := s.prepare(j.t, ws, nil); err != nil {
		res.Outcome, res.Error = "error", err.Error()
		return res
	}
	for _, d := range []string{".config", ".cache", ".local/share", ".local/state", "tmp"} {
		_ = os.MkdirAll(filepath.Join(home, d), 0o700)
	}
	a := []string{"-C", ws, "--mode", "edits", "--new"}
	if j.model != "auto" {
		a = append(a, "--model", j.model)
	}
	a = append(append(a, extra...), "-p", j.t.Prompt)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, bin, a...)
	c.Dir = ws
	c.Env = append(isolated(home), j.env...)
	c.WaitDelay = 10 * time.Second
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	t0 := time.Now()
	err := c.Run()
	res.Seconds = time.Since(t0).Seconds()
	trace := se.String()
	for _, m := range reModel.FindAllStringSubmatch(trace, -1) {
		if len(res.Models) == 0 || res.Models[len(res.Models)-1] != m[1] {
			res.Models = append(res.Models, m[1])
			if len(res.Models) > 1 {
				res.Switches = append(res.Switches, m[2])
			}
		}
	}
	if m := reCost.FindStringSubmatch(trace); m != nil {
		res.CostUSD, _ = strconv.ParseFloat(m[1], 64)
		res.TokensIn, _ = strconv.Atoi(m[2])
		res.TokensOut, _ = strconv.Atoi(m[3])
	}
	switch {
	case strings.Contains(trace, "✓ verified"):
		res.Verified = "verified"
	case strings.Contains(trace, "✗ verification failed"):
		res.Verified = "failed"
	case strings.Contains(trace, "\n  ? "):
		res.Verified = "unverified"
	}
	if m := reError.FindAllStringSubmatch(trace, -1); m != nil {
		res.Error = m[len(m)-1][1]
	}
	res.Quota = reQuota.MatchString(res.Error) || strings.Contains(trace, "usage limit")
	d := gitenv.Command(context.Background(), append(append([]string{"-C", ws}, untrustedGit...), "diff", "--no-ext-diff", "--no-textconv")...)
	diff, _ := d.Output()
	add := gitenv.Command(context.Background(), append(append([]string{"-C", ws}, untrustedGit...), "status", "--porcelain")...)
	st, _ := add.Output()
	res.Diff = string(diff)
	res.DiffLines = changedLines(res.Diff)
	if untracked := untrackedFiles(string(st)); len(untracked) > 0 {
		res.Diff += "\n# untracked: " + strings.Join(untracked, ", ") + "\n"
	}
	pass, otail, oerr := s.oracle(j.t, ws)
	if oerr != nil {
		res.Outcome, res.Error = "error", oerr.Error()
		return res
	}
	res.Pass = pass
	switch {
	case pass:
		res.Outcome = "pass"
	case ctx.Err() != nil:
		res.Outcome = "timeout"
	case strings.TrimSpace(string(st)) == "":
		res.Outcome = "no-change"
	case err != nil && res.Error != "":
		res.Outcome = "error"
	default:
		res.Outcome = "oracle-fail"
	}
	if !pass {
		res.Oracle = otail
	}
	_ = os.WriteFile(filepath.Join(out, "logs", sanitize(fmt.Sprintf("%s-%s-%s-%d.log", j.t.ID, j.model, j.arm, j.run))), []byte(trace+"\n--- answer ---\n"+so.String()), 0o644)
	return res
}

// isolated is the environment of one run: its own HOME and XDG directories
// (the harness tripwire, eval.HarnessIsolated, refuses anything else), no
// inherited git environment, no API keys, offline toolchains.
func isolated(home string) []string {
	env := scrub(os.Environ())
	gocache, _ := exec.Command("go", "env", "GOCACHE").Output()
	gomod, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	return append(env, "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"), "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"TMPDIR="+filepath.Join(home, "tmp"), "TERNLY_HARNESS=1", "TERNLY_BACKGROUND_EVAL=1",
		"GOCACHE="+strings.TrimSpace(string(gocache)), "GOMODCACHE="+strings.TrimSpace(string(gomod)),
		"GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOPROXY=off", "CARGO_NET_OFFLINE=true")
}

func sanitize(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(s, "_")
}

func changedLines(diff string) int {
	n := 0
	for _, l := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
			n++
		}
	}
	return n
}

func untrackedFiles(porcelain string) []string {
	var out []string
	for _, l := range strings.Split(porcelain, "\n") {
		if strings.HasPrefix(l, "?? ") {
			out = append(out, strings.TrimPrefix(l, "?? "))
		}
	}
	return out
}

// ─────────────────────────── report ───────────────────────────

func readResults(paths []string) ([]Result, error) {
	var rs []Result
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var r Result
			if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
				rs = append(rs, r)
			}
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	}
	return rs, nil
}

type agg struct {
	pass, n    int
	secs       []float64
	cost       float64
	in, out    int
	outcomes   map[string]int
	quota, esc int
	firstModel map[string]int
}

func report(w *os.File, paths []string) error {
	rs, err := readResults(paths)
	if err != nil {
		return err
	}
	groups := map[string]*agg{}
	key := func(r Result, class string) string { return r.Model + "\t" + r.Arm + "\t" + class }
	for _, r := range rs {
		for _, class := range []string{r.Class, "all"} {
			k := key(r, class)
			a := groups[k]
			if a == nil {
				a = &agg{outcomes: map[string]int{}, firstModel: map[string]int{}}
				groups[k] = a
			}
			a.n++
			if r.Pass {
				a.pass++
			}
			a.secs = append(a.secs, r.Seconds)
			a.cost += r.CostUSD
			a.in += r.TokensIn
			a.out += r.TokensOut
			a.outcomes[r.Outcome]++
			if r.Quota {
				a.quota++
			}
			if len(r.Models) > 1 {
				a.esc++
			}
			if len(r.Models) > 0 {
				a.firstModel[r.Models[0]]++
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "%-30s %-14s %-8s %7s %-16s %8s %8s %10s %10s  %s\n", "model", "arm", "class", "pass", "pass@1 [95%]", "med s", "cost $", "Mtok in", "Mtok out", "outcomes")
	for _, k := range keys {
		a := groups[k]
		f := strings.Split(k, "\t")
		lo, hi := eval.Counts{Bad: a.pass, N: a.n}.Wilson()
		var oc []string
		for o, n := range a.outcomes {
			oc = append(oc, fmt.Sprintf("%s %d", o, n))
		}
		sort.Strings(oc)
		extra := ""
		if a.quota > 0 {
			extra += fmt.Sprintf("; quota hit %d", a.quota)
		}
		if a.esc > 0 {
			extra += fmt.Sprintf("; switched model %d", a.esc)
		}
		fmt.Fprintf(w, "%-30s %-14s %-8s %3d/%-3d %.2f [%.2f, %.2f] %8.0f %8.4f %10.2f %10.3f  %s%s\n",
			f[0], orDash(f[1]), f[2], a.pass, a.n, float64(a.pass)/float64(a.n), lo, hi, median(a.secs), a.cost, float64(a.in)/1e6, float64(a.out)/1e6, strings.Join(oc, ", "), extra)
		if f[0] == "auto" && f[2] == "all" {
			var fm []string
			for m, n := range a.firstModel {
				fm = append(fm, fmt.Sprintf("%s %d", m, n))
			}
			sort.Strings(fm)
			fmt.Fprintf(w, "%-30s routed to: %s\n", "", strings.Join(fm, ", "))
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func median(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// ─────────────────────────── reproducibility ───────────────────────────

// repro reports, per task and model, over repeated runs: outcome agreement
// (the share of runs with the majority outcome, pass/fail) and diff
// similarity (mean pairwise Jaccard similarity of the changed lines).
func repro(w *os.File, paths []string) error {
	rs, err := readResults(paths)
	if err != nil {
		return err
	}
	groups := map[string][]Result{}
	for _, r := range rs {
		k := r.Task + "\t" + r.Model + "\t" + r.Arm
		groups[k] = append(groups[k], r)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "%-24s %-30s %-10s %4s %10s %10s %8s\n", "task", "model", "arm", "runs", "agreement", "diff sim", "passes")
	var agrees, sims []float64
	for _, k := range keys {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		pass := 0
		for _, r := range g {
			if r.Pass {
				pass++
			}
		}
		agree := float64(max(pass, len(g)-pass)) / float64(len(g))
		var sim []float64
		for i := range g {
			for j := i + 1; j < len(g); j++ {
				sim = append(sim, jaccard(g[i].Diff, g[j].Diff))
			}
		}
		ms := mean(sim)
		agrees, sims = append(agrees, agree), append(sims, ms)
		f := strings.Split(k, "\t")
		fmt.Fprintf(w, "%-24s %-30s %-10s %4d %10.2f %10.2f %5d/%d\n", f[0], f[1], orDash(f[2]), len(g), agree, ms, pass, len(g))
	}
	fmt.Fprintf(w, "mean outcome agreement %.2f, mean diff similarity %.2f over %d task×model groups\n", mean(agrees), mean(sims), len(agrees))
	return nil
}

// jaccard is the similarity of two diffs' sets of changed lines (whitespace
// trimmed); two empty diffs are identical.
func jaccard(a, b string) float64 {
	set := func(d string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(d, "\n") {
			if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
				m[l[:1]+strings.TrimSpace(l[1:])] = true
			}
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) == 0 && len(y) == 0 {
		return 1
	}
	inter := 0
	for k := range x {
		if y[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(x)+len(y)-inter)
}

func mean(x []float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}
