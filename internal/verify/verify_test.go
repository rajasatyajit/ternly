package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runner runs commands like the sandbox does (bash -c in the workspace),
// with fake tools first on PATH.
func runner(t *testing.T, root string, fake map[string]string) Runner {
	bin := t.TempDir()
	for name, script := range fake {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return func(ctx context.Context, cmd string, _ bool) (string, int, error) {
		c := exec.CommandContext(ctx, "bash", "-c", cmd)
		c.Dir = root
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GOFLAGS=-mod=mod", "CARGO_TERM_COLOR=never")
		out, err := c.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode(), nil
		}
		return string(out), 0, err
	}
}

func files(t *testing.T, root string, fs map[string]string) {
	for p, body := range fs {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func need(t *testing.T, tools ...string) {
	for _, x := range tools {
		if _, err := exec.LookPath(x); err != nil {
			t.Skip(x + " not installed")
		}
	}
}

func gaps(r *Report) string {
	var s []string
	for _, g := range r.Gaps {
		s = append(s, g.File+": "+g.Why)
	}
	return strings.Join(s, "; ")
}

// The dogfooding case (ADR 014): a stray empty go.mod. `go build ./...` from
// the root passes; coverage builds the file in its own module and fails.
func TestGoNestedModuleIsNotSkipped(t *testing.T) {
	need(t, "go")
	root := t.TempDir()
	files(t, root, map[string]string{
		"go.mod":                        "module example.com/x\n\ngo 1.22\n",
		"main.go":                       "package main\n\nfunc main() {}\n",
		"internal/netguard/go.mod":      "",
		"internal/netguard/netguard.go": "package netguard\n\nfunc broken( {\n",
	})
	run := runner(t, root, nil)
	if out, code, _ := run(context.Background(), "go build ./...", false); code != 0 {
		t.Fatalf("setup: the root build should pass (that's the trap): %s", out)
	}
	r := Cover(context.Background(), root, []string{"internal/netguard/go.mod", "internal/netguard/netguard.go"}, run)
	if r.OK() || r.Failed == "" && len(r.Gaps) == 0 {
		t.Fatalf("verified a package that doesn't compile: %+v", r)
	}
	t.Logf("failed=%q gaps=%s", firstLine(r.Failed), gaps(r))
}

func TestGoCoverage(t *testing.T) {
	need(t, "go")
	root := t.TempDir()
	files(t, root, map[string]string{
		"go.mod":            "module example.com/x\n\ngo 1.22\n",
		"a/a.go":            "package a\n\nfunc A() int { return 1 }\n",
		"a/a_test.go":       "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n",
		"a/win.go":          "//go:build windows\n\npackage a\n",
		"b/b.go":            "package b\n\nimport \"example.com/x/a\"\n\nvar B = a.A()\n",
		"tools/go.mod":      "module example.com/tools\n\ngo 1.22\n",
		"tools/gen/main.go": "package main\n\nfunc main() {}\n",
		"README.md":         "# x\n",
	})
	r := Cover(context.Background(), root, []string{"a/a.go", "a/a_test.go", "a/win.go", "b/b.go", "tools/gen/main.go", "README.md"}, runner(t, root, nil))
	if r.Failed != "" {
		t.Fatal(r.Failed)
	}
	if strings.Join(r.Covered, " ") != "a/a.go a/a_test.go b/b.go tools/gen/main.go" {
		t.Fatalf("covered %v", r.Covered)
	}
	if len(r.Gaps) != 1 || r.Gaps[0].File != "a/win.go" || !strings.Contains(r.Gaps[0].Why, "build constraints") || r.Skipped != 1 {
		t.Fatalf("gaps %s, skipped %d", gaps(r), r.Skipped)
	}
	t.Log(r.Summary, r.Ran)

	// A test file that doesn't type-check fails (vet compiles tests).
	files(t, root, map[string]string{"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = Nope() }\n"})
	if r := Cover(context.Background(), root, []string{"a/a_test.go"}, runner(t, root, nil)); r.Failed == "" {
		t.Fatal("a broken test file passed")
	}
}

func TestPythonAndJS(t *testing.T) {
	need(t, "python3", "node")
	root := t.TempDir()
	files(t, root, map[string]string{"ok.py": "def f():\n    return 1\n", "ok.mjs": "export const x = 1;\n", "bad.py": "def f(:\n"})
	run := runner(t, root, nil)
	if r := Cover(context.Background(), root, []string{"ok.py", "ok.mjs"}, run); !r.OK() || len(r.Covered) != 2 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "__pycache__")); err == nil {
		t.Error("checking wrote bytecode into the workspace")
	}
	if r := Cover(context.Background(), root, []string{"bad.py"}, run); r.Failed == "" {
		t.Fatal("a syntax error passed")
	}
	files(t, root, map[string]string{"bad.cjs": "module.exports = {\n"})
	if r := Cover(context.Background(), root, []string{"bad.cjs"}, run); r.Failed == "" {
		t.Fatal("a JS syntax error passed")
	}
}

