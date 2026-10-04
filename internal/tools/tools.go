// Package tools implements the agent's tools with security as a design
// constraint: workspace path confinement, a permission policy, sandboxed shell,
// secret redaction, and output caps that also keep token usage low.
package tools

import (
	"bufio"
	"bytes"
	"context"
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
	"syscall"

	"github.com/rajasatyajit/ternly/internal/llm"
)

type Kind int

const (
	ReadOnly Kind = iota
	Edit
	Exec
	External
)

type Tool struct {
	Spec    llm.ToolSpec
	Kind    Kind
	Run     func(ctx context.Context, args json.RawMessage) (string, error)
	Summary func(args json.RawMessage) string // one-line description for UI + permission prompt
	schema  *jschema                          // compiled Spec.Schema; nil = not validated
}

type Registry struct {
	Root    string
	Policy  *Policy
	Sandbox *Sandbox
	Redact  *Redactor
	Frame   *Framer
	mu      sync.RWMutex
	tools   map[string]*Tool
	order   []string
}

func NewRegistry(root string, pol *Policy, sb *Sandbox, rd *Redactor) (*Registry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	r := &Registry{Root: abs, Policy: pol, Sandbox: sb, Redact: rd, Frame: NewFramer(), tools: map[string]*Tool{}}
	r.builtin()
	return r, nil
}

func (r *Registry) Add(t *Tool) {
	t.schema = compileSchema(t.Spec.Schema)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Spec.Name]; !ok {
		r.order = append(r.order, t.Spec.Name)
	}
	r.tools[t.Spec.Name] = t
}

func (r *Registry) Get(name string) *Tool { r.mu.RLock(); defer r.mu.RUnlock(); return r.tools[name] }

// Specs are returned in a stable order so the prompt prefix stays cacheable.
func (r *Registry) Specs() []llm.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Spec)
	}
	return out
}

// maxToolBytes is the last-resort cap on any tool's output (~16k tokens).
const maxToolBytes = 64 << 10

// Call validates the arguments, checks permission, runs the tool, and returns
// its output redacted, capped and framed as untrusted data. Harness messages
// (rejections) are not framed: they are the only trusted text in a result.
func (r *Registry) Call(ctx context.Context, tc llm.ToolCall) Result {
	t := r.Get(tc.Name)
	if t == nil {
		msg := fmt.Sprintf("error: unknown tool %q.", tc.Name)
		if near := closest(tc.Name, r.names()); near != "" {
			msg += fmt.Sprintf(" Did you mean %q?", near)
		}
		return Result{Out: msg + " Available tools: " + strings.Join(r.names(), ", "), IsErr: true, Rejected: true}
	}
	args := json.RawMessage(tc.Args)
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := jsonErr(args); err != "" {
		return Result{Out: fmt.Sprintf("error: arguments for %s are not valid JSON (%s). Send one complete JSON object matching the schema; for large content prefer several smaller edit_file calls.", tc.Name, err), IsErr: true, Rejected: true}
	}
	if t.schema != nil {
		errs, fixed := t.schema.validate(args)
		if len(errs) > 0 {
			return Result{Out: fmt.Sprintf("error: invalid arguments for %s:\n- %s\nSchema: %s\nFix the arguments and call the tool again.", tc.Name, strings.Join(errs, "\n- "), Cap(compactJSON(t.Spec.Schema), 1500)), IsErr: true, Rejected: true}
		}
		if fixed != nil {
			args = fixed
		}
	}
	if ok, why := r.Policy.Check(ctx, t, tc.Name, t.Summary(args)); !ok {
		return Result{Out: "permission denied by user" + why + ". Do not retry the same action; ask the user or choose another approach.", IsErr: true, Rejected: true}
	}
	res, err := t.Run(ctx, args)
	if err != nil {
		res = "error: " + err.Error() + "\n" + res
	}
	out, flagged := r.Frame.Wrap(tc.Name, Cap(r.Redact.Apply(res), maxToolBytes))
	return Result{Out: out, IsErr: err != nil, Flagged: flagged}
}

