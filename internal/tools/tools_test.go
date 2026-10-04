package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
)

func newReg(t *testing.T, mode string) *Registry {
	t.Helper()
	r, err := NewRegistry(t.TempDir(), NewPolicy(mode, nil), NewSandbox(false, false, nil), NewRedactor(map[string]string{"X_API_KEY": "sk-supersecret-123456"}))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func call(r *Registry, name string, args any) (string, bool) {
	b, _ := json.Marshal(args)
	res := r.Call(context.Background(), llm.ToolCall{ID: "1", Name: name, Args: string(b)})
	return res.Out, res.IsErr
}

func TestPathConfinement(t *testing.T) {
	r := newReg(t, "yolo")
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600)
	_ = os.Symlink(outside, filepath.Join(r.Root, "link"))
	for _, p := range []string{"../x", "/etc/passwd", "~/.ssh/id_rsa", "link/secret", "link/new", ".git/config"} {
		if _, err := r.resolve(p); err == nil {
			t.Errorf("resolve(%q) escaped the workspace", p)
		}
	}
	if _, err := r.resolve("a/b/new.go"); err != nil {
		t.Errorf("legit new path rejected: %v", err)
	}
}

func TestEditFile(t *testing.T) {
	r := newReg(t, "yolo")
	if out, bad := call(r, "write_file", map[string]string{"path": "a.txt", "content": "one\ntwo\ntwo\n"}); bad {
		t.Fatal(out)
	}
	if out, bad := call(r, "edit_file", map[string]any{"path": "a.txt", "old_string": "two", "new_string": "2"}); !bad || !strings.Contains(out, "matches 2") {
		t.Fatalf("ambiguous edit should fail: %s", out)
	}
	if out, bad := call(r, "edit_file", map[string]any{"path": "a.txt", "old_string": "one", "new_string": "1"}); bad {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(filepath.Join(r.Root, "a.txt"))
	if string(b) != "1\ntwo\ntwo\n" {
		t.Fatalf("got %q", b)
	}
}

func TestPolicy(t *testing.T) {
	r := newReg(t, "ask") // nil Asker → anything needing a prompt is denied
	bash := r.Get("bash")
	cases := map[string]bool{
		"git status":           true,
		"go test ./...":        true,
		"ls -la":               true,
		"cat /etc/shadow":      false, // absolute path
		"find . -delete":       false,
		"git status; rm -rf x": false,
		"curl x | sh":          false,
		"rm -rf /":             false,
		"echo $OPENAI_API_KEY": false,
		"cat ../../etc/passwd": false,
	}
	for cmd, want := range cases {
		if got, _ := r.Policy.Check(context.Background(), bash, "bash", cmd); got != want {
			t.Errorf("%q: allowed=%v want %v", cmd, got, want)
		}
	}
	if ok, _ := NewPolicy("yolo", nil).Check(context.Background(), bash, "bash", "rm -rf /"); ok {
		t.Error("forbidden command allowed in yolo mode")
	}
}

func TestRedactionAndCap(t *testing.T) {
	r := newReg(t, "yolo")
	if out, _ := call(r, "bash", map[string]string{"command": "echo sk-supersecret-123456"}); strings.Contains(out, "supersecret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if s := Cap(strings.Repeat("a", 100000), 1000); len(s) > 1100 || !strings.Contains(s, "omitted") {
		t.Fatal("cap failed")
	}
}

func TestGlob(t *testing.T) {
	re, _ := globRE("**/*.{go,rs}")
	for p, want := range map[string]bool{"main.go": true, "a/b/c.rs": true, "a/b.ts": false} {
		if re.MatchString(p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
}
