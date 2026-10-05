package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Everything ternly loads by itself from a workspace — instruction files,
// rules, skills and agents (through a linked .claude directory), source
// files for the code graph — is read confined to the workspace: a
// repository of symlinks to a key never gets the key into a request.
func TestWorkspaceLinksNeverLoaded(t *testing.T) {
	outside := t.TempDir()
	key := filepath.Join(outside, "id_rsa")
	_ = os.WriteFile(key, []byte("LINKED-SECRET-91c2\n"), 0o600)
	// A directory elsewhere shaped like .claude, with the key behind it.
	for _, d := range []string{"skills/keys", "agents", "commands"} {
		_ = os.MkdirAll(filepath.Join(outside, "claude", d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(outside, "claude", "skills", "keys", "SKILL.md"), []byte("---\nname: keys\ndescription: LINKED-SECRET-91c2\n---\nLINKED-SECRET-91c2"), 0o644)
	_ = os.WriteFile(filepath.Join(outside, "claude", "agents", "spy.md"), []byte("---\nname: spy\ndescription: LINKED-SECRET-91c2\n---\nLINKED-SECRET-91c2"), 0o644)
	_ = os.WriteFile(filepath.Join(outside, "lib.py"), []byte("def leaked_fn():\n    return 'LINKED-SECRET-91c2'\n"), 0o644)

	f := newProvider(t,
		step{call: [2]string{"use_skill", `{"name":"keys"}`}},
		step{call: [2]string{"find_symbol", `{"query":"leaked_fn"}`}},
		step{call: [2]string{"read_file", `{"path":"AGENTS.md"}`}},
		step{text: "done"})
	home := testHome(t, f.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "main.py"), []byte("def main():\n    pass\n"), 0o644)
	for link, target := range map[string]string{
		"AGENTS.md":           key,
		"CLAUDE.md":           key,
		"TERNLY.md":           key,
		".cursorrules":        key,
		".claude":             filepath.Join(outside, "claude"),
		"GEMINI.md":           key,
		"app/lib.py":          filepath.Join(outside, "lib.py"),
		".cursor/rules/r.mdc": key,
		"package.json":        key,
	} {
		p := filepath.Join(ws, link)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := ternly(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws, "-p", "look around").CombinedOutput()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.raw) < 2 {
		t.Fatalf("too few requests (%d):\n%s", len(f.raw), out)
	}
	for i, b := range f.raw {
		if strings.Contains(b, "LINKED-SECRET-91c2") {
			j := strings.Index(b, "LINKED")
			t.Errorf("request %d carries the linked secret: …%s…", i, b[max(0, j-200):min(len(b), j+60)])
		}
	}
}
