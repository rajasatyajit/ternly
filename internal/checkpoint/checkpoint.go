// Package checkpoint snapshots the workspace into a private ("shadow") git
// repository, so any turn's changes — including shell side effects — can be
// undone without touching the user's .git, index, branches or stash.
// A checkpoint is a git tree id; .gitignore'd files are never captured, restored or deleted,
// and neither are files that look like secrets (SecretPatterns).
//
// One object store per project (Repo) is shared by all of its sessions; each
// session (Store) has its own index and refs under refs/ternly/<session>/,
// so content is stored once and GC keeps exactly what saved sessions reach.
package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Repo is one project's shadow object store.
type Repo struct {
	root, gitDir string
	git          string
	excludes     string // user's global core.excludesFile, kept because global config is disabled
	grace        string // gc prune expiry; objects younger than this survive even if unreferenced
}

// Store is one session's view of the Repo: its own index and refs.
type Store struct {
	*Repo
	session, index string
	mu             sync.Mutex
}

// SecretPatterns (gitignore syntax) are never snapshotted. Directories and
// source files are re-included so e.g. credentials.go or an id_gen.go keep
// their undo history; the price is that a directory named "credentials" is
// still captured file by file.
var SecretPatterns = []string{".env*", "*.pem", "*.key", "id_*", "*credentials*", "*.p12"}

var keepExts = []string{"go", "rs", "c", "h", "cc", "cpp", "hpp", "java", "kt", "kts", "scala", "py", "rb", "php",
	"js", "mjs", "cjs", "ts", "tsx", "jsx", "swift", "cs", "ex", "exs", "erl", "hs", "ml", "lua", "sh", "zig", "dart",
	"vue", "svelte", "sql", "proto", "md", "rst", "adoc"}

// IsSecretPath reports whether a workspace path matches SecretPatterns (and
// isn't a source file re-included by extension), so it is never checkpointed,
// pinned into a prompt or committed by /commit.
func IsSecretPath(p string) bool {
	base := filepath.Base(p)
	if i := strings.LastIndexByte(base, '.'); i > 0 && slices.Contains(keepExts, base[i+1:]) {
		return false
	}
	for _, pat := range SecretPatterns {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
	}
	return false
}

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

// OpenRepo opens (creating if needed) the object store for the project whose
// workspace is root, keyed by key. Pre-M2 per-session repositories under
// checkpoints/<key>/ are removed: they never outlived their process.
func OpenRepo(root, cacheDir, key string) (*Repo, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git not found")
	}
	if !validID(key) {
		return nil, fmt.Errorf("invalid project key %q", key)
	}
	base := filepath.Join(cacheDir, "checkpoints")
	_ = os.RemoveAll(filepath.Join(base, key)) // M1.1 layout
	r := &Repo{root: root, git: gitBin, gitDir: filepath.Join(base, key+".git"), grace: "1.hour.ago"}
	if out, err := exec.Command(gitBin, "config", "--global", "--path", "core.excludesFile").Output(); err == nil {
		r.excludes = strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(r.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return nil, err
		}
		if out, err := exec.Command(gitBin, "init", "-q", "--bare", r.gitDir).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	// Highest-precedence attributes: store and restore bytes exactly (no EOL
	// conversion, filters or ident expansion from the repo's .gitattributes).
	info := filepath.Join(r.gitDir, "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte("* -text -eol -crlf -filter -ident -working-tree-encoding\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(info, "exclude"), excludeFile(), 0o600); err != nil {
		return nil, err
	}
	return r, nil
}

func validID(s string) bool { return s != "" && !strings.ContainsAny(s, "/\\. \x00") }

// Session returns the handle for one session's checkpoints.
func (r *Repo) Session(id string) (*Store, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	st := &Store{Repo: r, session: id, index: filepath.Join(r.gitDir, "index-"+id)}
	if _, err := os.Stat(st.index); err != nil {
		r.seedIndex(st.index)
	}
	return st, nil
}

