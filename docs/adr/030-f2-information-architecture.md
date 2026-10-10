# ADR 030 — Phase F2: an extended rubric, the information architecture, and the layout (proposed)

Status: **proposed, for review before any implementation.** The owner's instruction: "Start with
the competitor survey, the extended rubric and a layout proposal … Stop for my review before
implementing."

The survey's method is `bench/f2/SPEC.md`: the harnesses, verified 2026-10-10, isolation, one
shared local model, 5 scenarios, 20 facts, and the tiers. Two parallel survey runs fill the
**Survey** section:
- `track2/f2-probe`: performance and adaptation per tier;
- `track2/f2-reach`: scenario recordings and information reach.

## Baseline: ternly today (main 1b0085a, captured 2026-10-10)

```
80×24, idle                                                         (actual capture)
◆ ternly  ~/…/scratch                                                bwrap · ask
  ◆ ternly  0.1.0 · Code, ternly.
  ● ollama      17 models, 15 with tools · local
  /help or Ctrl+K for commands · @ for files · tasks are routed to the cheapest      ← cut at 80
  …16 empty rows…
╭──────────────────────────────────────────────────────────────────────────────╮
│ Ask ternly to build, fix or explain…  (/help)                                │
╰──────────────────────────────────────────────────────────────────────────────╯
 auto-routing                                                     ↑0 ↓0  $0.0000
```

**What it shows at 0 keys:**
- workspace;
- sandbox;
- permission mode;
- routing on/off;
- session tokens and cost.

That's 3 of the 20 facts (workspace, permission mode, cost).

**Defects visible in the capture:**
- The welcome hint is cut off at 80 columns.
- The Ctrl+K palette misaligns at 80 columns: one row's description starts in a different column
  ("/clear … new conversation in this sessio…").
- The provider line counts cloud models although the Ollama Cloud quota is exhausted. The UI
  shows no quota state.
- 16 idle rows carry nothing.

**Already done in Phase F (ADR 023), so not targets here:**
- the 10k-line transcript is virtualised: redraw p95 1.35–1.48 ms in CI, down from 33 ms;
- keystroke p95 0.4–0.6 ms;
- startup 58 ms;
- idle 0.7–1.0 %;
- terminal output escaped;
- Ctrl+K, `@`, Ctrl+O, Ctrl+B;
- `--accessible`, reduced motion, `NO_COLOR`.

## The extended rubric (0–4 per line)
The eight Phase F lines stay (ADR 023), with their evidence rules. Four lines are added. Every
score cites recorded evidence from the survey; a line with no evidence for a harness is "n/m"
(not measured), never guessed.

| Line | What scores | 4 means | 0 means |
|---|---|---|---|
| **Information reach** (new) | keystrokes to see each of the 20 facts (SPEC.md), from the idle prompt mid-session | ≥ 16 facts at ≤ 1 key and none unavailable | ≤ 5 facts at ≤ 1 key, or ≥ 8 unavailable |
| **Render cost and bytes per frame** (new) | per tier: ms per frame (in-process and observed), bytes per keystroke frame, bytes per streamed token, the compressed total for S1 | best or within 10 % of the best measured harness on every tier | ≥ 4× the best on any tier, or full-screen repaints per token |
| **Adaptivity** (new) | colour (truecolor/256/16/none), glyphs (Unicode/ASCII), sizes 80×24 → 250×70 with live reflow, slow-link behaviour, an image protocol only where supported, bounded memory | every tier usable and correct; documented | breaks (garbled, unusable or crashing) on any tier |
| **Parallel-agent visibility** (new) | what's shown of concurrently running sub-agents: count, each one's model, step, cost and status, live | live lanes at 0–1 key with all four fields | not visible until the end |

**Scoring the lines:**
- Information reach and bytes per frame score from the matrices; the formulas are in
  `bench/f2/rubric.go`, written with the survey results so the scores are recomputable.
- Adaptivity scores 1 point per tier family passed (colour, glyphs, size/reflow, slow link and
  memory), max 4.
- Parallel-agent visibility is judged against S3's recording.

## The information architecture (proposal)
Progressive disclosure in three levels, plus the palette. Nothing important is more than 1 key
away.

