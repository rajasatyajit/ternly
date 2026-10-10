# F2 survey: the performance and adaptation matrix

The method for the "Performance tiers" section of `bench/f2/SPEC.md` (track2/f2). The results are
in `results/`; the tables come from `tuiprobe -summarize results`.

## Tools (Go only; shell is thin glue)
- **`bench/tuiprobe`** (its own Go module, so its VT-emulator dependency stays out of ternly's
  `go.mod`):
  - runs a command in a pseudo-terminal of a given size and environment;
  - records every output byte with timestamps;
  - emulates the screen (charmbracelet/x/vt), which answers the terminal's startup queries and
    gives text snapshots;
  - runs scripted steps:
    - `until` (ms to a screen text);
    - `stable`;
    - `keys` (per key: write → first byte, and bytes until quiet);
    - `type`/`enter`;
    - `stream` (bytes, gzip bytes, frames, fps, bytes per frame);
    - `idle` (process-tree CPU % from /proc);
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
  sets any first-run step: Crush's "initialize? Nope", Pi's trust prompt, and Gemini CLI's trust
  prompt. Gemini CLI has no shared model; it stops at Google auth.
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
does and how eviction works."). Recorded until 8 s of quiet: bytes, gzip bytes (a proxy for
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
