# F2 survey: scenarios and information reach (method)

This follows `bench/f2/SPEC.md` (on track2/f2): the harnesses, isolation, the shared model, the
scenarios S1–S5 and the 20 facts. Everything is driven by shell glue around Go tools. There is no
Python (the owner's rule).

## Layout
| Path | What |
|---|---|
| `scenarios/launch.sh` | starts one harness in a private tmux server with an isolated `HOME`/`XDG_*` and its documented custom-endpoint config for the local `gemma4:latest` (via the proxy on :11435) |
| `scenarios/onboard.sh` | answers first-run dialogs by screen text; "ready" must hold on 3 consecutive polls, 2 s apart |
| `scenarios/rec.sh` | tmux recorder: `type`, `wait` (a timeline of screen captures until idle or a limit), `snap`, `stop` |
| `scenarios/run.sh` | one scenario cell: fixture, launch, onboard, prompt(s), timeline, final and scrollback screens, timings, provisional outcome, cleanup |
| `scenarios/batch.sh` | every cell, resumable and detached-safe (details below) |
| `scenarios/tools/prep` | makes a fresh fixture from the Phase C suite's pinned repositories; S2 seeds the lru-resize bug, S4 leaves two seeded bugs uncommitted |
| `scenarios/tools/proxy` | TCP forwarder :11435 → Ollama :11434. S5 stops it mid-session to make a real provider failure, then restarts it |
| `scenarios/tools/homekill` | stops every process whose environment has `HOME` inside a run directory (by `/proc/<pid>/environ`, never by command line) |
| `scenarios/tools/score` | computes each cell's outcome from its saved recording and fixture |
| `reach/discover.sh` | captures a harness's command surfaces (no prompt sent) |
| `reach/reach.sh`, `reach/reach-all.sh` | the information-reach session per harness, resumable |

## How the batch runs, and why
**The batch is detached and resumable:**
- start: `R=<runtime> setsid nohup bench/f2/scenarios/batch.sh > $R/batch.log 2>&1 < /dev/null &`;
- progress: `$R/out/state.txt`;
- a rerun skips any cell that has `result.txt`.

**The shared lock is taken with `flock -o`,** so nothing a cell starts inherits it. The first
survey run deadlocked for 7¾ hours because a helper started inside the lock kept the lock's file
descriptor.

**Every cell has a hard limit:** 1200 s, 1800 s for S3, 4500 s for S5. A hung harness becomes
`outcome=timeout`, never a stalled batch.

**Cleanup after every cell:** the tmux session is killed and `homekill` stops every process with
that cell's HOME. That includes Codex's `app-server` daemon, which keeps running in its own
session after the TUI exits.

**The model proxy runs outside the lock,** started by the batch and stopped when it ends.

**Outcomes come from `tools/score`, not the inline check in `run.sh`.** The inline S1 check
grepped the scrollback, which includes the prompt ("…how eviction works", "simplelru/lru.go"). It
passed a Codex run whose prompt was never submitted: Codex's folder-trust dialog appears about
3.5 s after its prompt glyph and swallowed the Enter. The scorer drops prompt lines first, and
`onboard.sh` now waits for 3 stable polls. That cell was re-run.

**Outcome checks** (objective only; the model is small and failures are data):

| Scenario | Outcome |
|---|---|
| S1 | the answer names the LRU mechanism (least-recently-used, MoveToFront, removeOldest, evictList…) |
| S2 | `go test ./simplelru/` passes on the fixture afterwards |
| S3 | `once(` exists and `emit` returns a value |
| S4 | how many of the two seeded bugs the review names |
| S5 | turns completed of 30; whether the provider failure was shown; whether the next turn recovered; RSS |

## Information reach
`reach.sh <harness>`:
1. **A real mid-session state:** on a fresh S2 fixture, the harness fixes the bug, which leaves an
   edit, a diff, a test run, tokens and cost. The idle screen afterwards is the 0-key evidence.
2. **Slash commands:** each candidate is typed **without Enter** and the autocomplete menu is
   captured. It runs only if the menu lists it, so an unknown command is never sent to the model.
   Commands that change state or call the model (review, init, compact, undo, rewind) are only
   checked for existence.
3. **Key chords:** Ctrl+O, Ctrl+T, Ctrl+G, Ctrl+B, Ctrl+], F1, F2, ← and `?`. Each is pressed,
   captured, and undone.

**Keystrokes are counted as typed:** `/cost⏎` = 6, a chord = 1, the minimum over the ways found.
Autocomplete could shorten a typed command; the count doesn't assume it. Every matrix cell cites
the screen that shows the fact.

