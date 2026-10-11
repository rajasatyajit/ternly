### Interaction at 120×40, truecolor (runs r1–r3; medians, echo p95 over every key)

| harness | runs ok | ready ms | key echo ms (p50 / p95) | bytes per key (p50) | idle CPU % | RSS MiB | notes |
|---|---|---|---|---|---|---|---|
| claude | 3/3 | 369 | 4.1 / 20.0 | 38 | 0.97 | 271 |  |
| codewhale | 3/3 | 218 | 0.4 / 0.6 | 161 | 1.23 | 201 |  |
| codex | 3/3 | 61 | 0.9 / 7.7 | 119 | 4.33 | 843 |  |
| crush | 3/3 | 85 | 10.2 / 13.7 | 41 | 0.97 | 112 |  |
| gemini | 3/3 | 1864 | 11.5 / 12.3 | 0 | 0.50 | 434 |  |
| opencode | 3/3 | 3744 | 6.1 / 9.8 | 88 | 23.43 | 728 |  |
| pi | 3/3 | 485 | 2.1 / 5.4 | 178 | 0.77 | 121 |  |
| ternly | 3/3 | 72 | 12.4 / 14.1 | 19 | 3.20 | 96 |  |

### Colour and glyph tiers at 120×40 (one run each)

Cell: echo p50 ms · bytes/key p50 · colours used in the output · non-ASCII bytes · full clears.

| harness | truecolor-r1 | 256 | 16 | nocolor | truecolor+ascii | dumb |
|---|---|---|---|---|---|---|
| claude | ready · 3.9 ms · 38 B · 24-bit+16 · 978 · 1 | ready · 4.2 ms · 38 B · 256+16 · 960 · 1 | ready · 4.3 ms · 38 B · 16 · 824 · 1 | ready · 4.0 ms · 38 B · none · 917 · 1 | ready · 4.1 ms · 38 B · 24-bit+16 · 923 · 1 | ready · 4.4 ms · 38 B · none · 1006 · 1 |
| codewhale | ready · 0.4 ms · 161 B · 24-bit+16 · 1681 · 1 | ready · 0.4 ms · 149 B · 256+16 · 1681 · 1 | ready · 0.3 ms · 149 B · 256+16 · 1681 · 1 | ready · 0.4 ms · 117 B · none · 1681 · 1 | ready · 0.3 ms · 161 B · 24-bit+16 · 1681 · 1 | ready · 0.4 ms · 117 B · none · 1681 · 1 |
| codex | ready · 0.9 ms · 119 B · 24-bit+16 · 1153 · 0 | ready · 1.1 ms · 114 B · 256 · 1148 · 0 | ready · 1.0 ms · 100 B · none · 1153 · 0 | ready · 0.8 ms · 80 B · none · 1153 · 0 | ready · 0.9 ms · 119 B · 24-bit+16 · 1153 · 0 | not ready · 0.2 ms · 1 B · none · 0 · 0 |
| crush | ready · 10.1 ms · 41 B · 24-bit+16 · 1155 · 1 | ready · 12.8 ms · 33 B · 256+16 · 1182 · 1 | ready · 10.7 ms · 27 B · 16 · 1128 · 1 | ready · 11.3 ms · 19 B · none · 1146 · 1 | ready · 9.2 ms · 41 B · 24-bit+16 · 1182 · 1 | ready · 9.8 ms · 19 B · none · 1122 · 1 |
| gemini | ready · 12.3 ms · 0 B · 24-bit+16 · 3456 · 0 | ready · 6.8 ms · 0 B · 256 · 4248 · 0 | ready · 7.1 ms · 0 B · 16 · 4248 · 0 | ready · 5.8 ms · 0 B · none · 4248 · 0 | ready · 10.6 ms · 0 B · 24-bit+16 · 3456 · 0 | ready · 5.7 ms · 0 B · none · 4257 · 0 |
| opencode | ready · 6.3 ms · 88 B · 24-bit+16 · 494 · 0 | ready · 6.1 ms · 75 B · 256 · 494 · 0 | ready · 5.6 ms · 88 B · 24-bit+16 · 494 · 0 | ready · 5.8 ms · 75 B · 256 · 494 · 0 | ready · 6.1 ms · 88 B · 24-bit+16 · 494 · 0 | ready · 6.2 ms · 88 B · 24-bit+16 · 494 · 0 |
| pi | ready · 2.0 ms · 178 B · 24-bit+16 · 748 · 2 | ready · 1.9 ms · 178 B · 256+16 · 748 · 2 | ready · 2.1 ms · 178 B · 256+16 · 748 · 2 | ready · 2.2 ms · 178 B · 256+16 · 748 · 2 | ready · 2.0 ms · 178 B · 24-bit+16 · 748 · 2 | ready · 2.1 ms · 178 B · 256+16 · 748 · 2 |
| ternly | ready · 12.4 ms · 19 B · 24-bit+16 · 745 · 1 | ready · 12.6 ms · 19 B · 256 · 745 · 1 | ready · 12.3 ms · 19 B · 16 · 745 · 1 | ready · 11.8 ms · 19 B · none · 745 · 1 | ready · 13.6 ms · 19 B · 24-bit+16 · 745 · 1 | ready · 13.0 ms · 19 B · none · 745 · 1 |

