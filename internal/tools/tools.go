// Package tools implements the agent's tools with security as a design
// constraint: workspace path confinement, a permission policy, sandboxed shell,
// secret redaction, and output caps that also keep token usage low.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	fs      *os.Root // all file access goes through it: no escape even if paths change under us

	// The tool set is an immutable snapshot behind an atomic pointer. Once
	// Hold is called (the first turn), Add and Remove only stage changes;
	// Commit publishes them at a turn boundary, so a request in flight never
	// sees a half-loaded set.
	cur  atomic.Pointer[toolSet]
	mu   sync.Mutex
	next *toolSet // staged (nil: none)
	held bool

	Hooks HookRunner // plugin hooks around tool calls (nil: none)
}

// HookRunner runs plugin hooks around tool calls. PreTool can only deny:
// nothing a hook returns grants a permission.
type HookRunner interface {
	PreTool(ctx context.Context, tool string, args json.RawMessage) (deny bool, reason string)
	PostTool(ctx context.Context, tool string, args json.RawMessage, out string, failed bool)
}

// Subset is a view of the published tools that allow accepts, sharing the
// workspace, policy and sandbox (subagents). It never changes.
func (r *Registry) Subset(allow func(name string) bool) *Registry {
	cur := r.cur.Load()
	set := &toolSet{tools: map[string]*Tool{}}
	for _, n := range cur.order {
		if allow(n) {
			set.order = append(set.order, n)
			set.tools[n] = cur.tools[n]
		}
	}
	v := &Registry{Root: r.Root, Policy: r.Policy, Sandbox: r.Sandbox, Redact: r.Redact, Frame: r.Frame, fs: r.fs, held: true, Hooks: r.Hooks}
	v.cur.Store(set)
	return v
}

type toolSet struct {
	tools map[string]*Tool
	order []string
}

func (s *toolSet) clone() *toolSet {
	n := &toolSet{tools: make(map[string]*Tool, len(s.tools)), order: append([]string(nil), s.order...)}
	for k, v := range s.tools {
		n.tools[k] = v
	}
	return n
}

func NewRegistry(root string, pol *Policy, sb *Sandbox, rd *Redactor) (*Registry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	fsys, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	r := &Registry{Root: abs, Policy: pol, Sandbox: sb, Redact: rd, Frame: NewFramer(), fs: fsys}
	r.cur.Store(&toolSet{tools: map[string]*Tool{}})
	r.builtin()
	return r, nil
}

// stage returns the staged set, cloning the published one first. r.mu held.
func (r *Registry) stage() *toolSet {
	if r.next == nil {
		r.next = r.cur.Load().clone()
	}
	return r.next
}

// Add adds or replaces a tool (staged once Hold was called).
func (r *Registry) Add(t *Tool) {
	t.schema = compileSchema(t.Spec.Schema)
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stage()
	if _, ok := s.tools[t.Spec.Name]; !ok {
		s.order = append(s.order, t.Spec.Name)
	}
	s.tools[t.Spec.Name] = t
	if !r.held {
		r.publish()
	}
}

// Remove removes the tools whose names have the prefix (staged once held).
func (r *Registry) Remove(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stage()
	kept := s.order[:0]
	for _, n := range s.order {
		if strings.HasPrefix(n, prefix) {
			delete(s.tools, n)
		} else {
			kept = append(kept, n)
		}
	}
	s.order = kept
	if !r.held {
		r.publish()
	}
}

func (r *Registry) publish() {
	if r.next != nil {
		r.cur.Store(r.next)
		r.next = nil
	}
}

// Hold makes later Add/Remove calls wait for Commit (call when turns start).
func (r *Registry) Hold() { r.mu.Lock(); r.held = true; r.mu.Unlock() }

// Commit publishes staged changes (at a turn boundary) and reports which
// tool names were added and removed.
func (r *Registry) Commit() (added, removed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		return nil, nil
	}
	old, nw := r.cur.Load(), r.next
	for _, n := range nw.order {
		if old.tools[n] == nil {
			added = append(added, n)
		}
	}
	for _, n := range old.order {
		if nw.tools[n] == nil {
			removed = append(removed, n)
		}
	}
	r.publish()
	return added, removed
}

func (r *Registry) Get(name string) *Tool { return r.cur.Load().tools[name] }

