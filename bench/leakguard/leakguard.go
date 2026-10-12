// Package leakguard finds what must not reach a public repository in files a
// harness writes for git: this machine's home path, user and host name, its
// kernel release and OS/distro name and version, and session IDs of the tools
// under test (OpenCode's "Session ID ses_…" in its debug panel, for one).
//
// Two kinds of check run together. Machine checks look for this machine's own
// values (read from the environment, os.Hostname, /proc and /etc/os-release),
// so they catch them in any shape. Pattern checks need no machine values and
// catch another machine's capture too: a kernel release, an "OS:" or
// "Kernel:" label, a distro name, a session ID.
package leakguard

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one thing a file must not contain.
type Finding struct {
	Line  int    // 1-based
	What  string // "home path", "kernel release", "session id", ...
	Match string
}

func (f Finding) String() string { return fmt.Sprintf("line %d: %s %q", f.Line, f.What, f.Match) }

type needle struct{ what, s string }

// Guard holds the machine values to look for; the patterns are shared.
type Guard struct{ needles []needle }

// New returns a guard for the given machine values, keyed by what they are
// ("hostname", "kernel release", ...). Values shorter than 3 bytes are
// ignored: they would match everywhere.
func New(values map[string][]string) *Guard {
	g := &Guard{}
	for what, vs := range values {
		for _, v := range vs {
			if v = strings.TrimSpace(v); len(v) >= 3 {
				g.needles = append(g.needles, needle{what, v})
			}
		}
	}
	return g
}

// ForThisMachine returns a guard for this machine's values.
func ForThisMachine() *Guard {
	v := map[string][]string{}
	if home, err := os.UserHomeDir(); err == nil {
		v["home path"] = []string{home}
	}
	v["user"] = []string{os.Getenv("USER"), os.Getenv("LOGNAME")}
	if host, err := os.Hostname(); err == nil {
		v["hostname"] = []string{host}
		if i := strings.IndexByte(host, '.'); i > 0 {
			v["hostname"] = append(v["hostname"], host[:i])
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		v["kernel release"] = []string{string(b)}
	}
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if f, err := os.Open(p); err == nil {
			v["os/distro"] = osRelease(f)
			f.Close()
			break
		}
	}
	return New(v)
}

// osRelease returns the identifying values of an os-release file: its names
// and version, not its URLs (which name the distro too, and so are covered by
// NAME and ID) or ID_LIKE ("arch", "debian": too common to search for).
func osRelease(f *os.File) []string {
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "NAME", "PRETTY_NAME", "ID", "VERSION", "VERSION_ID", "VERSION_CODENAME":
			out = append(out, strings.Trim(val, `"'`))
		}
	}
	return out
}

var patterns = []struct {
	what string
	re   *regexp.Regexp
}{
	// 7.2.9-1-cachyos, 6.8.0-45-generic, 6.6.87.2-microsoft-standard-WSL2.
	{"kernel release", regexp.MustCompile(`\b\d+\.\d+\.\d+(?:\.\d+)?-(?:\d+-)?[a-z][a-z0-9_.+-]*\b`)},
	// "Linux 7.2.9", "Darwin 24.1.0", "Darwin Kernel Version 24.1.0".
	{"kernel release", regexp.MustCompile(`(?i)\b(?:linux|darwin(?: kernel version)?|freebsd)[ /]v?\d+\.\d+`)},
	// A debug panel's or bug report's label: "OS  Linux …", "Kernel: …".
	{"os label", regexp.MustCompile(`(?im)^[\s│┃|>*-]*(?:os|operating system|kernel|distro|distribution|host ?name|platform)\b[ \t]*[:=│]?[ \t]+\S`)},
	{"os/distro", regexp.MustCompile(`(?i)\b(?:cachyos|ubuntu|debian|fedora|arch ?linux|manjaro|endeavouros|nixos|opensuse|centos|red hat|rhel|rocky linux|almalinux|gentoo|pop!?_os|linux ?mint|elementary os|void linux|alpine linux|macos \d+|mac os x|windows (?:1[01]|server))\b`)},
	// OpenCode ("ses_…"), Claude Code ("session_01…"), and a labelled one.
	{"session id", regexp.MustCompile(`\b(?:ses|session)_[A-Za-z0-9]{16,}\b`)},
	{"session id", regexp.MustCompile(`(?i)\bsession[ _-]?id\b["']?[ \t]*[:=│]?[ \t]*["']?[A-Za-z0-9_-]{8,}`)},
	{"session id", regexp.MustCompile(`(?i)\bsession\b\W{0,3}[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)},
}

// Scan returns everything in text that must not reach git.
func (g *Guard) Scan(text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		for _, n := range g.needles {
			if containsWord(line, n.s) {
				out = append(out, Finding{i + 1, n.what, n.s})
			}
		}
		for _, p := range patterns {
			for _, m := range p.re.FindAllString(line, -1) {
				if p.what == "kernel release" && archSuffix.MatchString(m) {
					continue // a release archive (ternly-bin-0.1.0-x86_64), not a kernel
				}
				out = append(out, Finding{i + 1, p.what, strings.TrimSpace(m)})
			}
		}
	}
	return out
}

// ScanDir scans the files under dir for which keep(path) is true and returns
// one line per finding, "path: line N: what "match"".
func (g *Guard) ScanDir(dir string, keep func(path string) bool) ([]string, error) {
	var bad []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !keep(p) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, f := range g.Scan(string(b)) {
			bad = append(bad, p+": "+f.String())
		}
		return nil
	})
	return bad, err
}

var archSuffix = regexp.MustCompile(`^[\d.]+-(?:x86|amd64$|aarch64|arm|i[3-6]86|riscv|ppc|s390|linux|darwin|windows)`)

// containsWord reports whether s occurs in line not as part of a longer word:
// the user "sam" is not in "samples", and "satyajit" not in "rajasatyajit".
func containsWord(line, s string) bool {
	for i := 0; ; {
		j := strings.Index(line[i:], s)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(s)
		if (j == 0 || !wordByte(line[j-1]) || !wordByte(s[0])) &&
			(end == len(line) || !wordByte(line[end]) || !wordByte(s[len(s)-1])) {
			return true
		}
		i = j + 1
	}
}

func wordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
