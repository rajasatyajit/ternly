package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/testutil"
)

func bwrapWorks() bool {
	return exec.Command("bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "true").Run() == nil
}

// Plugin code can't see the user's home (keys, ternly memory), environment
// or network, and gets only the workspace access granted.
func TestConfine(t *testing.T) {
	testutil.Require(t, "bubblewrap", bwrapWorks())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SECRET_API_KEY", "sk-should-not-leak")
	for p, c := range map[string]string{".ssh/id_ed25519": "SSH-KEY-MARKER", ".local/share/ternly/projects/x/memory.log": "MEMORY-MARKER", ".config/gh/hosts.yml": "GH-TOKEN-MARKER"} {
		_ = os.MkdirAll(filepath.Join(home, filepath.Dir(p)), 0o700)
		_ = os.WriteFile(filepath.Join(home, p), []byte(c), 0o600)
	}
	plugDir := filepath.Join(home, ".local/share/ternly/plugins/evil/abc123")
	_ = os.MkdirAll(plugDir, 0o755)
	_ = os.WriteFile(filepath.Join(plugDir, "hook.sh"), []byte("echo plugin-file-ok"), 0o755)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "main.go"), []byte("WS-MARKER"), 0o644)
	sb := NewSandbox(true, false, nil)
	run := func(c Confine, script string) string {
		cmd, err := sb.PluginCmd(context.Background(), ws, c, "sh", "-c", script)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	c := Confine{Dir: plugDir, Home: filepath.Join(home, ".local/share/ternly/plugins/evil/home")}
	out := run(c, `cat ~/.ssh/id_ed25519 ~/.local/share/ternly/projects/*/memory.log ~/.config/gh/hosts.yml; env; sh `+plugDir+`/hook.sh; cat main.go; echo x > ~/state && echo home-writable; curl -s -m 2 https://example.com >/dev/null && echo NET-OK || echo net-blocked`)
	for _, leak := range []string{"SSH-KEY-MARKER", "MEMORY-MARKER", "GH-TOKEN-MARKER", "sk-should-not-leak", "WS-MARKER", "NET-OK"} {
		if strings.Contains(out, leak) {
			t.Errorf("confined code saw %s:\n%s", leak, out)
		}
	}
	for _, want := range []string{"plugin-file-ok", "home-writable", "net-blocked", "TERNLY_PLUGIN=1", "CLAUDE_PLUGIN_ROOT=" + plugDir} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "state")); err == nil {
		t.Error("wrote to the real home")
	}

	c.Workspace = "ro"
	if out := run(c, `cat main.go; echo y > main.go 2>/dev/null && echo WROTE || echo ro-ok`); !strings.Contains(out, "WS-MARKER") || !strings.Contains(out, "ro-ok") {
		t.Errorf("read-only workspace: %s", out)
	}
	c.Workspace = "rw"
	run(c, `echo changed > new.txt`)
	if b, _ := os.ReadFile(filepath.Join(ws, "new.txt")); string(b) != "changed\n" {
		t.Error("read-write workspace not writable")
	}
}
