// Package gitenv runs git in a repository that ternly, or a test, owns,
// isolated from whatever git environment ternly itself was started in.
//
// git exports GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_CONFIG_PARAMETERS
// and more to hooks, and users can export them too. A git command inherits
// them, and they override `-C dir` and repository discovery. So a test that
// runs `git init` and `git commit` in its own temp directory, run from a
// pre-push hook in a worktree (absolute GIT_DIR), acted on ternly's own
// repository: commits by "t <t@t>", its user.name rewritten, core.bare=true
// (2026-10-08; ADR 024).
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Clean returns base without any GIT_* variable, plus GIT_CONFIG_GLOBAL=
// /dev/null and GIT_CONFIG_NOSYSTEM=1: no repository, index, work tree,
// object directory, namespace, -c parameter or user/system config leaks in.
func Clean(base []string) []string {
	out := strip(base)
	return append(out, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

// UserConfig is for reading the user's own global settings (such as
// core.excludesFile): the repository variables go, the global config stays,
// including a GIT_CONFIG_GLOBAL the user set on purpose.
func UserConfig(base []string) []string { return Workspace(base) }

func strip(base []string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(k, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

// Workspace returns base without the variables that point git at a
// repository or change its config (git rev-parse --local-env-vars, plus the
// numbered GIT_CONFIG_KEY_n/VALUE_n and GIT_NAMESPACE), keeping the user's
// own settings (credentials, GIT_SSH_COMMAND, askpass, global config). For
// git on the user's workspace or a remote URL, where those settings must
// apply but an inherited GIT_DIR must not redirect it.
func Workspace(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !repoVar(k) {
			out = append(out, kv)
		}
	}
	return out
}

// repoVars is git's own list (git rev-parse --local-env-vars, git 2.56).
var repoVars = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_COUNT": true, "GIT_OBJECT_DIRECTORY": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
	"GIT_IMPLICIT_WORK_TREE": true, "GIT_GRAFT_FILE": true, "GIT_INDEX_FILE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true,
}

func repoVar(k string) bool {
	return repoVars[k] || strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_")
}

// Command is exec.CommandContext for git with the Clean environment.
// Callers append their own GIT_* settings (GIT_DIR of their own repository,
// GIT_TERMINAL_PROMPT=0) to cmd.Env after it.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", args...)
	c.Env = Clean(os.Environ())
	return c
}
