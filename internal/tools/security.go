package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ─────────────────────────── permission policy ───────────────────────────

type Decision int

const (
	Deny Decision = iota
	Allow
	AllowAlways
)

// Asker is implemented by the UI; it blocks until the user answers.
type Asker func(ctx context.Context, tool, summary string, danger bool) Decision

type Policy struct {
	mode    string // ask | edits | yolo; via Mode/SetMode (changed by the UI mid-turn)
	Ask     Asker
	Trusted map[string]bool // MCP servers trusted in config
	mu      sync.Mutex
	always  map[string]bool
}

func NewPolicy(mode string, ask Asker) *Policy {
	return &Policy{mode: mode, Ask: ask, always: map[string]bool{}, Trusted: map[string]bool{}}
}

func (p *Policy) Mode() string        { p.mu.Lock(); defer p.mu.Unlock(); return p.mode }
func (p *Policy) SetMode(mode string) { p.mu.Lock(); p.mode = mode; p.mu.Unlock() }

var (
	// Never run, regardless of mode.
	reForbidden = regexp.MustCompile(`(?i)rm\s+-[a-z]*r[a-z]*f?[a-z]*\s+(/|~|\$HOME|/\*)(\s|$)|mkfs|dd\s+.*of=/dev/(sd|nvme|vd|mmcblk)|:\(\)\s*\{|>\s*/dev/(sd|nvme)|chmod\s+-R\s+[0-7]*7\s+/(\s|$)`)
	// Always ask, even in edits mode; highlighted red.
	reDanger = regexp.MustCompile(`(?i)\b(sudo|doas|su)\b|rm\s+-[a-z]*[rf]|git\s+(push|reset\s+--hard|clean\s+-[a-z]*f|checkout\s+--)|curl[^|]*\|\s*(ba|z)?sh|wget[^|]*\|\s*(ba|z)?sh|chmod\s+(-R\s+)?777|>\s*/etc/|systemctl|pacman\s+-S|npm\s+publish|cargo\s+publish|docker\s+(rm|system\s+prune)|kill(all)?\s`)
	// Read-only commands auto-approved when they contain no shell metacharacters.
	reSafe = regexp.MustCompile(`^(ls|pwd|cat|head|tail|wc|file|stat|tree|which|echo|date|env\s+--?help|rg|grep|find(\s+[^-]|\s*$)|git\s+(status|diff|log|show|branch|blame|ls-files|rev-parse)|go\s+(build|vet|test|list|version|env|doc)|gofmt\s+-l|cargo\s+(check|build|test|clippy|fmt\s+--check|tree)|npm\s+(test|run\s+(test|lint|build|typecheck))|pnpm\s+(test|lint|build)|yarn\s+(test|lint|build)|pytest|python3?\s+-m\s+(pytest|compileall)|ruff\s+check|mypy|make\s+(test|check|lint|build)?$|tsc\s+--noEmit)\b`)
	reMeta = regexp.MustCompile("[;&|`$<>(){}\\n]")
	// Flags/paths that make an otherwise read-only command write or escape the workspace.
	reUnsafeArg = regexp.MustCompile(`(^|\s)(-delete|-exec|-execdir|-ok|-fprint\S*|--output\S*|-D|-d|-m|-M)(\s|$)|(^|\s)(/|~)|(^|[\s/])\.\.(/|\s|$)`)
)

// PlanDenied is the reason given for a mutation refused in plan mode.
const PlanDenied = " (plan mode is read-only: investigate and propose the change instead of making it; the user leaves plan mode with /code)"

type restrictKey struct{}

// WithRestriction marks the calls made under ctx as coming from a model
// that is measured as easily baited by injected instructions (ADR 013): in
// edits and yolo modes, every edit and every command that isn't read-only
// needs confirmation, and "always" doesn't apply.
func WithRestriction(ctx context.Context, why string) context.Context {
	return context.WithValue(ctx, restrictKey{}, why)
}

func restriction(ctx context.Context) string { s, _ := ctx.Value(restrictKey{}).(string); return s }

