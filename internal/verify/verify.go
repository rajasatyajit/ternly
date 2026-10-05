// Package verify decides whether a turn's changes were verified (ADR 015).
// A check counts only if it covers every changed source file: the files'
// own packages, modules or crates are built and checked, and coverage is
// shown, not assumed. A file no check can be shown to cover is a gap, and
// a turn with a gap is "unverified", never ✓.
//
// The class this closes: in dogfooding (ADR 014) a stray go.mod made a
// package a separate module, `go build ./...` skipped it, and ternly said
// "verified" about code that didn't compile.
package verify

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/rootfs"
)

// Runner runs a shell command in the workspace root (the sandbox), returning
// its combined output and exit code.
type Runner func(ctx context.Context, cmd string) (out string, code int, err error)

// Gap is a changed source file no check covered, and why.
type Gap struct{ File, Why string }

// Report is what the coverage checks did.
type Report struct {
	Covered []string // changed source files a passing check covered
	Gaps    []Gap
	Ran     []string // commands run
	Failed  string   // the first failing command's output ("" if none failed)
	Summary []string // "go build+vet: 2 packages", …
	Skipped int      // changed files that aren't source (docs, config)
}

// OK reports a clean pass with no gaps.
func (r *Report) OK() bool { return r.Failed == "" && len(r.Gaps) == 0 }

// Sources are the extensions treated as source code. A changed file with
// one of these that no checker handles is a gap.
var lang = map[string]string{
	".go": "go", ".py": "python", ".pyi": "python",
	".js": "js", ".mjs": "js", ".cjs": "js",
	".ts": "ts", ".tsx": "ts", ".mts": "ts", ".cts": "ts", ".jsx": "ts",
	".rs": "rust", ".java": "java",
	".c": "", ".h": "", ".cc": "", ".cpp": "", ".hpp": "", ".cs": "", ".kt": "", ".kts": "", ".swift": "", ".scala": "",
	".rb": "", ".php": "", ".lua": "", ".zig": "", ".ex": "", ".exs": "", ".dart": "", ".m": "", ".sh": "", ".bash": "",
}

// Cover checks the changed files (workspace-relative, slash-separated; the
// ones that still exist). root is the workspace.
func Cover(ctx context.Context, root string, changed []string, run Runner) *Report {
	r := &Report{}
	ws, err := rootfs.Open(root)
	if err != nil {
		for _, f := range changed {
			r.Gaps = append(r.Gaps, Gap{f, "workspace unreadable: " + err.Error()})
		}
		return r
	}
	defer ws.Close()
	by := map[string][]string{}
	for _, f := range changed {
		l, ok := lang[path.Ext(f)]
		switch {
		case !ok:
			r.Skipped++
		case l == "":
			r.Gaps = append(r.Gaps, Gap{f, "no coverage check for " + path.Ext(f) + " files"})
		default:
			by[l] = append(by[l], f)
		}
	}
	real, _ := filepath.EvalSymlinks(root)
	c := &checker{root: filepath.ToSlash(root), real: filepath.ToSlash(real), ws: ws, run: run, r: r}
	for _, l := range []string{"go", "python", "js", "ts", "rust", "java"} {
		if fs := by[l]; len(fs) > 0 && r.Failed == "" {
			sort.Strings(fs)
			map[string]func(context.Context, []string){"go": c.golang, "python": c.python, "js": c.js, "ts": c.ts, "rust": c.rust, "java": c.java}[l](ctx, fs)
		}
	}
	sort.Strings(r.Covered)
	sort.Slice(r.Gaps, func(i, j int) bool { return r.Gaps[i].File < r.Gaps[j].File })
	return r
}

type checker struct {
	root string
	real string // root with symlinks resolved
	ws   *rootfs.Dir
	run  Runner
	r    *Report
}

