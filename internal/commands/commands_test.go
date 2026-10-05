package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrecedenceAndFormats(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, ".ternly/commands/fix.md"), "---\ndescription: Fix an issue\nargument-hint: <issue>\n---\nFix issue $1.\n")
	write(t, filepath.Join(home, ".config/ternly/commands/fix.md"), "user version: loses to the project\n")
	write(t, filepath.Join(root, ".claude/commands/frontend/component.md"), "Make a component called $0 ($ARGUMENTS).")
	write(t, filepath.Join(root, ".gemini/commands/git/commit.toml"), "description = \"Commit\"\nprompt = \"\"\"\nWrite a commit message for:\n{{args}}\n\"\"\"\n")
	write(t, filepath.Join(home, ".codex/prompts/draft.md"), "---\ndescription: Draft a PR\n---\nDraft a PR for $FILE with $1.")
	write(t, filepath.Join(home, ".codex/prompts/sub/ignored.md"), "codex reads only top-level prompts")
	write(t, filepath.Join(root, ".opencode/commands/help.md"), "can't shadow a built-in")
	cs, errs := Load(Dirs(root, home), func(n string) bool { return n == "help" })
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	want := []string{"fix", "frontend:component", "git:commit", "prompts:draft"}
	if !slices.Equal(names, want) || len(errs) != 1 || !strings.Contains(errs[0].Error(), "built-in") {
		t.Fatalf("names %v errs %v", names, errs)
	}
	fix := cs[0]
	if !fix.Project || fix.Description != "Fix an issue" || fix.ArgHint != "<issue>" || fix.Template != "Fix issue $1." {
		t.Fatalf("fix: %+v", fix)
	}
}

func TestExpand(t *testing.T) {
	shell := Hooks{Shell: func(cmd string) (string, error) { return "<" + cmd + ">", nil }}
	for _, c := range []struct {
		origin   Origin
		template string
		args     string
		want     string
	}{
		{OpenCode, "Fix $1 then $2; all: $ARGUMENTS", `123 "high priority"`, `Fix 123 then high priority; all: 123 "high priority"`},
		{Ternly, "Status: !`git status`", "", "Status: <git status>"},
		{Claude, "First $0, second $1, again $ARGUMENTS[1]; cost \\$5", "a b", "First a, second b, again b; cost $5"},
		{Claude, "No placeholder here.", "some args", "No placeholder here.\n\nARGUMENTS: some args"},
		{Codex, "Review $FILE focusing on $1; price $$9", "FOCUS=x FILE=main.go security", "Review main.go focusing on security; price $9"},
		{Gemini, "Explain {{args}}", "the router", "Explain the router"},
		{Gemini, "Diff: !{git diff -- {{args}}}", "a b.go", "Diff: <git diff -- 'a b.go'>"},
		{Gemini, "Summarise.", "briefly", "Summarise.\n\nbriefly"},
	} {
		got, err := (&Command{Origin: c.origin, Template: c.template}).Expand(c.args, shell)
		if err != nil || got != c.want {
			t.Errorf("%s %q %q → %q (%v), want %q", c.origin, c.template, c.args, got, err, c.want)
		}
	}
	if _, err := (&Command{Origin: OpenCode, Template: "!`ls`"}).Expand("", Hooks{}); err == nil {
		t.Error("shell ran without a hook")
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`a "b c" 'd e' f\ g ""`)
	if want := []string{"a", "b c", "d e", "f g", ""}; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}