func TestTypeScriptInclusion(t *testing.T) {
	root := t.TempDir()
	files(t, root, map[string]string{
		"web/tsconfig.json": `{"include":["src"]}`,
		"web/src/a.ts":      "export const a = 1;\n",
		"web/scripts/b.ts":  "export const b = 1;\n",
		// A stand-in tsc: lists src/ files, type-checks nothing.
		"web/node_modules/.bin/tsc": "#!/bin/sh\nif [ \"$1\" = --listFilesOnly ]; then for f in \"$3\"/src/*.ts; do echo \"$PWD/$f\"; done; fi\nexit 0\n",
	})
	_ = os.Chmod(filepath.Join(root, "web/node_modules/.bin/tsc"), 0o755)
	r := Cover(context.Background(), root, []string{"web/src/a.ts", "web/scripts/b.ts", "x.ts"}, runner(t, root, nil))
	if strings.Join(r.Covered, " ") != "web/src/a.ts" || len(r.Gaps) != 2 {
		t.Fatalf("covered %v gaps %s", r.Covered, gaps(r))
	}
	t.Log(gaps(r))
}

func TestRustUnreachableFile(t *testing.T) {
	need(t, "cargo")
	root := t.TempDir()
	files(t, root, map[string]string{
		"Cargo.toml":    "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs":    "mod used;\npub fn f() -> u8 { used::g() }\n",
		"src/used.rs":   "pub fn g() -> u8 { 1 }\n",
		"src/orphan.rs": "pub fn h() -> u8 { this does not compile }\n",
	})
	r := Cover(context.Background(), root, []string{"src/lib.rs", "src/used.rs", "src/orphan.rs"}, runner(t, root, nil))
	if r.Failed != "" {
		t.Fatal(r.Failed)
	}
	if strings.Join(r.Covered, " ") != "src/lib.rs src/used.rs" || len(r.Gaps) != 1 || r.Gaps[0].File != "src/orphan.rs" {
		t.Fatalf("covered %v gaps %s", r.Covered, gaps(r))
	}
}

func TestJavaClassFreshness(t *testing.T) {
	need(t, "javac")
	root := t.TempDir()
	files(t, root, map[string]string{
		"pom.xml":                  "<project/>",
		"src/main/java/p/A.java":   "package p; public class A {}\n",
		"src/main/java/p/Old.java": "package p; public class Old {}\n",
		"lib/Stray.java":           "class Stray {}\n",
	})
	// A stand-in mvn: compiles A.java only (as if Old were excluded).
	mvn := "mkdir -p target/classes && javac -d target/classes src/main/java/p/A.java\n"
	_ = os.MkdirAll(filepath.Join(root, "target/classes/p"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "target/classes/p/Old.class"), nil, 0o644)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(root, "target/classes/p/Old.class"), old, old) // stale
	r := Cover(context.Background(), root, []string{"src/main/java/p/A.java", "src/main/java/p/Old.java", "lib/Stray.java"}, runner(t, root, map[string]string{"mvn": mvn}))
	if strings.Join(r.Covered, " ") != "src/main/java/p/A.java" || len(r.Gaps) != 2 {
		t.Fatalf("covered %v gaps %s failed %s", r.Covered, gaps(r), r.Failed)
	}
}

func TestUnknownLanguageIsAGap(t *testing.T) {
	r := Cover(context.Background(), t.TempDir(), []string{"x.rb", "notes.txt", "c.yaml"}, func(context.Context, string, bool) (string, int, error) { return "", 0, nil })
	if len(r.Gaps) != 1 || r.Skipped != 2 || r.OK() {
		t.Fatalf("%+v", r)
	}
}

func TestMissingToolIsAGapNotAPass(t *testing.T) {
	root := t.TempDir()
	files(t, root, map[string]string{"go.mod": "module x\n", "a.go": "package x\n"})
	r := Cover(context.Background(), root, []string{"a.go"}, func(context.Context, string, bool) (string, int, error) { return "go: not found", 127, nil })
	if r.OK() || len(r.Gaps) != 1 || r.Failed != "" {
		t.Fatalf("%+v", r)
	}
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

// A workspace reached through a symlink (macOS: /var → /private/var): the
// tools print resolved paths, which must still map back to the workspace.
func TestLinkedWorkspaceRoot(t *testing.T) {
	need(t, "go")
	real := t.TempDir()
	root := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, root); err != nil {
		t.Fatal(err)
	}
	files(t, root, map[string]string{"go.mod": "module x\n\ngo 1.22\n", "a/a.go": "package a\n"})
	if r := Cover(context.Background(), root, []string{"a/a.go"}, runner(t, root, nil)); !r.OK() || len(r.Covered) != 1 {
		t.Fatalf("%+v %s", r, gaps(r))
	}
}

// Unsandboxed, a runner declines commands that run the repository's code;
// their files become gaps that say so — never a pass, never a failure.
func TestDeclinedCommandIsAGap(t *testing.T) {
	need(t, "cargo", "python3")
	root := t.TempDir()
	files(t, root, map[string]string{"Cargo.toml": "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n", "src/lib.rs": "pub fn f() {}\n", "a.py": "x = 1\n"})
	inner := runner(t, root, nil)
	var asked []string
	run := func(ctx context.Context, cmd string, repoCode bool) (string, int, error) {
		if repoCode {
			asked = append(asked, cmd)
			return "", 0, fmt.Errorf("%w: unsandboxed, and running the repository's code wasn't approved", ErrNotRun)
		}
		return inner(ctx, cmd, repoCode)
	}
	r := Cover(context.Background(), root, []string{"src/lib.rs", "a.py"}, run)
	if r.Failed != "" || strings.Join(r.Covered, " ") != "a.py" || len(r.Gaps) != 1 || !strings.Contains(r.Gaps[0].Why, "wasn't approved") {
		t.Fatalf("%+v %s", r, gaps(r))
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "cargo check") {
		t.Fatalf("asked %q", asked)
	}
}