// exec runs cmd; a failure is recorded and reported false.
func (c *checker) exec(ctx context.Context, cmd string) (string, bool) {
	c.r.Ran = append(c.r.Ran, cmd)
	out, code, err := c.run(ctx, cmd)
	if err != nil || code != 0 {
		if err != nil {
			out += "\n" + err.Error()
		}
		if code == 127 { // the tool isn't installed: not a failure of the code
			return out, false
		}
		if c.r.Failed == "" {
			c.r.Failed = "$ " + cmd + "\n" + strings.TrimSpace(out)
		}
		return out, false
	}
	return out, true
}

func (c *checker) gaps(files []string, why string) {
	for _, f := range files {
		c.r.Gaps = append(c.r.Gaps, Gap{f, why})
	}
}

// owner returns the nearest directory at or above dir (inside the
// workspace) holding one of the marker files, and which one.
func (c *checker) owner(dir string, markers ...string) (string, string) {
	for d := dir; ; d = path.Dir(d) {
		for _, m := range markers {
			if c.ws.Exists(path.Join(d, m)) {
				return d, m
			}
		}
		if d == "." || d == "/" || d == "" {
			return "", ""
		}
	}
}

// group maps each file to its owner directory (files with none get why).
func (c *checker) group(files []string, why string, markers ...string) (map[string][]string, map[string]string) {
	out, kind := map[string][]string{}, map[string]string{}
	for _, f := range files {
		d, m := c.owner(path.Dir(f), markers...)
		if d == "" {
			c.gaps([]string{f}, why)
			continue
		}
		out[d] = append(out[d], f)
		kind[d] = m
	}
	return out, kind
}

func sortedKeys(m map[string][]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Go: each file's package, in its own module, listed (so a file excluded
// by build constraints, or outside any package, is a gap) and then built
// and vetted — vet type-checks the test files too.
func (c *checker) golang(ctx context.Context, files []string) {
	mods, _ := c.group(files, "not in a Go module (no go.mod above it)", "go.mod")
	pkgs := 0
	for _, mod := range sortedKeys(mods) {
		dirs := map[string][]string{}
		for _, f := range mods[mod] {
			rel := strings.TrimPrefix(strings.TrimPrefix(path.Dir(f), mod), "/")
			dirs["./"+rel] = append(dirs["./"+rel], f)
		}
		var args []string
		for _, d := range sortedKeys(dirs) {
			args = append(args, quote(goPkgArg(d)))
		}
		const sep = "\x1f"
		out, ok := c.exec(ctx, "cd "+quote(mod)+" && go list -e -f '{{.Dir}}"+sep+"{{join .GoFiles \" \"}} {{join .CgoFiles \" \"}} {{join .TestGoFiles \" \"}} {{join .XTestGoFiles \" \"}}"+sep+"{{join .IgnoredGoFiles \" \"}}"+sep+"{{if .Error}}{{.Error}}{{end}}' "+strings.Join(args, " "))
		if !ok {
			c.gaps(mods[mod], "`go list` failed in module "+mod)
			continue
		}
		in, ignored := map[string]bool{}, map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			p := strings.Split(line, sep)
			if len(p) != 4 {
				continue
			}
			if p[3] != "" {
				c.r.Failed = "$ go list (module " + mod + ")\n" + p[3]
				return
			}
			dir := c.rel(p[0])
			for _, n := range strings.Fields(p[1]) {
				in[path.Join(dir, n)] = true
			}
			for _, n := range strings.Fields(p[2]) {
				ignored[path.Join(dir, n)] = true
			}
		}
		var covered []string
		for _, d := range sortedKeys(dirs) {
			for _, f := range dirs[d] {
				switch {
				case in[f]:
					covered = append(covered, f)
				case ignored[f]:
					c.gaps([]string{f}, "excluded by build constraints (no check compiles it)")
				default:
					c.gaps([]string{f}, "not part of any package `go list` reports")
				}
			}
		}
		if _, ok := c.exec(ctx, "cd "+quote(mod)+" && go build -o /dev/null "+strings.Join(args, " ")+" && go vet "+strings.Join(args, " ")); !ok {
			if c.r.Failed == "" { // go missing
				c.gaps(covered, "the go command isn't available")
			}
			return
		}
		c.r.Covered = append(c.r.Covered, covered...)
		pkgs += len(args)
	}
	if pkgs > 0 {
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("go build+vet: %d package(s)", pkgs))
	}
}

