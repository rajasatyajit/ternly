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

func TestWarnings(t *testing.T) {
	old := &Command{Name: "x", Origin: Claude, Template: "Fix issue $1 in $2"}
	if w := old.Warnings(); len(w) != 1 || !strings.Contains(w[0], "0-based") {
		t.Fatalf("1-based Claude command not flagged: %v", w)
	}
	for _, c := range []*Command{
		{Origin: Claude, Template: "Fix $0 then $1"},
		{Origin: Claude, Template: "Fix $ARGUMENTS"},
		{Origin: OpenCode, Template: "Fix $1"},
	} {
		if w := c.Warnings(); len(w) != 0 {
			t.Errorf("%+v flagged: %v", c, w)
		}
	}
}

// A repository whose .claude is a link out of the workspace (or whose command
// file is a link) loads none of those commands: reads are confined to the
// workspace (rootfs).
func TestLinkedCommandDirsNotLoaded(t *testing.T) {
	outside := t.TempDir()
	_ = os.MkdirAll(filepath.Join(outside, "commands"), 0o755)
	_ = os.WriteFile(filepath.Join(outside, "commands", "leak.md"), []byte("LINKED-SECRET"), 0o644)
	_ = os.WriteFile(filepath.Join(outside, "key"), []byte("LINKED-SECRET"), 0o644)
	root, home := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, ".opencode", "commands"), 0o755)
	_ = os.Symlink(filepath.Join(outside, "key"), filepath.Join(root, ".opencode", "commands", "k.md"))
	_ = os.WriteFile(filepath.Join(root, ".opencode", "commands", "ok.md"), []byte("fine"), 0o644)
	cs, _ := Load(Dirs(root, home), nil)
	for _, c := range cs {
		if strings.Contains(c.Template, "LINKED-SECRET") || c.Name == "leak" || c.Name == "k" {
			t.Fatalf("loaded %s from outside the workspace", c.Name)
		}
	}
	if len(cs) != 1 || cs[0].Name != "ok" {
		t.Fatalf("commands: %v", cs)
	}
}