## The boundary (every harness, ternly included): `scenarios/box.sh`
The rivals run with their own permission prompts off (`claude --dangerously-skip-permissions`,
`crush --yolo`, OpenCode's allow-all permissions), so a temporary HOME alone wouldn't stop a
harness, or the model driving it, from reading or writing the owner's files by absolute path.

Every harness therefore runs under bubblewrap:
- `--die-with-parent --unshare-all --share-net`;
- a tmpfs over `/home/satyajit`, so the real home is invisible apart from the binds below;
- read-only: `/usr`, a minimal `/etc` (passwd, group, hosts, resolv.conf, nsswitch, ssl,
  ca-certificates, localtime), the harness installs, the Node runtime, Claude Code's version
  directory and the ternly binary;
- writable: only the cell's workspace and its temporary HOME;
- a fresh `/tmp`, `/run`, `/proc` and `/dev`.

`box.sh` fails closed: with no bwrap, nothing runs. Where a harness has its own sandbox it is used
as well: Codex runs `--sandbox workspace-write --ask-for-approval never` instead of its bypass
flag, and CodeWhale runs `sandbox_mode = "workspace-write"`. ternly's own shell sandbox nests
inside the box; nested bwrap was checked to work.

**Network:** the host network stays reachable (`--share-net`), because the harnesses need the local
model proxy on 127.0.0.1:11435. The harnesses are configured only for that endpoint, with
telemetry and autoupdate off where they offer it, but the box itself does not block outbound
traffic.

**Self-check, per cell:** `run.sh` runs `box.sh --check` in the cell's own directories before starting
the harness, and refuses the cell on any FAIL. The result is saved as `sandbox-check.txt` in the
cell. It checks:
- a canary file under the real home (`~/.ternly-f2-canary`), the ternly repo and `~/.ssh` are
  invisible;
- the harness install, `/usr` and the ternly binary are read-only;
- the workspace and temp HOME are writable;
- the proxy is reachable;
- a write under the real-home path does not persist outside the sandbox.

Mutation: binding the real home into the box makes the canary, repo and `~/.ssh` lines FAIL
(checked).

**Tests run in the box too:** S2's tests run model-written code, so `run.sh` and `tools/score` run
them inside it.

**Before the boundary existed:**
- Cells run without it were archived outside the results and re-run under it.
- An audit of the owner's home for that window found no write by an F2 run outside its
  isolation. No harness config file in the real home mentions an F2 path.
- The root filesystem is mounted `noatime`, so reads can't be audited and are not ruled out.

## Isolation and what is not touched
- Each harness gets a fresh temp `HOME`, so no real config, credentials or folder-trust list is
  read or written.
- No subscription sign-in, and no paid API.
- Gemini CLI needs a Google sign-in, and the Cursor agent a Cursor account, so both are docs-only.
- The command-surface discovery pass ran at `nice 19` on its own tmux socket, without the lock.
  It sent no prompt, except one discovery keystroke in Claude Code that sent `//help`; that's
  noted in its capture.

## Results (2026-10-10/11, gemma4:latest, 120×40, all cells inside the boundary)

Scores come from `scenarios/tools/score` and are saved as `scenarios/results/scores.tsv`. The screens and timelines are in `scenarios/results/<harness>/<S>/`; files over 64 KB are gzipped.

| harness | S1 explain | S2 fix | S3 feature | S4 review (2 bugs) | S5 30 turns + outage |
|---|---|---|---|---|---|
| ternly | answered | fail: no edit; the model said "I have corrected the loop condition" and ternly didn't flag it | none: the no-progress guard stopped a repeated failing edit | 0/2: asked the user for the diff instead of running git diff | 30/30, error shown, recovered |
| Claude Code | answered | **pass** | none | 1/2 (resize) | 30/30, error shown, recovered |
| Codex | answered | fail (no edit) | none | 2/2 | 30/30, error shown, recovered |
| Crush | no answer: gemma4 printed `[uses ls tool]` as text | fail | none | 1/2 (peek) | 30/30, error shown, recovered |
| OpenCode | no answer: gemma4 printed `read{filePath:…}` as text | fail | none | 2/2 | 30/30, error shown, recovered |
| Pi | answered | fail | **both** | 2/2 | 30/30, error shown, recovered |
| CodeWhale | answered | fail (no edit) | none | 2/2 | 30/30, error shown, recovered |

- **Single runs.** Every cell is one run with a 9.6 GB local model, so the outcomes are noisy. In its reach session, ternly made the S2 fix and verified it, though it failed the timed S2 cell.
- **RSS after S5 is n/m.** The first probe summed only the pane process (box.sh, about 3.7 MB for every harness). `run.sh` now sums the whole process tree (`rss_tree_kb`), and the scorer ignores the old value. Agent A's performance probe measures RSS separately.
- **The idle detector waits for real idleness.** A screen is idle only when nothing changed for the quiet period and none of the last 8 non-empty lines shows a busy marker. ternly's "Thinking…" does not animate, so the two ternly cells that first ended mid-turn (S3, S5) were archived and re-run.
- **Information reach:** see `reach/MATRIX.md`; the evidence is in `reach/results/<harness>/`.