// rel maps an absolute path a tool printed (the sandbox binds the
// workspace at its own path) to a workspace-relative one.
func (c *checker) rel(abs string) string {
	abs = filepath.ToSlash(strings.TrimSpace(abs))
	for _, r := range []string{c.root, c.real} { // tools print the resolved path (macOS: /var → /private/var)
		if r = strings.TrimSuffix(r, "/"); r != "" && (abs == r || strings.HasPrefix(abs, r+"/")) {
			return path.Clean(strings.TrimPrefix(strings.TrimPrefix(abs, r), "/"))
		}
	}
	return abs
}

// Python: every file compiled (syntax and compile-time errors), without
// writing bytecode.
func (c *checker) python(ctx context.Context, files []string) {
	args := quoteAll(files)
	if _, ok := c.exec(ctx, `python3 -c 'import sys
for f in sys.argv[1:]:
    compile(open(f, "rb").read(), f, "exec", dont_inherit=True)' `+args); !ok {
		if c.r.Failed == "" {
			c.gaps(files, "python3 isn't available")
		}
		return
	}
	c.r.Covered = append(c.r.Covered, files...)
	c.r.Summary = append(c.r.Summary, fmt.Sprintf("python compile: %d file(s)", len(files)))
}

// JavaScript: node --check on each file.
func (c *checker) js(ctx context.Context, files []string) {
	if _, ok := c.exec(ctx, `for f in `+quoteAll(files)+`; do node --check "$f" || exit 1; done`); !ok {
		if c.r.Failed == "" {
			c.gaps(files, "node isn't available")
		}
		return
	}
	c.r.Covered = append(c.r.Covered, files...)
	c.r.Summary = append(c.r.Summary, fmt.Sprintf("node --check: %d file(s)", len(files)))
}

// TypeScript (and JSX): the project's own tsc. --listFilesOnly shows the
// file is in the program; --noEmit type-checks it.
func (c *checker) ts(ctx context.Context, files []string) {
	projs, _ := c.group(files, "no tsconfig.json above it", "tsconfig.json")
	for _, p := range sortedKeys(projs) {
		bin := ""
		for d := p; ; d = path.Dir(d) {
			if b := path.Join(d, "node_modules/.bin/tsc"); c.ws.Exists(b) {
				bin = b
				break
			}
			if d == "." || d == "" {
				break
			}
		}
		if bin == "" {
			c.gaps(projs[p], "no TypeScript compiler installed in the project (node_modules/.bin/tsc)")
			continue
		}
		list, ok := c.exec(ctx, quote("./"+bin)+" --listFilesOnly -p "+quote(p))
		if !ok {
			c.gaps(projs[p], "tsc --listFilesOnly failed")
			continue
		}
		in := map[string]bool{}
		for _, l := range strings.Split(list, "\n") {
			in[c.rel(l)] = true
		}
		var covered []string
		for _, f := range projs[p] {
			if in[f] {
				covered = append(covered, f)
			} else {
				c.gaps([]string{f}, "not included by "+path.Join(p, "tsconfig.json"))
			}
		}
		if _, ok := c.exec(ctx, quote("./"+bin)+" --noEmit -p "+quote(p)); !ok {
			return
		}
		c.r.Covered = append(c.r.Covered, covered...)
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("tsc --noEmit: %d file(s) in %s", len(covered), p))
	}
}