// Specs are returned in a stable order so the prompt prefix stays cacheable.
func (r *Registry) Specs() []llm.ToolSpec {
	s := r.cur.Load()
	out := make([]llm.ToolSpec, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.tools[n].Spec)
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
	if r.Hooks != nil {
		if deny, why := r.Hooks.PreTool(ctx, tc.Name, args); deny {
			return Result{Out: "blocked by a plugin hook: " + Cap(why, 2000) + ". Do not retry the same action.", IsErr: true, Rejected: true}
		}
	}
	if ok, why := r.Policy.Check(ctx, t, tc.Name, t.Summary(args)); !ok {
		return Result{Out: "permission denied by user" + why + ". Do not retry the same action; ask the user or choose another approach.", IsErr: true, Rejected: true}
	}
	res, err := t.Run(ctx, args)
	if err != nil {
		res = "error: " + err.Error() + "\n" + res
	}
	if r.Hooks != nil {
		r.Hooks.PostTool(ctx, tc.Name, args, res, err != nil)
	}
	out, flagged := r.Frame.Wrap(tc.Name, Cap(r.Redact.Apply(res), maxToolBytes))
	return Result{Out: out, IsErr: err != nil, Flagged: flagged}
}

func (r *Registry) names() []string { return append([]string(nil), r.cur.Load().order...) }

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

// inRoot converts a resolved absolute path to the form os.Root expects.
func (r *Registry) inRoot(p string) string {
	if s, err := filepath.Rel(r.Root, p); err == nil {
		return s
	}
	return p // absolute: os.Root rejects it
}

// rootErr explains os.Root's "path escapes from parent" for rel: it names the
// symlink responsible and, for an absolute link that points inside the
// workspace, the relative link to replace it with.
func (r *Registry) rootErr(rel string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "escapes from parent") {
		return err
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		fi, lerr := r.fs.Lstat(p)
		if lerr != nil {
			break
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, _ := r.fs.Readlink(p)
		full := filepath.Join(r.Root, filepath.Dir(p), target)
		if filepath.IsAbs(target) {
			full = filepath.Clean(target)
		}
		if full != r.Root && !strings.HasPrefix(full, r.Root+string(filepath.Separator)) {
			return fmt.Errorf("%s is a symlink to %s, which is outside the workspace", p, target)
		}
		if filepath.IsAbs(target) {
			relT, _ := filepath.Rel(filepath.Join(r.Root, filepath.Dir(p)), full)
			return fmt.Errorf("%s is a symlink with an absolute target (%s). ternly accesses files through a workspace-rooted handle that only follows relative symlinks, so it can't use this link even though it points inside the workspace. Replace it with a relative link: ln -sfn %s %s", p, target, relT, p)
		}
	}
	return fmt.Errorf("%w: %s changed while it was being accessed", ErrOutside, rel)
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

var skipDirNames = []string{".git", "node_modules", "vendor", "target", "dist", "build", ".venv", "venv", "__pycache__", ".next", ".cache"}

var skipDirs = func() map[string]bool {
	m := map[string]bool{}
	for _, d := range skipDirNames {
		m[d] = true
	}
	return m
}()

const (
	maxReadLines = 400
	maxLineLen   = 1000
	maxOutBytes  = 12 << 10
	maxReadBytes = 48 << 10
	maxFileBytes = 16 << 20 // edit_file / grep fallback refuse to slurp more
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
			f, err := r.openRegular(r.inRoot(p))
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
				if _, err := r.fs.Lstat(r.inRoot(p)); err == nil {
					return "", errors.New("file exists; provide old_string to edit it")
				}
				return r.write(p, v.NewString)
			}
			b, err := r.readRegular(r.inRoot(p))
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
			_ = fs.WalkDir(r.fs.FS(), ".", func(p string, d fs.DirEntry, err error) error {
				if err != nil || ctx.Err() != nil {
					return fs.SkipDir
				}
				if d.IsDir() && p != "." && skipDirs[d.Name()] {
					return fs.SkipDir
				}
				if !d.IsDir() && re.MatchString(p) {
					hits = append(hits, p)
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

// openRegular opens rel (inside the root) without blocking on FIFOs/devices
// and refuses non-regular files.
// ReadFile reads a workspace file for the UI (pinned files, @mentions)
// through the same os.Root guard as the file tools; rel is relative to Root.
func (r *Registry) ReadFile(rel string) ([]byte, error) {
	full, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	return r.readRegular(r.rel(full))
}

// Rel is p relative to the workspace root.
func (r *Registry) Rel(p string) string { return r.rel(p) }

func (r *Registry) openRegular(rel string) (*os.File, error) {
	f, err := r.fs.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, r.rootErr(rel, err)
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = fmt.Errorf("%s is not a regular file", filepath.Base(rel))
		}
		return nil, err
	}
	return f, nil
}

func (r *Registry) readRegular(rel string) ([]byte, error) {
	f, err := r.openRegular(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s is %d MB; files over %d MB are not edited or searched in full", filepath.Base(rel), fi.Size()>>20, maxFileBytes>>20)
	}
	return io.ReadAll(io.LimitReader(f, maxFileBytes+1))
}

// write atomically replaces p (a resolved path) via a temp file, all inside the root.
func (r *Registry) write(p, content string) (string, error) {
	rel := r.inRoot(p)
	dir := filepath.Dir(rel)
	if err := r.fs.MkdirAll(dir, 0o755); err != nil {
		return "", r.rootErr(dir, err)
	}
	mode := os.FileMode(0o644)
	if fi, err := r.fs.Stat(rel); err == nil {
		mode = fi.Mode().Perm()
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".ternly-"+hex.EncodeToString(rnd[:]))
	f, err := r.fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", r.rootErr(tmp, err)
	}
	defer r.fs.Remove(tmp) // no-op after a successful rename
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return "", err
	}
	_ = f.Chmod(mode) // fchmod on the open file: Root.Chmod is racy on Unix
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := r.fs.Rename(tmp, rel); err != nil { // atomic replace
		return "", r.rootErr(rel, err)
	}
	return fmt.Sprintf("wrote %s (%d lines)", rel, lines(content)), nil
}