// seedIndex starts a new session's index as a copy of the project's most
// recently used one: git reuses its cached file stats instead of re-hashing
// the whole workspace, and re-checks any entry whose stats changed.
func (r *Repo) seedIndex(dst string) {
	idx, _ := filepath.Glob(filepath.Join(r.gitDir, "index-*"))
	var newest string
	var mod int64
	for _, p := range idx {
		if strings.Contains(filepath.Base(p), ".") || strings.HasSuffix(p, "index-none") {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.ModTime().UnixNano() > mod {
			newest, mod = p, fi.ModTime().UnixNano()
		}
	}
	if newest == "" {
		return
	}
	if b, err := os.ReadFile(newest); err == nil && os.WriteFile(dst, b, 0o600) == nil {
		// keep the original mtime: git's racy-entry check compares entry
		// times against the index file's own mtime
		t := time.Unix(0, mod)
		_ = os.Chtimes(dst, t, t)
	}
}

// SetRoot points the repo at a moved workspace (trees are path-independent).
func (r *Repo) SetRoot(root string) { r.root = root }

func (s *Store) ref(name string) string { return "refs/ternly/" + s.session + "/" + name }

// Keep pins tree (a checkpoint) for this session until the session is dropped.
func (s *Store) Keep(ctx context.Context, tree string) error {
	_, err := s.run(ctx, s.index, nil, "update-ref", s.ref("cp-"+tree), tree)
	return err
}

// Drop deletes the session's refs and index; GC then frees what only it reached.
func (s *Store) Drop(ctx context.Context) error {
	if err := s.dropRefs(ctx, s.session); err != nil {
		return err
	}
	_ = os.Remove(s.index)
	return nil
}

func (r *Repo) refs(ctx context.Context, session string) ([]string, error) {
	prefix := "refs/ternly/"
	if session != "" {
		prefix += session + "/"
	}
	out, err := r.run(ctx, "", nil, "for-each-ref", "--format=%(refname)", prefix)
	return strings.Fields(out), err
}

func (r *Repo) dropRefs(ctx context.Context, session string) error {
	refs, err := r.refs(ctx, session)
	if err != nil || len(refs) == 0 {
		return err
	}
	var in strings.Builder
	for _, ref := range refs {
		in.WriteString("delete " + ref + "\n")
	}
	_, err = r.run(ctx, "", []byte(in.String()), "update-ref", "--stdin")
	return err
}

// CopyRefs gives session to the same checkpoints as from (used by /fork; no objects are copied).
func (r *Repo) CopyRefs(ctx context.Context, from, to string) error {
	if !validID(to) {
		return fmt.Errorf("invalid session id %q", to)
	}
	refs, err := r.refs(ctx, from)
	if err != nil {
		return err
	}
	var in strings.Builder
	for _, ref := range refs {
		name := strings.TrimPrefix(ref, "refs/ternly/"+from+"/")
		fmt.Fprintf(&in, "update refs/ternly/%s/%s %s\n", to, name, ref)
	}
	_, err = r.run(ctx, "", []byte(in.String()), "update-ref", "--stdin")
	return err
}

// Sessions lists the ids of sessions that hold refs.
func (r *Repo) Sessions(ctx context.Context) ([]string, error) {
	refs, err := r.refs(ctx, "")
	var ids []string
	for _, ref := range refs {
		if id, _, ok := strings.Cut(strings.TrimPrefix(ref, "refs/ternly/"), "/"); ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, err
}

// GC drops refs and index files of sessions keep rejects, then prunes objects
// no remaining ref reaches (older than the grace period, so objects a live
// session in another process just wrote survive). One collector at a time.
func (r *Repo) GC(ctx context.Context, keep func(session string) bool) error {
	lock, err := lockFile(filepath.Join(r.gitDir, "gc.lock"))
	if err != nil {
		return nil // another process is collecting
	}
	defer lock.Close()
	ids, err := r.Sessions(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !keep(id) {
			if err := r.dropRefs(ctx, id); err != nil {
				return err
			}
		}
	}
	idx, _ := filepath.Glob(filepath.Join(r.gitDir, "index-*"))
	for _, p := range idx {
		if id := strings.TrimPrefix(filepath.Base(p), "index-"); !strings.Contains(id, ".") && !keep(id) {
			_ = os.Remove(p)
		}
	}
	_, err = r.run(ctx, "", nil, "gc", "--quiet", "--prune="+r.grace)
	return err
}

// Size is the bytes used by the object store.
func (r *Repo) Size() int64 {
	var n int64
	_ = filepath.WalkDir(filepath.Join(r.gitDir, "objects"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// EnforceCap keeps the store under capBytes. The first time it is over, it
// only warns (and remembers that it did); the next time, it drops the
// checkpoints of sessions in oldestFirst order until under the cap.
func (r *Repo) EnforceCap(ctx context.Context, capBytes int64, oldestFirst []string) (string, error) {
	marker := filepath.Join(r.gitDir, "cap-warned")
	size := r.Size()
	if capBytes <= 0 || size <= capBytes {
		_ = os.Remove(marker)
		return "", nil
	}
	if _, err := os.Stat(marker); err != nil {
		_ = os.WriteFile(marker, nil, 0o600)
		return fmt.Sprintf("checkpoints use %s, over the %s cap: the oldest sessions' checkpoints will be pruned at the next start (raise checkpoint_cap_mb to keep them)", HumanSize(size), HumanSize(capBytes)), nil
	}
	before, n := size, 0
	for _, id := range oldestFirst {
		if size <= capBytes {
			break
		}
		if err := r.dropRefs(ctx, id); err != nil {
			return "", err
		}
		n++
		if _, err := r.run(ctx, "", nil, "gc", "--quiet", "--prune="+r.grace); err != nil {
			return "", err
		}
		size = r.Size()
	}
	if size <= capBytes {
		_ = os.Remove(marker)
	}
	return fmt.Sprintf("pruned checkpoints of %d oldest session(s): %s → %s (cap %s)", n, HumanSize(before), HumanSize(size), HumanSize(capBytes)), nil
}

// HumanSize formats a byte count as B, KB, MB or GB.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func lockFile(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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

func (r *Repo) cmd(ctx context.Context, index string, args ...string) *exec.Cmd {
	cfg := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false",
		"-c", "core.quotePath=false", "-c", "advice.addEmbeddedRepo=false",
		// ternly collects explicitly (GC); git must never start a detached gc of its own
		"-c", "gc.auto=0", "-c", "gc.autoDetach=false"}
	if r.excludes != "" {
		cfg = append(cfg, "-c", "core.excludesFile="+r.excludes)
	}
	c := exec.CommandContext(ctx, r.git, append(cfg, args...)...)
	c.Dir = r.root
	// Its own process group, stopped whole on cancel: `git gc` runs `git
	// repack` and `git pack-objects`, which outlived a killed gc and kept
	// writing into the repository after ternly exited (they left test homes
	// behind). SIGTERM, so git removes its lock files (a SIGKILLed `git add`
	// leaves index.lock, and the next snapshot fails); SIGKILL only if it
	// hasn't exited after WaitDelay.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second
	// User/system config can define filters (git-lfs) or fsmonitor hooks; checkpoints must not run them.
	c.Env = append(os.Environ(), "GIT_DIR="+r.gitDir, "GIT_WORK_TREE="+r.root, "GIT_INDEX_FILE="+index,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return c
}

func (r *Repo) run(ctx context.Context, index string, stdin []byte, args ...string) (string, error) {
	if index == "" { // repo-level command: no index involved
		index = filepath.Join(r.gitDir, "index-none")
	}
	c := r.cmd(ctx, index, args...)
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
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	// head keeps everything in this session's index reachable, so another
	// process's GC can't prune objects this index still refers to.
	if _, err := s.run(ctx, s.index, nil, "update-ref", s.ref("head"), tree); err != nil {
		return "", err
	}
	return tree, nil
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
