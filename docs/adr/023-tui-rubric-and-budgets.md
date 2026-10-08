# ADR 023 — Phase F: a measured rubric for the TUI, budgets in CI, and what was built

Status: proposed (Phase F, Track 2; review before merge).

## Problem
The plan asks for "the best terminal UI available as of October 2026", and says to make "best"
measurable:
- survey today's coding-agent TUIs;
- score them on a rubric with recorded evidence;
- set ternly's targets above the best score on each line;
- enforce the performance budgets in CI.

Status, routing, quota, trust and connection data come only through `internal/surface` (ADR 021).

## Survey (2026-10-08, this machine: Linux, Wayland, 16 threads; tmux 3.7c at 120×40)
**What was measured and how:**
- Installed and measured first-hand: Claude Code 2.1.293, Codex CLI 0.157.1, Crush v0.97.1,
  OpenCode v2.0.22.
- Not installed: Gemini CLI. Its evidence below is second-hand, from its docs, and marked so.
- Every harness was started in a detached tmux pane with no prompt sent; nothing was signed in and
  no trust prompt was accepted.
- Scripts and raw output are in `bench/dogfood/phase-f-survey/`.

| Measure | ternly before | **ternly after** | Claude Code | Codex CLI | OpenCode | Crush |
|---|---|---|---|---|---|---|
| First paint (tmux, median of 5) | 108 ms | ≈100 ms (tmux); **45 ms** pty to input box | 412 ms | 323 ms | 1.20 s | 1.63 s |
| Key echo, typed char to screen (tmux path, median of 20) | 10.6 ms | IDLE_HEAD | 6.4 ms | CODEX_KEY | 11.0 ms | 43.8 ms (provider picker) |
| Idle CPU, 20 s after settling | 2.7 % | **0.30–0.65 %** | CLAUDE_IDLE | CODEX_IDLE | 0.55 % | 0.30 % |
| `NO_COLOR=1` honoured (colour SGR on first screen) | NOCOLOR_TERNLY | | NOCOLOR_CLAUDE | NOCOLOR_CODEX | NOCOLOR_OPENCODE | NOCOLOR_CRUSH |

**What each harness shows before work can start:**
- **Claude Code and Codex:** a folder-trust prompt in an untrusted directory.
- **Codex:** also an update prompt.
- **Crush:** a first-run provider picker.
- **OpenCode:** an "update available" line.
- **ternly:** goes straight to the input box.

**Documented features** (sources with dates under Sources):

| | Claude Code | Codex CLI | OpenCode | Crush | Gemini CLI (2nd-hand) |
|---|---|---|---|---|---|
| Command palette / discoverability | `/` menu, `?` shortcuts | `/` commands, `/keymap` | Ctrl+P commands, leader key Ctrl+X | Ctrl+P | `/settings`, docs page |
| Diff review | `/diff`, diff dialog, `/code-review` | `/diff` incl. untracked, approvals | — | diff renders | — |
| Collapsible tool output | Ctrl+O transcript | Ctrl+T transcript | — | — | Ctrl+T tool descriptions |
| Screen reader | `--ax-screen-reader` (linear text) | — | — | — | `--screen-reader` |
| No alt-screen | — | `--no-alt-screen` | — | — | — |
| Themes | yes | — | many, incl. "system" | — | yes |
| Remappable keys | keybindings.json | `tui.keymap` | tui.json | — | yes |

## Rubric (0–4 per line; evidence above and in the recordings)

