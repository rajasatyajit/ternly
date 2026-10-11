# F2 survey: the performance and adaptation matrix

The method for the "Performance tiers" section of `bench/f2/SPEC.md` (track2/f2). The raw results
(one JSON, `.err` and gzipped pty log per cell, in `results/` and the earlier `results-*/` sets) are
recordings of other harnesses' screens and of this machine's environment: they stay local and are
gitignored. Git has `metrics.jsonl`, one line per cell with every measurement and no environment,
command line, screen text or process ids (pty frame sizes summarised as count, median and max), and
the tables in `SUMMARY.md`, from `tuiprobe -summarize results`. Read "Validity" below before using
any number.

## Tools (Go only; shell is thin glue)
- **`bench/tuiprobe`** (its own Go module, so its VT-emulator dependency stays out of ternly's
  `go.mod`):
  - runs a command in a pseudo-terminal of a given size and environment;
  - records every output byte with timestamps;
  - emulates the screen (charmbracelet/x/vt), which answers the terminal's startup queries and
    gives text snapshots. It is primed with `CSI ?2026l`, so a DECRQM query for synchronized
    output gets "recognized, reset" (`?2026;2$y`), as from a modern terminal; harnesses that ask
    before using 2026 (Bubble Tea v2: ternly, Crush) then use it (`-sync-capable=false` turns this
    off);
  - runs scripted steps:
    - `until` (ms to a screen text);
    - `stable`;
    - `keys` (per key: write → first byte, and bytes until quiet);
    - `type`/`enter`;
    - `stream QUIET MAX [MINBYTES]` (bytes, gzip bytes, frames, fps, bytes per frame; quiet only
      ends it after MINBYTES, because a model's thinking is quiet too);
    - `idle` (process-tree CPU % from /proc: utime+stime+cutime+cstime, so a child that exits
      mid-window can't make the figure negative);
    - `resize` (TIOCSWINSZ plus SIGWINCH);
    - `rss`;
    - `snap`.
  - scans the output for its technique: alternate screen, DECSET 2026 synchronized updates, ED 2
    full clears, SGR colour depth, non-ASCII bytes, kitty keyboard, mouse, bracketed paste.
  - **Cleanup:** after a run (or on SIGTERM) it stops the program's process group, then every
    process whose environment has the run's unique `HOME`, by reading /proc/<pid>/environ and
    never by command-line pattern. Codex's `app-server` daemon leaves the process group, and this
    is how it's found. SIGTERM goes first, then SIGKILL after the grace period; survivors are
    reported.
- **`harnesses.sh`:** one profile per harness. It writes the harness's documented
  custom-endpoint configuration into an isolated `HOME` for the shared local model (Ollama
  `gemma4:latest`, digest dc35e8d9c606) and sets the screen text that means "ready". It also
  sets any first-run step: Crush's "initialize? Nope" and Gemini CLI's trust
  prompt (Pi 1.1.0 asks nothing; its ready text is the context meter). Gemini CLI has no shared
  model; it stops at Google auth.
- **`run1.sh`:** one cell.
  - It creates a fresh isolated `HOME` and a fresh copy of the golang-lru fixture.
  - It sets the colour and glyph tier, and runs tuiprobe under a timeout (SIGTERM at the limit).
  - It reaps the cell's `HOME` and deletes the cell.
  - A timed-out cell is written as `{"outcome":"timeout"}`.
- **`matrix.sh`:** every cell, in a fixed order.
  - Each cell takes the shared lock with **`flock -o`**, so nothing the cell starts can inherit
    the lock (the cause of the 7¾-hour deadlock on 2026-10-10).
  - **Resumable:** a cell whose JSON exists (a measurement, or a recorded timeout or no-result)
    is skipped. `results/state.txt` says where the run is.
- **`start.sh`:** runs `matrix.sh` detached (`setsid nohup`), so it survives the session that
  started it.

```
bench/f2/probe/start.sh $PWD/bench/f2/probe/results         # start, or resume
cat  bench/f2/probe/results/state.txt                        # progress
tail -f bench/f2/probe/results/matrix.log
(cd bench/tuiprobe && go run . -summarize ../f2/probe/results)   # tables
```

## Cells
For each harness: ternly (main's static build, sha256 in `results/ternly.version`), Claude Code
2.1.296, Codex 0.162.1, Gemini CLI 0.63.0, Crush 0.98.1, OpenCode 1.18.35, Pi 1.1.0 and
CodeWhale 0.10.1.

**Base cells:** wait for "ready", wait until stable, type 10 keys one at a time, idle for 30 s,
then RSS.
- 120×40 truecolor, 3 runs;
- 120×40 at 256, 16, no colour (`NO_COLOR=1`), truecolor with ASCII (`LANG=C`), and `TERM=dumb`;
- 80×24 and 250×70, truecolor.

**Resize cell:** ready at 120×40, then 80×24, then 250×70, with a snapshot after each.

**Stream cells** (every harness except Gemini CLI): S1's prompt ("Explain what simplelru/lru.go
does and how eviction works."), typed, then a 700 ms pause before Enter (Codex and CodeWhale
treat an Enter that follows typing at once as part of a paste). Recorded until 8 s of quiet after
at least 3000 bytes (1500 per long-session turn): bytes, gzip bytes (a proxy for
ssh -C), frames (bursts separated by ≥ 4 ms), fps, bytes per frame. 3 runs each at 120×40 and
80×24.

**Long cell:** 10 one-line questions in a row at 120×40, with RSS after each answer.

**Limits:** base 300 s, resize 240 s, stream 600 s, long 3600 s.

## What the numbers are and aren't
- **Keystroke echo** is the time from writing the key to the pty until the first output byte.
  It includes the terminal emulator's work in tuiprobe, but not a GPU terminal's painting.
- **Bytes per frame** count the pty output: the payload SSH would carry, before encryption.
  gzip stands in for ssh -C compression.
- **Idle CPU** covers the harness's process tree. A daemon that left the tree (Codex's
  app-server) isn't counted in its idle CPU, but it is counted in `reaped`.
- **Streaming numbers mix the UI with the model's output rate.** Every harness has the same
  model, but each has its own system prompt, tools and answer, so bytes per answer differ partly
  because the answers differ. Bytes per frame and fps are the UI-level figures.
- **Runs are sequential under the shared lock,** on one machine (Linux, 16 threads, RTX 3060
  6 GB). Medians are over 3 runs where marked; single-run cells say so.
- Anything that wasn't measured is "n/a" or "not run", never 0.

## Validity
**v1 (2026-10-10, `results-v1/`, not used).** Inspecting its snapshots found five flaws. Each
was fixed in the probe or the matrix, and then every cell was re-run:
1. **DECSET 2026 read as 0 for ternly and Crush.** Bubble Tea v2 asks the terminal (DECRQM) and
   uses 2026 only when the mode is recognized. The emulator answered "not recognized", which
   no modern terminal does. Fixed by priming (see Tools).
2. **Quiet ended streams too early.** Claude's long-session turns were about 570 B in 7 ms,
   because the model was still thinking. Fixed by the stream minimum.
3. **Codex and CodeWhale never submitted S1:** an Enter right after typing counts as part of a
   paste. Fixed by the 700 ms pause.
4. **Codex's idle CPU was −0.93 %:** a child exited mid-window. Fixed by counting reaped
   children.
5. **Pi never reached "ready"** in v1: its READY text was a trust prompt that 1.1.0 doesn't
   show.

**v2 (2026-10-11T00:29Z–02:46Z, `results/`):** 137/137 cells; no timeouts. Every stream cell's
final screen was read by hand. Nine cells were invalid. They were moved to
`results-v2-invalid/` and re-run once with the cause removed (or, for a one-off dialog, as is):
- **CodeWhale, all 6 stream cells and the long cell:** its error "This message (~12428 tokens
  with Codewhale's instructions) does not fit gemma4:latest's window (~5120 tokens usable)".
  CodeWhale assumes a small window for an unknown Ollama model. Its profile now sets
  `context_window = 32768` under `[providers.ollama]`, the window Pi is given (`config set` has
  no key for it). Ollama itself runs gemma4 with a 131072 context.
- **Claude Code, stream 120×40 r1:** a modal "auto mode classifier billing" notice ("Enter to
  continue") appeared mid-turn, because requests go to a non-Anthropic endpoint. The answer
  stopped behind it. The re-run didn't show it. Claude's base cells show a different auto-mode
  notice as a banner, which doesn't take input.
- **Codex, stream 80×24 r3:** Codex stopped at its default approval prompt ("Would you like to
  run … `cat simplelru/lru.go`?"). The model chose a shell command instead of a read. The
  re-run read the file and answered.

**Answers, checked by hand on each stream cell's final screen.** The table's "eviction text on
screen" column is a heuristic; it counts the model announcing that it will explain eviction.
Hand-checked counts:

| harness | 120×40 | 80×24 | what the other runs show |
|---|---|---|---|
| claude, codewhale, codex, pi, ternly | 3/3 | 3/3 | |
| crush | 1/3 (r3) | 1/3 (r2) | the model writes "[uses ls tool]" as text, then "Ready?"; the turn ends with no answer |
| opencode | 0/3 | 0/3 | gemma4's tool call is printed as text (`glob{pattern:…}`, `task:explore{…<\|"\|>…}`), not executed; the turn ends |

So for Crush and OpenCode, the stream figures (bytes, frames, fps, bytes per frame) measure the
UI rendering a 13–22 s turn of reasoning and a printed tool call, not an answer. Their
bytes-per-frame and fps still describe how the UI paints streamed text. Their totals and times
aren't comparable with the others. This is gemma4 with those harnesses' tool-call formats, under
the shared-model rule; a model they handle would be needed to compare answers. The long-session
"turns answered" only means each turn passed 1500 bytes. Those screens weren't kept per turn, so
they weren't hand-checked.

## Anomalies (v2)
- **Gemini CLI:** without a Google account it stops at its trust dialog, so typed keys go
  nowhere: 0 bytes per key. **Its interaction figures (echo, bytes per key) are n/a.** Startup to
  the dialog (1.9 s), idle CPU, RSS, resize and technique are real. It has no stream or long
  cells.
- **OpenCode ignores the colour tier.** At `TERM=xterm` (16 colours) and under `NO_COLOR=1` it
  still sends 24-bit or 256-colour SGR. Under `TERM=dumb` it sends 24-bit. Its idle CPU is
  23 % (others: 0.5–4.3 %). It takes 3.7 s to start.
- **Pi ignores the colour tier too:** 256-colour SGR under 16 colours, `NO_COLOR` and dumb.
- **CodeWhale** downgrades 16 colours to 256 and honours `NO_COLOR` and dumb.
- **Codex:** under `TERM=dumb` it never reaches ready (1 byte). On a resize from 80×24 to 250×70,
  it sent 4 bytes and did not repaint; the snapshot still shows the 80-column layout. RSS is
  843 MiB at rest and 909 MiB after 10 turns.
- **ternly** keeps its non-ASCII glyphs (745 bytes) under `LANG=C` and `TERM=dumb`; so do all the
  others except Codex under dumb. No harness here has an ASCII fallback. ternly's key echo is
  12–13 ms, against Claude's 4, Pi's 2 and Codex's or CodeWhale's under 1. That is the slowest
  echo after Gemini, though it sends the fewest bytes per key (19). Its idle CPU is 3.2 % while
  the cursor blinks. Its stream runs at 13.5 fps, where most rivals run at 22–28.
- **Claude Code** is the only harness besides Gemini with no DECSET 2026 (it paints at 7 fps).
  Under `TERM=dumb` and `NO_COLOR` it still paints its full UI, without colour.
- **Sync updates** in the technique table count the whole base cell (startup + 10 keys + idle).

## Gaps (n/a, and why)
- **Gemini CLI streaming, long session and interaction:** no shared model without a Google
  account (paid or signed-in only), so not run.
- **Answer-length comparisons for Crush and OpenCode:** see Validity.
- **Real GPU-terminal paint time** isn't measured; echo stops at the first pty byte.
- **ssh -C** is approximated by gzip of the pty stream, not measured over ssh.
- **Network:** shared with the host (see Sandbox).

## Sandbox (every harness, ternly included for parity)
Rival harnesses run in their default or permissive modes, and an isolated `HOME` alone doesn't stop
them reaching the owner's files by absolute path. So every cell runs its harness under bubblewrap
(`sandbox_prefix` in `harnesses.sh`):
- `--die-with-parent --unshare-all --share-net`;
- `/usr` and `/etc` (with `/bin`, `/lib`, `/lib64` and `/sbin` as symlinks into `/usr`), `/opt`
  and `/sys` read-only;
- the harness's install directory and Node (nvm) read-only;
- **a tmpfs over the real home (`$HOME`),** so the real home isn't visible; only the cell's own directory
  (its workspace copy and temp HOME) is bound read-write;
- a fresh `/tmp`, `--proc /proc` and `--dev /dev`;
- fails closed when bwrap is missing.

Harness setup commands that run the harness (CodeWhale's `config set`) run inside the same
sandbox.

**Network is shared** (`--share-net`). The harnesses need only the local Ollama
(127.0.0.1:11434), but the sandbox doesn't restrict them to it, so they could reach the internet.
Filesystem isolation is complete; network isolation isn't.

ternly's own bubblewrap still works nested inside the wrapper (checked), so ternly runs exactly
as it would outside.

**Self-check** (`selfcheck.sh`, once per harness, inside that harness's own wrapper; results in
`results/selfcheck.<harness>.json`):
- a canary file under the real home is unreadable;
- a write under the real home doesn't reach the disk;
- writes to `/usr` and `/etc` fail;
- the real `~/workspace` isn't visible;
- the cell's workspace and HOME are writable;
- the binary is present.

All 8 harnesses pass (2026-10-10T19:28Z). The check is proven by breaking it: with the real home
visible (`--ro-bind / /`), it reports `canary_read=FAIL` and `workspace_visible=FAIL`.

**The wrapper's cost:** `bwrap … true` has a median startup of 6.6 ms, against 0.9 ms for `true`
alone, over 20 runs each (`results/wrapper-overhead.txt`). So it adds about **5.6 ms to every
startup**; per-key and per-frame numbers are unaffected, because the wrapper only sets up
namespaces. For a direct comparison, `results-nowrap/` keeps 9 ternly cells measured without the
wrapper before it was added: the same tiers, the same binary.

## Footprints of runs before the sandbox (checked 2026-10-11)
Files written since the survey began (2026-10-10 16:30 IST) under the owner's home, excluding
caches, Claude Code's own data, the installs the owner asked for, and this repo's `.git` and
worktrees. The ones that came from survey runs:

| When (IST) | Path | What | Impact |
|---|---|---|---|
| 16:38 | `~/.local/share/opencode/log/opencode.log` | one log line: `opencode --version` (the installed beta 2.0.22): a version check when the survey began | an appended log line |
| 16:41–42 | `~/.local/share/ternly/projects/923f…` | the survey's ternly screen capture, run with the real HOME | ternly's own data directory |
| 16:42 | `~/.gemini/projects.json.*.tmp` (2 files) | `{"projects": {}}`, 20 bytes each: a Gemini CLI start with the real HOME | two leftover temp files (not deleted; the owner's call) |
| 16:44 | `~/.pi/agent/` | the directory's mtime only; every file inside is unchanged since 2026-09-30 | none |

No other file under the home or in the main repository's working tree was written by survey runs.
The other recent writes are the owner's own activity (the paru Crush install, a Crush session in
`workspace/llama.cpp/app`, a Gemini sign-in in `workspace/tokvera`) and unrelated apps
(Nextcloud, Serena, MongoDB MCP, Cline, GNOME).

**During the sandboxed v2 run** (2026-10-11 00:29Z onward), the same search finds no file
written by a harness. The only recent writes are this session's own (Claude Code's
`~/.claude.json`, the repo's `.remember/`, Go telemetry counters from building the probe) and
Nextcloud/GNOME.
