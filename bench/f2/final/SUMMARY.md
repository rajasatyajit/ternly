### Interaction at 120×40, truecolor (runs r1–r3; medians, echo p95 over every key)

| harness | runs ok | ready ms | key echo ms (p50 / p95) | bytes per key (p50) | idle CPU % | RSS MiB | notes |
|---|---|---|---|---|---|---|---|
| claude | 3/3 | 376 | 3.5 / 13.5 | 38 | 0.67 | 259 |  |
| codex | 3/3 | 62 | 0.5 / 6.9 | 119 | 0.27 | 812 |  |
| ternly | 3/3 | 56 | 5.6 / 10.6 | 19 | 0.93 | 71 |  |

### Colour and glyph tiers at 120×40 (one run each)

Cell: echo p50 ms · bytes/key p50 · colours used in the output · non-ASCII bytes · full clears.

| harness | truecolor-r1 | 256 | 16 | nocolor | truecolor+ascii | dumb |
|---|---|---|---|---|---|---|
| claude | ready · 3.6 ms · 38 B · 24-bit+16 · 1018 · 1 | ready · 3.6 ms · 38 B · 256+16 · 875 · 1 | ready · 3.5 ms · 38 B · 16 · 978 · 1 | ready · 3.4 ms · 38 B · none · 875 · 1 | ready · 3.4 ms · 38 B · 24-bit+16 · 1011 · 1 | ready · 3.6 ms · 38 B · none · 918 · 1 |
| codex | ready · 0.5 ms · 119 B · 24-bit+16 · 1148 · 0 | ready · 0.5 ms · 114 B · 256 · 1153 · 0 | ready · 0.5 ms · 100 B · none · 1153 · 0 | ready · 0.4 ms · 80 B · none · 1153 · 0 | ready · 0.5 ms · 119 B · 24-bit+16 · 1153 · 0 | not ready · 0.1 ms · 1 B · none · 0 · 0 |
| ternly | ready · 4.6 ms · 19 B · 24-bit+16 · 739 · 1 | ready · 4.4 ms · 19 B · 256 · 739 · 1 | ready · 5.5 ms · 19 B · 16 · 739 · 1 | ready · 5.8 ms · 19 B · none · 739 · 1 | ready · 6.9 ms · 19 B · 24-bit+16 · 0 · 1 | ready · 7.1 ms · 19 B · none · 739 · 1 |

### Sizes and live resize (truecolor)

| harness | 80×24: ready / echo p50 / bytes per key | 250×70: ready / echo p50 / bytes per key | resize 120×40→80×24: ms / bytes | →250×70: ms / bytes |
|---|---|---|---|---|
| claude | 440 ms / 3.7 ms / 38 B | 423 ms / 3.6 ms / 38 B | 13 ms / 1677 B | 13 ms / 2618 B |
| codex | 54 ms / 0.4 ms / 119 B | 61 ms / 0.9 ms / 119 B | 77 ms / 786 B | 0 ms / 4 B |
| ternly | 51 ms / 6.4 ms / 19 B | 61 ms / 9.0 ms / 19 B | 7 ms / 1299 B | 9 ms / 2335 B |

### Streaming S1 ("Explain what simplelru/lru.go does…"; medians over 3 runs)

| harness | size | runs ok | bytes | gzip bytes | frames | fps | bytes per frame (p50) | answer ms | eviction text on screen (heuristic) |
|---|---|---|---|---|---|---|---|---|---|
| claude | 120x40 | 3/3 | 122397 | 9584 | 675 | 6.8 | 57 | 100306 | 3/3 |
| claude | 80x24 | 3/3 | 117943 | 10279 | 706 | 7.3 | 60 | 105943 | 3/3 |
| codex | 120x40 | 3/3 | 349991 | 35619 | 1905 | 20.8 | 88 | 85453 | 2/3 |
| codex | 80x24 | 3/3 | 333284 | 33195 | 2076 | 26.1 | 68 | 79065 | 2/3 |
| ternly | 120x40 | 3/3 | 116211 | 10881 | 726 | 13.8 | 155 | 52426 | 3/3 |
| ternly | 80x24 | 3/3 | 121047 | 10451 | 747 | 13.8 | 161 | 54142 | 3/3 |

### Long session (10 turns at 120×40): RSS and output growth

| harness | turns answered | RSS MiB start → end | total bytes | gzip bytes |
|---|---|---|---|---|
| claude | 9/10 | 259 → 312 | 102113 | 9956 |
| codex | 10/10 | 908 → 883 | 517585 | 40728 |
| ternly | 10/10 | 90 → 62 | 930895 | 43064 |

### Rendering technique (from base-120x40-truecolor-r1's whole output)

| harness | alt screen | sync updates (DECSET 2026) | full clears (ED 2) | kitty keyboard | mouse | bracketed paste | leftovers reaped |
|---|---|---|---|---|---|---|---|
| claude | true | 0 | 1 | false | true | true | 0 |
| codex | true | 50 | 0 | true | true | true | 0 |
| ternly | true | 29 | 1 | true | false | true | 0 |