| Line | What scores | Best today | ternly before | ternly after | Target |
|---|---|---|---|---|---|
| Responsiveness | startup, key echo, idle CPU, redraw at 10k lines | Codex/Claude startup 0.3–0.4 s; OpenCode 0.55 % idle | 3 | **4**: 45 ms start, 0.3–0.65 % idle, 0.4 ms keystroke p95 and 1.1 ms 10k redraw in-process | 4 |
| Density without clutter | first screen useful; status in one line | OpenCode, Claude: 3 | 3 | 3: meter adds context, quota and trust only when known | 4 (session sidebar, plan panel: not built) |
| Discoverability | palette, inline hints, help | OpenCode, Claude: 3 | 2: `/help`, slash completion | **3**: Ctrl+K palette over every command, `@` file picker, keys on the welcome line | 4 |
| Diff and review | inline diff, per-hunk accept/reject | Claude Code: 3 | 2: `/diff` coloured | 2: `/diff` escaped and coloured | 4 (needs a contract change: see open decisions) |
| Error clarity | actionable, never raw provider markup | (not scored without the recordings) | 3 | 3 | 4 |
| Accessibility | screen reader, reduced motion, no colour | Claude Code, Gemini: 3 | 1 | **3**: screen-reader words, reduced motion, real cursor, NO_COLOR | 4 (a linear plain mode like Claude Code's) |
| Terminal compatibility | modern terminals, tmux, degraded terminals | (no harness publishes a matrix) | 2: untested | **3**: matrix below | 4 (kitty, WezTerm, SSH untested) |
| Security of the screen (added) | outside text can't drive the terminal | — (no harness documents it) | **0** | **4**: three layers, tested | 4 |

## What was built (all behind the existing UI, nothing removed)

**1. Untrusted text never reaches the terminal as control sequences.** Before this, a model, a file
it read, a tool, a provider error or a model name could:
- rewrite the permission dialog with cursor moves;
- write the clipboard (OSC 52);
- retitle the window;
- hide a link (OSC 8).

There are now three layers:
- `untrusted()` on every agent event and permission request;
- the same again in `renderBlock` for every block ternly doesn't style;
- `safeFrame`, which lets only SGR through for the whole frame, so even an info line quoting git,
  a plugin or an MCP server can't.

`/diff` escapes file contents. The threat model has a row for this.

**2. Idle costs ~0 %:**
- the 70 ms ticker runs only while something animates;
- the terminal's own cursor replaces the blinking virtual one, so the cursor never redraws.

**3. A virtualised transcript.** Blocks keep their rendered lines, and a frame joins only the visible
window. A 10k-line redraw went from 33 ms (`BenchmarkRedraw10k`) to 1.1 ms p95.

**4. Startup.** `GODEBUG=inittrace=1` showed 50 of a minimal Bubble Tea program's 65 ms going to
`go-runewidth` v0.0.27's init. v0.0.28 builds its tables lazily (upstream issue #104), and the
diff was reviewed. Result: input box at 65 → 45 ms. ternly's own `main` reaches the program in
12 ms.

**5. Ctrl+K palette.** Every command, fuzzy-filtered; the completion list scrolls.

**6. `@` file picker.** Workspace paths from the confined `glob` tool, off the UI goroutine.

**7. Ctrl+O.** Tool output in full; collapsed, the "… +N lines" line says how to expand it.

**8. Meter, `/why` and connections, from `internal/surface` (ADR 021):**
- **status bar:** context fill (amber ≥ 70 %, red ≥ 90 %), the current connection's quota or
  "quota out until", and "trust lost" (ADR 020);
- **`/why`:** `Actions.Explain`;
- **`/status`:** connections with the next step.

Built and tested against `surface/fake`. With no surface (until Phase B's adapter lands), nothing
changes.

**9. Access settings:**
- `TERNLY_REDUCED_MOTION`: no shimmer, spinner, blink or ticker;
- `TERNLY_SCREEN_READER`: also words instead of glyphs;
- `TERNLY_MOUSE`: wheel scrolling, opt-in because capture takes over selection;
- regaining focus re-reads the terminal background, so a GNOME light/dark switch is followed;
- `NO_COLOR`: honoured through the colour profile.

**10. Budgets in CI.** One non-race step runs:
- `TestTUIStartupBudget`: a real build, median of 7 ≤ 100 ms;
- `TestTUIIdleCPU`: a gross guard at 2 % over 20 s, catching a busy loop or a redraw storm (a
  redraw storm reads 120 %). The floor is Bubble Tea's 60 fps change check: 0.3–0.65 % here, up to
  0.95 % on a 4-vCPU CI runner, the same range as the old ticker. `TestTickerStopsWhenIdle` is the
  exact guard on the ticker;
- `TestFrameBudgets`: keystroke and 10k redraw p95 < 16 ms.

`TestTUINoFlicker` runs everywhere: no full-screen clear after the first frame, and every update
inside a synchronized-output bracket when the terminal supports it. The perf gate keeps
`BenchmarkKeystroke`/`Redraw10k`/`RenderAnswer` for relative regressions.

**Guards broken in turn, each failing its test:**
- the sanitiser (2 guards, the screen test fails 5/5);
- the frame filter;
- the ticker (unit test, and the live idle test at 0.95 %);
- the palette;
- the picker;
- Ctrl+O;
- the cursor's offset and blink;
- reduced motion;
- screen-reader words;
- focus;
- the wheel;
- a transcript off-by-one;
- the meter: refresh, trust, quota, connections;
- startup (+50 ms → 123 ms);
- frame time (re-render per redraw: 833 ms);
- flicker (ClearScreen per event).

## Compatibility matrix
A probe ran inside each terminal. It asked for what ternly relies on, then ran ternly for 4 s, which
exited cleanly on SIGTERM in all five. Results are in `bench/dogfood/phase-f-survey/compat/`.

| Terminal | Sync output (2026) | Focus (1004) | Bracketed paste | SGR mouse | kitty keyboard | Background (OSC 11) | ternly ran |
|---|---|---|---|---|---|---|---|
| Ghostty 1.3.1 | yes | yes | yes | yes | yes | yes | yes |
| Alacritty 0.17.0 | yes | yes | yes | yes | yes | yes | yes |
| GNOME Terminal 3.60 / VTE 0.84 | **no** (permanently reset) | yes | yes | yes | no | yes | yes |
| tmux 3.7c in Alacritty | yes | yes | yes | yes | no | yes | yes |
| tmux 3.7c, detached | yes | yes | yes | yes | no | no | yes |

- **GNOME Terminal has no synchronized output.** Bubble Tea's cell diffing is what keeps redraws
  small there.
- **Without the kitty keyboard protocol** (GNOME Terminal, tmux), Shift+Enter can't be told apart.
  Alt+Enter and Ctrl+J insert a newline everywhere.
- **Not tested:**
  - kitty and WezTerm: not installed, and installing system-wide was out of bounds;
  - SSH: no SSH server runs here, and starting one would change system configuration.

## Recordings
RECORDINGS

## Open decisions (for review)
1. **Per-hunk accept/reject for edits.**
   - **Why it can't be built today:** `tools.Asker` receives a one-line summary, not the proposed
     change. Per-hunk review needs the permission prompt to carry the edit's hunks and to return a
     subset.
   - **What it changes:** the core's permission interface (tools, agent). That is a contract change
     to agree with Track 1 (ADR 021 §2), not something Track 2 can do alone.
   - **Proposal:** `Asker` gains an optional `Preview` (unified diff hunks) and a reply listing the
     hunks to apply. The edit tool applies only those, and checkpoints record what was applied.
2. **A linear plain mode,** like Claude Code's `--ax-screen-reader`: no alternate screen, no
   redraws, typed y/n answers. The screen-reader setting here only changes glyphs. A full linear
   mode is a second renderer, worth a decision before building it.
3. **Session sidebar and plan panel.** Not built: there is no todo/plan data in the agent today
   (the plan panel needs it through `surface.Snapshot.Plan`). The sidebar trades transcript width
   for something `/resume` already offers.
4. **Image paste.** Not built: it needs multimodal requests in `internal/llm` (core), and a
   clipboard reader (`wl-paste`/`xclip`) run as a subprocess.

## Findings outside Track 2 (reported, not changed)
- **Capability catalog refresh CPU.** On every start, `capability.(*Catalog).Refresh` →
  `memory.(*Store).Upsert` → `index.remove` → `rebuild` re-indexes the whole memory index per
  upserted entry. That is 1–3 % CPU for minutes after start (profiled), with the default config.
- **The pre-push hook let tests act on the repository.** In a worktree, git exports an absolute
  `GIT_DIR` to hooks. Tests that run git in temp repositories committed into this repository and
  set its shared `user.name`/`user.email` to the fixtures' "t <t@t>". Phase B hit it first (its
  branch carries those commits) and fixed the hook; this branch has the same fix. The polluted
  config was removed and this branch's commits were re-authored.

## Sources (accessed 2026-10-08)
- Claude Code, [interactive mode](https://code.claude.com/docs/en/interactive-mode),
  [accessibility](https://code.claude.com/docs/en/accessibility),
  [keybindings](https://code.claude.com/docs/en/keybindings).
- Codex CLI, [slash commands](https://developers.openai.com/codex/cli/slash-commands).
  Approval prompts block the transcript: [#47865](https://github.com/openai/codex/issues/47865),
  [#48635](https://github.com/openai/codex/issues/48635).
- OpenCode, [TUI](https://opencode.ai/docs/tui/), [keybinds](https://opencode.ai/docs/keybinds/),
  [themes](https://opencode.ai/docs/themes/).
- Crush, [README](https://github.com/charmbracelet/crush/blob/main/README.md).
- Gemini CLI (second-hand: not installed), [commands](https://geminicli.com/docs/reference/commands/),
  [settings](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/settings.md).
