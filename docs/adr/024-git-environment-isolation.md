# ADR 024 — Git environment isolation: no git call inherits a repository

Status: accepted (from the Phase B/F review, 2026-10-08).

## Incident
On 2026-10-08, a push from the git worktree `.claude/worktrees/phase-b` ran `.githooks/pre-push`,
which runs the fast test tier. Tests that create their own temp repositories (`git init`,
`git commit`) acted on ternly's repository instead:
- **11 commits authored "t <t@t>"** on track1/phase-b. Its tree shrank to 3 files, so PR #20 would
  have deleted the repository.
  - Two of the commits added symlinks to the tests' fake keys in `/tmp`. The paths reached GitHub,
    the contents didn't.
- **The repository's local `user.name` and `user.email`** were set to "t".
- **`core.bare=true`** in the main repository's config.

main was never touched. The branch was rebuilt the same day, the config restored, and `git fsck` is
clean.

## Mechanism, confirmed
Reproduced in a throwaway repository with a worktree and a hook that records its environment:
- **git exports its environment to hooks** so that git commands inside the hook find the
  repository:
  - pre-push gets `GIT_DIR`;
  - pre-commit also gets `GIT_INDEX_FILE`, `GIT_AUTHOR_*` and `GIT_CONFIG_PARAMETERS` (every `-c`);
  - `GIT_WORK_TREE` comes when the caller sets it.
- **In the main checkout, `GIT_DIR` is the relative `.git`,** which resolves inside each test's
  temp directory, so it was harmless. **In a worktree it is absolute**
  (`…/.git/worktrees/<name>`), so it points at the real repository from anywhere.
- **Every git command a test runs inherits it, and it overrides `-C dir` and repository
  discovery:**
  - a test-style `git init` in a fresh temp directory re-initialises the real repository and sets
    `core.bare=true`;
  - its `git commit` lands there (commit 313cf7a in the reproduction).

### The same class in ternly itself
ternly started with `GIT_DIR` set (from inside a git hook, or exported by the user) inherits it
just the same:
- **the checkpoint store's first `git init --bare <its dir>`** would re-initialise that repository;
- **a plugin install's `git fetch`** would write into it. The CI sentinel below reproduced this:
  objects and a `shallow` file appeared in the sentinel;
- **the branch shown to the model and the session key** (the root commit) would be read from it.

## Decision
1. **`internal/gitenv`** is the one way to run git:
   - **`Clean`** drops every `GIT_*` and sets `GIT_CONFIG_GLOBAL=/dev/null` and
     `GIT_CONFIG_NOSYSTEM=1`. It is for repositories ternly or a test owns: the checkpoint store
     (which then adds its own `GIT_DIR`, `GIT_WORK_TREE` and `GIT_INDEX_FILE`), and every git
     call in tests.
   - **`Command`** is `exec.CommandContext("git", …)` with `Clean`.
   - **`Workspace`** drops only what points git at a repository or changes its config: git's own
     `git rev-parse --local-env-vars`, plus `GIT_CONFIG_KEY_n`/`VALUE_n` and `GIT_NAMESPACE`. It
     keeps the user's credentials, `GIT_SSH_COMMAND`, askpass and global config. It is for git on
     the user's workspace or a remote URL: branch name, session root commit, `ls-remote` of a
     plugin URL, plugin fetch. The checkpoint store's lookup of the user's `core.excludesFile`
     uses it too, because reading the user's config is its purpose.
2. **Test helpers that start child processes** (the ternly binary, `go list`) give them the
   `Clean` environment, so the child's own git calls can't inherit a hook's `GIT_DIR`.
3. **The pre-push hook** unsets `git rev-parse --local-env-vars` before the tests. This is a second
   layer: the guarantee is (1) and (2).

## Enforcement
- **`TestGitCallsIsolated` (lint, per call):**
  - every `exec.Command` of `"git"`, and every exec in `internal/checkpoint`, must have its `Env`
    set from `gitenv` on that command, or be `gitenv.Command`;
  - the production `Workspace` sites are listed with reasons;
  - no test file may be listed.
- **`TestCommandIgnoresInheritedGitDir`:** the incident in miniature. With `GIT_DIR` pointing at a
  sentinel, `git init` plus a commit in a temp directory leaves the sentinel untouched.
- **`bench/gitenv-sentinel.sh`, a CI step:** the whole suite runs with `GIT_DIR`, `GIT_WORK_TREE`
  and `GIT_INDEX_FILE` pointing at a sentinel repository. Every file under its `.git`, its refs and
  its config must be byte-identical afterwards.
- **Mutation tests:** each guard was broken in turn, and all nine were caught:

| Broken | Caught by |
|---|---|
| `Command` passes the inherited environment | TestCommandIgnoresInheritedGitDir |
| `Clean` keeps `GIT_DIR` | TestClean, TestCommandIgnoresInheritedGitDir |
| `Workspace` keeps `GIT_DIR` | TestClean |
| plugin fetch with the inherited environment | the lint; the sentinel (objects and `shallow` written into it; TestSymlinkNeutralised and TestInstallUpdateDiffAndTamper fail) |
| checkpoint `init` with the inherited environment | the lint (per call, after a per-function version let it through); the sentinel (TestUserRepoUntouched, TestRestoreRoundTrip and others fail) |
| a test runs `git` directly | the lint |
| child ternly processes inherit `GIT_*` | the sentinel (TestTUICommands) |

## Consequences
- **Inside ternly's own repositories, user and system git config no longer apply.** The checkpoint
  store already ignored them; now its first `init` does too. Only `excludesFile` is read from the
  user's global config, on purpose.
- **Plugin fetches and remote checks keep the user's credentials** (`GIT_SSH_COMMAND`, askpass) but
  no inherited repository.
- **Adding a git call means choosing `Clean` or `Workspace` explicitly.** The lint makes the choice
  visible in review.
