# ADR 004 — Durable sessions: event log, pause/stop/resume, auto-resume, in-place switching

Status: accepted (M2)

## Problem
A session lived only in process memory. Esc, Ctrl+C, a crash or closing the terminal lost the
conversation, ledger, model choice and checkpoints. Requirements 10 and 11 call for a crash-safe
store, resume (explicit and automatic), and switching sessions without restarting.

## Options
| Concern | Options | Decision |
|---|---|---|
| Format | SQLite (cgo, or a pure-Go port with a large dependency); bbolt; append-only JSONL | **Append-only JSONL, one file per session**, each line `crc32 json\n`. Appends are O(1) and crash-safe (a torn or corrupt tail is detected by CRC and truncated on open), human-inspectable, no dependency. The memory store (M4) will be benchmarked separately, as requirement 4 asks. |
| State model | snapshot the agent's state periodically; log every mutation | **Log every mutation as a record**, and have the agent itself apply records (`state.apply`). Live operation and replay go through the same code, so a resumed session equals the live one by construction. |
| Durability | fsync per event; never fsync; fsync at boundaries | Every record is `write(2)`n immediately by a background writer (survives process crash, kill -9 and Ctrl+C: the data is in the page cache). `fsync` happens at turn boundaries, pause, stop and switch, and while there are unsynced writes at least every 3 s (group commit). A power loss therefore costs at most about 3 s of records, even during a long turn. Callers only enqueue, so the UI and agent never block on disk. |
| Identity | path only; git root commit only | Project key = sha256(real path). `project.json` stores the path and the git root commit. A directory with no project whose root commit matches a project whose path **no longer exists** adopts it (rename/move). Two live clones never share sessions. |
| Location | `~/.cache` | `~/.local/share/ternly/projects/<key>/sessions/<id>/` (`events.log`, `meta.json`, `lock`), dirs `0700`, files `0600`. Masked from the sandbox since M1.1. |

## Record types
`meta` (id, created, path, root commit) · `turn` (prompt, history index) · `msg` (an `llm.Message`) ·
`usage` (tokens and cost delta) · `model` · `cp` (turn checkpoint tree) · `tree` (workspace at the
end of a turn, for drift) · `compact` · `rewind` · `reset` · `settings` (mode, pinned model,
verify, limits, budget) · `stats` · `title` · `status` · `snapshot` (a whole state, used by `/fork`).
Strings are redacted before they are written.

## Validity
On load: a torn or CRC-failing tail is truncated. Any assistant tool call without a result gets a
synthetic `cancelled: the session ended before this tool call finished` result, so the history is valid for both
wire formats. A `/pause` mid-turn stops at the next safe point (between tool calls), gives the
remaining calls `cancelled: paused by the user` results and persists.

## Lifecycle
- Esc interrupts the turn. `/pause` stops at a safe point and persists (status `paused`; the next
  prompt resumes). `/stop` persists, marks `stopped` and exits.
- **Auto-resume** (TUI only): the most recently active session. If that one was `/stop`ped, or is
  open in another process, a new session starts and the banner says why (`/resume <id>` or
  `/fork <id>`). An older session is never resumed silently in its place. A one-line banner shows title, age, turns, cost, `/sessions`, `/new`. `--new` or
  config `auto_resume: false` opts out. Headless `-p` starts a new session unless `-c` or `--resume`
  is given: CI one-shots should not silently append to an old conversation.
- `-c/--continue` resumes the latest; `--resume <id>` a chosen one (`--resume` alone lists them).
- **Restore:** conversation, pinned model, permission mode, verify, limits, budget, ledger and guard
  stats. CLI flags win over restored settings. Then **drift**: the session's last `tree` versus a
  fresh snapshot. The baseline is recorded after mutating turns and whenever a session is paused,
  switched away from or closed, so chat-only sessions also get one. If files changed outside the session, the next prompt carries a one-paragraph note
  listing them (at most 20).
- **Locks:** `flock` on `lock`, held for the session's lifetime. A second process can't open a
  locked session. Auto-resume then starts a new session and suggests `/fork <id>`; an explicit
  `--resume` errors.

## Switching in place (`/sessions`, `/switch`, `/resume <id>`, `/new`, `/fork`, `/delete`)
Order: (1) pause the current session at a safe point and flush with fsync, (2) release its lock,
(3) open and lock the target and replay it, (4) swap the agent's state and checkpoint handle under
the agent mutex, (5) re-render the transcript.
- If (3) fails, the old session is re-opened and stays current; nothing was deleted, so nothing is lost.
- A kill between any two steps leaves both logs intact and both locks free (flock dies with the process).
- `/fork [turn]` writes a new session with one `snapshot` record of the state up to that turn and
  copies its checkpoint refs (no object copies).
- `/delete` refuses the current or a locked session, asks for confirmation, and drops its checkpoint refs.
- `/export md|json` writes a transcript or the raw records into the workspace.
- Titles come from the cheapest utility model (≤ 24 output tokens, after the first turn), falling
  back to the first words of the prompt. `/rename` overrides.

## Trade-offs
- JSONL is larger than a binary format, and replay cost grows with history. A 1,000-turn resume is
  measured below; `/fork`'s snapshot record is also the compaction path if logs ever get too long.
- A power loss can lose records written since the last fsync (at most the current turn).
- The TUI re-renders only the last 40 turns on resume. Earlier ones stay in the model's history and
  in `/export`.

## Measurement (2026-10-04, Ryzen 7 6800H, NVMe ext4, go1.27.1)
| Claim | Result |
|---|---|
| Append cost (target < 1 ms, never blocking) | 5.3 µs per record (2 KB tool result: JSON, CRC, enqueue). The caller never waits for I/O. |
| Durability cost | `fsync` 6.9 ms on NVMe ext4, always in the background writer. In a 10 s tool-heavy turn (~4,650 × 2 KB records): 4 group-commit fsyncs, and `Record` latency p50 13 µs, p99 32 µs, the same as with boundary-only syncs (p50 13 µs, p99 35 µs). |
| Resume, 1,000 turns (target < 100 ms) | 2.7 MB log: open, replay and attach in 21 ms. **Real binary in a pty, process start to an interactive TUI with the transcript drawn: 64 ms** (bare process start-up is 12 ms). |
| Kill during append | SIGKILL while appending 4 KB records as fast as possible, 5 rounds: the log reopens every time (1,948–2,405 turns kept), with a torn tail truncated |
| Kill mid-switch | real binary SIGKILLed after each phase (persisted, unlocked, loaded, swapped): both sessions open with every turn, no stale lock, valid histories; `-c` works afterwards |
| Failed switch | the target is missing or locked: the current session stays current and journaling, and nothing is lost |
| Crash mid-tool | a log ending in an unanswered tool call reopens with a synthetic cancelled result (valid for both wire formats), and stays valid on re-open |
| Real model (local qwen3.6) | `-p` then `-c` in a new process: the model recalls a codeword from the first run. Title generated by the model: "Codeword storage with noted reply". |

**Known limitation (pre-existing).** bubbletea v1's package `init` queries the terminal background
colour before `main` runs. A terminal that never answers OSC 11 (and isn't `TERM=screen*`/`tmux*`)
stalls start-up for termenv's 5 s timeout: measured 5.08 s. Terminals that answer don't stall. This
can't be fixed from ternly's code in v1, because the package init order is fixed. bubbletea v2
removes the query.
