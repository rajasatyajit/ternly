package tui

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestFuzzyRanking(t *testing.T) {
	names := []string{"rewind", "review", "rename", "resume", "remember", "memory", "permissions", "git:commit", "commit"}
	rank := func(q string) []string {
		var out []string
		for _, n := range names {
			if fuzzyScore(q, n) >= 0 {
				out = append(out, n)
			}
		}
		slices.SortStableFunc(out, func(a, b string) int { return fuzzyScore(q, b) - fuzzyScore(q, a) })
		return out
	}
	if got := rank("rew"); got[0] != "rewind" {
		t.Errorf("rew → %v", got)
	}
	if got := rank("cmt"); !slices.Contains(got, "commit") || !slices.Contains(got, "git:commit") {
		t.Errorf("cmt → %v", got)
	}
	if got := rank("perm"); got[0] != "permissions" {
		t.Errorf("perm → %v", got)
	}
	if fuzzyScore("xyz", "rewind") >= 0 {
		t.Error("non-subsequence matched")
	}
}

// Every command docs/commands.md marks as built in ("yes") resolves, and
// every built-in is documented.
func TestCommandsDocumented(t *testing.T) {
	b, err := os.ReadFile("../../docs/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\| `/([a-z:?-]+)[^|]*\\|[^|]*\\|[^|]*\\| (yes|alias)")
	documented := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(string(b), -1) {
		documented[m[1]] = true
		if lookup(m[1]) == nil {
			t.Errorf("docs/commands.md lists /%s as built in, but it isn't", m[1])
		}
	}
	for _, c := range builtins {
		if !documented[c.name] && !strings.Contains(string(b), "`/"+c.name) {
			t.Errorf("/%s isn't in docs/commands.md", c.name)
		}
	}
	// Aliases (the parenthesised names in the first cell of built-in rows) resolve too.
	line := regexp.MustCompile("(?m)^\\| (`/[^|]*)\\|[^|]*\\|[^|]*\\| (yes|alias)")
	alias := regexp.MustCompile("`/([a-z-]+)`")
	for _, m := range line.FindAllStringSubmatch(string(b), -1) {
		for _, a := range alias.FindAllStringSubmatch(m[1], -1) {
			if lookup(a[1]) == nil {
				t.Errorf("documented name /%s doesn't resolve", a[1])
			}
		}
	}
}

func TestHTMLText(t *testing.T) {
	got := htmlText(`<html><head><style>p{}</style><script>alert(1)</script></head><body><h1>Title</h1><p>One &amp; two</p><ul><li>a</li><li>b</li></ul></body></html>`)
	for _, want := range []string{"Title", "One & two", "a\nb"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "p{}") {
		t.Errorf("script/style kept: %q", got)
	}
}

func TestLintCommand(t *testing.T) {
	dir := t.TempDir()
	if lintCommand(dir) != "" {
		t.Error("lint for an empty dir")
	}
	_ = os.WriteFile(dir+"/go.mod", []byte("module x"), 0o644)
	if got := lintCommand(dir); got != "go vet ./..." {
		t.Errorf("go → %q", got)
	}
}
