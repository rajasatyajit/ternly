package checkpoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/testutil"
)

var ctx = context.Background()

func open(t testing.TB) (*Store, string) {
	t.Helper()
	testutil.Require(t, "git", testutil.Have("git"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo, err := OpenRepo(root, t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.Session("test")
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func write(t testing.TB, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(root, rel string) string { b, _ := os.ReadFile(filepath.Join(root, rel)); return string(b) }

func snap(t testing.TB, s *Store) string {
	t.Helper()
	tree, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestRestoreRoundTrip(t *testing.T) {
	s, root := open(t)
	write(t, root, "a.go", "package a\n")
	write(t, root, "b.go", "package b\n")
	write(t, root, ".gitignore", "build/\n")
	write(t, root, "build/out", "v1")
	t0 := snap(t, s)

	// what a turn might do, including shell side effects
	write(t, root, "a.go", "package a // changed\n")
	_ = os.Remove(filepath.Join(root, "b.go"))
	write(t, root, "gen/deep/c.go", "package c\n")
	write(t, root, "build/out", "v2") // ignored: must survive

	pend, err := s.Pending(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(pend, 10); got != "1 modified, 1 recreated, 1 deleted: M a.go, A b.go, D gen/deep/c.go" {
		t.Fatalf("pending: %s", got)
	}
	if _, err := s.Restore(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if read(root, "a.go") != "package a\n" || read(root, "b.go") != "package b\n" {
		t.Fatal("content not restored")
	}
	if _, err := os.Stat(filepath.Join(root, "gen")); !os.IsNotExist(err) {
		t.Fatal("added file or its empty dirs not removed")
	}
	if read(root, "build/out") != "v2" {
		t.Fatal("ignored file was touched")
	}
	if pend, _ := s.Pending(ctx, t0); len(pend) != 0 {
		t.Fatalf("workspace still differs: %v", pend)
	}
}

func TestUserRepoUntouched(t *testing.T) {
	s, root := open(t)
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	write(t, root, "a.txt", "1\n")
	git("add", "a.txt")
	git("commit", "-qm", "init")
	write(t, root, "a.txt", "2\n")
	git("add", "a.txt") // user has a staged change
	before := git("status", "--porcelain") + git("rev-parse", "HEAD") + git("for-each-ref") + git("stash", "list")

	t0 := snap(t, s)
	write(t, root, "a.txt", "3\n")
	write(t, root, "new.txt", "x")
	if _, err := s.Restore(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if after := git("status", "--porcelain") + git("rev-parse", "HEAD") + git("for-each-ref") + git("stash", "list"); after != before {
		t.Fatalf("user repo changed:\nbefore %s\nafter  %s", before, after)
	}
	if read(root, "a.txt") != "2\n" {
		t.Fatal("not restored")
	}
}

// A repo's .gitattributes must not alter bytes on snapshot or restore.
func TestByteExactDespiteGitattributes(t *testing.T) {
	s, root := open(t)
	write(t, root, ".gitattributes", "* text eol=crlf ident filter=evil\n")
	lf, crlf, bin := "a\nb\n$Id$\n", "a\r\nb\r\n", "\x00\x01\r\n\xff"
	write(t, root, "lf.txt", lf)
	write(t, root, "crlf.txt", crlf)
	write(t, root, "bin.dat", bin)
	t0 := snap(t, s)
	for _, f := range []string{"lf.txt", "crlf.txt", "bin.dat"} {
		write(t, root, f, "clobbered")
	}
	if _, err := s.Restore(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if read(root, "lf.txt") != lf || read(root, "crlf.txt") != crlf || read(root, "bin.dat") != bin {
		t.Fatalf("bytes changed: %q %q %q", read(root, "lf.txt"), read(root, "crlf.txt"), read(root, "bin.dat"))
	}
}

// After a checkpoint, a directory is swapped for a symlink to outside the
// workspace; restoring must not write through it.
func TestRestoreRefusesSymlinkEscape(t *testing.T) {
	s, root := open(t)
	outside := t.TempDir()
	write(t, root, "sub/f.txt", "orig")
	t0 := snap(t, s)
	_ = os.RemoveAll(filepath.Join(root, "sub"))
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Restore(ctx, t0)
	if _, statErr := os.Stat(filepath.Join(outside, "f.txt")); statErr == nil {
		t.Fatal("restore wrote outside the workspace")
	}
	if err == nil {
		// git may also replace the symlink with a real directory; either way nothing escapes
		if fi, _ := os.Lstat(filepath.Join(root, "sub")); fi.Mode()&os.ModeSymlink != 0 {
			t.Fatal("restore reported success but left the symlink")
		}
	}
}

func TestNestedRepoAndUnreadableFileDontAbort(t *testing.T) {
	s, root := open(t)
	write(t, root, "a.txt", "a")
	_ = exec.Command("git", "init", "-q", filepath.Join(root, "vendor-repo")).Run() // no commits: plain `git add` fails
	write(t, root, "vendor-repo/x.txt", "x")
	write(t, root, "locked.txt", "secret")
	_ = os.Chmod(filepath.Join(root, "locked.txt"), 0o000)
	defer os.Chmod(filepath.Join(root, "locked.txt"), 0o600)
	t0 := snap(t, s)
	write(t, root, "a.txt", "b")
	if _, err := s.Restore(ctx, t0); err != nil || read(root, "a.txt") != "a" {
		t.Fatalf("restore: %v %q", err, read(root, "a.txt"))
	}
}

// Workspace of n small files across n/50 directories.
func tree(b testing.TB, root string, n int) {
	for i := range n {
		write(b, root, fmt.Sprintf("pkg%03d/f%05d.go", i/50, i), fmt.Sprintf("package p\n\n// file %d\nfunc F%d() int { return %d }\n", i, i, i))
	}
}

func TestCheckpointTiming(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	s, root := open(t)
	const n = 10000
	tree(t, root, n)
	t0 := time.Now()
	base := snap(t, s)
	first := time.Since(t0)
	t0 = time.Now()
	snap(t, s)
	same := time.Since(t0)
	write(t, root, "pkg000/f00000.go", "package p // edited\n")
	t0 = time.Now()
	snap(t, s)
	edit := time.Since(t0)
	t0 = time.Now()
	if _, err := s.Restore(ctx, base); err != nil {
		t.Fatal(err)
	}
	restore := time.Since(t0)
	t.Logf("%d files: first snapshot %v, unchanged %v, after 1 edit %v, restore 1 file %v", n, first.Round(time.Millisecond), same.Round(time.Millisecond), edit.Round(time.Millisecond), restore.Round(time.Millisecond))
}

func BenchmarkSnapshotUnchanged10k(b *testing.B) {
	s, root := open(b)
	tree(b, root, 10000)
	snap(b, s)
	for b.Loop() {
		snap(b, s)
	}
}

func BenchmarkSnapshotOneEdit10k(b *testing.B) {
	s, root := open(b)
	tree(b, root, 10000)
	snap(b, s)
	i := 0
	for b.Loop() {
		i++
		write(b, root, "pkg000/f00000.go", strings.Repeat("x", i%7)+"package p\n")
		snap(b, s)
	}
}

func names(t *testing.T, s *Store, tree string) string {
	out, err := s.run(ctx, s.index, nil, "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSecretsNeverSnapshotted(t *testing.T) {
	s, root := open(t)
	secrets := map[string]string{".env": "API_KEY=s3cr3t", "config/.env.local": "x", "id_ed25519": "key", "certs/server.pem": "pem",
		"tls.key": "k", "aws_credentials.json": "c", "store.p12": "p"}
	kept := map[string]string{"main.go": "package main", "internal/credentials/credentials.go": "package credentials",
		"id_gen.go": "package main", "README.md": "readme"}
	for p, c := range secrets {
		write(t, root, p, c)
	}
	for p, c := range kept {
		write(t, root, p, c)
	}
	t0 := snap(t, s)
	in := names(t, s, t0)
	for p := range secrets {
		if strings.Contains(in, p) {
			t.Errorf("secret %s was snapshotted", p)
		}
	}
	for p := range kept {
		if !strings.Contains(in, p) {
			t.Errorf("%s was wrongly excluded", p)
		}
	}
	got, err := s.SecretFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	var want []string
	for p := range secrets {
		want = append(want, p)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SecretFiles = %v, want %v", got, want)
	}
	// A gitignored directory full of secret-like files is not expanded or reported.
	write(t, root, ".gitignore", "node_modules/\n")
	write(t, root, "node_modules/pkg/test.pem", "x")
	write(t, root, "certs/deep/node_modules/x.pem", "x")
	if again, _ := s.SecretFiles(ctx); len(again) != len(secrets) {
		t.Fatalf("SecretFiles with ignored dir = %v", again)
	}
	// Restore never touches secrets: an edited .env keeps its new value, a new key file stays.
	write(t, root, ".env", "API_KEY=rotated")
	write(t, root, "new.pem", "n")
	write(t, root, "main.go", "package changed")
	if _, err := s.Restore(ctx, t0); err != nil {
		t.Fatal(err)
	}
	if read(root, ".env") != "API_KEY=rotated" || read(root, "new.pem") != "n" || read(root, "main.go") != "package main" {
		t.Fatalf("restore: .env=%q new.pem=%q main.go=%q", read(root, ".env"), read(root, "new.pem"), read(root, "main.go"))
	}
}

func objects(t *testing.T, r *Repo) map[string]bool {
	out, err := r.run(ctx, "", nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, o := range strings.Fields(out) {
		m[o] = true
	}
	return m
}

// Sessions share one object store; dropping a session and collecting frees
// only what nothing else reaches; a fork reaches its parent's checkpoints.
func TestSharedStoreRefsAndGC(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo, err := OpenRepo(root, t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	repo.grace = "now" // single process, every live index is referenced by its head ref
	a, _ := repo.Session("a")
	b, _ := repo.Session("b")
	write(t, root, "shared.txt", strings.Repeat("common\n", 1000))
	ta := snap(t, a)
	_ = a.Keep(ctx, ta)
	write(t, root, "only-b.txt", "unique to b")
	tb := snap(t, b)
	_ = b.Keep(ctx, tb)
	if objects(t, repo)[ta] != true || len(objects(t, repo)) < 4 {
		t.Fatal("objects missing")
	}
	before := len(objects(t, repo))

	fork, _ := repo.Session("b-fork")
	if err := repo.CopyRefs(ctx, "b", "b-fork"); err != nil {
		t.Fatal(err)
	}
	if err := b.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.GC(ctx, func(id string) bool { return id != "b" }); err != nil {
		t.Fatal(err)
	}
	if !objects(t, repo)[tb] {
		t.Fatal("GC freed a tree the fork still references")
	}
	_ = os.Remove(filepath.Join(root, "only-b.txt"))
	if _, err := fork.Restore(ctx, tb); err != nil || read(root, "only-b.txt") != "unique to b" {
		t.Fatalf("fork restore: %v", err)
	}
	_ = fork.Drop(ctx)
	if err := repo.GC(ctx, func(id string) bool { return id == "a" }); err != nil {
		t.Fatal(err)
	}
	after := objects(t, repo)
	if after[tb] || !after[ta] || len(after) >= before {
		t.Fatalf("GC: tb kept=%v ta kept=%v objects %d→%d", after[tb], after[ta], before, len(after))
	}
	if ids, _ := repo.Sessions(ctx); strings.Join(ids, ",") != "a" {
		t.Fatalf("sessions with refs: %v", ids)
	}
}

// A collector in another process must not prune objects a live session's
// index refers to (they are reachable through its head ref) nor objects it
// wrote moments ago (grace period).
func TestGCSparesLiveSessions(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	cache := t.TempDir()
	repo, _ := OpenRepo(root, cache, "proj")
	live, _ := repo.Session("live")
	write(t, root, "a.txt", "v1")
	t1 := snap(t, live) // only head references it
	other, _ := OpenRepo(root, cache, "proj")
	other.grace = "now"
	if err := other.GC(ctx, func(id string) bool { return id == "live" }); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.txt", "v2")
	snap(t, live) // reuses index entries; would fail on pruned objects
	if _, err := live.Restore(ctx, t1); err != nil || read(root, "a.txt") != "v1" {
		t.Fatalf("restore after foreign GC: %v %q", err, read(root, "a.txt"))
	}
	// objects with no ref at all survive the default grace period
	h, _ := repo.run(ctx, "", []byte("fresh blob"), "hash-object", "-w", "--stdin")
	repo2, _ := OpenRepo(root, cache, "proj") // default grace
	_ = repo2.GC(ctx, func(string) bool { return true })
	if !objects(t, repo)[strings.TrimSpace(h)] {
		t.Fatal("default GC pruned a fresh unreferenced object")
	}
}

func TestEnforceCap(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo, _ := OpenRepo(root, t.TempDir(), "proj")
	repo.grace = "now"
	for i, id := range []string{"old", "mid", "new"} {
		s, _ := repo.Session(id)
		write(t, root, id+".bin", strings.Repeat(fmt.Sprintf("%d-random-ish-%s-", i, id), 40000)) // ~1 MB each, distinct
		tr := snap(t, s)
		_ = s.Keep(ctx, tr)
		_ = os.Remove(filepath.Join(root, id+".bin"))
		snap(t, s) // head moves off the big blob; only the cp ref keeps it
	}
	_, _ = repo.run(ctx, "", nil, "gc", "--quiet", "--prune=now")
	full := repo.Size()
	limit := full - full/4 // forces pruning of at least one session
	msg, err := repo.EnforceCap(ctx, limit, []string{"old", "mid"})
	if err != nil || !strings.Contains(msg, "will be pruned at the next start") {
		t.Fatalf("first pass must only warn: %q %v", msg, err)
	}
	if ids, _ := repo.Sessions(ctx); len(ids) != 3 {
		t.Fatal("warning pass pruned something")
	}
	msg, err = repo.EnforceCap(ctx, limit, []string{"old", "mid"})
	if err != nil || !strings.Contains(msg, "pruned checkpoints of") || repo.Size() > limit {
		t.Fatalf("second pass: %q %v size %d > cap %d", msg, err, repo.Size(), limit)
	}
	ids, _ := repo.Sessions(ctx)
	if slices.Contains(ids, "old") || !slices.Contains(ids, "new") {
		t.Fatalf("pruned the wrong sessions: %v left", ids)
	}
	t.Logf("cap %s: %s", HumanSize(limit), msg)
}

// Set TERNLY_BIG_TREE to a large directory (e.g. a copy of $(go env GOROOT)/src) to time it.
func TestBigTreeTiming(t *testing.T) {
	dir := os.Getenv("TERNLY_BIG_TREE")
	if dir == "" {
		t.Skip("TERNLY_BIG_TREE not set")
	}
	repo, err := OpenRepo(dir, t.TempDir(), "big")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := repo.Session("big")
	ms := func(f func()) time.Duration { t0 := time.Now(); f(); return time.Since(t0).Round(time.Millisecond) }
	first := ms(func() { snap(t, s) })
	again := ms(func() { snap(t, s) })
	var secrets []string
	scan := ms(func() { secrets, err = s.SecretFiles(ctx) })
	t.Logf("first snapshot %v, unchanged %v, secret scan %v (%d secret-like files: %v, err %v)", first, again, scan, len(secrets), secrets, err)
}

// Disk use of 20 sessions on one project: one shared store versus one store
// per session (the M1.1 design). Set TERNLY_BIG_TREE.
func TestDiskSharedVsPerSession(t *testing.T) {
	dir := os.Getenv("TERNLY_BIG_TREE")
	if dir == "" {
		t.Skip("TERNLY_BIG_TREE not set")
	}
	const n = 20
	edit := filepath.Join(dir, "ternly-disk-test.txt")
	defer os.Remove(edit)
	cache := t.TempDir()
	shared, _ := OpenRepo(dir, cache, "shared")
	t0 := time.Now()
	for i := range n {
		s, _ := shared.Session(fmt.Sprintf("s%02d", i))
		_ = os.WriteFile(edit, []byte(fmt.Sprintf("session %d", i)), 0o644)
		_ = s.Keep(ctx, snap(t, s))
	}
	sharedTime := time.Since(t0)
	var per int64
	t0 = time.Now()
	for i := range n {
		r, _ := OpenRepo(dir, cache, fmt.Sprintf("per%02d", i))
		s, _ := r.Session("only")
		_ = os.WriteFile(edit, []byte(fmt.Sprintf("session %d", i)), 0o644)
		_ = s.Keep(ctx, snap(t, s))
		per += r.Size()
	}
	perTime := time.Since(t0)
	t.Logf("%d sessions: shared store %s (%v to snapshot all), per-session stores %s (%v)", n, HumanSize(shared.Size()), sharedTime.Round(time.Millisecond), HumanSize(per), perTime.Round(time.Millisecond))
}
