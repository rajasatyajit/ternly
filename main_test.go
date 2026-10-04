package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacy(t *testing.T) {
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	setup := func(t *testing.T) (oldDir, newDir string) {
		base := t.TempDir()
		oldDir, newDir = filepath.Join(base, ".config", "vane"), filepath.Join(base, ".config", "ternly")
		if err := os.MkdirAll(oldDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oldDir, "keys.env"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		return oldDir, newDir
	}

	t.Run("moves when ternly dir is absent", func(t *testing.T) {
		oldDir, newDir := setup(t)
		if moved, err := migrateLegacy(oldDir, newDir); !moved || err != nil {
			t.Fatalf("moved=%v err=%v", moved, err)
		}
		if got := read(filepath.Join(newDir, "keys.env")); got != "old" {
			t.Fatalf("content not migrated: %q", got)
		}
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Fatal("legacy dir still present after move")
		}
		if moved, _ := migrateLegacy(oldDir, newDir); moved {
			t.Fatal("second run must be a no-op")
		}
	})

	t.Run("never overwrites an existing ternly dir", func(t *testing.T) {
		oldDir, newDir := setup(t)
		_ = os.MkdirAll(newDir, 0o700)
		_ = os.WriteFile(filepath.Join(newDir, "keys.env"), []byte("new"), 0o600)
		if moved, err := migrateLegacy(oldDir, newDir); moved || err != nil {
			t.Fatalf("moved=%v err=%v", moved, err)
		}
		if read(filepath.Join(newDir, "keys.env")) != "new" || read(filepath.Join(oldDir, "keys.env")) != "old" {
			t.Fatal("existing directories were modified")
		}
	})

	t.Run("dangling symlink at ternly path counts as existing", func(t *testing.T) {
		oldDir, newDir := setup(t)
		if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), newDir); err != nil {
			t.Fatal(err)
		}
		if moved, _ := migrateLegacy(oldDir, newDir); moved {
			t.Fatal("replaced a symlink at the ternly path")
		}
		if read(filepath.Join(oldDir, "keys.env")) != "old" {
			t.Fatal("legacy dir modified")
		}
	})

	t.Run("no legacy dir or legacy path is a file", func(t *testing.T) {
		base := t.TempDir()
		if moved, err := migrateLegacy(filepath.Join(base, "nope"), filepath.Join(base, "ternly")); moved || err != nil {
			t.Fatalf("moved=%v err=%v", moved, err)
		}
		f := filepath.Join(base, "vane")
		_ = os.WriteFile(f, []byte("x"), 0o600)
		if moved, _ := migrateLegacy(f, filepath.Join(base, "ternly")); moved {
			t.Fatal("moved a regular file")
		}
	})
}
