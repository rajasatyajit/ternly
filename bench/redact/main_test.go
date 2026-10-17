package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rajasatyajit/ternly/bench/leakguard"
)

func TestRedact(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "r.jsonl")
	in := `{"log":"/home/alice/work/repo/.claude/worktrees/x/a.go:3","tmp":"/home/alice/.jobs/1/tmp/out"}` + "\n"
	if err := os.WriteFile(f, []byte(in), 0o640); err != nil {
		t.Fatal(err)
	}
	ms := maps{{"/home/alice/work/repo/.claude/worktrees/x", "$REPO"}, {"/home/alice/.jobs/1/tmp", "$JOB_TMP"}, {"/home/alice", "$HOME"}}
	g := leakguard.New(map[string][]string{"user": {"alice"}})
	if bad, err := run(ms, []string{f}, true, g); err != nil || len(bad) == 0 {
		t.Fatalf("-check found nothing in %q: %v %v", in, bad, err)
	}
	if b, _ := os.ReadFile(f); string(b) != in {
		t.Fatal("-check changed the file")
	}
	bad, err := run(ms, []string{f}, false, g)
	if err != nil || len(bad) != 0 {
		t.Fatalf("left after redaction: %v %v", bad, err)
	}
	b, _ := os.ReadFile(f)
	if want := `{"log":"$REPO/a.go:3","tmp":"$JOB_TMP/out"}` + "\n"; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	os.WriteFile(f, []byte("cd /home/bob/src\n"), 0o644) // another machine's home: no map covers it
	if bad, _ := run(nil, []string{f}, false, g); len(bad) != 1 {
		t.Fatalf("another home went unreported: %v", bad)
	}
}
