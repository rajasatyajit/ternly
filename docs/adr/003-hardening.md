# ADR 003 — M1.1 hardening: private data, race-free paths, settings races, TUI test, budget warning

Status: accepted (M1.1)

## (a) Checkpoint and session data stay private
**Problem.** bubblewrap bound `~/.cache` read-write, so a model-driven shell command could read every
project's shadow checkpoint repo under `~/.cache/ternly`, including copied untracked secrets.

**Decisions.**
- `Sandbox.Mask` lists directories mounted as empty tmpfs *after* the writable binds. `main` passes
  ternly's config, cache and data dirs (`~/.local/share/ternly`, the session store in M2). Masks
  win over the `~/.cache` bind. The dirs are created up front, because bwrap can't mount over a
  missing path.
- **Secret patterns.** `.env*`, `*.pem`, `*.key`, `id_*`, `*credentials*`, `*.p12` go in the shadow
  repo's `info/exclude`, so they are never captured, restored or deleted.
  - Directories and source files (`*.go`, `*.py`, … `*.md`) are re-included, so `credentials.go`,
    `internal/credentials/` or `id_gen.go` keep their undo history. Without this, `*credentials*` would
    silently drop whole Go packages from checkpoints.
  - `SecretFiles` lists exactly what was skipped. It asks `git check-ignore -v` which rule decided, so
    gitignored files aren't reported, and it doesn't descend into ignored directories like
    `node_modules/`. The user sees the list at the first checkpoint and in every `/rewind`
    confirmation.
- **Retention follows the session.** There is one shadow repo per session at
  `checkpoints/<root-hash>/<session>.git`, holding an `flock` while live.
  - `Destroy` deletes it. Until M2 a session is the process, so `main` destroys it on exit.
  - `Open` sweeps sibling repos whose lock is free (crashed processes) and M1's shared
    `checkpoints/<root-hash>.git`, which could hold copied secrets.
  - The lock is taken before `git init`, and the sweep never creates lock files, so it can't delete a
    repo being created.
  - **M2 must change the sweep rule** to "not referenced by any stored session": paused sessions
    outlive their process.

**Trade-offs.**
- Each session pays a full first snapshot. It runs in the background, measured below.
- Test fixtures named `*.pem`/`*.key` are not undoable (31 such files in the Go tree).
- Without bwrap (`--no-sandbox`, or not installed) nothing is masked. The existing startup note
  already warns about unsandboxed execution.

## (b) Path escapes through a symlink swap (TOCTOU)
**Problem.** `resolve()` checked a path, then `os.Open` re-resolved it. A process swapping a
directory for a symlink in between could redirect reads and writes outside the workspace.

**Verified API.** `$(go env GOROOT)/api/go1.24.txt` and `go1.25.txt` list every `os.Root` method used
here; `go.mod` targets go1.26.
- go1.24: `OpenRoot`, `Open`, `OpenFile`, `Stat`, `Lstat`, `Remove`, `FS`.
- go1.25: `MkdirAll`, `Rename`, `Chmod`.

`go doc os.Root`: symlinks are followed only within the root, and absolute symlinks are refused.
`Root.Chmod` is documented as racy on Unix.

**Decision.**
- **File tools:** the Registry opens the workspace as an `os.Root`. `read_file`, `edit_file`,
  `write_file`, `glob` and the Go grep fallback access files only through it.
- **Writes:** a temp file with `O_EXCL`, `File.Chmod` (fchmod, not the racy `Root.Chmod`), then
  `Root.Rename`.
- **Kept:** `resolve()` stays for early, friendly errors and the `.git` rule.
- **ripgrep:** rg received a path string, so it was re-resolved too.
  - On Linux, ternly opens the target through the root and passes it to rg as fd 3. rg searches
    `/dev/fd/3`, i.e. the already-validated inode, with `--no-require-git` so `.gitignore` still
    applies, plus the fallback's skip-dir globs.
  - On other OSes, grep uses the race-free Go fallback, because traversing `/dev/fd/N` is Linux-specific.

**Behaviour changes.**
- A symlink inside the workspace with an *absolute* target now errors. `os.Root` refuses it even when
  the target is inside the workspace.
- `edit_file` refuses files over 16 MB instead of reading them whole.

## (c) Settings changed mid-turn
`/verify` wrote `Agent.Verify` while `Run` read it. The same race existed for `/mode`
(`Policy.Mode` against `Policy.Check`) and for `/budget`, which M1 already fixed.
- **Verify:** the command is now private behind `VerifyCmd`/`SetVerify` under the agent mutex. A turn
  snapshots it at start, so a change applies from the next turn.
- **Mode:** `Policy.Mode()`/`SetMode()` take the policy mutex, and `Check` reads the mode once.
- **Also fixed:** `/verify off` never disabled verification. The old code mapped `off` to `""` and then
  only stored non-empty values.

## (d) Real-TUI end-to-end test
The test binary re-executes itself as ternly (`TestMain`), on a pseudo-terminal opened with
`golang.org/x/sys/unix` (already a dependency; no `creack/pty` added). It talks to a fake
OpenAI-compatible provider in a throwaway HOME. It types prompts, `/limits`, `/rewind`, `/undo` and
`/rewind 1 code`, answers the confirmation dialog, and asserts on screen text (ANSI stripped), on
workspace files, and on the prompts the provider received. The test is Linux-only and skips elsewhere.

## (e) Budget visibility
The status bar shows `turn $spent/$limit` in amber once the turn's spend reaches 75% of the
per-turn limit.

## Measurement (2026-10-04, Ryzen 7 6800H, go1.27.1)
| Claim | Result |
|---|---|
| TOCTOU closed | symlink-swap race, 1.5 s per search path. **M1: 53 leaks in 3,488 calls.** M1.1: 0 leaks in 3,428 calls (ripgrep path) and 0 in 14,222 (Go fallback). Every write stayed inside. 5/5 repeat runs clean. |
| os.Root cost | `read_file` 84 → 88 µs, `edit_file` 32.6 → 38.4 µs (median of 5) |
| Private dirs masked | real binary, bwrap: a model-run `cat` of config, cache, session and checkpoint-repo files returns none of 4 markers. The `--no-sandbox` control returns all 4. |
| Secrets not checkpointed | 7/7 secret-pattern files absent from snapshots; 4/4 look-alike source/doc files kept; `.env` untouched by restore |
| Secret scan cost | 27 ms on 12,018 files (first snapshot 1.2–1.4 s in background, unchanged 20 ms) |
| Retention | destroyed on exit (real binary); crashed sessions and M1 shared repos swept; live session refused to a 2nd opener |
| Races | `-race` tests changing verify and mode mid-turn pass. With the setter locks removed, the race detector reports 2 races. |
| TUI | `/undo`, `/rewind`, `/rewind 1 code`, `/limits` driven through a pty: ~6.8 s, 5/5 repeat runs pass |
