package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A made-up debug-panel capture, as a tool under test might show it; none of
// these values are a real machine's.
const debugPanel = `                  Debug
                  OS         Linux 6.8.0-45-generic (x64)
                  Session ID ses_0a1b2c3d4e5fABCDEFGHijklmn
`

func TestPrivacyFindings(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("REPORT.md", "| facts at 0 keys | 18 |\n")
	write("120x40/screen-start.txt", " ternly · ~/proj-lru · main\n")
	write("120x40/probe.json", debugPanel)          // local only
	write("120x40/home/screen-old.txt", debugPanel) // the fixture: local only
	if bad, err := privacyFindings(dir); err != nil || len(bad) > 0 {
		t.Fatalf("clean run: %v %v", bad, err)
	}

	write("120x40/screen-k-debug.txt", debugPanel)
	bad, err := privacyFindings(dir)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(bad, "\n")
	for _, want := range []string{"screen-k-debug.txt", "kernel release", "os label", "session id"} {
		if !strings.Contains(all, want) {
			t.Errorf("findings miss %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "probe.json") || strings.Contains(all, "/home/") {
		t.Errorf("local-only files scanned:\n%s", all)
	}
}
