# Layout recordings (2026-10-11 23:15)

ternly: `ternly 0.1.0`

- `200x50-ascii-nocolor`: screens are pure ASCII; colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 0 non-ASCII bytes
- `200x50-unicode-16`: colour SGR in the output: 0 truecolor, 0 256-colour, 1143 16-colour
- `120x40-ascii-nocolor`: screens are pure ASCII; colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 0 non-ASCII bytes
- `120x40-unicode-16`: colour SGR in the output: 0 truecolor, 0 256-colour, 862 16-colour
- `80x24-ascii-nocolor`: screens are pure ASCII; colour SGR in the output: 0 truecolor, 0 256-colour, 0 16-colour; 0 non-ASCII bytes
- `80x24-unicode-16`: colour SGR in the output: 0 truecolor, 0 256-colour, 737 16-colour
- a 429 without Retry-After (`120x40-quota-estimated`): the status line says `quota out, retry in about …h (estimated)`, with no clock time

## Information reach (keys to see each fact; 0 = on the idle screen)

| # | Fact | 200x50-unicode | 200x50-ascii-nocolor | 200x50-unicode-16 | 120x40-unicode | 120x40-ascii-nocolor | 120x40-unicode-16 | 80x24-unicode | 80x24-ascii-nocolor | 80x24-unicode-16 |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | current model | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2 | why this model | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3 | cost so far | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 4 | quota remaining | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 5 | context used / window | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 6 | tokens this turn | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 7 | files changed | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 8 | the diff of a change | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 9 | current plan step | n/o | n/o | n/o | n/o | n/o | n/o | n/o | n/o | n/o |
| 10 | the whole plan | n/o | n/o | n/o | n/o | n/o | n/o | n/o | n/o | n/o |
| 11 | running agents | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 12 | last error | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 13 | verify / test status | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 14 | session name / id | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 15 | permission mode | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 16 | pending approval | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 17 | git branch / dirty | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 18 | turn elapsed | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 19 | undo / checkpoints | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 20 | workspace / cwd | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| | **at 0 keys** | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 |
| | **at ≤ 1 key** | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 |
| | **reachable at all** | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 | 18 |
| | **∞ (not in the UI)** | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |

## Performance at 120×40 (budgets: ADR 030)

| Tier | Startup ms (≤ 60) | Echo p50 / p95 ms (target ≤ 1 / ≤ 4; ADR 032: 6.5 at 120 fps) | Bytes per key (≤ 19) | Idle CPU % over 20 s (≤ 0.5) | RSS MiB (≤ 96) |
|---|---|---|---|---|---|
| unicode | 50 | 4.6 / 6.9 | 19 | 0.70 | 51 |
| ascii-nocolor | 53 | 4.6 / 6.2 | 19 | 0.90 | 48 |
