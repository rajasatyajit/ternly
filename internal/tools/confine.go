package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// Confine is how plugin code (hooks, MCP servers) runs. It is stricter than
// the model's sandbox: the user's home is replaced by an empty directory,
// so ~/.ssh, ternly's memory and sessions, other tools' configs and keys
// don't exist; the plugin gets a private writable home of its own, its own
// directory read-only, the workspace only as granted, network only as
// granted, and none of the user's environment.
type Confine struct {
	Dir       string            // the plugin's files (read-only)
	Home      string            // private writable home (created if missing)
	Workspace string            // "", "ro" or "rw"
	Network   bool              // reach the network
	Env       map[string]string // extra environment (after ${VAR} expansion by the caller)
}

// toolchainDirs are runtimes plugins may need (node via nvm, uv's pythons,
// cargo and go binaries). They are read-only and hold no credentials.
var toolchainDirs = []string{".nvm", ".volta", ".bun", ".deno", ".cargo/bin", ".rustup/toolchains", "go/bin", ".local/bin", ".local/share/uv", ".pyenv", "sdk"}

// ErrNoSandbox: plugin code never runs without bubblewrap.
var ErrNoSandbox = errors.New("plugin code (hooks, MCP servers) runs only inside bubblewrap, which isn't available; install bubblewrap")

// PluginCmd builds the confined command for argv (not started).
func (s *Sandbox) PluginCmd(ctx context.Context, root string, c Confine, argv ...string) (*exec.Cmd, error) {
	bw := s.Bwrap
	if bw == "" {
		if p, err := exec.LookPath("bwrap"); err == nil {
			bw = p // the model's sandbox may be off (--no-sandbox); plugins stay confined
		} else {
			return nil, ErrNoSandbox
		}
	}
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return nil, err
	}
	a := []string{bw, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--new-session",
		"--tmpfs", home, // the real home is gone…
		"--bind", c.Home, home, // …and the plugin's own takes its place
	}
	if !c.Network {
		a = append(a, "--unshare-net")
	}
	switch c.Workspace {
	case "rw":
		a = append(a, "--bind", root, root)
	case "ro":
		a = append(a, "--ro-bind", root, root)
	}
	if c.Dir != "" {
		a = append(a, "--ro-bind", c.Dir, c.Dir)
	}
	for _, d := range toolchainDirs { // runtimes installed under home (npx, uvx, …), read-only
		if p := filepath.Join(home, d); exists(p) {
			a = append(a, "--ro-bind", p, p)
		}
	}
	for _, f := range []string{".netrc", ".git-credentials", ".npmrc", ".pypirc"} { // in case the private home is under the real one
		a = append(a, "--ro-bind-try", "/dev/null", filepath.Join(home, f))
	}
	dir := c.Dir
	if c.Workspace != "" {
		dir = root
	}
	a = append(a, "--chdir", orDefault(dir, home), "--clearenv")
	env := map[string]string{"HOME": home, "PATH": os.Getenv("PATH"), "LANG": orDefault(os.Getenv("LANG"), "C.UTF-8"), "TERNLY": "1", "TERNLY_PLUGIN": "1"}
	if c.Dir != "" {
		env["CLAUDE_PLUGIN_ROOT"] = c.Dir // the variable plugins expect (Claude Code)
	}
	if c.Workspace != "" {
		env["CLAUDE_PROJECT_DIR"] = root
	}
	for k, v := range c.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a = append(a, "--setenv", k, env[k])
	}
	a = append(append(a, "--"), argv...)
	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	cmd.Env = []string{} // bwrap itself gets nothing either
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	return cmd, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
