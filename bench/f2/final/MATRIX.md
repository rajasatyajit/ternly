# F2 final: information reach, ternly (final build) vs Claude Code and Codex

**Method:** the survey's (`bench/f2/reach/MATRIX.md`), re-run on 2026-10-12.
- **Builds and model:** ternly main 79ebb29, Claude Code 2.1.296, Codex 0.162.1 (the survey's versions); gemma4:latest
  (digest dc35e8d9c606) on Ollama 0.40.3 (the survey had 0.35.1).
- **Setup:** 120×40, inside box.sh, after one real turn on the S2 fixture.
- **Keys:** counted as typed (`/cost⏎` = 6, a chord = 1).
- **Legend:** as in the survey: 0, n keys, ∞ (not in the UI), n/o (the session never reached that state), n/a, L
  (listed but not run).

**Session states differ, and the cells show it:**
- ternly fixed the bug and verified it.
- Claude Code entered plan mode and stopped at its plan-approval prompt, so its plan and approval facts are observed
  and its edit facts aren't.
- Codex worked for 11 s and made no edit.

| # | Fact | ternly | Claude Code | Codex |
|---|---|---|---|---|
| 1 | current model | 0 status `◆ ollama/gemma4:latest T1` | 7 `/model⏎` "Kept model as gemma4:latest"; 8 `/status⏎` | 0 footer `gemma4:latest default` |
| 2 | why this model | 0 status `▸ cheapest` and the routing card | ∞ (`/why`: unknown command) | ∞ |
| 3 | cost so far | 0 status `$0.0000` | 7 `/usage⏎` "$0.3946 (costs may be inaccurate…)", for a free local model | ∞ (tokens only) |
| 4 | quota remaining | n/a | n/a | n/a (`/status` "Limits: data not available yet") |
| 5 | context used / window | 0 `ctx 5.9k/131.1k` | 9 `/context⏎` breakdown | 8 `/status⏎` "10.7K used / 258K" |
| 6 | tokens this turn | 0 receipt `↳ turn 32.2k↑ 3.8k↓` | ∞ | ∞ (session total in `/status`) |
| 7 | files changed | 0 receipt "1 file changed (simplelru/lru.go)" | n/o (no edit; `/diff` "Diff panel shown") | n/o (no edit) |
| 8 | the diff of a change | 0 inline `-`/`+` under the edit | n/o | n/o |
| 9 | current plan step | n/o (no plan data until Phase C) | n/o (a plan document, without step states) | n/o |
| 10 | the whole plan | n/o | 0 "Here is Claude's plan" at the approval prompt | n/o (Plan mode entered, no plan) |
| 11 | running agents | n/o (the model didn't delegate) | n/o (`← for agents` cue, none running) | n/o (`←` agent centre, none running) |
| 12 | last error | 0 framed box "✗ the provider can't be reached" and `⚠ 1 error` (S5) | 0 inline "API Error: Connection refused" (S5) | 0 inline "Reconnecting… Connection failed" (S5); 1 `F2` warning pane |
| 13 | verify / test status | 0 `verify ✓` and `● Verify go build … go vet` | ∞ | ∞ |
| 14 | session name / id | 0 header title | 8 `/status⏎` name and id | 8 `/status⏎` id |
| 15 | permission mode | 0 header `yolo` | 0 footer "plan mode on" | 8 `/status⏎` "Custom (workspace, never)" |
| 16 | pending approval | n/o (yolo) | 0 "Would you like to proceed?" with the plan | n/o |
| 17 | git branch / dirty | 0 header `master*` | ∞ (no screen names a branch) | 8 `/agents⏎` shows `master` |
| 18 | turn elapsed | 0 `1m44s` | 0 "Baked for 4m 5s" | 0 "Worked for 11s" |
| 19 | undo / checkpoints | 0 status `undo 1` | 8 `/rewind⏎` L | 1 `←` transcript, "↵ rewind" |
| 20 | workspace / cwd | 0 header | 8 `/status⏎` cwd | 0 footer |

## Totals

| | ternly | Claude Code | Codex |
|---|---|---|---|
| at 0 keys | **15** | 5 | 4 |
| at ≤ 1 key | **15** | 5 | 5 |
| reachable at all | **15** | 11 | 9 |
| ∞ | **0** | 4 | 4 |
| n/o | 4 | 4 | 6 |
| n/a | 1 | 1 | 1 |

**In the survey (main 1b0085a):** ternly had 8 at 0 keys, 12 reachable at all and 4 ∞.

**What the reach script didn't credit:**
- It doesn't press ternly's Ctrl+X panel keys, so the panels' depth (an error in full, every agent, the changes) isn't
  credited here.
- The scripted layout recordings (`bench/f2/layout/results/step6-adaptive`) show ternly's lanes and approvals at 0
  keys (18 of 20).
- The four ternly n/o cells are states this session didn't reach.