func (r *Registry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// jsonErr explains invalid JSON with a byte offset ("" when valid).
func jsonErr(b []byte) string {
	var v any
	err := json.Unmarshal(b, &v)
	if err == nil {
		return ""
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Sprintf("%v at byte %d of %d", se, se.Offset, len(b))
	}
	if strings.Contains(err.Error(), "unexpected end") {
		return fmt.Sprintf("input ends after %d bytes — truncated", len(b))
	}
	return err.Error()
}

func compactJSON(b []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return string(b)
	}
	return buf.String()
}

// ─────────────────────────── path confinement ───────────────────────────

var ErrOutside = errors.New("path is outside the workspace")

// resolve maps p to an absolute path that must stay inside Root, even through symlinks.
func (r *Registry) resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if strings.HasPrefix(p, "~") {
		return "", ErrOutside
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.Root, p)
	}
	p = filepath.Clean(p)
	// Resolve the longest existing prefix (target may not exist yet).
	existing, rest := p, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	full := filepath.Join(real, rest)
	if full != r.Root && !strings.HasPrefix(full, r.Root+string(filepath.Separator)) {
		return "", ErrOutside
	}
	if strings.Contains(full+string(filepath.Separator), string(filepath.Separator)+".git"+string(filepath.Separator)) {
		return "", errors.New("direct access to .git internals is blocked; use git commands")
	}
	return full, nil
}

func (r *Registry) rel(p string) string {
	if s, err := filepath.Rel(r.Root, p); err == nil {
		return s
	}
	return p
}

// ─────────────────────────── built-in tools ───────────────────────────

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func arg[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true, ".cache": true}

const (
	maxReadLines = 400
	maxLineLen   = 1000
	maxOutBytes  = 12 << 10
	maxReadBytes = 48 << 10
)