**Level 0, always on (≤ 2 lines at 80 columns; it grows only with information, never with
decoration):**
- **Header:** workspace · git branch and dirty mark · session name · permission mode · sandbox.
- **Status line:**
  - the current model, with a one-glyph reason (cheapest capable / pinned / escalated / failover);
  - context used/window;
  - this turn's tokens;
  - the session's $ and quota remaining (or "quota exhausted until …");
  - the verify state of the last change (✓/✗/?);
  - the turn's elapsed time;
  - the undo depth.
- **Lanes,** when sub-agents run: one row per agent, with its status glyph · name · model · step
  k/n · tokens · $ · elapsed. At 80×24 they collapse to one summary row ("3 agents: 2 running,
  1 done ▸").
- **Plan,** when a plan exists (Phase C's plan data): the current step on the status line,
  "plan 3/7 write tests".

**Level 1, inline in the transcript (zero keys, scrolls with the work):**
- **A routing card per turn:** the model, why it was chosen, the runner-up and why not ("kimi-k3:
  quota exhausted").
- **A receipt per turn:** tokens · $ · time · verify verdict · files changed.
- **Errors in a framed box,** with ternly's own one-line cause, the next step, and "Ctrl+E for
  detail".

**Level 2, one-key panels (toggle; overlay below 100 columns, a docked rail at ≥ 160):**

| Key (proposed) | Panel | Facts it reaches |
|---|---|---|
| Ctrl+B | sessions (exists) | 14 |
| Ctrl+G | agents: lanes in detail, each agent's last tool call | 11 |
| Ctrl+P | plan: every step with its state | 9, 10 |
| Ctrl+D | changes: files changed this session and a diff viewer with per-hunk state | 7, 8 |
| Ctrl+E | the last error in full: the provider's code, the retries, the failover path | 12 |
| Ctrl+R | routing: `/why` for the current turn (the ranked table) | 2 |
| Ctrl+O | full tool output (exists) | — |

**Key conflicts to decide:**
- Ctrl+D is EOF and Ctrl+R is reverse search in shells.
- Ctrl+P and Ctrl+G are free in ternly today.
- Alternatives: Alt+letters, or F-keys.

The final map needs your decision; the palette lists every panel either way.

**Level 3, the Ctrl+K palette:** every command and panel, fuzzy-searched, with its key shown, so a
key never has to be memorised.

## Responsive layouts (text mockups)

```
≥ 160 columns (e.g. 200×50): docked right rail with lanes, plan and sessions
┌ ternly ~/proj  main*  “fix-resize”  edits  bwrap ────────────────────────────┬ agents ───────────────────────┐
│ ◆ gemma4 (T2, local) — cheapest capable for a T2 task · next: kimi-k3 (quota  │ ● lexer   gemma4  3/5 parse   │
│   exhausted until reset)                                                     │   1.2k tok  $0  0:42          │
│ … transcript …                                                               │ ✓ tests   granite 5/5 done    │
│ ⎿ edit simplelru/lru.go (-1 +3)                                              │ ○ docs    queued              │
│ ↳ 4.1k↑ 380↓ · $0.000 · 0:38 · verified ✓ · 1 file                          ├ plan ─────────────────────────┤
│                                                                              │ ✓ 1 read the failing test     │
│                                                                              │ ▸ 2 fix Resize eviction       │
│                                                                              │   3 run the suite             │
│                                                                              ├ sessions ─────────────────────┤
├──────────────────────────────────────────────────────────────────────────────┤ ▸ fix-resize      0:38 $0      │
│ Ask ternly…                                                                  │   explain-lru     1d   $0      │
└ gemma4 ▸cheapest · ctx 12%/128k · 4.1k↑380↓ · $0.00 · quota ✗ · ✓ · 0:38 · undo 3 ┴──────────────────────────────┘

120×40: lanes inline above the input; panels overlay
◆ ternly ~/proj  main*  “fix-resize”  edits  bwrap
 … transcript with routing cards and receipts …
 ● lexer gemma4 3/5 parse 0:42 │ ✓ tests granite 5/5 │ ○ docs queued          ← lanes row(s)
╭ Ask ternly… ───────────────────────────────────────────────────────────────╮
╰────────────────────────────────────────────────────────────────────────────╯
 gemma4 ▸cheapest · ctx 12% · 4.1k↑ 380↓ · $0.00 · quota ✗ · ✓ · plan 2/3 · 0:38 · undo 3

80×24: one summary row for agents; status abbreviated, never wrapped
◆ ~/proj main* fix-resize · edits · bwrap
 … transcript …
 3 agents: 2 running, 1 done ▸ Ctrl+G
╭ Ask ternly… ─────────────────────────────────────────────────────────────╮
╰──────────────────────────────────────────────────────────────────────────╯
 gemma4▸ ctx12% 4.1k↑ $0 quota✗ ✓ p2/3 0:38 u3

ASCII / no colour (LANG=C, NO_COLOR=1): the same structure, ASCII glyphs, no ANSI colour
* ~/proj main* fix-resize | edits | bwrap
 [run] lexer gemma4 3/5 parse | [ok] tests granite 5/5 | [..] docs queued
 gemma4> ctx12% 4.1k^ $0 quota:x verify:ok p2/3 0:38 u3

Accessible mode (--accessible): linear and announced; no redraws, no animation
[status] model gemma4, chosen as cheapest capable; context 12 percent; cost 0 dollars; quota exhausted.
[agent lexer] step 3 of 5: parse. [agent tests] done. [agent docs] queued.
[edit] simplelru/lru.go, 1 line removed, 3 added. [verify] passed.
```

## Adapting to the environment (detection → behaviour)

| Signal | Detection | Behaviour |
|---|---|---|
| Colour depth | `COLORTERM`, `TERM`, `NO_COLOR`, terminfo colours | the truecolor theme, then 256 and 16 palettes chosen for contrast; none = structure by glyphs and position only |
| Glyphs | `LANG`/`LC_*` UTF-8, `TERM=linux`, a measured width probe | the Unicode set, or ASCII equivalents (◆→\*, ●→o, ⎿→\`-, box drawing → `+-\|`) |
| Size | SIGWINCH | four layouts (≥160, 100–159, 80–99, <80); reflow on resize; nothing wraps the status line |
| Slow link | `SSH_CONNECTION`/`SSH_TTY`; a measured write-to-flush time | redraws coalesced to ≤ 30 fps (≤ 10 over SSH when frames back up); cell-diff output only; no spinner animation; streamed text batched per line |
| Low-end CPU | measured frame time | frame-rate cap; the ticker off when idle (exists); work bounded per frame |
| Long sessions | transcript length | virtualised rendering (exists); older turns' rendered cells released and re-rendered on scroll, bounding memory; a budget for RSS growth per 1k turns |
| Images | kitty/sixel queries (DA1/XTGETTCAP), with a timeout | an image protocol only when it answers; otherwise a text placeholder |
| Synchronized output | DECRQM 2026 | used where supported (exists); GNOME Terminal and VTE lack it, so cell-diff output keeps frames small there |

## Budgets per tier (enforced in CI; the "before" column is measured in the survey)

| Budget | Local, truecolor | 256 / 16 colours | No colour / ASCII | Over SSH (simulated slow link) |
|---|---|---|---|---|
| Keystroke to render, p95 | < 16 ms | < 16 ms | < 16 ms | < 16 ms render; bytes ≤ the budget below |
| 10k-line transcript redraw, p95 | < 16 ms | < 16 ms | < 16 ms | < 16 ms |
| Startup to input box | < 100 ms | < 100 ms | < 100 ms | < 100 ms (excluding the link) |
| Idle CPU | ~0 (gross guard 2 %) | same | same | same, plus 0 bytes/s idle |
| Bytes per keystroke frame | from the survey's best | ≤ the truecolor tier | ≤ the 16-colour tier | from the survey's best SSH-tier measurement |
| Bytes per streamed token | from the survey's best | — | — | coalesced: ≤ 1 frame per 100 ms |

The bytes numbers are set from the survey's measured best, not invented here. They're filled in
with the survey results, before review.

## Survey (filled from bench/f2/probe and bench/f2/reach)
*Pending the two survey runs.*

## Decisions needed (for the owner's review)
1. The key map for the Level-2 panels (the Ctrl+D and Ctrl+R conflicts).
2. Whether the always-on status may take 2 lines at 80×24, or must stay 1.
3. The bytes-per-frame budgets, once the survey numbers are in.
4. Gemini CLI and the Cursor agent can only be scored from docs without a Google or Cursor
   sign-in. Do you want a sign-in for a first-hand measurement?
