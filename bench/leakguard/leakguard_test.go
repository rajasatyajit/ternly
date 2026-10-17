package leakguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/gitenv"
)

// debugPanel is a made-up capture of a coding agent's debug panel, the shape
// that reached the public repository once (OpenCode's, in a reach recording):
// none of these values are a real machine's.
const debugPanel = `  ┃  Bug report: in package simplelru, Resize evicts one entry.
     ▣  Build ·
                  Debug                                                  esc
                  Version    1.18.35 (latest)
                  Date       2026-01-02T03:04:05.000Z
                  OS         Linux 6.8.0-45-generic (x64)
                  Terminal   xterm-256color
                  Session ID ses_0a1b2c3d4e5fABCDEFGHijklmn
                  Model      ollama/some-model:latest
                  Share this when reporting an issue.               copy enter
   ~/proj-lru                                           7.3K  ctrl+p commands
`

func TestDebugPanelCapture(t *testing.T) {
	g := New(map[string][]string{
		"hostname":       {"devbox-x8664"},
		"kernel release": {"6.8.0-45-generic"},
		"os/distro":      {"Ubuntu", "24.04.1 LTS (Noble Numbat)"},
	})
	got := map[string]int{}
	for _, f := range g.Scan(debugPanel) {
		got[f.What]++
		if f.Line < 6 && f.What != "os label" {
			t.Errorf("finding on a clean line: %v", f)
		}
	}
	for _, what := range []string{"kernel release", "os label", "session id"} {
		if got[what] == 0 {
			t.Errorf("debug panel: no %q finding; got %v", what, got)
		}
	}
	// Each must be caught on its own, not only together with the others.
	for _, line := range []string{
		"OS         Linux 6.8.0-45-generic (x64)",
		"uname: 6.8.0-45-generic",
		"uname -r on the runner: 5.15.0-1034-azure", // not this guard's value: the pattern alone
		"running on Linux 6.8 today",
		"Session ID ses_0a1b2c3d4e5fABCDEFGHijklmn",
		"resume with ses_0a1b2c3d4e5fABCDEFGHijklmn",
		`"sessionId": "3f2a9c1e-77aa-4c1b-9d5e-0123456789ab"`,
		"session 3f2a9c1e-77aa-4c1b-9d5e-0123456789ab resumed",
		"built on devbox-x8664",
		"Ubuntu 24.04.1 LTS (Noble Numbat)",
		"Distro: Fedora 40",
		"host: CachyOS",
		"Hostname   devbox",
	} {
		if fs := g.Scan(line); len(fs) == 0 {
			t.Errorf("not caught: %q", line)
		}
	}
}

func TestHarnessOutputIsClean(t *testing.T) {
	g := New(map[string][]string{"user": {"sam"}, "hostname": {"devbox"}})
	for _, line := range []string{
		"BenchmarkKeystroke   220 µs/op   19 B/key",
		" ternly 0.2.0 · ~/proj-lru · main · m1 · quota:x · trust:x",
		"samples: 42, resample", // the user is not in a longer word
		"go1.27.2, staticcheck@v0.7.0-0.dev, v0.0.0-20230101120000-abcdef012345",
		"ternly-bin-0.1.0-x86_64.tar.gz ternly-bin-0.1.0-aarch64.tar.gz",
		"/sessions lists your sessions; the session id stays local",
		"startup 51 ms · echo 6.2 ms · idle 0.70 %",
	} {
		if fs := g.Scan(line); len(fs) > 0 {
			t.Errorf("false finding in %q: %v", line, fs)
		}
	}
}

func TestShortValuesIgnored(t *testing.T) {
	if fs := New(map[string][]string{"user": {"a", "ab", " "}}).Scan("a ab abc"); len(fs) > 0 {
		t.Errorf("values under 3 bytes must be ignored: %v", fs)
	}
}

// TestCommittedHarnessOutput scans what the harnesses committed under bench/f2
// (recordings, reports, metrics, scores) and bench/phasec (run data, logs, and
// the scripts that ran: there a script is a record of the run, so it is scanned) with this machine's values and the
// patterns, so a leak fails CI whichever harness wrote it.
func TestCommittedHarnessOutput(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := gitenv.Command(context.Background(), "-C", root, "ls-files", "bench/f2", "bench/phasec").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	g := ForThisMachine()
	n := 0
	for _, p := range strings.Fields(string(out)) {
		switch ext := filepath.Ext(p); {
		case ext == ".go" || ext == ".mod" || ext == ".sum":
			continue // the harnesses' own source
		case ext == ".sh" && strings.HasPrefix(p, "bench/f2/"):
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			continue // deleted in the work tree
		}
		n++
		for _, f := range g.Scan(string(b)) {
			t.Errorf("%s: %v", p, f)
		}
	}
	if n == 0 {
		t.Fatal("no committed harness output found under bench/f2 or bench/phasec")
	}
}

// Any machine's home path is found, not only this machine's: CI runs as
// another user, so a capture from a developer's machine needs the pattern.
func TestHomePathPattern(t *testing.T) {
	g := New(nil)
	for _, s := range []string{
		"cd /home/alice/workspace/ternly",
		`no Go files in /Users/bob/.cache/x`,
		`C:\Users\carol\AppData`,
		`"dir": "C:\\Users\\dave\\src"`,
		"edit_file=file:/home/erin/x.go",
	} {
		if f := g.Scan(s); len(f) != 1 || f[0].What != "home path" {
			t.Errorf("%q: %v", s, f)
		}
	}
	for _, s := range []string{
		"the path elides (`/home/someone/wor…/project-x`)",
		"HOME=/home/u",
		"workspace  /var/tmp/ternly-layout-1/home/proj-lru",
		`mkdir -p "$1/home/.config"`,
		"github.com/rajasatyajit/ternly",
		`"want": ["1.0.0-alpha.2", "1.0.0-rc.11", "1.4.3-beta.5", "1.0.0-rc"]`,
	} {
		if f := g.Scan(s); len(f) != 0 {
			t.Errorf("%q: false positive %v", s, f)
		}
	}
}