func (r *Registry) builtin() {
	r.Add(&Tool{Kind: ReadOnly,
		Spec: llm.ToolSpec{Name: "read_file", Description: "Read a text file. Returns numbered lines. Use offset/limit for large files instead of reading everything.",
			Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based start line"},"limit":{"type":"integer","description":"max lines (default 400)"}},"required":["path"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string {
			v, _ := arg[struct{ Path string }](a)
			return v.Path
		},
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct {
				Path          string
				Offset, Limit int
			}](a)
			if err != nil {
				return "", err
			}
			p, err := r.resolve(v.Path)
			if err != nil {
				return "", err
			}
			f, err := openRegular(p)
			if err != nil {
				return "", err
			}
			defer f.Close()
			head := make([]byte, 8000)
			n, _ := f.Read(head)
			if bytes.IndexByte(head[:n], 0) >= 0 {
				return "", errors.New("binary file")
			}
			_, _ = f.Seek(0, 0)
			if v.Offset < 1 {
				v.Offset = 1
			}
			if v.Limit <= 0 || v.Limit > 2000 {
				v.Limit = maxReadLines
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 4<<20)
			var sb strings.Builder
			line, shown := 0, 0
			for sc.Scan() {
				line++
				if line < v.Offset {
					continue
				}
				if shown == v.Limit || sb.Len() >= maxReadBytes {
					fmt.Fprintf(&sb, "… (truncated; continue with offset=%d)\n", line)
					break
				}
				t := sc.Text()
				if len(t) > maxLineLen {
					t = t[:maxLineLen] + "…"
				}
				fmt.Fprintf(&sb, "%6d\t%s\n", line, t)
				shown++
			}
			if line == 0 {
				return "(empty file)", nil
			}
			return sb.String(), sc.Err()
		}})

	r.Add(&Tool{Kind: Edit,
		Spec: llm.ToolSpec{Name: "edit_file", Description: "Replace an exact, unique snippet in a file (preferred over write_file: far fewer tokens). old_string must match exactly once unless replace_all is true. Empty old_string creates a new file.",
			Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string {
			v, _ := arg[struct {
				Path      string
				NewString string `json:"new_string"`
				OldString string `json:"old_string"`
			}](a)
			return fmt.Sprintf("%s  (-%d +%d lines)", v.Path, lines(v.OldString), lines(v.NewString))
		},
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct {
				Path       string
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}](a)
			if err != nil {
				return "", err
			}
			p, err := r.resolve(v.Path)
			if err != nil {
				return "", err
			}
			if v.OldString == "" {
				if _, err := os.Stat(p); err == nil {
					return "", errors.New("file exists; provide old_string to edit it")
				}
				return r.write(p, v.NewString)
			}
			b, err := readRegular(p)
			if err != nil {
				return "", err
			}
			s := string(b)
			n := strings.Count(s, v.OldString)
			switch {
			case n == 0:
				return "", errors.New("old_string not found — re-read the file; whitespace must match exactly")
			case n > 1 && !v.ReplaceAll:
				return "", fmt.Errorf("old_string matches %d places; add surrounding context or set replace_all", n)
			}
			if v.ReplaceAll {
				s = strings.ReplaceAll(s, v.OldString, v.NewString)
			} else {
				s = strings.Replace(s, v.OldString, v.NewString, 1)
			}
			if _, err := r.write(p, s); err != nil {
				return "", err
			}
			return fmt.Sprintf("edited %s (%d replacement(s))", r.rel(p), max(1, n*boolInt(v.ReplaceAll))), nil
		}})

	r.Add(&Tool{Kind: Edit,
		Spec: llm.ToolSpec{Name: "write_file", Description: "Create or fully overwrite a file. For changes to existing files use edit_file.",
			Schema: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string {
			v, _ := arg[struct{ Path, Content string }](a)
			return fmt.Sprintf("%s  (%d lines)", v.Path, lines(v.Content))
		},
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct{ Path, Content string }](a)
			if err != nil {
				return "", err
			}
			p, err := r.resolve(v.Path)
			if err != nil {
				return "", err
			}
			return r.write(p, v.Content)
		}})

	r.Add(&Tool{Kind: ReadOnly,
		Spec: llm.ToolSpec{Name: "glob", Description: "Find files by glob pattern relative to the workspace, e.g. **/*.go or internal/**/handler*.ts. Skips .git, node_modules, vendor, build dirs.",
			Schema: schema(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string { v, _ := arg[struct{ Pattern string }](a); return v.Pattern },
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct{ Pattern string }](a)
			if err != nil {
				return "", err
			}
			re, err := globRE(v.Pattern)
			if err != nil {
				return "", err
			}
			var hits []string
			_ = filepath.WalkDir(r.Root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || ctx.Err() != nil {
					return filepath.SkipDir
				}
				if d.IsDir() && p != r.Root && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				if rel := r.rel(p); !d.IsDir() && re.MatchString(filepath.ToSlash(rel)) {
					hits = append(hits, rel)
				}
				if len(hits) > 500 {
					return fs.SkipAll
				}
				return nil
			})
			sort.Strings(hits)
			if len(hits) == 0 {
				return "no matches", nil
			}
			return strings.Join(hits, "\n"), nil
		}})

	r.Add(&Tool{Kind: ReadOnly,
		Spec: llm.ToolSpec{Name: "grep", Description: "Search file contents with a regex (ripgrep when available). Returns path:line:text, capped. Narrow with path or glob.",
			Schema: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"ignore_case":{"type":"boolean"}},"required":["pattern"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string {
			v, _ := arg[struct{ Pattern, Path string }](a)
			return strings.TrimSpace(v.Pattern + "  " + v.Path)
		},
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct {
				Pattern, Path, Glob string
				IgnoreCase          bool `json:"ignore_case"`
			}](a)
			if err != nil {
				return "", err
			}
			p, err := r.resolve(v.Path)
			if err != nil {
				return "", err
			}
			return r.grep(ctx, v.Pattern, p, v.Glob, v.IgnoreCase)
		}})

	r.Add(&Tool{Kind: Exec,
		Spec: llm.ToolSpec{Name: "bash", Description: "Run a shell command in the workspace (sandboxed). Use for builds, tests, git. Output is truncated; prefer targeted commands. Default timeout 120s.",
			Schema: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["command"],"additionalProperties":false}`)},
		Summary: func(a json.RawMessage) string { v, _ := arg[struct{ Command string }](a); return v.Command },
		Run: func(ctx context.Context, a json.RawMessage) (string, error) {
			v, err := arg[struct {
				Command    string
				TimeoutSec int `json:"timeout_sec"`
			}](a)
			if err != nil {
				return "", err
			}
			out, code, err := r.Sandbox.Run(ctx, r.Root, v.Command, v.TimeoutSec)
			if err != nil {
				return out, err
			}
			if code != 0 { // an error result, so failing commands count as failures (not as proof of success)
				return fmt.Sprintf("%s\n[exit %d]", out, code), fmt.Errorf("command exited with status %d", code)
			}
			return out, nil
		}})
}

// lookRG is swapped in tests to exercise the pure-Go grep fallback.
var lookRG = exec.LookPath

// openRegular opens p without blocking on FIFOs/devices and refuses non-regular files.
func openRegular(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = fmt.Errorf("%s is not a regular file", filepath.Base(p))
		}
		return nil, err
	}
	return f, nil
}

func readRegular(p string) ([]byte, error) {
	f, err := openRegular(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (r *Registry) write(p, content string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".ternly-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), p); err != nil { // atomic replace
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d lines)", r.rel(p), lines(content)), nil
}

func (r *Registry) grep(ctx context.Context, pat, path, glob string, icase bool) (string, error) {
	if rg, err := lookRG("rg"); err == nil {
		args := []string{"--line-number", "--no-heading", "--color=never", "--max-columns=300", "--max-count=50", "-e", pat}
		if icase {
			args = append(args, "-i")
		}
		if glob != "" {
			args = append(args, "--glob", glob)
		}
		out, _ := exec.CommandContext(ctx, rg, append(args, path)...).Output()
		s := strings.ReplaceAll(string(out), r.Root+string(filepath.Separator), "")
		if s == "" {
			return "no matches", nil
		}
		return Cap(s, maxOutBytes), nil
	}
	flags := ""
	if icase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pat)
	if err != nil {
		return "", err
	}
	var gre *regexp.Regexp
	if glob != "" {
		gre, _ = globRE("**/" + strings.TrimPrefix(glob, "**/"))
	}
	var sb strings.Builder
	count := 0
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil || count >= 200 {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if p != path && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // symlinks may point outside the workspace; FIFOs block
			return nil
		}
		rel := r.rel(p)
		if gre != nil && !gre.MatchString(filepath.ToSlash(rel)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			return nil
		}
		for i, ln := range strings.Split(string(b), "\n") {
			if re.MatchString(ln) {
				if len(ln) > 300 {
					ln = ln[:300] + "…"
				}
				fmt.Fprintf(&sb, "%s:%d:%s\n", rel, i+1, ln)
				if count++; count >= 200 {
					break
				}
			}
		}
		return nil
	})
	if sb.Len() == 0 {
		return "no matches", nil
	}
	return Cap(sb.String(), maxOutBytes), nil
}

// globRE converts a ** glob to an anchored regexp.
func globRE(g string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	g = filepath.ToSlash(strings.TrimPrefix(g, "./"))
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '{':
			sb.WriteString("(?:")
		case '}':
			sb.WriteString(")")
		case ',':
			sb.WriteString("|")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// Cap keeps head and tail of long outputs (errors are usually at the end).
func Cap(s string, n int) string {
	if len(s) <= n {
		return s
	}
	h, t := n*2/5, n*3/5
	return s[:h] + fmt.Sprintf("\n… [%d bytes omitted] …\n", len(s)-h-t) + s[len(s)-t:]
}

func lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
