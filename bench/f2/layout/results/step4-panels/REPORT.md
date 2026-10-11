# Layout recordings (2026-10-11 20:52)

ternly: `ternly 0.1.0`

- `200x50-ascii-nocolor`: 24 distinct non-ASCII glyphs on screen (·×478 —×27 “×6 ”×6 …×56 ›×1 ↑×2 →×35 ↓×2 ↻×29 ≥×35 ⎿×49 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 4650 non-ASCII bytes
- `120x40-ascii-nocolor`: 24 distinct non-ASCII glyphs on screen (·×298 —×15 “×6 ”×6 …×64 ›×1 ↑×2 →×17 ↓×2 ↻×13 ≥×16 ⎿×35 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 3017 non-ASCII bytes
- `80x24-ascii-nocolor`: 22 distinct non-ASCII glyphs on screen (·×212 —×11 “×3 ”×3 …×87 ↑×1 →×2 ↓×1 ↻×7 ≥×2 ⎿×16 ─×4256 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 2147 non-ASCII bytes
- a 429 without Retry-After (`120x40-quota-estimated`): the status line says `quota out, retry in about …h (estimated)`, with no clock time

## Information reach (keys to see each fact; 0 = on the idle screen)

| # | Fact | 200x50-unicode | 200x50-ascii-nocolor | 120x40-unicode | 120x40-ascii-nocolor | 80x24-unicode | 80x24-ascii-nocolor |
|---|---|---|---|---|---|---|---|
| 1 | current model | 0 | 0 | 0 | 0 | 0 | 0 |
| 2 | why this model | 0 | 0 | 0 | 0 | 0 | 0 |
| 3 | cost so far | 0 | 0 | 0 | 0 | 0 | 0 |
| 4 | quota remaining | 0 | 0 | 0 | 0 | 0 | 0 |
| 5 | context used / window | 0 | 0 | 0 | 0 | 0 | 0 |
| 6 | tokens this turn | 0 | 0 | 0 | 0 | 0 | 0 |
| 7 | files changed | 0 | 0 | 0 | 0 | 0 | 0 |
| 8 | the diff of a change | 0 | 0 | 0 | 0 | 0 | 0 |
| 9 | current plan step | n/o | n/o | n/o | n/o | n/o | n/o |
| 10 | the whole plan | n/o | n/o | n/o | n/o | n/o | n/o |
| 11 | running agents | 0 | 0 | 0 | 0 | 0 | 0 |
| 12 | last error | 0 | 0 | 0 | 0 | 0 | 0 |
| 13 | verify / test status | 0 | 0 | 0 | 0 | 0 | 0 |
| 14 | session name / id | 0 | 0 | 0 | 0 | 0 | 0 |
| 15 | permission mode | 0 | 0 | 0 | 0 | 0 | 0 |
| 16 | pending approval | 0 | 0 | 0 | 0 | 0 | 0 |
| 17 | git branch / dirty | 0 | 0 | 0 | 0 | 0 | 0 |
| 18 | turn elapsed | 0 | 0 | 0 | 0 | 0 | 0 |
| 19 | undo / checkpoints | 0 | 0 | 0 | 0 | 0 | 0 |
| 20 | workspace / cwd | 0 | 0 | 0 | 0 | 0 | 0 |
| | **at 0 keys** | 18 | 18 | 18 | 18 | 18 | 18 |
| | **at ≤ 1 key** | 18 | 18 | 18 | 18 | 18 | 18 |
| | **reachable at all** | 18 | 18 | 18 | 18 | 18 | 18 |
| | **∞ (not in the UI)** | 0 | 0 | 0 | 0 | 0 | 0 |

## Performance at 120×40 (budgets: ADR 030)

| Tier | Startup ms (≤ 60) | Echo p50 / p95 ms (target ≤ 1 / ≤ 4; ADR 032: 6.5 at 120 fps) | Bytes per key (≤ 19) | Idle CPU % over 20 s (≤ 0.5) | RSS MiB (≤ 96) |
|---|---|---|---|---|---|
| unicode | 48 | 4.9 / 6.9 | 19 | 0.90 | 48 |
| ascii-nocolor | 54 | 5.2 / 9.1 | 19 | 0.85 | 48 |