func (r *Registry) grep(ctx context.Context, pat, path, glob string, icase bool) (string, error) {
	rel := r.inRoot(path)
	// ripgrep (Linux only) does the fast filtering, starting from the directory
	// opened through the root (fd 3, walked as /dev/fd/3). It still opens
	// deeper paths by name, so a nested directory swapped for a symlink
	// mid-walk could feed it outside content: every reported line is re-read
	// through the root and kept only if the confined bytes match. Elsewhere:
	// the Go fallback, which reads only through the root.
	if rg, err := lookRG("rg"); err == nil && runtime.GOOS == "linux" {
		f, err := r.fs.Open(rel)
		if err != nil {
			return "", r.rootErr(rel, err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file or directory", filepath.Base(rel))
		}
		args := []string{"--line-number", "--no-heading", "--with-filename", "--null", "--color=never", "--max-columns=300", "--max-count=50",
			"--no-require-git", "-e", pat} // no .git above /dev/fd/3, but .gitignore files should still apply
		for _, d := range skipDirNames {
			args = append(args, "--glob", "!"+d)
		}
		if icase {
			args = append(args, "-i")
		}
		if glob != "" {
			args = append(args, "--glob", glob)
		}
		c := exec.CommandContext(ctx, rg, append(args, "/dev/fd/3")...)
		c.ExtraFiles = []*os.File{f}
		out, _ := c.Output()
		goRE, _ := regexp.Compile(map[bool]string{true: "(?i)"}[icase] + pat) // nil if the dialects differ
		return r.confirmMatches(string(out), filepath.ToSlash(rel), fi.IsDir(), goRE), nil
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
	start := filepath.ToSlash(r.inRoot(path))
	_ = fs.WalkDir(r.fs.FS(), start, func(rel string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil || count >= 200 {
			return fs.SkipDir
		}
		if d.IsDir() {
			if rel != start && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // don't follow symlinks; FIFOs would block
			return nil
		}
		if gre != nil && !gre.MatchString(rel) {
			return nil
		}
		b, err := r.readRegular(rel)
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

// confirmMatches turns rg --null output into path:line:text, keeping only
// lines whose content, re-read through the root, is what rg reported.
func (r *Registry) confirmMatches(out, rel string, dir bool, re *regexp.Regexp) string {
	files := map[string][]string{} // confined contents, nil = unreadable inside the root
	var sb strings.Builder
	dropped := 0
	for _, rec := range strings.Split(out, "\n") {
		path, rest, ok := strings.Cut(rec, "\x00")
		if !ok || sb.Len() > maxOutBytes {
			continue
		}
		num, text, ok := strings.Cut(rest, ":")
		n, err := strconv.Atoi(num)
		if !ok || err != nil || n < 1 {
			continue
		}
		switch {
		case path == "/dev/fd/3":
			path = rel
		case strings.HasPrefix(path, "/dev/fd/3/") && dir:
			path = strings.TrimPrefix(path, "/dev/fd/3/")
			if rel != "." {
				path = rel + "/" + path
			}
		default:
			continue
		}
		lines, seen := files[path]
		if !seen {
			if b, err := r.readRegular(filepath.FromSlash(path)); err == nil {
				lines = strings.Split(string(b), "\n")
			}
			files[path] = lines
		}
		if n > len(lines) {
			dropped++
			continue
		}
		line := lines[n-1]
		// rg elides lines over --max-columns ("[Omitted long matching line]";
		// older: "[Omitted long line with N matches]"): nothing to compare, so
		// the confined line must match on its own where Go can compile the pattern.
		long := strings.HasPrefix(text, "[Omitted long") && len(line) > 300 && (re == nil || re.MatchString(line))
		if line != text && !long {
			dropped++ // changed or swapped since rg read it
			continue
		}
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		fmt.Fprintf(&sb, "%s:%d:%s\n", path, n, line)
	}
	if dropped > 0 {
		fmt.Fprintf(&sb, "(%d match(es) dropped: the file changed or left the workspace during the search)\n", dropped)
	}
	if sb.Len() == 0 {
		return "no matches"
	}
	return Cap(sb.String(), maxOutBytes)
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
