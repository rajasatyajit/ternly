package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/discover"
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

// Memory notes go to a local model, else the session's own model; any other
// remote model only with memory_enrich: "remote".
func TestEnrichModelPrivacy(t *testing.T) {
	local := &discover.Provider{ID: "ollama", Local: true}
	cloudP := &discover.Provider{ID: "openrouter"}
	small := &discover.Model{Provider: local, ProvID: "ollama", ID: "tiny", Tier: 1, Tools: true, Ctx: 32000}
	big := &discover.Model{Provider: local, ProvID: "ollama", ID: "big", Tier: 2, Tools: true, Ctx: 32000}
	cloud := &discover.Model{Provider: local, ProvID: "ollama", ID: "x:cloud", Cloud: true, Tier: 3, Tools: true, Ctx: 32000}
	paid := &discover.Model{Provider: cloudP, ProvID: "openrouter", ID: "cheap", Tier: 1, Tools: true, Ctx: 32000, Priced: true, In: 0.1, Out: 0.1}
	session := &discover.Model{Provider: cloudP, ProvID: "openrouter", ID: "session-model", Tier: 3, Tools: true, Ctx: 200000, Priced: true, In: 3, Out: 15}

	r := discover.NewRouter()
	r.SetModels([]*discover.Model{small, big, cloud, paid, session})
	if got := enrichModel(r, session, false); got != big {
		t.Fatalf("with local models: %v", got)
	}
	r.SetModels([]*discover.Model{cloud, paid, session})
	if got := enrichModel(r, session, false); got != session {
		t.Fatalf("no local model: %v, want the session's own", got)
	}
	if got := enrichModel(r, nil, false); got != nil {
		t.Fatalf("no local model, no session model: %v, want none (remote needs opt-in)", got)
	}
	if got := enrichModel(r, nil, true); got == nil || got.Local() {
		t.Fatalf("remote opt-in: %v", got)
	}

	for in, want := range map[string]enrichSetting{`false`: enrichOff, `true`: enrichLocal, `"local"`: enrichLocal, `"remote"`: enrichRemote, `null`: enrichLocal} {
		var e enrichSetting
		if err := json.Unmarshal([]byte(in), &e); err != nil || e != want {
			t.Errorf("%s → %v %v", in, e, err)
		}
	}
	var e enrichSetting
	if err := json.Unmarshal([]byte(`"cloud"`), &e); err == nil {
		t.Error("unknown value accepted")
	}
}

// The default build embeds only the code graph's six grammars: it must not
// link gotreesitter's grammars package (which embeds all 206, +18 MB).
// -tags ternly_all_grammars opts in.
func TestDefaultBuildOnlyBuiltinGrammars(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l == "github.com/odvcencio/gotreesitter/grammars" {
			t.Fatal("the default build links gotreesitter/grammars (every grammar embedded)")
		}
	}
}
