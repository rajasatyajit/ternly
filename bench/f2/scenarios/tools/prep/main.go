// prep makes a fresh fixture for an F2 scenario: prep S1|S2|S3|S4|S5 <dst>.
// Copies the Phase C suite's pinned repo, commits it, and for S2 seeds the
// lru-resize bug (committed, so the bug is the starting state) and for S4
// leaves two seeded bugs as an uncommitted change to review.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rajasatyajit/ternly/internal/gitenv"
)

const cache = "/home/satyajit/.cache/ternly-suite/repos/"

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "prep:", err)
		os.Exit(1)
	}
}

func git(dir string, args ...string) {
	c := gitenv.Command(context.Background(), append([]string{"-C", dir}, args...)...) // a repository the survey owns (ADR 024)
	out, err := c.CombinedOutput()
	if err != nil {
		must(fmt.Errorf("git %v: %v %s", args, err, out))
	}
}

func replace(p, old, new string) {
	b, err := os.ReadFile(p)
	must(err)
	if strings.Count(string(b), old) != 1 {
		must(fmt.Errorf("%s: %q found %d times", p, old, strings.Count(string(b), old)))
	}
	must(os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644))
}

func main() {
	if len(os.Args) != 3 {
		must(fmt.Errorf("usage: prep S1|S2|S3|S4|S5 <dst>"))
	}
	sc, dst := os.Args[1], os.Args[2]
	repo := "golang-lru@9c13c57de0bedd6b3e21183b34072e417ffa69d1"
	if sc == "S3" {
		repo = "mitt@6b41670516ed8e8b738612f60491995470aa63b3"
	}
	must(os.RemoveAll(dst))
	cp := exec.Command("cp", "-a", cache+repo, dst)
	if out, err := cp.CombinedOutput(); err != nil {
		must(fmt.Errorf("cp: %v %s", err, out))
	}
	_ = os.RemoveAll(filepath.Join(dst, ".git"))
	_ = os.Remove(filepath.Join(dst, ".fetched"))
	lru := filepath.Join(dst, "simplelru", "lru.go")
	if sc == "S2" {
		replace(lru, "\tfor i := 0; i < diff; i++ {\n\t\tc.removeOldest()", "\tfor i := 0; i <= diff; i++ {\n\t\tc.removeOldest()")
	}
	git(dst, "init", "-q")
	git(dst, "add", "-A")
	git(dst, "-c", "user.name=f2", "-c", "user.email=f2@example.invalid", "commit", "-q", "-m", "fixture "+sc)
	if sc == "S4" { // the change to review: two seeded bugs, left uncommitted
		replace(lru, "\tfor i := 0; i < diff; i++ {\n\t\tc.removeOldest()", "\tfor i := 0; i <= diff; i++ {\n\t\tc.removeOldest()")
		replace(lru, "\tif ent, ok = c.items[key]; ok {\n\t\treturn ent.Value, true\n\t}\n\treturn\n}", "\tif ent, ok = c.items[key]; ok {\n\t\tc.evictList.MoveToFront(ent)\n\t\treturn ent.Value, true\n\t}\n\treturn\n}")
	}
	fmt.Println(dst)
}
