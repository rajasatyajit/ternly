# ADR 030 — Phase F2: an extended rubric, the information architecture, and the layout (accepted)

Status: **accepted with the owner's decisions (2026-10-11), recorded at the end.** It was first proposed for review before
any implementation. The owner's instruction: "Start with
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

That's 3 of the 20 facts (workspace, permission mode, cost) on a fresh screen. **Mid-session, 8 are at 0
keys** (model + tier, context, cost, last error, verify, session, mode, workspace; measured in the survey
below).

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

## Survey, part 1: scenarios and information reach (track2/f2-reach, PR #30)
**Method:**
- All 7 harnesses ran the same local model (gemma4:latest), at 120×40, each inside a bubblewrap boundary
  (`bench/f2/scenarios/box.sh`: a tmpfs over the real home, only the workspace and a temp HOME writable).
- All 35 per-cell canary checks passed.
- One run per cell. A small model makes task outcomes noisy, so the outcomes judge the UI's behaviour around
  the work, not the model.
- Gemini CLI and the Cursor agent need an account sign-in, so they're docs-only and not scored on these lines.

| Harness | S1 explain | S2 fix | S3 feature | S4 review | S5 30 turns + outage |
|---|---|---|---|---|---|
| ternly | answered | fail (claimed a fix it didn't make; not flagged) | none (no-progress guard stopped a repeated failing edit) | 0/2 (asked the user for the diff) | 30/30, error shown, recovered |
| Claude Code 2.1.296 | answered | **pass** | none | 1/2 | 30/30, error shown, recovered |
| Codex 0.162.1 | answered | fail (no edit) | none | 2/2 | 30/30, error shown, recovered |
| Crush v0.98.1 | no answer (tool calls printed as text) | fail | none | 1/2 | 30/30, error shown, recovered |
| OpenCode v1.18.35 | no answer (same) | fail | none | 2/2 | 30/30, error shown, recovered |
| Pi v1.1.0 | answered | fail | **both** | 2/2 | 30/30, error shown, recovered |
| CodeWhale v0.10.1 | answered | fail (no edit) | none | 2/2 | 30/30, error shown, recovered |

**Information reach, 20 facts** (bench/f2/reach/MATRIX.md, with an evidence screen per cell):

| | ternly | Claude Code | Codex | Crush | OpenCode | Pi | CodeWhale |
|---|---|---|---|---|---|---|---|
| at 0 keys | 8 | 5 | 4 | **10** | 5 | 5 | 6 |
| reachable at all | 12 | 12 | 12 | 10 | 9 | 7 | **13** |
| not in the UI (∞) | 4 | 4 | 4 | 5 | 7 | 7 | 3 |
| not observed (no plan or approval occurred) | 3 | 3 | 3 | 4 | 3 | 4 | 3 |

**Where each harness leads, measured:**
- **ternly:** the only "why this model" in any harness (`/why`), the only inline verify status, and session
  and mode in a pinned header.
- **Crush:** 10 facts at 0 keys in a permanent sidebar, including the files changed. It costs ~30 columns.
- **Codex:** an agent command center at 1 key (`←`), and a footer warning counter.
- **Claude Code:** inline per-edit diffs, a footer that pairs the mode with how to change it, and a live agent cue.
- **CodeWhale:** the densest footer (elapsed, time to first token, tok/s), and an honest "cost: unknown" where
  Claude Code showed $0.28 for a free local model.
- **Pi:** the branch in the footer.

**ternly's measured gaps:**
- running agents ∞;
- turn elapsed ∞;
- tokens this turn ∞;
- git branch ∞;
- the mode truncated by a long workspace path at 120 columns;
- Esc and Ctrl+U don't clear typed input;
- a false "I fixed it" went unflagged (S2). That's a core fact-check gap, reported for Track 1; not a UI change.

**Not measured:**
- RSS after S5: the first probe counted only the wrapper. Fixed for the next run.
- The plan and pending-approval facts: no session reached those states.

## Survey, part 2: performance and adaptation per tier (track2/f2-probe, PR #29)
**Method:**
- `bench/tuiprobe` (Go) drives each harness in a pty with a VT emulator; 137 cells, all under bubblewrap.
- All 8 self-checks passed; the wrapper adds ~5.6 ms to startup.
- Medians of 3 runs for the 120×40 interaction and the streaming cells.
- **A first full run (v1) was discarded** for probe errors:
  - DECSET 2026 was misread;
  - streams were cut off while the model was still thinking;
  - prompts were not submitted.

  It's kept in `results-v1/`.
- 9 invalid v2 cells were found by reading screens and re-run.
- Detail: `bench/f2/probe/SUMMARY.md` and `README.md`.

**120×40, truecolor:**

| Harness | Ready ms | Echo p50 / p95 ms | Bytes per key | Idle CPU % | RSS MiB | S1 fps | Bytes per frame | S1 gzip bytes | RSS over 10 turns | DECSET 2026 |
|---|---|---|---|---|---|---|---|---|---|---|
| ternly | 72 | 12.4 / 14.1 | **19** | 3.2 † | **96** | 13.5 | 151 | **8 555** | 211 → 239 | yes |
| Claude Code | 369 | 4.1 / 20.0 | 38 | 0.97 | 271 | 6.9 | **58** | 8 973 | 260 → 330 | no |
| Codex | **61** | 0.9 / 7.7 | 119 | 4.3 | 843 | **28.1** | 82 | 37 738 | 610 → 909 | yes |
| Crush | 85 | 10.2 / 13.7 | 41 | 0.97 | 112 | 22.6 ‡ | 318 ‡ | 10 252 ‡ | 110 → 115 | yes |
| OpenCode | 3 744 | 6.1 / — | 88 | 23.4 | 728 | 25.4 ‡ | 107 ‡ | — ‡ | 938 → 912 | yes |
| Pi | 485 | 2.1 / — | 178 | **0.77** | 121 | 25.0 | 486 | — | 130 → 145 | yes |
| CodeWhale | 218 | **0.4 / 0.6** | 161 | 1.2 | 201 | 23.7 | 395 | 69 753 | 202 → 233 | yes |
| Gemini CLI | 1 864 | n/a (no account) | n/a | 0.50 | 434 | n/a | n/a | n/a | n/a | no |

† **ternly's idle CPU isn't the UI.** In a fresh home with default config, ternly starts a background eval
(`glm-5.3:cloud`, while the cloud quota is exhausted) and a 9.9 MB catalog download during "idle". Measured
10–40 s after start: 2.2 % (fresh home) and 3.5 % (second start). Phase F measured the UI alone at 0.3–1.0 %.

‡ **No answer was produced:** gemma4's tool calls were printed as text. These describe painting only.

**Measured facts that shape the proposal:**
- **Echo is ternly's worst number:** 12.4 ms against the best, 0.4 ms (CodeWhale). The two Bubble Tea harnesses
  are the two slowest (ternly 12.4, Crush 10.2); everyone else is under 5 ms.
  - **Hypothesis, to verify first in implementation:** the renderer paints on a frame tick, not on input.
  - It's within the 16 ms budget, so "best" needs ≤ 0.4 ms.
- **ternly's streaming is the cheapest over a link:** the smallest compressed total for S1 (8.6 KB).
  - Its 13.5 fps is coalescing, not cost.
  - Codex pairs 28 fps with 82 B per frame, the best smoothness at a low frame cost.
- **Adaptation:**
  - ternly's colour degrades correctly at every tier (24-bit, 256, 16, none);
  - OpenCode emits 24-bit or 256 colours under 16 colours, NO_COLOR and TERM=dumb, and Pi emits 256;
  - Codex never becomes ready under TERM=dumb, and didn't redraw when grown to 250×70;
  - **no harness falls back to ASCII glyphs under LANG=C, ternly included.**
- **Resize:** ternly reflows in 7 ms / 1.3 KB (to 80×24) and 14 ms / 2.4 KB (to 250×70).

**Not measured:**
- Gemini CLI's interaction (it stops at its trust dialog without an account);
- a real GPU terminal's paint time;
- a real SSH link (gzip of the pty stream stands in for ssh -C);
- network isolation (the sandbox shares the network).

## Rubric scores (the extended lines; Phase F's eight lines are in ADR 023)
| Line | ternly | Claude Code | Codex | Crush | OpenCode | Pi | CodeWhale |
|---|---|---|---|---|---|---|---|
| Information reach (facts at ≤ 1 key: 0 at ≤ 5, 4 at ≥ 16, linear) | 8 → **1** | 5 → 0 | 7 → **1** | 10 → **2** | 5 → 0 | 5 → 0 | 6 → 0 |
| Parallel-agent visibility (S3 and the reach sessions) | **0** (none shown) | 2 (0-key cue, 7-key list) | **3** (1-key command center with status filters) | 0 | 0 | 0 (no sub-agents) | 2 (8-key `/agents` with model and elapsed) |

**Render cost and adaptivity aren't given single scores.**
- **Render cost:** as written ("within 10 % of the best on every tier, 0 at ≥ 4× on any"), the line gives
  every harness 0, because each is ≥ 4× behind the best on some metric. Tiers of metrics are listed above
  instead; the line needs a revised formula (decision 5).
- **Adaptivity:** scored per family from SUMMARY.md:

  | Family | ternly | Notes on rivals |
  |---|---|---|
  | colour | ✓ | OpenCode ✗, Pi ✗ |
  | ASCII glyphs | ✗ | every harness ✗ |
  | size and reflow | ✓ | Codex ✗ |
  | slow link and memory | ✓ | |

  That gives ternly 3; the other harnesses' full scores need their dumb-terminal and ASCII cells read by
  hand, which is listed as remaining.

## Budgets per tier, from the measured best (replacing the placeholders above)
| Budget | Measured best | ternly now | Target |
|---|---|---|---|
| Keystroke echo, p50 / p95 | 0.4 / 0.6 ms (CodeWhale) | 12.4 / 14.1 ms | ≤ 1 / ≤ 4 ms; "best" only if ≤ 0.4 ms is measured |
| Bytes per keystroke frame | 19 B (ternly) | 19 B | ≤ 19 B at every colour tier |
| Startup to ready | 61 ms (Codex) | 72 ms (sandboxed) | ≤ 60 ms |
| Streaming smoothness / frame cost | 28 fps at 82 B (Codex) | 13.5 fps at 151 B | ≥ 25 fps at ≤ 82 B per frame (local) |
| Compressed S1 total (link proxy) | 8.6 KB (ternly) | 8.6 KB | ≤ 8.6 KB; on slow links ≤ 10 fps coalesced |
| Idle CPU, UI only | 0.5–0.97 % (Gemini, Pi, Claude, Crush; default configs) | 0.3–1.0 % UI; 2–3.5 % with background work | ≤ 0.5 % UI; background work separated, deferred while the user is active, and budgeted |
| RSS at start / growth per turn | 96 MiB (ternly) / 0.5 MiB per turn (Crush) | 96 / 2.8 MiB per turn | ≤ 96 MiB / ≤ 1 MiB per turn |
| 10k-line redraw, p95 | — (ternly in-process) | 1.35–1.48 ms in CI | < 16 ms (met; keep) |
| ASCII glyphs under LANG=C | none | none | correct ASCII set (no harness does this today) |

## What the proposal takes from the survey (targets for ternly)
| Fact | Today | Target | Source of the idea |
|---|---|---|---|
| running agents | ∞ | 0 (lanes) / 1 (Ctrl+G panel) | Claude's live cue; Codex's command center, with filters |
| turn elapsed + ttft + tok/s | ∞ | 0 (receipt per turn and status) | CodeWhale's footer |
| tokens this turn | ∞ | 0 (receipt) | none at 0 keys today; ternly would be first |
| git branch / dirty | ∞ / 6 | 0 (header) | Pi's and Crush's branch |
| files changed | 6 | 0 at ≥ 160 columns (rail), 1 (Ctrl+D) otherwise | Crush's Modified Files |
| the diff of a change | 6 | 0 inline per edit, 1 panel | Claude's inline diffs; OpenCode's review view |
| cost | 0 | 0, and it says "unknown" when it can't price | CodeWhale; never Claude's mispricing |
| mode | 0, truncatable | 0, never truncated; shows how to change it | Claude's footer |
| last error | 0 inline | 0 inline + a footer counter + 1 key for detail | Codex's warning counter |
| why this model | 5 | 0 (one-line reason on the routing card) / 1 (Ctrl+R) | ternly's own `/why` |

**Target:** ≥ 16 of 20 facts at ≤ 1 key, none unavailable. The best today is 10 at 0 keys (Crush) and 13 reachable
at all (CodeWhale).

**Bugs to fix along the way:**
- the mode truncation;
- Esc and Ctrl+U clearing input;
- the welcome hint cut off at 80 columns;
- the palette misaligned at 80 columns.

## Decisions needed (for the owner's review)
0. Start the implementation with the echo latency (verify the frame-tick hypothesis, then fix), the
   separation of background work from the UI's idle budget, and the four bugs above; then the information
   architecture.
1. The key map for the Level-2 panels (the Ctrl+D and Ctrl+R conflicts).
2. Whether the always-on status may take 2 lines at 80×24, or must stay 1.
3. The bytes-per-frame budgets, once the survey numbers are in.
4. Gemini CLI and the Cursor agent can only be scored from docs without a Google or Cursor
   sign-in. Do you want a sign-in for a first-hand measurement? (A Gemini sign-in now exists in your
   real home; the survey's isolated runs don't use it.)
5. The render-cost line's formula: as written it scores every harness 0. Proposed instead: per metric,
   4 if within 10 % of the best, 3 within 2×, 2 within 4×, 1 within 10×, 0 beyond, averaged across metrics.
6. ternly's default background evals and catalog refresh: keep them on by default but pause them while
   a session is in the foreground, or turn them off by default.

## Decisions (owner's review, 2026-10-11)
1. **Order: (a) echo, (b) background work, (c) the four bugs, (d) the layout.** Stop for review after (a)–(c).
   - (b) is a bug: never schedule an eval when the quota is exhausted (check the balance first); fetch the catalog
     conditionally (ETag/If-Modified-Since), with no unchanged re-download.
   - Echo: verify the frame-tick hypothesis, then target < 2 ms.
2. **Keys:**
   - a configurable leader key plus a letter per panel, with a which-key popup;
   - the Ctrl+K palette for everything;
   - no Ctrl+D/R/S/Q/Z bindings;
   - direct shortcuts only as extras where enhanced keyboard support exists.
3. **2-line status at 80×24:** yes. Fields are priority-ordered and truncate by importance. The status collapses to 1
   line below 24 rows, or when the input grows. The transcript keeps ≥ 16 rows.
4. **Bytes per frame:** where ternly is best, the budget is the current value (no regression beyond the gate's noise
   threshold); elsewhere the target is the best measured, with the gap recorded.
5. **Gemini CLI and the Cursor agent:** docs only, for now.
6. **Render-cost scoring:** one neutral formula applied identically to every harness (e.g. best/value), plus rankings
   under two alternative formulas. Record that it was revised after seeing the data. It was: the first formula scored
   every harness 0.
7. **Background work:**
   - pause evals and the catalog refresh during active sessions;
   - run them only when idle, with quota available, and on AC power;
   - the owner's config stays as it is;
   - new installs: evals are opt-in through a first-run question; the catalog refresh is idle-only and conditional.

## Implementation status (owner's review of (a)–(c), 2026-10-11)
- **(a) Echo: the < 2 ms target is NOT met yet.** ternly echoes in 6.5 ms median (p95 7.0–7.4) at 120 fps, down from
  14.6 ms. A Bubble Tea fork measured 1.3 ms, but it needed a `go.mod` `replace`, which breaks `go install`, so it was
  withdrawn. The fix is proposed upstream (charmbracelet/bubbletea#1857, draft PRs #1858 and #1859), and ternly issue
  #37 tracks adopting it. **The target stays < 2 ms and isn't loosened.** Bubble Tea has no runtime frame-rate control,
  so dropping to a low rate when idle isn't possible; the 120 fps idle cost (0.45–0.55% for the TUI) is accepted until
  #1858 lands. See ADR 032.
- **(b) Background work:** done (ADR 031).
- **(c) The four bugs:** fixed, and widths now count display columns (CJK and emoji).

## The information-reach target: what it still needs (owner's review of (d) steps 1–2, 2026-10-11)
**The target stays ≥ 16 of 20 facts at ≤ 1 key, none unavailable. It isn't dropped.** After steps 1–2, the layout
harness (`bench/f2/layout`, a scripted session through a pseudo-terminal) measures **15 at 0 keys and none missing
from the UI** at every size and tier. That's every fact its session can show. The other five need sessions that
produce them, and a fact the harness never sees can't count:

| Fact | Needs | When |
|---|---|---|
| 11 running agents and their status | a scripted session with parallel sub-agents | step 3 (agent lanes) |
| 16 pending approval | a scripted session in `ask` mode that stops on an edit | step 3 |
| 4 quota remaining | a provider that reports a quota (headers), and one that runs out (HTTP 429) | step 3 |
| 9 current plan step | plan data from the core (Phase C's plan) | deferred until Phase C resumes |
| 10 the whole plan | the same | deferred until Phase C resumes |

Step 3 adds the first three scenarios to the harness and re-measures. Until plan data exists, the best reachable count is 18 of 20,
and the report will name the two facts it can't yet show.

**After step 3 (agent lanes, 2026-10-11):** the harness gained the agent, approval and quota scenarios. It now measures
**18 of 20 facts at 0 keys and none missing from the UI**, at every size and tier:
- running agents: the lanes;
- pending approval: the box or the review, with its why;
- quota: "quota out until …" after Ollama Cloud's 429.

The two left are the plan facts (9 and 10), which wait for plan data from the core (deferred with Phase C). The
≥ 16 target is met, with 18 at 0 keys. "None unavailable" holds for every fact a session can produce today.
- **Quota "remaining":** shown as a percentage only where the provider reports it (Ollama Cloud's balance API, with
  `OLLAMA_API_KEY`). A pay-per-token API's 429 is a rate limit: routing keeps the model (ADR 031), so the status line
  doesn't call it a quota.

## Step 4: panels and the leader key (2026-10-11)
Decision 2 is implemented as follows.
- **The leader is Ctrl+X by default**, with a which-key popup listing the letters while it waits:
  - a agents, c changes, e last error, p plan, r why (routing);
  - s sessions, o tool output, k commands.
- **The leader is configurable** (`"keys": {"leader": …}`): a Ctrl or Alt letter, or F1–F12. ternly refuses, with a note at
  startup, these keys:
  - Ctrl+D/R/S/Q/Z;
  - keys that type or edit in the input (including Ctrl+G, select all, and Alt+b/c/d/f/l/u, word editing);
  - its own direct keys (Ctrl+K/O/B/L).
- **Direct shortcuts:** Alt+letter, only once the terminal reports key disambiguation (the kitty protocol); on other
  terminals Alt+x is Esc then x. Letters the input uses with Alt have no direct key (only c, changes, among the panels).
- **Placement:** at ≥ 160 columns a panel docks as a 64-column rail beside the transcript; narrower, it takes the
  transcript's place. One panel or the session sidebar at a time. Esc closes it (after clearing typed text); PgUp/PgDn
  scroll it.
- **Panels are rebuilt on the events they show** (subagent, error, routing, edit, turn end, resize), never per frame.
  The changes panel reads `git diff` off the UI goroutine when it opens and when a turn ends.
- **The palette lists every panel with its keys** (`/panel <name>`). The accessible mode prints the keys and the panels
  as text.
- **The plan panel exists but says there is no plan data:** facts 9 and 10 still wait for Phase C.

**Measured** (`bench/f2/layout/results/step4-panels`, the step-4 binary):
- **Reach:** 18 of 20 facts at 0 keys and none missing, at 200×50, 120×40 and 80×24, Unicode and ASCII/no colour. That's
  unchanged: the panels add depth (a fact in full), not new facts. The two left are the plan facts.
- **The estimated quota:** a 429 without Retry-After shows "quota out, retry in about 1h (estimated)" and no clock time
  (`120x40-quota-estimated`).
- **Desktop perf:** the owner's desktop, which was busy (a test suite ran during the recording), so these are not budget
  checks:
  - startup 48 / 54 ms (Unicode / ASCII);
  - echo p50 4.9 / 5.2 ms;
  - 19 B/key;
  - RSS 48 MiB.
- **Idle CPU against the budget:** measured on a CI runner instead (below).

**Idle CPU on a quiet CI runner** (`.github/workflows/tui-idle.yml`; Intel Xeon Platinum 8573C, 4 vCPUs, load ≈ 0;
7 interleaved rounds × 20 s, 120×40, start screen). [Run 38149601460](https://github.com/rajasatyajit/ternly/actions/runs/38149601460)
compared step 3, step 3 again (an A/A pair), step 2 and pre-layout main:
- every build measures a median of 0.65–0.70 %. **The ≤ 0.5 % budget is not met**, and wasn't before the layout work
  either;
- the paired differences from step 3 lie within −0.10…+0.10 points, the same as the A/A pair's. The layout steps add
  nothing measurable.

The cost is the 120 fps frame ticker accepted in ADR 032 until charmbracelet/bubbletea#1858 lands.

**Step 4 against step 3, on a CI runner:** [run 38151578966](https://github.com/rajasatyajit/ternly/actions/runs/38151578966),
the same method, step 4 listed twice.

| | Intel Xeon 8573C ([38149601460](https://github.com/rajasatyajit/ternly/actions/runs/38149601460)) | AMD EPYC 9V74 ([38151578966](https://github.com/rajasatyajit/ternly/actions/runs/38151578966)) |
|---|---|---|
| step 3, idle median, Unicode / ASCII | 0.70 / 0.70 % | 1.15 / 1.10 % |
| step 4, idle median, Unicode / ASCII | — | 1.10 / 1.15 % |
| step 4 − step 3, paired median Δ | — | +0.05 / −0.05 points (range −0.15…+0.20) |
| A/A pair's range of paired Δ | −0.10…+0.10 points | −0.15…+0.20 points |

- **Step 4 adds nothing measurable.** Its paired differences lie inside the A/A pair's range.
- **The runner's CPU decides the level:** the same step-3 build measures 0.70 % on Intel and about 1.1 % on AMD. That
  explains why `TestTUIIdleCPU`'s single samples in CI fall in two clusters (0.35–0.85 % and 1.15–1.60 %).
- **On neither runner does ternly meet ≤ 0.5 %.** Paired comparisons are only valid within one run, and the workflow
  records which CPU it ran on.

## Steps 5 and 6: the palette, and adapting to the environment (2026-10-12)
**Step 5, the palette (Level 3):**
- Each row shows, at its right, the keys that do the same (`/panel error … Ctrl+X e`; `/output … Ctrl+O · Ctrl+X o`).
  The keys drop out where the description would lose its room.
- A query of three letters or more also matches descriptions, ranked below name matches: `diff` finds the changes
  panel.
- With nothing typed, the five most recently used commands come first.
- `/output` and `/sidebar` make Ctrl+O and Ctrl+B reachable from the palette.

**Step 6, adapting:**
- **Glyphs:** in the ASCII mode the whole frame is made ASCII before it's written. ternly's glyphs get same-width
  stand-ins; box drawing becomes `+ - |`, which fixes the input box's corners; anything else outside ASCII becomes `?`
  per column. The recordings show 0 non-ASCII bytes at every size.
- **Colour:** a 16-colour tier (`TERM=xterm`) was added to the recordings. It emits only 16-colour SGR.
- **Accessible mode:** it announces each subagent's change of state.

**Frame rate, chosen at startup by profile** (Bubble Tea can't change it while running). `ui.fps` overrides it.
`/doctor` shows the profile and why, and below 30 fps it warns how long a key can wait for its frame (up to one frame
interval, half of one on average). The owner's decision (2026-10-12) is **low-power = 30 fps, and its idle budget stays
0.5 %, unmet, pending upstream #1858**.

| Profile | When | fps | Idle CPU: budget / measured | Echo p95: budget / measured |
|---|---|---|---|---|
| full | AC power, local terminal, ≥ 4 CPUs at ≥ 2 GHz | 120 | pending upstream #1858 / 0.70–1.55 % | ≤ 16 ms / 9.2–9.5 ms: **met** |
| low-power | on battery, or < 4 CPUs, or < 2 GHz | 30 | ≤ 0.5 % / 0.30 % (AMD 9V45), 0.45–0.50 % (Intel 8370C), 0.70–0.85 % (AMD 7763): **unmet on the slowest runner; pending upstream #1858** | ≤ 40 ms (a deliberate trade, below) / 33.1–33.7 ms: **met** |
| remote | over SSH (also stops the spinner) | 30 | as low-power | as low-power |

**Echo budgets by profile (the owner's decision, 2026-10-12):**
- **full:** ≤ 16 ms p95.
- **low-power and remote:** ≤ 40 ms p95. This is a deliberate trade, not "within budget":
  - over SSH the network's latency comes on top of every keystroke and dominates it;
  - on battery the lower frame rate is an explicit trade for power.
- **When upstream #1858 lands** (a frame written as soon as input arrives), every profile returns to ≤ 16 ms.

**The echo measurement was wrong until now, and is fixed.**
- **The flaw:** tuiprobe sent each key 150 ms after the previous frame went quiet. Frames come on a ticker, so every
  key landed at the same point of the frame interval, and "echo" measured that phase: about 48 ms at 10, 15 and 20 fps
  alike, and 14–15 ms at 30 and 60 fps.
- **The fix:** each key now waits a random 0–125 ms first (with a fixed seed).
- **What it shows:** a key waits for the next frame, so the p95 echo is about one frame interval and the p50 about
  half of one plus ~5 ms. The owner's choice of 30 fps assumed echo of about 15 ms (the old number); it is 33 ms p95.
- **What is affected:** every echo figure taken before (ADR 032's 6.5 ms, the survey's per-harness echo, steps 1–4)
  carries the same phase bias. They are re-measured in the final comparison.

**The runs behind the table** (`tui-idle.yml`; 7 interleaved rounds × 20 s, 120×40; Unicode / ASCII medians):

| Run | Runner CPU | Echo probe | Result |
|---|---|---|---|
| [38195393234](https://github.com/rajasatyajit/ternly/actions/runs/38195393234) | AMD EPYC 9V45 | random pause (fixed) | 120 fps: 0.70 / 0.70 %, echo 6.1 / 9.5 ms (A/A Δ −0.15…+0.10)<br>60: 0.50 %, 10.3 / 17.8 ms<br>30: 0.30 %, 20.8 / 33.7 ms<br>10: 0.20 %, 49.3 / 97.3 ms |
| [38157307607](https://github.com/rajasatyajit/ternly/actions/runs/38157307607) | AMD EPYC 7763 | fixed 150 ms (biased) | idle 120 fps: 1.55 %; 60: 1.10–1.15 %; 30: 0.70–0.75 % |
| [38164163926](https://github.com/rajasatyajit/ternly/actions/runs/38164163926) | AMD EPYC 7763 | fixed 150 ms (biased) | idle 60 fps: 1.45 %; 30: 0.85 %; 20: 0.65 %; 15: 0.50 % |
| [38161144184](https://github.com/rajasatyajit/ternly/actions/runs/38161144184) | Intel Xeon 8370C | fixed 150 ms (biased) | idle 30 fps: 0.45–0.50 %; 20: 0.35 %; 15: 0.30 %; 10: 0.20–0.25 % |
| [38167139795](https://github.com/rajasatyajit/ternly/actions/runs/38167139795) | AMD EPYC 9V45 | fixed 150 ms (biased) | idle 10 fps: 0.20–0.25 % (A/A Δ ±0.05); 15: 0.25–0.30 % |

The idle figures in the biased runs are sound; only their echo figures carry the phase bias. What the runs show:
- **Idle CPU is the frame ticker's wakeups, and the runner's CPU sets its level:** 120 fps costs 0.70 % on an AMD 9V45
  and 1.55 % on an AMD 7763. Locally at 1 fps, ternly wakes about 7 times a second and uses no measurable CPU.
- **No fixed frame rate meets both budgets on every runner measured.**
  - 16 ms echo p95 needs about 60 fps or more (17.8 ms at 60).
  - ≤ 0.5 % idle on the AMD 7763 needs about 15 fps or less.
  - Only writing a frame as soon as input arrives, not on the next tick, removes the conflict: that is upstream #1858.

**Recordings** (`bench/f2/layout/results/step5-palette`, `…/step6-adaptive`):
- Reach: 18 of 20 facts at 0 keys and none missing, in all nine runs (200×50, 120×40 and 80×24, in Unicode, ASCII/no
  colour and 16 colours).
- The estimated-quota scenario passes.
- The recordings ran on the owner's desktop under the full profile, so the frame-rate change doesn't touch them.

## F2 closed: the final comparison (2026-10-12)
**Method, per the F2 brief:** ternly's final build (main 79ebb29) against the two best-scoring rivals, with the same
scenarios (S1–S5), reach session, perf matrix and model as the survey.

**The rivals:**
- **Codex** scored best on the extended lines (reach 1, parallel agents 3).
- **Claude Code** scored best on most of ADR 023's eight lines (density, discoverability, diff review,
  accessibility). On the extended lines it ties with Crush and CodeWhale (2).

**Versions:** Claude Code 2.1.296 and Codex 0.162.1, the survey's versions; gemma4:latest, digest dc35e8d9c606, the
survey's model. Ollama is 0.40.3 now (0.35.1 in the survey).

**Data:** `bench/f2/final/` holds `SUMMARY.md` (the perf matrix, 54 cells), `MATRIX.md` (reach with evidence),
`scores.tsv` (scenario outcomes) and `metrics.jsonl` (measurements only). The screens stay local.

**Two fixes were needed before the numbers could be used:**
- **The model proxy** (`bench/f2/scenarios/tools/proxy`) didn't close Ollama's side when a harness was killed
  mid-request. Ollama went on generating for nobody, the requests (the longest 3 h 45 m) piled up until they held every
  slot, and every S5 turn of all three harnesses timed out. It's fixed and tested, and the whole run was redone.
- **The echo probe** pauses randomly before each key (above). Echo figures from before that change are biased.

**Scenarios** (outcomes from `tools/score`; model-bound, the same as in the survey for all three):

| | ternly | Claude Code | Codex |
|---|---|---|---|
| S1 explain | answered | answered | answered |
| S2 fix (tests pass after) | fail | pass (`changed=n`: the scorer saw no working-tree change; not investigated) | fail |
| S3 plan and parallel work | none | none (but delegated: "Waiting for 1 background agent") | none |
| S4 review (seeded bugs named) | 0/2 | 1/2 | **2/2** |
| S5 30 turns, then a provider failure | 30/30, error shown, recovered | 30/30, shown, recovered | 30/30, shown, recovered |

**Rubric, the extended lines:**

| Line | ternly before → final | Claude Code | Codex |
|---|---|---|---|
| Information reach: facts at ≤ 1 key (0 at ≤ 5, 4 at ≥ 16, linear, rounded as in the survey) | 8 → **15**: 1 → **4** (3.6; 15 is under the 16 threshold) | 5: 0 | 5: 0 |
| Parallel-agent visibility | 0 → **3** | 2 (a 0-key cue, `↓` to manage; seen live in S3) | 3 (a 1-key command centre with filters; not exercised live) |
| Render cost: mean of best/value per metric × 4 (decision 6) | **2.97** | 2.19 | 2.32 |
| Render cost, alternative 1: geometric mean × 4 | **2.68** | 1.83 | 1.67 |
| Render cost, alternative 2: metrics best of the three | **3 of 7** (startup, bytes/key, RSS) | 2 (S1 gzip, bytes per frame) | 2 (echo, idle CPU) |

Notes on the extended lines:
- **ternly's parallel-agent score rests on the scripted recordings.** They show lanes at 0 keys (model, step, call,
  tokens, cost, time) and the agents panel at 2 keys. ternly couldn't delegate in the live S3: its subagent tool (`task`) is offered only when agent definitions exist, and the S3 fixture had none (see the correction below);
  Claude Code's was.
- **Render cost** uses this run's desktop medians: ready ms, echo p95, bytes per key, idle CPU, RSS, S1 gzip bytes and
  bytes per frame.

**ADR 023's eight lines** (ternly's scores are judged from the evidence named; the rivals' best is ADR 023's, from
2026-10-08, same versions, not re-judged):

| Line | ternly after F | ternly final | Evidence | ADR 023's best rival |
|---|---|---|---|---|
| Responsiveness | 4 | 3 | startup 56 ms (the least of the three); echo p95 10.6 ms vs Codex 6.9; idle 0.93 % vs Codex 0.27 % (desktop) | Codex/Claude startup, OpenCode idle |
| Density without clutter | 3 | 4 | 15 facts at 0 keys; 2-line status by priority; lanes; receipts | OpenCode, Claude: 3 |
| Discoverability | 3 | 4 | the palette with keys; the leader key with which-key; `/panel` | OpenCode, Claude: 3 |
| Diff and review | 2 | 4 | inline diff per edit; the changes panel; per-hunk review in ask mode | Claude Code: 3 |
| Error clarity | 3 | 4 | a framed cause and next step; an error counter; the error panel; estimates marked as such | OpenCode: 3 |
| Accessibility | 3 | 4 | linear mode with subagent announcements; reduced motion; NO_COLOR; ASCII frames | Claude Code, Gemini: 3 |
| Terminal compatibility | 3 | 3 | ready at truecolor, 256, 16, none, ASCII and dumb; kitty, WezTerm and SSH still untested | — |
| Security of the screen | 4 | 4 | unchanged (three layers, tested) | — |

**Budgets, before (survey, main 1b0085a) → final.** "Desktop" is the owner's machine, where all three harnesses ran
interleaved; budget checks on idle and echo come from CI (ADR 030 step 6).

| Budget | Before | Final | Met? |
|---|---|---|---|
| Startup to input ≤ 60 ms (desktop, 120×40) | 72 ms | 56 ms | **met** |
| Echo p95 ≤ 16 ms: full profile, 120 fps (CI) | 14.1 ms (biased probe) | 9.2–9.5 ms (CI), 10.6 ms (desktop) | **met** |
| Echo p95 ≤ 40 ms: low-power and remote, 30 fps (CI; a deliberate trade, returning to ≤ 16 ms with upstream #1858) | — | 33.1–33.7 ms | **met** |
| Idle CPU ≤ 0.5 %: full, 120 fps | 3.20 % (desktop) | 0.70–1.55 % (CI), 0.93 % (desktop) | **pending upstream #1858** |
| Idle CPU ≤ 0.5 %: low-power, 30 fps | — | 0.30 % (AMD 9V45), 0.45–0.50 % (Intel 8370C), 0.70–0.85 % (AMD 7763) | **unmet on the slowest runner; pending upstream #1858** |
| Bytes per key ≤ 19 | 19 | 19 | **met** |
| RSS ≤ 96 MiB (base) | 96 | 71 | **met** |
| RSS growth over 10 turns | 211 → 239 MiB | 90 → 62 MiB | **met** (no growth) |
| Non-ASCII bytes in the ASCII tier: 0 | 745 | 0 | **met** |
| S1 compressed bytes ≤ 8.6 KB (the survey's best, ternly's own) | 8.6 KB | 10.9 KB | **unmet** here; **met after F2.1** (3.7–4.2 KB, below) |
| Bytes per frame while streaming ≤ the best measured (57 B, Claude Code) | 151 | 155 | **unmet** here; **met after F2.1** (55 B) |
| Long session, compressed bytes over 10 turns | 20.9 KB | 43.1 KB | no budget set; **doubled** here; **4.9–5.4 KB after F2.1** (below) |

**Where ternly is not best, as measured in this run:**
- **echo:** Codex 0.5 / 6.9 ms (p50 / p95) vs ternly 5.6 / 10.6. Codex writes on input; ternly waits for a frame.
- **idle CPU (desktop):** Codex 0.27 %, Claude Code 0.67 %, ternly 0.93 %.
- **S1 compressed bytes:** Claude Code 9.6 KB vs ternly 10.9 KB.
- **bytes per streaming frame:** Claude Code 57, Codex 88, ternly 155.
- **long-session bytes:** Claude Code 10.0 KB vs ternly 43.1 KB compressed.
- **task outcomes:** S2 (Claude Code passed) and S4 (Codex named both bugs, ternly none). These are model-bound, with
  the same model everywhere.
- **live parallel work:** only Claude Code delegated in S3. ternly had no subagent tool to offer there (no agent definitions in the fixture); a built-in general subagent is Phase C's item 6.

**Where it is best, among these three in this run:**
- reach (15 facts at ≤ 1 key, vs 5 and 5);
- startup (56 ms vs 62 and 376);
- bytes per key (19 vs 38 and 119);
- RSS (71 MiB vs 259 and 812);
- the ASCII tier (0 non-ASCII bytes vs 1011 and 1153);
- render cost under all three formulas.

Nothing here is claimed beyond these three harnesses and this run.

**Left open:**
- echo and idle at once: upstream #1858;
- the streaming and long-session byte regressions: an issue to file after review;
- the plan facts (9, 10): Phase C;
- kitty, WezTerm and SSH terminal checks.

## F2.1: output bytes, and the two fact counts (2026-10-12)
**The two fact counts measure different sessions, and both stand.**

| Count | Instrument and session | What it covers |
|---|---|---|
| **18 of 20 at 0 keys** (steps 3–6) | `bench/f2/layout`: ternly alone, against a scripted provider (`bench/fakeprovider`), at 200×50, 120×40 and 80×24 in every tier. The session runs four turns written to reach every state: an edit with verification, two parallel subagents, an approval in ask mode, and a 429 that exhausts the quota | every fact a session can produce today; only the plan (9, 10) is unobservable, until Phase C |
| **15 of 20 at ≤ 1 key** (the final comparison) | `bench/f2/reach`: ternly, Claude Code and Codex alike, against the real model (gemma4), at 120×40, after one real turn on the S2 fixture | only what that one turn produced. Agents (11), approval (16) and the plan (9, 10) never happened in it (n/o), and the quota (4) doesn't apply to a free local model (n/a) |

The 3 facts between them are 4, 11 and 16:
- the scripted session reaches their states;
- the real one-turn session doesn't, for any of the three harnesses;
- none of the three is "missing" in either count: ternly has 0 ∞ cells in both.

The final comparison uses the reach session because it's the same for all three harnesses. The layout harness is how
ternly's own steps are proven. Neither count is a claim about the other's sessions.

**Output bytes: the cause, found by row.** `tuiprobe -rowbytes` attributes a recording's output to the screen rows it
was written to, by following the cursor. In the final comparison's recordings:
- **The long session:** the status line (row 39) took 59 % of all bytes, entered 2,428 times. It was rewritten whole,
  for two reasons:
  - its cache field was `⚡`, a wide character. Lines holding one are re-sent on every frame by the renderer (the
    ultraviolet behaviour recorded earlier for 006e29f);
  - the running elapsed time counted tenths of a second, so the line changed about ten times a second.
- **The activity line** ("Thinking…", row 34 here, row 14 in S1): 18 % of the long session and 32 % of S1. Its shimmer
  recoloured every letter on each 70 ms tick.
- **The lanes:** these sessions had no subagents, so their cost wasn't measured here. They now follow the same 1 Hz
  rule.

**Fixes:**
- the cache field is `cache 35%`;
- elapsed times, on the status line and the lanes, count whole seconds while running (at most one change a second),
  and keep the tenth once done;
- the activity line is a spinner cell (advancing every 4th tick, about 3.5 a second) and static text.
- Tests cover each change; the 9 mutants are all killed.

**Re-measured as a paired A/B** (`bench/f2/probe/ab.sh`): main 25e930b against F2.1, alternating, in the final comparison's
cells (S1 stream × 3, the 10-turn long session × 2, 120×40 truecolor, gemma4). Every run answered, with answers of
similar length (29–34 lines).

| Budget | Final comparison | main (this A/B) | F2.1 (this A/B) | Met? |
|---|---|---|---|---|
| S1 compressed bytes ≤ 8.6 KB | 10.9 KB | 10.2–17.4 KB | **3.7–4.2 KB** | **met** |
| Bytes per streaming frame ≤ 57 B (Claude Code's best in the final comparison) | 155 B | 151–373 B | **55 B** | **met** |
| Long session, whole output compressed (survey before: 20.9 KB) | 43.1 KB | 49.5–58.8 KB | **4.9–5.4 KB** | down 10×; below the survey's 20.9 KB |
| Long session, whole output raw | 931 KB | 1.11–1.37 MB | **68–83 KB** | |
| S1 frames | 726 | 743–902 | 339–414 | |

The main rows vary with the answers' length (S1 total 122–302 KB); the F2.1 rows vary much less, because the bytes
now follow the content rather than the timers. Claude Code's long session was 10.0 KB compressed in the final
comparison, a different run; the two aren't compared here as a paired measurement.

**Correction (2026-10-12): ternly couldn't delegate in S3.** The final comparison said gemma4 "never delegated" in
ternly's live S3. That was the wrong reading:
- ternly offers its subagent tool (`task`) only when agent definitions exist (`plugins.Runtime.stageAgents`), and
  the S3 fixture (mitt, a fresh HOME) has none, so the tool wasn't there to call;
- Claude Code ships a built-in general-purpose agent, which is what it used.

The gap is ternly's, not the model's. Phase C's item 6 becomes: a built-in general subagent when no agents are
defined, then a measurement of how often each local model delegates with it, against today's behaviour.
