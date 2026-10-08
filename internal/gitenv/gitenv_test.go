package gitenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	in := []string{"HOME=/h", "GIT_DIR=/real/.git", "GIT_INDEX_FILE=/real/.git/index", "GIT_WORK_TREE=/real",
		"GIT_CONFIG_PARAMETERS='core.bare'='true'", "GIT_OBJECT_DIRECTORY=/x", "GIT_CONFIG_GLOBAL=/u/.gitconfig", "PATH=/bin"}
	got := Clean(in)
	want := []string{"HOME=/h", "PATH=/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("Clean = %q, want %q", got, want)
	}
	ws := Workspace(append(in, "GIT_SSH_COMMAND=ssh -i k", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=true", "GIT_NAMESPACE=n"))
	for _, want := range []string{"GIT_CONFIG_GLOBAL=/u/.gitconfig", "GIT_SSH_COMMAND=ssh -i k", "HOME=/h"} {
		if !slices.Contains(ws, want) {
			t.Errorf("Workspace dropped %q: %q", want, ws)
		}
	}
	for _, gone := range []string{"GIT_DIR=", "GIT_INDEX_FILE=", "GIT_WORK_TREE=", "GIT_CONFIG_PARAMETERS=", "GIT_OBJECT_DIRECTORY=", "GIT_CONFIG_KEY_0=", "GIT_CONFIG_VALUE_0=", "GIT_NAMESPACE="} {
		if slices.ContainsFunc(ws, func(s string) bool { return strings.HasPrefix(s, gone) }) {
			t.Errorf("Workspace kept %s: %q", gone, ws)
		}
	}
}

// The incident, reproduced: with GIT_DIR pointing at a sentinel repository,
// `git init` + commit in a fresh temp directory must not touch the sentinel.
func TestCommandIgnoresInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	sentinel := t.TempDir()
	run := func(dir string, env []string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir, c.Env = dir, env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(sentinel, Clean(os.Environ()), "init", "-q")
	cfg, _ := os.ReadFile(filepath.Join(sentinel, ".git", "config"))
	t.Setenv("GIT_DIR", filepath.Join(sentinel, ".git"))
	t.Setenv("GIT_WORK_TREE", sentinel)
	scratch := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "junk"}} {
		c := Command(context.Background(), args...)
		c.Dir = scratch
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(sentinel, ".git", "config")); string(after) != string(cfg) {
		t.Fatalf("the sentinel's config changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(sentinel, ".git", "refs", "heads", "master")); err == nil {
		t.Fatal("a commit landed in the sentinel")
	}
	if _, err := os.Stat(filepath.Join(sentinel, ".git", "refs", "heads", "main")); err == nil {
		t.Fatal("a commit landed in the sentinel")
	}
	if _, err := os.Stat(filepath.Join(scratch, ".git", "HEAD")); err != nil {
		t.Fatal("git init didn't create the scratch repository")
	}
}
