package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HarnessIsolated is the tripwire for measurement runs (bench/run.sh e2e and
// fabrication, TERNLY_HARNESS=1): HOME and every XDG base directory must be
// set and lie inside a temporary directory — under os.TempDir(), or in a
// .e2e-work-* directory bench/run.sh made with mktemp — so a harness run can
// never read or write the user's real config, memory, sessions or plugins.
func HarnessIsolated() error {
	var bad []string
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if v := os.Getenv(k); !isTemp(v) {
			bad = append(bad, fmt.Sprintf("%s=%q", k, v))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("refusing to run: a harness run needs HOME and the XDG directories inside a temporary directory (%s) — run it through bench/run.sh, which sets them", strings.Join(bad, ", "))
	}
	return nil
}

func isTemp(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	p = resolve(filepath.Clean(p))
	for _, tmp := range []string{filepath.Clean(os.TempDir()), resolve(filepath.Clean(os.TempDir()))} {
		if p != tmp && strings.HasPrefix(p, tmp+string(filepath.Separator)) {
			return true
		}
	}
	for _, el := range strings.Split(p, string(filepath.Separator)) {
		if strings.HasPrefix(el, ".e2e-work-") {
			return true
		}
	}
	return false
}

// resolve follows symlinks in the longest prefix of p that exists (the XDG
// directories need not exist yet; on macOS /var is a link to /private/var).
func resolve(p string) string {
	rest := ""
	for q := p; ; q = filepath.Dir(q) {
		if real, err := filepath.EvalSymlinks(q); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(q) == q {
			return p
		}
		rest = filepath.Join(filepath.Base(q), rest)
	}
}
