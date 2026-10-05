package agent

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/rajasatyajit/ternly/internal/rootfs"
)

var reGoAll = regexp.MustCompile(`\bgo\s+(build|vet|test)\b[^&|;]*\./\.\.\.`)

// uncovered names the files changed this turn that a passing Go verify
// command can't have checked: `./...` stops at a nested go.mod, so a stray
// go.mod (a model's workaround for a refused mkdir, in dogfooding — ADR 014)
// silently removes a package from the build, vet and tests.
func (a *Agent) uncovered(ctx context.Context, st *turnState, cmd string) string {
	if a.CP == nil || st.tree == "" || !reGoAll.MatchString(cmd) {
		return ""
	}
	cs, err := a.CP.Pending(ctx, st.tree)
	if err != nil {
		return ""
	}
	ws, err := rootfs.Open(a.Reg.Root)
	if err != nil {
		return ""
	}
	nested := map[string][]string{} // go.mod → changed files under it
	for _, c := range cs {
		// Pending describes restoring: 'A' (recreate) is a file this turn deleted.
		if c.Status == 'A' || !strings.HasSuffix(c.Path, ".go") && path.Base(c.Path) != "go.mod" {
			continue
		}
		for d := path.Dir(c.Path); d != "." && d != "/"; d = path.Dir(d) {
			if mod := d + "/go.mod"; ws.Exists(mod) {
				nested[mod] = append(nested[mod], c.Path)
				break
			}
		}
	}
	if len(nested) == 0 {
		return ""
	}
	var b strings.Builder
	for mod, files := range nested {
		fmt.Fprintf(&b, "%s makes %s a separate module, so `%s` did not build, vet or test %s.\n", mod, path.Dir(mod), cmd, listFew(files, 4))
	}
	b.WriteString("If it isn't meant to be a module, delete that go.mod (delete_file); write_file creates directories by itself.")
	return b.String()
}
