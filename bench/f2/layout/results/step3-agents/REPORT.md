# Layout recordings (2026-10-11 19:21)

ternly: `ternly 0.1.0`

- `200x50-ascii-nocolor`: 24 distinct non-ASCII glyphs on screen (·×308 —×16 “×6 ”×6 …×31 ›×1 ↑×2 →×15 ↓×2 ↻×13 ≥×15 ⎿×39 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 3320 non-ASCII bytes
- `120x40-ascii-nocolor`: 24 distinct non-ASCII glyphs on screen (·×243 —×12 “×6 ”×6 …×53 ›×1 ↑×2 →×14 ↓×2 ↻×13 ≥×13 ⎿×34 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 2388 non-ASCII bytes
- `80x24-ascii-nocolor`: 20 distinct non-ASCII glyphs on screen (·×161 —×8 “×3 ”×3 …×69 ↑×1 ↓×1 ↻×7 ⎿×15 ─×2736 │×58 ╭×18 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1752 non-ASCII bytes

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
| unicode | 51 | 6.3 / 6.8 | 19 | 0.75 | 47 |
| ascii-nocolor | 52 | 6.2 / 8.5 | 19 | 0.70 | 47 |
