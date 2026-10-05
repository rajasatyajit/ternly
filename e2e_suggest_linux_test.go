package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/capability"
)

// Capability suggestion in the real TUI, offline: a task needs PostgreSQL;
// after the turn a ranked suggestion appears; accepting it reviews and
// installs the plugin (from a marketplace), usable from the next prompt;
// the same need isn't suggested again, and an unrelated task gets nothing.
func TestTUISuggestion(t *testing.T) {
	f := newProvider(t, step{text: "I can't query it from here."}, step{text: "second"}, step{text: "third"}, step{text: "fourth"})
	home := testHome(t, f.URL)
	cfg := filepath.Join(home, ".config", "ternly", "config.json")
	b, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(b), `"suggestions":false`, `"suggestions":true,"memory":false,"catalog_sources":{"offline":true}`, 1)), 0o600)

	mkt := t.TempDir()
	for p, c := range map[string]string{
		".claude-plugin/marketplace.json":            `{"name":"testmkt","owner":{"name":"Test"},"plugins":[{"name":"pgtools","source":"./plugins/pgtools","description":"Query PostgreSQL databases"}]}`,
		"plugins/pgtools/skills/pg/SKILL.md":         "---\ndescription: Query PostgreSQL with psql\n---\nUse psql -c.",
		"plugins/pgtools/.claude-plugin/plugin.json": `{"name":"pgtools","version":"0.1.0"}`,
	} {
		_ = os.MkdirAll(filepath.Join(mkt, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(mkt, p), []byte(c), 0o644)
	}
	cat := &capability.Catalog{Dir: filepath.Join(home, ".local", "share", "ternly", "catalog")}
	if err := cat.Put(
		capability.Entry{ID: "plugin:pgtools@testmkt", Kind: "plugin", Name: "pgtools", Description: "Query PostgreSQL databases", Publisher: "Test", Source: "marketplace:testmkt", Install: "pgtools@testmkt " + mkt, Coverage: 1, Tokens: 60, Runs: "plugin files (reviewed at install)"},
		capability.Entry{ID: "plugin:jira@testmkt", Kind: "plugin", Name: "jira", Description: "Jira issues", Source: "marketplace:testmkt", Coverage: 1},
	); err != nil {
		t.Fatal(err)
	}
	_ = cat.Close()

	ws := t.TempDir()
	c := ternly(t, home, "-model", "fake/m1", "-mode", "yolo", "-C", ws)
	c.Env = append(c.Env, "TERM=xterm-256color", "TERNLY_THEME=dark", "COLORTERM=")
	scr := &screen{}
	tty, err := startInPTY(c, scr, 170, 60)
	if err != nil {
		t.Fatalf("pseudo-terminal: %v", err)
	}
	defer func() { _ = c.Process.Kill() }()
	send := func(s string) { _, _ = tty.Write([]byte(s)); time.Sleep(80 * time.Millisecond) }
	scr.waitFor(t, "Code, ternly.")
	scr.waitFor(t, "fake")

	send("Query the orders table in our Postgres database\r")
	scr.waitFor(t, "I can't query it from here.")
	scr.waitFor(t, "ternly can add PostgreSQL")
	scr.waitFor(t, "pgtools")
	send("\r") // review & install the preselected one
	scr.waitFor(t, "install plugin?")
	send("y")
	scr.waitFor(t, "installed pgtools")
	scr.mu.Lock()
	screenText := reANSI.ReplaceAllString(scr.buf.String(), "")
	scr.mu.Unlock()
	if m := regexp.MustCompile(`usable from your next prompt \(([0-9.]+[mµ]?s) after approval\)`).FindStringSubmatch(screenText); m != nil {
		t.Logf("confirmation → usable: %s", m[1])
	}

	send("Query the orders table in our Postgres database again\r")
	scr.waitFor(t, "second")
	_, users := f.seen()
	if !strings.Contains(users[1], "new tools available: use_skill") {
		t.Fatalf("installed skill not announced: %q", users[1])
	}
	send("Refactor the parser\r")
	scr.waitFor(t, "third")
	time.Sleep(500 * time.Millisecond) // detection runs after the turn
	scr.mu.Lock()
	all := reANSI.ReplaceAllString(scr.buf.String(), "")
	scr.mu.Unlock()
	if after := all[strings.LastIndex(all, "Query the orders table in our Postgres database again"):]; strings.Contains(after, "ternly can add") {
		t.Fatal("suggested again after it was accepted this session, or for an unrelated task")
	}
	send("/exit\r")
	_ = c.Wait()
}
