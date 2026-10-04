// Package checkpoint snapshots the workspace into a private ("shadow") git
// repository, so any turn's changes — including shell side effects — can be
// undone without touching the user's .git, index, branches or stash.
// A checkpoint is a git tree id; .gitignore'd files are never captured, restored or deleted,
// and neither are files that look like secrets (SecretPatterns).
//
// Each session owns its own shadow repository, locked while the session is
// live and deleted with it (Destroy), so copied workspace content never
// outlives the session that captured it.
package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
)

type Store struct {
	root, gitDir, index string
	git                 string
	excludes            string // user's global core.excludesFile, kept because global config is disabled
	lock                *os.File
	mu                  sync.Mutex
}

// SecretPatterns (gitignore syntax) are never snapshotted. Directories and
// source files are re-included so e.g. credentials.go or an id_gen.go keep
// their undo history; the price is that a directory named "credentials" is
// still captured file by file.
var SecretPatterns = []string{".env*", "*.pem", "*.key", "id_*", "*credentials*", "*.p12"}

var keepExts = []string{"go", "rs", "c", "h", "cc", "cpp", "hpp", "java", "kt", "kts", "scala", "py", "rb", "php",
	"js", "mjs", "cjs", "ts", "tsx", "jsx", "swift", "cs", "ex", "exs", "erl", "hs", "ml", "lua", "sh", "zig", "dart",
	"vue", "svelte", "sql", "proto", "md", "rst", "adoc"}

func excludeFile() []byte {
	var b strings.Builder
	b.WriteString("# written by ternly: likely secrets are never checkpointed\n")
	for _, p := range SecretPatterns {
		b.WriteString(p + "\n")
	}
	for _, e := range keepExts {
		b.WriteString("!*." + e + "\n")
	}
	b.WriteString("!*/\n")
	return []byte(b.String())
}

// Change is one path that differs between two trees (git diff-tree statuses).
type Change struct {
	Status byte // 'A' added, 'D' deleted, 'M' modified, 'T' type changed
	Path   string
}

// Open creates the shadow repository for one session of root under cacheDir
// and holds its lock until Close or Destroy. Repositories of this root whose
// lock is free (their process died) are deleted first, as are pre-M1.1
// shared repositories.
func Open(root, cacheDir, session string) (*Store, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git not found")
	}
	if session == "" || strings.ContainsAny(session, "/\\.") {
		return nil, fmt.Errorf("invalid session id %q", session)
	}
	sum := sha256.Sum256([]byte(root))
	key := hex.EncodeToString(sum[:8])
	base := filepath.Join(cacheDir, "checkpoints", key)
	_ = os.RemoveAll(filepath.Join(cacheDir, "checkpoints", key+".git")) // M1 layout: one shared repo per root
	sweep(base)
	s := &Store{root: root, git: gitBin, gitDir: filepath.Join(base, session+".git")}
	s.index = filepath.Join(s.gitDir, "index")
	if out, err := exec.Command(gitBin, "config", "--global", "--path", "core.excludesFile").Output(); err == nil {
		s.excludes = strings.TrimSpace(string(out))
	}
	// Lock before init: a concurrent sweep only claims existing lock files.
	if err := os.MkdirAll(s.gitDir, 0o700); err != nil {
		return nil, err
	}
	if s.lock, err = lockFile(filepath.Join(s.gitDir, "ternly.lock"), true); err != nil {
		return nil, fmt.Errorf("session %s is in use by another ternly process", session)
	}
	if out, err := exec.Command(gitBin, "init", "-q", "--bare", s.gitDir).CombinedOutput(); err != nil {
		s.Close()
		return nil, fmt.Errorf("git init: %v: %s", err, out)
	}
	// Highest-precedence attributes: store and restore bytes exactly (no EOL
	// conversion, filters or ident expansion from the repo's .gitattributes).
	info := filepath.Join(s.gitDir, "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte("* -text -eol -crlf -filter -ident -working-tree-encoding\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(info, "exclude"), excludeFile(), 0o600); err != nil {
		return nil, err
	}
	return s, nil
}

func lockFile(p string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(p, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// sweep deletes session repositories under base whose lock nobody holds.
func sweep(base string) {
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if f, err := lockFile(filepath.Join(dir, "ternly.lock"), false); err == nil {
			_ = os.RemoveAll(dir)
			f.Close()
		}
	}
}

// Close releases the session lock, keeping the repository (a later Open of
// the same root sweeps it unless the session is resumed — M2).
func (s *Store) Close() {
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
}

// Destroy deletes the session's repository and every checkpoint in it.
func (s *Store) Destroy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.RemoveAll(s.gitDir)
	s.Close()
	return err
}

// SecretFiles lists workspace files skipped only because they match
// SecretPatterns (files ignored by .gitignore are not listed).
func (s *Store) SecretFiles(ctx context.Context) ([]string, error) {
	// --directory collapses ignored directories (node_modules/), keeping the
	// listing cheap, but also directories whose files are all ignored
	// (certs/ holding only *.pem); expand those recursively.
	ls := func(directory bool, spec ...string) ([]string, error) {
		args := []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard"}
		if directory {
			args = append(args, "--directory", "--no-empty-directory")
		}
		out, err := s.run(ctx, s.index, nil, append(append(args, "--"), spec...)...)
		return strings.FieldsFunc(out, func(r rune) bool { return r == 0 }), err
	}
	top, err := ls(true, ".")
	if err != nil {
		return nil, err
	}
	var files, dirs []string
	for _, p := range top {
		if strings.HasSuffix(p, "/") {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}
	ign, err := s.ignored(ctx, dirs, false)
	if err != nil {
		return nil, err
	}
	var expand []string
	for _, d := range dirs {
		if !slices.Contains(ign, d) {
			expand = append(expand, d)
		}
	}
	if len(expand) > 0 {
		more, err := ls(false, expand...)
		if err != nil {
			return nil, err
		}
		files = append(files, more...)
	}
	return s.ignored(ctx, files, true)
}

// ignored returns the paths git ignores; with byUs, only those decided by our info/exclude.
func (s *Store) ignored(ctx context.Context, paths []string, byUs bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	in := []byte(strings.Join(paths, "\x00") + "\x00")
	res, err := s.run(ctx, s.index, in, "check-ignore", "-z", "-v", "--stdin")
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 { // 1: nothing ignored
			return nil, err
		}
	}
	var out []string
	f := strings.Split(res, "\x00") // source, line, pattern, path
	for i := 0; i+3 < len(f); i += 4 {
		if strings.HasPrefix(f[i+2], "!") {
			continue // re-included
		}
		if !byUs || strings.HasSuffix(f[i], filepath.Join("info", "exclude")) {
			out = append(out, f[i+3])
		}
	}
	return out, nil
}

