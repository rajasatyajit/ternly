package verify

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Unformatted code never ends ✓ verified (v0.1.1 review).

func TestGofmtFailsUnformatted(t *testing.T) {
	need(t, "go", "gofmt")
	root := t.TempDir()
	files(t, root, map[string]string{
		"go.mod": "module m\n\ngo 1.22\n",
		"a.go":   "package m\n\nfunc A() int {\nreturn 1\n}\n", // compiles, isn't gofmt'd
		"b.go":   "package m\n\nfunc B() int { return 2 }\n",
	})
	r := Cover(context.Background(), root, []string{"a.go", "b.go"}, runner(t, root, nil))
	if r.OK() || !strings.Contains(r.Failed, "\nnot formatted: a.go\n") || !strings.Contains(r.Failed, "gofmt -w") {
		t.Fatalf("%+v", r)
	}
	files(t, root, map[string]string{"a.go": "package m\n\nfunc A() int {\n\treturn 1\n}\n"})
	r = Cover(context.Background(), root, []string{"a.go", "b.go"}, runner(t, root, nil))
	if !r.OK() || !strings.Contains(strings.Join(r.Summary, ";"), "gofmt: 2 file(s)") {
		t.Fatalf("formatted: %+v", r)
	}
}

func TestRustfmtJudgesChangedFilesOnly(t *testing.T) {
	need(t, "cargo", "rustfmt")
	root := t.TempDir()
	files(t, root, map[string]string{
		"Cargo.toml": "[package]\nname = \"c\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs": "mod old;\n\npub fn f() -> u8 {\n    old::g()\n}\n",
		"src/old.rs": "pub fn g()->u8{0}\n", // unformatted, but not changed this turn
	})
	r := Cover(context.Background(), root, []string{"src/lib.rs"}, runner(t, root, nil))
	if !r.OK() || !strings.Contains(strings.Join(r.Summary, ";"), "rustfmt: 1 file(s)") {
		t.Fatalf("an unchanged child module's formatting failed the turn: %+v", r)
	}
	r = Cover(context.Background(), root, []string{"src/lib.rs", "src/old.rs"}, runner(t, root, nil))
	if r.OK() || !strings.Contains(r.Failed, "\nnot formatted: src/old.rs\n") || !strings.Contains(r.Failed, "--edition 2021") {
		t.Fatalf("a changed unformatted file passed: %+v", r)
	}
}

func TestRustfmtMissingIsAGap(t *testing.T) {
	need(t, "cargo")
	root := t.TempDir()
	files(t, root, map[string]string{
		"Cargo.toml": "[package]\nname = \"c\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs": "pub fn f() -> u8 {\n    0\n}\n",
	})
	r := Cover(context.Background(), root, []string{"src/lib.rs"}, runner(t, root, map[string]string{"rustfmt": "exit 127"}))
	if r.OK() || len(r.Gaps) != 1 || !strings.Contains(r.Gaps[0].Why, "rustfmt isn't installed") {
		t.Fatalf("%+v", r)
	}
}

// Python and JS/TS have no standard formatter: only one the project
// configures is run, and it decides.
func TestConfiguredFormatters(t *testing.T) {
	need(t, "python3", "node")
	for _, c := range []struct {
		name  string
		files map[string]string
		fake  map[string]string
		check []string
		fail  string // "" = passes
		ran   string
	}{
		{"python, nothing configured", map[string]string{"a.py": "x=1\n"}, nil, []string{"a.py"}, "", ""},
		{"python, ruff configured, unformatted", map[string]string{"a.py": "x=1\n", "pyproject.toml": "[tool.ruff]\nline-length = 100\n"},
			map[string]string{"ruff": "echo 'Would reformat: a.py'; exit 1"}, []string{"a.py"}, "Would reformat: a.py", "ruff format --check"},
		{"python, black configured, formatted", map[string]string{"a.py": "x = 1\n", "pyproject.toml": "[tool.black]\n"},
			map[string]string{"black": "exit 0"}, []string{"a.py"}, "", "black --check"},
		{"python, ruff configured, not installed", map[string]string{"a.py": "x=1\n", "ruff.toml": ""},
			map[string]string{"ruff": "exit 127"}, []string{"a.py"}, "", ""},
		{"js, prettier configured, unformatted", map[string]string{"a.js": "let x=1\n", ".prettierrc": "{}", "node_modules/.bin/prettier": "#!/bin/sh\necho '[warn] a.js'; exit 1\n"},
			nil, []string{"a.js"}, "[warn] a.js", "prettier"},
		{"js, prettier configured but not installed", map[string]string{"a.js": "let x=1\n", ".prettierrc": "{}"}, nil, []string{"a.js"}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			files(t, root, c.files)
			if p, ok := c.files["node_modules/.bin/prettier"]; ok {
				files(t, root, map[string]string{"node_modules/.bin/prettier": p})
				chmodX(t, root+"/node_modules/.bin/prettier")
			}
			r := Cover(context.Background(), root, c.check, runner(t, root, c.fake))
			if c.fail == "" && !r.OK() || c.fail != "" && !strings.Contains(r.Failed, c.fail) {
				t.Fatalf("%+v", r)
			}
			if ran := strings.Join(r.Ran, ";"); c.ran != "" && !strings.Contains(ran, c.ran) || c.ran == "" && (strings.Contains(ran, "ruff") || strings.Contains(ran, "black") || strings.Contains(ran, "prettier")) {
				t.Fatalf("ran %q, want %q", ran, c.ran)
			}
		})
	}
}

func chmodX(t *testing.T, p string) {
	t.Helper()
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// Both rustfmt diff headers: "path:line:" (rustfmt ≥ 1.5) and "path at line N:".
func TestRustfmtDiffHeaders(t *testing.T) {
	for _, h := range []string{"Diff in /w/src/old.rs:1:", "Diff in /w/src/old.rs at line 1:"} {
		if m := reDiffFile.FindStringSubmatch(h); m == nil || m[1] != "/w/src/old.rs" {
			t.Errorf("%q → %v", h, m)
		}
	}
}
