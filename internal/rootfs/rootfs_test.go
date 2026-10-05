package rootfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every way out is closed: "..", an absolute path elsewhere, a linked file,
// a linked directory on the way, a link with an absolute target; links that
// stay inside work.
func TestConfined(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	_ = os.WriteFile(secret, []byte("SECRET"), 0o600)
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "docs", "a.md"), []byte("ok"), 0o644)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(secret, filepath.Join(ws, "AGENTS.md")))
	must(os.Symlink(outside, filepath.Join(ws, ".claude")))
	must(os.Symlink("docs/a.md", filepath.Join(ws, "inside.md")))
	d, err := Open(ws)
	must(err)
	defer d.Close()
	for _, p := range []string{"AGENTS.md", ".claude/id_rsa", "../" + filepath.Base(outside) + "/id_rsa", secret, filepath.Join(ws, ".claude", "id_rsa")} {
		if b, err := d.ReadFile(p); err == nil || strings.Contains(string(b), "SECRET") {
			t.Errorf("%s: read %q (err %v)", p, b, err)
		}
	}
	if b, err := d.ReadFile("inside.md"); err != nil || string(b) != "ok" {
		t.Errorf("a link inside the directory: %q %v", b, err)
	}
	if b, err := d.ReadFile(filepath.Join(ws, "docs", "a.md")); err != nil || string(b) != "ok" {
		t.Errorf("absolute path inside: %q %v", b, err)
	}
	var walked []string
	must(d.WalkDir(".claude", func(p string, e fs.DirEntry, err error) error { walked = append(walked, p); return nil }))
	must(d.WalkDir(".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Type().IsRegular() {
			walked = append(walked, p)
		}
		return nil
	}))
	for _, w := range walked {
		if strings.Contains(w, "id_rsa") || !strings.HasPrefix(w, ws) {
			t.Errorf("walked to %s", w)
		}
	}
}