func (p *Policy) Check(ctx context.Context, t *Tool, name, summary string) (bool, string) {
	key, mode := name, p.Mode()
	if why := restriction(ctx); why != "" && (mode == "edits" || mode == "yolo") && t.Kind != ReadOnly {
		if t.Kind == Exec {
			cmd := strings.TrimSpace(summary)
			if reForbidden.MatchString(cmd) {
				return false, " (command is on the forbidden list)"
			}
			if reSafe.MatchString(cmd) && !reDanger.MatchString(cmd) && !reMeta.MatchString(cmd) && !reUnsafeArg.MatchString(cmd) {
				return true, ""
			}
		}
		danger := t.Kind == Exec && reDanger.MatchString(summary)
		if p.Ask == nil {
			return false, " (non-interactive: " + why + ", so its edits and commands need a person to confirm; rerun with another model, or interactively)"
		}
		if p.Ask(ctx, name, summary+"  ["+why+"]", danger) == Deny {
			return false, " (denied by user)"
		}
		return true, ""
	}
	if mode == "plan" { // read, search and safe commands only
		switch t.Kind {
		case ReadOnly:
			return true, ""
		case Exec:
			cmd := strings.TrimSpace(summary)
			if reSafe.MatchString(cmd) && !reDanger.MatchString(cmd) && !reMeta.MatchString(cmd) && !reUnsafeArg.MatchString(cmd) {
				return true, ""
			}
		}
		return false, PlanDenied
	}
	switch t.Kind {
	case ReadOnly:
		return true, ""
	case Exec:
		cmd := strings.TrimSpace(summary)
		if reForbidden.MatchString(cmd) {
			return false, " (command is on the forbidden list)"
		}
		danger := reDanger.MatchString(cmd)
		if !danger && reSafe.MatchString(cmd) && !reMeta.MatchString(cmd) && !reUnsafeArg.MatchString(cmd) {
			return true, ""
		}
		if mode == "yolo" && !danger {
			return true, ""
		}
		f := strings.Fields(cmd)
		key = "bash:" + strings.Join(f[:min(2, len(f))], " ")
		if !danger && p.isAlways(key) {
			return true, ""
		}
		return p.ask(ctx, name, summary, danger, key)
	case Edit:
		if mode == "edits" || mode == "yolo" || p.isAlways("edit") {
			return true, ""
		}
		key = "edit"
	case External:
		srv := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)[0]
		p.mu.Lock()
		trusted := p.Trusted[srv]
		p.mu.Unlock()
		if trusted || mode == "yolo" || p.isAlways(name) {
			return true, ""
		}
	}
	return p.ask(ctx, name, summary, false, key)
}

// Always lists what was allowed for the rest of the session ("always").
func (p *Policy) Always() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.always))
	for k := range p.always {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *Policy) isAlways(k string) bool { p.mu.Lock(); defer p.mu.Unlock(); return p.always[k] }

func (p *Policy) ask(ctx context.Context, name, summary string, danger bool, key string) (bool, string) {
	if p.Ask == nil {
		switch {
		case danger:
			return false, " (non-interactive: potentially destructive commands always need a person to confirm)"
		case strings.HasPrefix(key, "bash:"):
			return false, " (non-interactive, mode " + p.Mode() + ": only single read-only/build/test commands run without confirmation — no &&, ;, |, redirects or paths outside the workspace; run checks one at a time, e.g. `go test ./...`)"
		}
		return false, " (non-interactive, mode " + p.Mode() + ": this needs confirmation; the user can rerun with --mode edits or yolo)"
	}
	switch p.Ask(ctx, name, summary, danger) {
	case AllowAlways:
		if !danger {
			p.mu.Lock()
			p.always[key] = true
			p.mu.Unlock()
		}
		return true, ""
	case Allow:
		return true, ""
	}
	return false, ""
}

// ─────────────────────────── sandbox ───────────────────────────

type Sandbox struct {
	Bwrap   string // path to bubblewrap, "" = unsandboxed
	NoNet   bool
	Mask    []string // directories hidden from commands (ternly's own config, cache and data)
	Binds   []string // extra writable directories, e.g. a GOCACHE under /tmp (see Writable)
	scrub   []string
	maxWait time.Duration
}

// NewSandbox enables bubblewrap when present. scrubEnv lists env var names to hide from commands.
func NewSandbox(enable, noNet bool, scrubEnv []string) *Sandbox {
	s := &Sandbox{NoNet: noNet, scrub: scrubEnv, maxWait: 10 * time.Minute}
	if enable {
		if p, err := exec.LookPath("bwrap"); err == nil {
			s.Bwrap = p
		}
	}
	return s
}

func (s *Sandbox) Mode() string {
	switch {
	case s.Bwrap == "":
		return "unsandboxed"
	case s.NoNet:
		return "bwrap, no-net"
	}
	return "bwrap"
}

// Run executes cmd with bash in root. Filesystem is read-only except the
// workspace, caches and /tmp; credential dirs are masked; keys are not inherited.
func (s *Sandbox) Run(ctx context.Context, root, cmd string, timeoutSec int) (string, int, error) {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	to := min(time.Duration(timeoutSec)*time.Second, s.maxWait)
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	argv := []string{"bash", "-c", cmd}
	if s.Bwrap != "" {
		argv = append(s.bwrapArgs(root), argv...)
	}
	c := exec.CommandContext(cctx, argv[0], argv[1:]...)
	c.Dir = root
	c.Env = s.env(root)
	c.Stdin = nil
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) } // whole process group
	c.WaitDelay = 3 * time.Second
	var buf bytes.Buffer
	lw := &limitWriter{w: &buf, n: 4 << 20}
	c.Stdout, c.Stderr = lw, lw
	err := c.Run()
	out := Cap(buf.String(), maxOutBytes)
	if cctx.Err() == context.DeadlineExceeded {
		return out, -1, fmt.Errorf("timed out after %s", to)
	}
	if ctx.Err() != nil {
		return out, -1, ctx.Err()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out, ee.ExitCode(), nil
	}
	return out, 0, err
}

