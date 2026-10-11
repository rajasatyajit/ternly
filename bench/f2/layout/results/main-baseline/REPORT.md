# Layout recordings (2026-10-11 18:14)

ternly: `ternly 0.1.0`

- `200x50-ascii-nocolor`: 23 distinct non-ASCII glyphs on screen (·×242 —×7 “×5 ”×5 …×19 ›×1 ↑×15 →×13 ↓×15 ↻×1 ≥×13 ⎿×39 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1934 non-ASCII bytes
- `120x40-ascii-nocolor`: 23 distinct non-ASCII glyphs on screen (·×194 —×7 “×5 ”×5 …×28 ›×1 ↑×15 →×9 ↓×15 ↻×1 ≥×9 ⎿×35 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1482 non-ASCII bytes
- `80x24-ascii-nocolor`: 22 distinct non-ASCII glyphs on screen (·×148 —×7 “×4 ”×4 …×25 ↑×14 →×6 ↓×14 ↻×1 ≥×6 ⎿×18 ─×2128 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1027 non-ASCII bytes

## Information reach (keys to see each fact; 0 = on the idle screen)

| # | Fact | 200x50-unicode | 200x50-ascii-nocolor | 120x40-unicode | 120x40-ascii-nocolor | 80x24-unicode | 80x24-ascii-nocolor |
|---|---|---|---|---|---|---|---|
| 1 | current model | 0 | 0 | 0 | 0 | 0 | 0 |
| 2 | why this model | 5 | 5 | 5 | 5 | 5 | 5 |
| 3 | cost so far | 0 | 0 | 0 | 0 | 0 | 0 |
| 4 | quota remaining | n/a | n/a | n/a | n/a | n/a | n/a |
| 5 | context used / window | 0 | 0 | 0 | 0 | 0 | 0 |
| 6 | tokens this turn | ∞ | ∞ | ∞ | ∞ | ∞ | ∞ |
| 7 | files changed | 6 | 6 | 6 | 6 | 6 | 6 |
| 8 | the diff of a change | 6 | 6 | 6 | 6 | 6 | 6 |
| 9 | current plan step | n/o | n/o | n/o | n/o | n/o | n/o |
| 10 | the whole plan | n/o | n/o | n/o | n/o | n/o | n/o |
| 11 | running agents | n/o | n/o | n/o | n/o | n/o | n/o |
| 12 | last error | 0 | 0 | 0 | 0 | 0 | 0 |
| 13 | verify / test status | 0 | 0 | 0 | 0 | 0 | 0 |
| 14 | session name / id | 0 | 0 | 0 | 0 | 0 | 0 |
| 15 | permission mode | 0 | 0 | 0 | 0 | 0 | 0 |
| 16 | pending approval | n/o | n/o | n/o | n/o | n/o | n/o |
| 17 | git branch / dirty | ∞ | ∞ | ∞ | ∞ | ∞ | ∞ |
| 18 | turn elapsed | ∞ | ∞ | ∞ | ∞ | ∞ | ∞ |
| 19 | undo / checkpoints | 8 | 8 | 8 | 8 | 8 | 8 |
| 20 | workspace / cwd | 0 | 0 | 0 | 0 | 0 | 0 |
| | **at 0 keys** | 8 | 8 | 8 | 8 | 8 | 8 |
| | **at ≤ 1 key** | 8 | 8 | 8 | 8 | 8 | 8 |
| | **reachable at all** | 12 | 12 | 12 | 12 | 12 | 12 |
| | **∞ (not in the UI)** | 3 | 3 | 3 | 3 | 3 | 3 |

## Performance at 120×40 (budgets: ADR 030)

| Tier | Startup ms (≤ 60) | Echo p50 / p95 ms (target ≤ 1 / ≤ 4; ADR 032: 6.5 at 120 fps) | Bytes per key (≤ 19) | Idle CPU % over 20 s (≤ 0.5) | RSS MiB (≤ 96) |
|---|---|---|---|---|---|
| unicode | 51 | 6.5 / 7.0 | 19 | 0.40 | 52 |
| ascii-nocolor | 47 | 6.5 / 7.0 | 19 | 0.40 | 52 |
