// Package checkpoint snapshots the workspace into a private ("shadow") git
// repository, so any turn's changes — including shell side effects — can be
// undone without touching the user's .git, index, branches or stash.
// A checkpoint is a git tree id; .gitignore'd files are never captured, restored or deleted.
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
	"strings"
	"sync"
)

type Store struct {
	root, gitDir, index string
	git                 string
	excludes            string // user's global core.excludesFile, kept because global config is disabled
	mu                  sync.Mutex
}

// Change is one path that differs between two trees (git diff-tree statuses).
type Change struct {
	Status byte // 'A' added, 'D' deleted, 'M' modified, 'T' type changed
	Path   string
}

// Open creates (or reuses) the shadow repository for root under cacheDir.
func Open(root, cacheDir string) (*Store, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git not found")
	}
	sum := sha256.Sum256([]byte(root))
	s := &Store{root: root, git: gitBin, gitDir: filepath.Join(cacheDir, "checkpoints", hex.EncodeToString(sum[:8])+".git")}
	s.index = filepath.Join(s.gitDir, fmt.Sprintf("index-%d", os.Getpid())) // per process: concurrent sessions don't share a lock
	if out, err := exec.Command(gitBin, "config", "--global", "--path", "core.excludesFile").Output(); err == nil {
		s.excludes = strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(s.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(s.gitDir), 0o700); err != nil {
			return nil, err
		}
		if out, err := exec.Command(gitBin, "init", "-q", "--bare", s.gitDir).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	// Highest-precedence attributes: store and restore bytes exactly (no EOL
	// conversion, filters or ident expansion from the repo's .gitattributes).
	attr := filepath.Join(s.gitDir, "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(attr), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(attr, []byte("* -text -eol -crlf -filter -ident -working-tree-encoding\n"), 0o600); err != nil {
		return nil, err
	}
	return s, nil
}

// Close removes this process's index file.
func (s *Store) Close() { _ = os.Remove(s.index) }

func (s *Store) cmd(ctx context.Context, index string, args ...string) *exec.Cmd {
	cfg := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false",
		"-c", "core.quotePath=false", "-c", "advice.addEmbeddedRepo=false", "-c", "gc.autoDetach=false"}
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

// Maintain runs `git gc --auto`, which prunes objects no checkpoint of a live
// session needs once enough have accumulated.
func (s *Store) Maintain(ctx context.Context) error {
	_, err := s.run(ctx, s.index, nil, "gc", "--auto", "--quiet")
	return err
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
