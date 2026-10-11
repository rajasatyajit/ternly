# Layout recordings (2026-10-11 18:24)

ternly: `ternly 0.1.0`

- `200x50-ascii-nocolor`: 23 distinct non-ASCII glyphs on screen (·×239 —×7 “×5 ”×5 …×25 ›×1 ↑×1 →×12 ↓×1 ↻×1 ≥×12 ⎿×39 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1919 non-ASCII bytes
- `120x40-ascii-nocolor`: 23 distinct non-ASCII glyphs on screen (·×184 —×7 “×5 ”×5 …×41 ›×1 ↑×1 →×9 ↓×1 ↻×1 ≥×9 ⎿×34 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1456 non-ASCII bytes
- `80x24-ascii-nocolor`: 20 distinct non-ASCII glyphs on screen (·×144 —×7 “×4 ”×4 …×36 →×6 ↻×1 ≥×6 ⎿×17 ─×2128 │×28 ╭×14 …); colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 1044 non-ASCII bytes

## Information reach (keys to see each fact; 0 = on the idle screen)

| # | Fact | 200x50-unicode | 200x50-ascii-nocolor | 120x40-unicode | 120x40-ascii-nocolor | 80x24-unicode | 80x24-ascii-nocolor |
|---|---|---|---|---|---|---|---|
| 1 | current model | 0 | 0 | 0 | 0 | 0 | 0 |
| 2 | why this model | 0 | 0 | 0 | 0 | 0 | 0 |
| 3 | cost so far | 0 | 0 | 0 | 0 | 0 | 0 |
| 4 | quota remaining | n/a | n/a | n/a | n/a | n/a | n/a |
| 5 | context used / window | 0 | 0 | 0 | 0 | 0 | 0 |
| 6 | tokens this turn | 0 | 0 | 0 | 0 | 0 | 0 |
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
| 18 | turn elapsed | 0 | 0 | 0 | 0 | 0 | 0 |
| 19 | undo / checkpoints | 0 | 0 | 0 | 0 | 0 | 0 |
| 20 | workspace / cwd | 0 | 0 | 0 | 0 | 0 | 0 |
| | **at 0 keys** | 12 | 12 | 12 | 12 | 12 | 12 |
| | **at ≤ 1 key** | 12 | 12 | 12 | 12 | 12 | 12 |
| | **reachable at all** | 14 | 14 | 14 | 14 | 14 | 14 |
| | **∞ (not in the UI)** | 1 | 1 | 1 | 1 | 1 | 1 |

## Performance at 120×40 (budgets: ADR 030)

| Tier | Startup ms (≤ 60) | Echo p50 / p95 ms (target ≤ 1 / ≤ 4; ADR 032: 6.5 at 120 fps) | Bytes per key (≤ 19) | Idle CPU % over 20 s (≤ 0.5) | RSS MiB (≤ 96) |
|---|---|---|---|---|---|
| unicode | 52 | 6.6 / 6.9 | 19 | 0.50 | 43 |
| ascii-nocolor | 55 | 6.5 / 7.1 | 19 | 0.50 | 45 |
