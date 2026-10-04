package checkpoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	s, err := Open(root, t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Destroy() })
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

// Retention follows the session: Destroy deletes the repo; repos whose
// process died (lock free) and M1's shared repos are swept on the next Open;
// a live session's repo is never touched.
func TestSessionRetention(t *testing.T) {
	testutil.Require(t, "git", testutil.Have("git"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	cache := t.TempDir()
	write(t, root, "a.txt", "a")
	live, err := Open(root, cache, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Destroy()
	crashed, _ := Open(root, cache, "crashed")
	snap(t, crashed)
	crashed.Close() // the process died: lock released, repo left behind
	if _, err := Open(root, cache, "live"); err == nil {
		t.Fatal("second process opened a session that is in use")
	}
	legacy := filepath.Join(cache, "checkpoints", filepath.Base(filepath.Dir(live.gitDir))+".git")
	_ = os.MkdirAll(legacy, 0o700)

	next, err := Open(root, cache, "next")
	if err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{crashed.gitDir: false, legacy: false, live.gitDir: true, next.gitDir: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", dir, err == nil, want)
		}
	}
	if err := next.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(next.gitDir); !os.IsNotExist(err) {
		t.Fatal("Destroy left the repository")
	}
}

// Set TERNLY_BIG_TREE to a large directory (e.g. a copy of $(go env GOROOT)/src) to time it.
func TestBigTreeTiming(t *testing.T) {
	dir := os.Getenv("TERNLY_BIG_TREE")
	if dir == "" {
		t.Skip("TERNLY_BIG_TREE not set")
	}
	s, err := Open(dir, t.TempDir(), "big")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Destroy()
	ms := func(f func()) time.Duration { t0 := time.Now(); f(); return time.Since(t0).Round(time.Millisecond) }
	first := ms(func() { snap(t, s) })
	again := ms(func() { snap(t, s) })
	var secrets []string
	scan := ms(func() { secrets, err = s.SecretFiles(ctx) })
	t.Logf("first snapshot %v, unchanged %v, secret scan %v (%d secret-like files: %v, err %v)", first, again, scan, len(secrets), secrets, err)
}