// Rust: cargo check on each file's package, all targets. A file is covered
// when a dep-info file written after the file changed lists it — so a file
// no `mod` reaches is a gap even though the check passed.
func (c *checker) rust(ctx context.Context, files []string) {
	pkgs, _ := c.group(files, "no Cargo.toml above it", "Cargo.toml")
	for _, p := range sortedKeys(pkgs) {
		meta, ok := c.exec(ctx, "cd "+quote(p)+" && cargo metadata --format-version 1 --no-deps --offline")
		target := ""
		if ok {
			if m := regexp.MustCompile(`"target_directory":"([^"]+)"`).FindStringSubmatch(meta); m != nil {
				target = c.rel(m[1])
			}
		}
		if target == "" || strings.HasPrefix(target, "/") {
			c.gaps(pkgs[p], "cargo's target directory is outside the workspace or unknown")
			continue
		}
		if _, ok := c.exec(ctx, "cd "+quote(p)+" && cargo check --quiet --all-targets"); !ok {
			if c.r.Failed == "" {
				c.gaps(pkgs[p], "cargo isn't available")
			}
			return
		}
		listed := map[string]time.Time{}
		_ = c.ws.WalkDir(path.Join(target, "debug"), func(fp string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || path.Ext(fp) != ".d" {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			b, err := c.ws.ReadFile(fp)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(string(b), "\n") {
				_, deps, ok := strings.Cut(line, ": ")
				if !ok {
					continue
				}
				for _, d := range strings.Fields(deps) {
					d = c.rel(d)
					if t, ok := listed[d]; !ok || info.ModTime().After(t) {
						listed[d] = info.ModTime()
					}
				}
			}
			return nil
		})
		var covered []string
		for _, f := range pkgs[p] {
			fi, err := c.ws.Stat(f)
			if t, ok := listed[f]; ok && err == nil && !t.Before(fi.ModTime()) {
				covered = append(covered, f)
			} else {
				c.gaps([]string{f}, "no crate target compiles it (not reachable through `mod`)")
			}
		}
		c.r.Covered = append(c.r.Covered, covered...)
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("cargo check: %d file(s) in %s", len(covered), p))
	}
}

// Java: Maven or Gradle compiles main and test sources; a file is covered
// when its class file is at least as new as the source.
func (c *checker) java(ctx context.Context, files []string) {
	projs, kind := c.group(files, "no pom.xml or build.gradle above it", "pom.xml", "build.gradle", "build.gradle.kts")
	for _, p := range sortedKeys(projs) {
		var cmd, mainOut, testOut string
		if kind[p] == "pom.xml" {
			cmd, mainOut, testOut = "mvn -q -DskipTests test-compile", "target/classes", "target/test-classes"
		} else {
			gradle := "gradle"
			if c.ws.Exists(path.Join(p, "gradlew")) {
				gradle = "./gradlew"
			}
			cmd, mainOut, testOut = gradle+" -q compileJava compileTestJava", "build/classes/java/main", "build/classes/java/test"
		}
		if _, ok := c.exec(ctx, "cd "+quote(p)+" && "+cmd); !ok {
			if c.r.Failed == "" {
				c.gaps(projs[p], "the build tool isn't available")
			}
			return
		}
		var covered []string
		for _, f := range projs[p] {
			rel := strings.TrimPrefix(strings.TrimPrefix(f, p), "/")
			class := ""
			for src, out := range map[string]string{"src/main/java/": mainOut, "src/test/java/": testOut} {
				if strings.HasPrefix(rel, src) {
					class = path.Join(p, out, strings.TrimSuffix(strings.TrimPrefix(rel, src), ".java")+".class")
				}
			}
			cs, cerr := c.ws.Stat(class)
			fi, ferr := c.ws.Stat(f)
			if class != "" && cerr == nil && ferr == nil && !cs.ModTime().Before(fi.ModTime()) {
				covered = append(covered, f)
			} else {
				c.gaps([]string{f}, "no up-to-date class file after the build (not in src/main/java or src/test/java?)")
			}
		}
		c.r.Covered = append(c.r.Covered, covered...)
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("%s: %d file(s) in %s", strings.Fields(cmd)[0], len(covered), p))
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func quoteAll(fs []string) string {
	q := make([]string, len(fs))
	for i, f := range fs {
		q[i] = quote(f)
	}
	return strings.Join(q, " ")
}

// goPkgArg keeps the ./ that makes a directory a path, not an import path.
func goPkgArg(d string) string {
	if c := path.Clean(d); c != "." {
		return "./" + c
	}
	return "."
}

// IsSource reports whether a coverage check is expected for the file.
func IsSource(file string) bool { _, ok := lang[path.Ext(file)]; return ok }