func (s *Store) cmd(ctx context.Context, index string, args ...string) *exec.Cmd {
	cfg := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false",
		"-c", "core.quotePath=false", "-c", "advice.addEmbeddedRepo=false"}
	if s.excludes != "" {
		cfg = append(cfg, "-c", "core.excludesFile="+s.excludes)
	}
	c := exec.CommandContext(ctx, s.git, append(cfg, args...)...)
	c.Dir = s.root
	// User/system config can define filters (git-lfs) or fsmonitor hooks; checkpoints must not run them.
	c.Env = append(os.Environ(), "GIT_DIR="+s.gitDir, "GIT_WORK_TREE="+s.root, "GIT_INDEX_FILE="+index,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return c
}

func (s *Store) run(ctx context.Context, index string, stdin []byte, args ...string) (string, error) {
	c := s.cmd(ctx, index, args...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, firstLine(errb.String()))
	}
	return out.String(), nil
}

// Snapshot records the current workspace and returns its tree id.
func (s *Store) Snapshot(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot(ctx)
}

func (s *Store) snapshot(ctx context.Context) (string, error) {
	// --ignore-errors: an unreadable file or a nested repo without commits is
	// skipped (exit 1) instead of aborting the whole checkpoint; 128 is fatal.
	if _, err := s.run(ctx, s.index, nil, "add", "-A", "--ignore-errors", "--", "."); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return "", err
		}
	}
	out, err := s.run(ctx, s.index, nil, "write-tree")
	return strings.TrimSpace(out), err
}

// Diff lists paths that differ from tree `from` to tree `to`.
func (s *Store) Diff(ctx context.Context, from, to string) ([]Change, error) {
	if from == to {
		return nil, nil
	}
	out, err := s.run(ctx, s.index, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-status", from, to)
	if err != nil {
		return nil, err
	}
	var cs []Change
	f := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		cs = append(cs, Change{Status: f[i][0], Path: f[i+1]})
	}
	return cs, nil
}

// Pending returns what Restore(target) would do. Statuses describe the effect
// of restoring: 'A' recreate, 'D' delete, 'M'/'T' rewrite.
func (s *Store) Pending(ctx context.Context, target string) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return s.Diff(ctx, now, target)
}

// Restore makes the (non-ignored) workspace match tree target and returns what
// it did (statuses as in Pending). Paths are written only inside root and never through a symlinked
// directory.
func (s *Store) Restore(ctx context.Context, target string) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	changes, err := s.Diff(ctx, now, target)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	var restore []byte
	var remove []string
	for _, c := range changes {
		if !s.safe(c.Path) {
			return nil, fmt.Errorf("refusing to restore %q: it would leave the workspace", c.Path)
		}
		if c.Status == 'D' { // present now, absent in target: delete
			remove = append(remove, c.Path)
		} else {
			restore = append(append(restore, c.Path...), 0)
		}
	}
	for _, p := range remove {
		full := filepath.Join(s.root, p)
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		s.pruneEmpty(filepath.Dir(full))
	}
	if len(restore) > 0 {
		tmp := s.index + ".restore"
		defer os.Remove(tmp)
		if _, err := s.run(ctx, tmp, nil, "read-tree", target); err != nil {
			return nil, err
		}
		if _, err := s.run(ctx, tmp, restore, "checkout-index", "-f", "-z", "--stdin"); err != nil {
			return nil, err
		}
	}
	_, _ = s.snapshot(ctx) // refresh the index to the restored state
	return changes, nil
}

// safe rejects paths that resolve outside root (e.g. a parent directory that
// was replaced by a symlink after the checkpoint).
func (s *Store) safe(rel string) bool {
	if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
		return false
	}
	dir := filepath.Join(s.root, filepath.Dir(rel))
	for d := dir; d != s.root && strings.HasPrefix(d, s.root); d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func (s *Store) pruneEmpty(dir string) {
	for dir != s.root && strings.HasPrefix(dir, s.root+string(filepath.Separator)) {
		if os.Remove(dir) != nil { // fails when not empty
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Describe summarises changes as "3 restored, 1 deleted: a.go, b.go, …".
func Describe(cs []Change, max int) string {
	var res, del, add int
	names := make([]string, 0, min(len(cs), max))
	for _, c := range cs {
		switch c.Status {
		case 'A':
			add++
		case 'D':
			del++
		default:
			res++
		}
		if len(names) < max {
			names = append(names, string(c.Status)+" "+c.Path)
		}
	}
	s := fmt.Sprintf("%d modified, %d recreated, %d deleted", res, add, del)
	if len(names) > 0 {
		s += ": " + strings.Join(names, ", ")
	}
	if len(cs) > max {
		s += fmt.Sprintf(", … +%d", len(cs)-max)
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