// Output runs argv (no shell) in root under the same sandbox as Run and
// returns its full stdout; stderr (capped) is folded into the error. For
// harness-driven tools such as `go list` that read the untrusted repository.
func (s *Sandbox) Output(ctx context.Context, root string, env []string, argv ...string) ([]byte, error) {
	if s.Bwrap != "" {
		argv = append(s.bwrapArgs(root), argv...)
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = root
	c.Env = append(s.env(root), env...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 3 * time.Second
	var stderr bytes.Buffer
	c.Stderr = &limitWriter{w: &stderr, n: 64 << 10}
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, Cap(stderr.String(), 2000))
	}
	return out, nil
}

func (s *Sandbox) bwrapArgs(root string) []string {
	home, _ := os.UserHomeDir()
	a := []string{s.Bwrap, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--new-session"}
	if s.NoNet {
		a = append(a, "--unshare-net")
	}
	// writable: workspace + toolchain caches
	for _, d := range writableHomeDirs {
		if p := filepath.Join(home, d); exists(p) {
			a = append(a, "--bind", p, p)
		}
	}
	a = append(a, "--bind", root, root)
	for _, p := range s.Binds { // after --tmpfs /tmp, so a bind under /tmp is visible again
		if exists(p) {
			a = append(a, "--bind", p, p)
		}
	}
	// masked: credentials
	for _, d := range []string{".ssh", ".gnupg", ".aws", ".azure", ".config/gcloud", ".kube", ".docker", ".password-store", ".mozilla", ".config/google-chrome", ".config/chromium"} {
		if p := filepath.Join(home, d); exists(p) {
			a = append(a, "--tmpfs", p)
		}
	}
	for _, p := range s.Mask { // after the writable ~/.cache bind, so it wins
		if exists(p) {
			a = append(a, "--tmpfs", p)
		}
	}
	for _, f := range []string{".netrc", ".git-credentials", ".npmrc", ".pypirc"} {
		if p := filepath.Join(home, f); exists(p) {
			a = append(a, "--ro-bind", "/dev/null", p)
		}
	}
	return append(a, "--chdir", root, "--")
}

// writableHomeDirs are the toolchain caches the sandbox binds read-write.
var writableHomeDirs = []string{".cache", "go", ".cargo", ".npm", ".rustup", ".local/share/pnpm", ".gradle", ".m2"}

// Writable reports whether commands in the sandbox can write path and have
// the result seen outside (it lies in the workspace or a bound cache).
func (s *Sandbox) Writable(root, path string) bool {
	if s.Bwrap == "" {
		return true
	}
	under := func(dir string) bool { return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) }
	if under(root) {
		return true
	}
	home, _ := os.UserHomeDir()
	for _, d := range writableHomeDirs {
		if p := filepath.Join(home, d); under(p) && exists(p) {
			return true
		}
	}
	for _, b := range s.Binds {
		if under(b) {
			return true
		}
	}
	return false
}

func (s *Sandbox) env(root string) []string {
	hide := map[string]bool{}
	for _, k := range s.scrub {
		hide[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if hide[k] || strings.HasSuffix(k, "_API_KEY") {
			continue
		}
		out = append(out, kv)
	}
	// A TMPDIR the sandbox can't write (hidden by its private /tmp, or read-only
	// like the rest of the filesystem) breaks every tool that makes a temp
	// file, go build included; use the sandbox's own /tmp instead.
	if t := os.Getenv("TMPDIR"); s.Bwrap != "" && t != "" && !s.Writable(root, t) {
		out = append(out, "TMPDIR=/tmp")
	}
	return append(out, "TERNLY=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1", "CI=1")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len() < l.n {
		l.w.Write(p[:min(len(p), l.n-l.w.Len())])
	}
	return len(p), nil
}

// ─────────────────────────── redaction ───────────────────────────

// Redactor strips known secret values from anything sent back to a model.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

func NewRedactor(keys map[string]string) *Redactor {
	r := &Redactor{}
	for k, v := range keys {
		if len(v) >= 12 && (strings.Contains(k, "KEY") || strings.Contains(k, "TOKEN") || strings.Contains(k, "SECRET")) {
			r.secrets = append(r.secrets, v)
		}
	}
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

// Add registers another secret value (e.g. a plugin's configured token).
func (r *Redactor) Add(v string) {
	if len(v) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, v)
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

func (r *Redactor) Apply(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.secrets {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return s
}