### Sizes and live resize (truecolor)

| harness | 80×24: ready / echo p50 / bytes per key | 250×70: ready / echo p50 / bytes per key | resize 120×40→80×24: ms / bytes | →250×70: ms / bytes |
|---|---|---|---|---|
| claude | 419 ms / 4.2 ms / 38 B | 426 ms / 4.5 ms / 38 B | 14 ms / 1672 B | 12 ms / 2613 B |
| codewhale | 165 ms / 0.4 ms / 161 B | 217 ms / 0.3 ms / 161 B | 3 ms / 4626 B | 12 ms / 43350 B |
| codex | 54 ms / 0.6 ms / 162 B | 64 ms / 1.6 ms / 119 B | 76 ms / 789 B | 1 ms / 4 B |
| crush | 78 ms / 11.2 ms / 41 B | 81 ms / 10.5 ms / 41 B | 15 ms / 3914 B | 18 ms / 6155 B |
| gemini | 1865 ms / 8.9 ms / 0 B | 1877 ms / 14.4 ms / 0 B | 12 ms / 4443 B | 16 ms / 5015 B |
| opencode | 3826 ms / 6.3 ms / 88 B | 3632 ms / 6.3 ms / 88 B | 107 ms / 6567 B | 111 ms / 24083 B |
| pi | 492 ms / 2.0 ms / 138 B | 491 ms / 2.0 ms / 308 B | 4 ms / 2533 B | 4 ms / 5485 B |
| ternly | 65 ms / 12.0 ms / 19 B | 68 ms / 13.1 ms / 19 B | 7 ms / 1266 B | 14 ms / 2387 B |

### Streaming S1 ("Explain what simplelru/lru.go does…"; medians over 3 runs)

| harness | size | runs ok | bytes | gzip bytes | frames | fps | bytes per frame (p50) | answer ms | eviction text on screen (heuristic) |
|---|---|---|---|---|---|---|---|---|---|
| claude | 120x40 | 3/3 | 101166 | 8973 | 500 | 6.9 | 58 | 74361 | 3/3 |
| claude | 80x24 | 3/3 | 95475 | 8896 | 628 | 7.1 | 57 | 88739 | 3/3 |
| codewhale | 120x40 | 3/3 | 670168 | 69753 | 1215 | 23.7 | 395 | 51287 | 3/3 |
| codewhale | 80x24 | 3/3 | 445666 | 49169 | 1031 | 20.5 | 272 | 50295 | 3/3 |
| codex | 120x40 | 3/3 | 359427 | 37738 | 2063 | 28.1 | 82 | 74116 | 3/3 |
| codex | 80x24 | 3/3 | 296258 | 29868 | 1885 | 26.3 | 68 | 73416 | 3/3 |
| crush | 120x40 | 3/3 | 104815 | 10252 | 338 | 22.6 | 318 | 14401 | 3/3 |
| crush | 80x24 | 3/3 | 85837 | 7717 | 287 | 22.0 | 318 | 12637 | 1/3 |
| opencode | 120x40 | 3/3 | 93666 | 3431 | 492 | 25.4 | 107 | 19410 | 0/3 |
| opencode | 80x24 | 3/3 | 108752 | 3953 | 560 | 25.3 | 107 | 22183 | 1/3 |
| pi | 120x40 | 3/3 | 643620 | 21591 | 845 | 25.0 | 486 | 33908 | 3/3 |
| pi | 80x24 | 3/3 | 372546 | 14789 | 755 | 25.2 | 366 | 30026 | 3/3 |
| ternly | 120x40 | 3/3 | 113522 | 8555 | 699 | 13.5 | 151 | 52343 | 3/3 |
| ternly | 80x24 | 3/3 | 99533 | 7330 | 630 | 13.6 | 151 | 46308 | 3/3 |

### Long session (10 turns at 120×40): RSS and output growth

| harness | turns answered | RSS MiB start → end | total bytes | gzip bytes |
|---|---|---|---|---|
| claude | 10/10 | 260 → 330 | 118667 | 10017 |
| codewhale | 10/10 | 202 → 233 | 1193409 | 106723 |
| codex | 10/10 | 610 → 909 | 540500 | 48688 |
| crush | 10/10 | 110 → 115 | 378041 | 36293 |
| opencode | 10/10 | 938 → 912 | 627635 | 27156 |
| pi | 10/10 | 130 → 145 | 794450 | 26686 |
| ternly | 10/10 | 211 → 239 | 1185510 | 20881 |

### Rendering technique (from base-120x40-truecolor-r1's whole output)

| harness | alt screen | sync updates (DECSET 2026) | full clears (ED 2) | kitty keyboard | mouse | bracketed paste | leftovers reaped |
|---|---|---|---|---|---|---|---|
| claude | true | 0 | 1 | false | true | true | 0 |
| codewhale | true | 72 | 1 | true | true | true | 0 |
| codex | true | 50 | 0 | true | true | true | 0 |
| crush | true | 11 | 1 | true | true | true | 0 |
| gemini | false | 0 | 0 | false | false | true | 0 |
| opencode | true | 15 | 0 | false | true | true | 0 |
| pi | true | 12 | 2 | true | true | true | 0 |
| ternly | true | 30 | 1 | true | false | true | 0 |
