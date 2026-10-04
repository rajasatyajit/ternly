# ADR 002 — Git checkpoints, /undo and /rewind

Status: accepted (M1)

## Problem
Edits and shell commands change the workspace with no way back except the user's own VCS
discipline. Requirement: git checkpoints before edits, `/undo` and `/rewind`.

## Verified prior art (2026-10-04)
- Claude Code `/rewind` (code.claude.com/docs/en/checkpointing) checkpoints before each prompt that
  starts a turn. It can restore code+conversation, conversation only, or code only. It tracks only
  its file-edit tools, **not Bash changes**.
- Aider `/undo`: "Undo the last git commit if it was done by aider" (aider auto-commits each change).
- Codex CLI (`codex-rs/tui/src/slash_command.rs` on main): neither `/undo` nor `/rewind`.

## Options
1. Back up each file before an edit tool touches it. Simple, but misses Bash side effects
   (`rm`, codegen, `go mod tidy`).
2. Commit into the user's repository (Aider style). Pollutes history and branches, and requires
   a repo.
3. **Shadow git repository**: `GIT_DIR=~/.cache/ternly/checkpoints/<sha256(root)[:16]>/<session>.git`
   (per session since M1.1; see ADR 003),
   `GIT_WORK_TREE=root`, private index. A checkpoint is `git add -A && git write-tree` (a tree id).

## Decision
Option 3. It captures **all** workspace changes including Bash, works in non-git directories, never
touches the user's `.git`, index, branches or stash, honours `.gitignore` (build output is never
snapshotted or deleted), and git deduplicates content.
- A baseline snapshot starts in the background at session start, so the expensive first
  `git add` doesn't stall the first edit. The agent takes a checkpoint before the first mutating
  tool call (edit, shell, MCP) of each turn.
- `/undo` rewinds the most recent turn: files back to that turn's checkpoint, and the conversation
  truncated to before its prompt. `/rewind` lists turns. `/rewind <n> [both|code|chat]` restores to
  before turn *n*. These are Claude Code's three modes; `both` is the default.
- Restore takes a fresh snapshot of the current state, diffs `target..now`, checks out changed or
  deleted paths from the target tree through a temporary index (`checkout-index`), and removes
  paths added since. Every path is re-checked to stay under the root, with no symlinked parent.
  The user confirms the file list first.
- git runs with `core.hooksPath=/dev/null`, `core.fsmonitor=false`, and `--ignore-errors`, so one
  unreadable file or uncommitted nested repo doesn't abort a checkpoint.

## Trade-offs and limits
- Needs `git` on PATH. Otherwise checkpoints are disabled with a note.
- Nested repositories are recorded as gitlinks, so their contents are not checkpointed.
- Disk: shadow objects are about 0.55× the tracked tree size on first snapshot (measured below).
  Since M1.1 the whole repository is deleted with its session (ADR 003). Secret-pattern files
  are never captured.
- Checkpoints live for the session; durable sessions (`/resume`) are M2.
- Ignored files are never restored or deleted. That is deliberate.

## Measurement
Shell experiment on a copy of the Go source tree (12,018 files, 157 MB): first snapshot 1.14 s
plus 56 ms `write-tree`; no-change snapshot ~20 ms; one-file edit ~26 ms; shadow store 86 MB.
`internal/checkpoint` benchmarks and restore tests (including symlink-escape attempts) give the
Go-level numbers in the M1 report.
