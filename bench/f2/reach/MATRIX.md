# F2 information reach: 20 facts × 7 harnesses

Each cell gives the minimum keystrokes from the idle prompt, mid-session, to see the fact. All harnesses ran the
same local model (gemma4:latest via Ollama), at 120×40, inside the box.sh boundary. Each session ran after one
real turn on the S2 fixture ("Resize evicts one entry; find the cause and fix it").

Keys are counted as typed: `/cost⏎` = 6, a chord = 1.

**Cell legend**
- **0**: visible on the idle screen.
- **n**: the keys to show it.
- **∞**: not in the UI, as observed.
- **n/o**: not observed. The session never reached that state (no plan, no approval prompt, or no edit).
- **n/a**: does not apply to a local, free, keyless model.
- **L**: the command is listed in the harness's own menu but wasn't run, because it changes state; the count assumes it shows the fact.

Evidence paths are relative to `results/<harness>/`.

| # | Fact | ternly | Claude Code | Codex | Crush | OpenCode | Pi | CodeWhale |
|---|---|---|---|---|---|---|---|---|
| 1 | current model | 0 footer `◆ ollama/gemma4:latest T1` (mid) | 7 `/model⏎` picker ✔ (run-model) | 0 footer (mid) | 0 sidebar (mid) | 0 `Build · gemma4 Ollama (local)` (mid) | 0 footer (mid) | 0 footer (mid) |
| 2 | why this model | 5 `/why⏎`: routing table with p, time, money, quota and score (run-why) | ∞ | ∞ | ∞ | ∞ | ∞ | ∞ (`/status` shows the route, not a reason) |
| 3 | cost so far | 0 footer `$0.0000` (mid) | 7 `/usage⏎` "$0.2801 (may be inaccurate)", for a free local model (run-usage) | ∞ (tokens only) | 0 sidebar `$0.00` (mid) | ∞ | ∞ | 0 footer `cost: unknown (billing basis unknown)`; `/cost⏎` audit (run-cost) |
| 4 | quota remaining | n/a (`/why` has a quota column) | n/a (`/usage` tab; subscription only) | n/a (`/status` "Limits: data not available yet") | n/a | n/a | n/a | n/a |
| 5 | context used / window | 0 `ctx 5.7k/131.1k` (mid) | 9 `/context⏎` with a category breakdown (run-context) | 8 `/status⏎` "10.8K used / 258K" (run-status) | 0 `39% (13K)` (mid) | 0 `7.3K` (used only, no window) (mid) | 0 `3.6%/128k (auto)` (mid) | 0 `context 12%`; `/context⏎` drill-down (run-context) |
| 6 | tokens this turn | ∞ (footer ↑↓ is the session total) | ∞ (session total in `/usage`) | ∞ (session total in `/status`) | ∞ | ∞ | ∞ (footer ↑↓ is the session total) | 8 `/tokens⏎` "Last API input … (turn telemetry)" (run-tokens) |
| 7 | files changed | 6 `/diff⏎` stat "1 file changed" (run-diff) | 6 `/diff⏎` panel "1 file changed +1 -1" (run-diff) | 6 `/diff⏎` pager (empty: no edit made) | 0 sidebar "Modified Files" (None: no edit made) (mid) | 6 `/diff⏎` "0 files" (no edit made) | ∞ | 6 `/diff⏎` "No changes since session start" |
| 8 | the diff of a change | 6 `/diff⏎` git diff (run-diff); transcript shows only `Edit (-2 +3 lines)` | 0 inline diff in the transcript (mid); 6 `/diff⏎` side panel | 6 `/diff⏎` (content n/o) | n/o | 6 `/diff⏎` review view (content n/o) | n/o | 6 `/diff⏎` (content n/o) |
| 9 | current plan step | n/o | n/o (Ctrl+T todo toggle showed nothing) | n/o | n/o | n/o | n/o | n/o |
| 10 | the whole plan | n/o | n/o | n/o | n/o | n/o | n/o | n/o |
| 11 | running agents and their status | ∞ (`/agents` lists definitions only) | 0 "Waiting for 1 background agent", `← for agents` (mid); 7 `/tasks⏎` | 1 `←` on an empty prompt: agent command center (run-agents) | ∞ (`[uses agent]` inline only) | ∞ (`/agents` = build/plan picker) | ∞ (no subagents) | 8 `/agents⏎` "live agent status · role · objective · model · elapsed" (run-agents) |
| 12 | last error | 0 inline (S5 error-screen) | 0 inline (S5) | 0 inline (S5); 1 `F2` warning pane, with a `⚠ 1 warning` counter in the footer | 0 inline (S5) | 0 inline (S5) | 0 inline (S5) | 0 inline (S5) |
| 13 | verify / test status | 0 inline `● Verify go build && go vet` (mid); 8 `/verify⏎` shows the command | ∞ | ∞ | ∞ | ∞ | ∞ | ∞ |
| 14 | session name / id | 0 title in the header (mid); 8 `/status⏎` id | 8 `/status⏎` name + id (run-status) | 8 `/status⏎` id | 0 sidebar title (mid) | 7 `/debug⏎` id; 10 `/sessions⏎` name | 9 `/session⏎` L | 9 `/session⏎` picker; 6 `/tree⏎` leaf id |
| 15 | permission mode | 0 header `bwrap · yolo` (cut to `· y` by a long path at 120 cols) | 0 footer "bypass permissions on (shift+tab to cycle)" | 8 `/status⏎` "Custom (workspace, never)"; footer only for Plan mode | 0 `YOLO MODE` | ∞ (the agent "Build" is shown, not permissions) | n/a (no permission system) | 0 footer `● never` + mode `work` |
| 16 | pending approval | n/o (all ran with approvals off) | n/o | n/o | n/o | n/o | n/o | n/o |
| 17 | git branch / dirty | ∞ branch; dirty via `/diff` 6 | ∞ branch; dirty via `/diff` 6 | 1 `←` agent center shows `master` | 0 sidebar `master` | ∞ mid (on the home screen only) | 0 footer `(master)` | ∞ mid (on the start banner only) |
| 18 | turn elapsed / latency | ∞ (only on a stopped turn: "2m33s") | 0 "Sautéed for 1m 59s · done 3:31 AM" | 0 "Worked for 12s • 03:36" | 0 "in 13s" | 0 "21.9s" | ∞ | 0 "worked 1m 11s · ttft 4.9s · 28 avg tok/s" |
| 19 | undo / checkpoints | 8 `/rewind⏎` turn list with ● for changed files (run-rewind) | 8 `/rewind⏎` L | 1 Ctrl+T transcript, footer "↵ rewind" (key-C-t) | ∞ | 6 `/undo⏎` L; 10 `/timeline⏎` | 6 `/tree⏎` L (session tree) | 6 `/undo⏎` L; 6 `/tree⏎` (run-tree) |
| 20 | workspace / cwd | 0 header (mid) | 8 `/status⏎` cwd | 0 footer | 0 sidebar | 0 footer | 0 footer | 8 `/status⏎` (0 on the start banner only) |

## Totals (20 facts)

| | ternly | Claude Code | Codex | Crush | OpenCode | Pi | CodeWhale |
|---|---|---|---|---|---|---|---|
| at 0 keys | **8** | 5 | 4 | **10** | 5 | 5 | 6 |
| reachable in n > 0 keys | 4 | 7 | 8 | 0 | 4 | 2 | 7 |
| reachable at all (0 or n) | 12 | 12 | 12 | 10 | 9 | 7 | **13** |
| ∞ | 4 | 4 | 4 | 5 | 7 | 7 | 3 |
| n/o | 3 | 3 | 3 | 4 | 3 | 4 | 3 |
| n/a | 1 | 1 | 1 | 1 | 1 | 2 | 1 |
| median keys over the n cells | 6 | 8 | 6 | — | 6 | 7.5 | 8 |

Gemini CLI and the Cursor agent are not in the matrix. Both need an account sign-in, so they're docs-only, and no
screen was recorded.

## Best idea per fact (described, not copied)

1. **Model:** a persistent footer slot, as nearly everyone has. ternly also shows the capability tier (T1) there.
2. **Why this model:** ternly's `/why` routing table (p(success), time, money, quota, score) is unique. No rival explains its choice.
3. **Cost:** the always-on cost slot should say when it can't price the model. CodeWhale shows "cost: unknown (billing basis unknown)", with an audit behind `/cost`. Claude Code instead reports $0.28 for a free local model.
4. **Quota:** Codex keeps a "Limits" row in `/status` even when no data exists yet. Keep the slot visible and state why it's empty.
5. **Context:** Pi's compact `3.6%/128k (auto)` at 0 keys, and a category breakdown one command away (Claude's `/context`, CodeWhale's drill-down).
6. **Tokens this turn:** no one shows it at 0 keys. CodeWhale's footer comes closest, with live latency (ttft, tok/s), and `/tokens` gives the per-turn telemetry.
7. **Files changed:** Crush's sidebar "Modified Files" list, always visible.
8. **Diff:** Claude's inline per-edit diff in the transcript, plus a `/diff` side panel with a per-file summary. OpenCode's `/diff` review view adds hunk navigation and "mark reviewed".
11. **Agents:** Claude's footer cue ("← for agents") and inline "waiting for 1 background agent". Codex's agent command center (`←` on an empty prompt) has the richest status, with All / Needs you / Working / Ready filters.
12. **Last error:** every harness shows it inline. Codex adds a footer counter (`⚠ 1 warning · f2 to view`) and a pager for recall.
13. **Verify status:** ternly's inline `● Verify …` line is unique.
14. **Session:** a title at 0 keys (Crush's sidebar, ternly's header). Claude's `/status` gives both name and id.
15. **Mode:** Claude's footer shows the state together with how to change it ("bypass permissions on (shift+tab to cycle)").
17. **Branch:** Pi's footer `cwd (master)`.
18. **Elapsed:** CodeWhale's footer "worked 1m 11s · ttft 4.9s · 28 avg tok/s".
19. **Undo:** ternly's `/rewind` turn list marks the turns that changed files (●). Codex reaches rewind from its transcript view in one chord.
20. **Workspace:** a footer or header path (Codex, Pi, OpenCode, ternly). A long path must not push out other slots: ternly's mode was cut off.

## Information-architecture notes per rival

- **Claude Code 2.1.296:** sparse footer (mode + agents cue); everything else is behind slash commands that open tabbed dialogs (`/status`, `/usage`, `/context`, `/model`, `/permissions`) or a side panel (`/diff`). The transcript itself carries diffs, timing and agent progress. The menu matches fuzzily (`/mode` opened the model picker). With a custom base URL it prices the local model at a frontier rate. Ctrl+G opens an external editor, which, with no `$EDITOR` set, took over the pane for the rest of the probe.
- **Codex 0.162.1:** a two-line footer (model, effort, cwd; mode only when non-default) and one dense `/status` card that answers 6 facts. `←` opens an agent command center; Ctrl+T opens a transcript browser with rewind; F2 opens the warnings pager, with a counter in the footer. There's no cost display. In the reach session it made no edit.
- **Crush v0.98.1:** a permanent right sidebar answers 10 facts at 0 keys: title, branch, cwd, model, context %, $, Modified Files, LSPs, MCPs, Skills. The command palette (`/` or Ctrl+P) is a filterable list, not a slash grammar. The sidebar costs about 30 columns at 120 cols. Ctrl+O opened nano, which took over the pane for the rest of the probe.
- **OpenCode v1.18.35:** a minimal footer (cwd, context tokens); the agent and model sit under the prompt. Dialogs for sessions, timeline, debug and models; `/diff` is a full review view. The branch shows only on the home screen. No cost at 120 cols.
- **Pi v1.1.0:** a two-line footer: cwd (branch); ↑↓ tokens, cache read, cache %, context %/window, model. There's almost no command surface for state; it's built for minimalism. There are no permission modes or subagents by design.
- **CodeWhale v0.10.1:** the densest footer (approval mode, work mode, elapsed, model, context %, cost-with-honesty, ttft, tok/s, cache %), plus a Ctrl+] work bar and many audit commands (`/cost`, `/tokens`, `/context`, `/tree`, `/agents`).
- **ternly (main 1b0085a):**
  - The pinned header shows cwd, session title, sandbox and mode; the footer shows model + tier, ctx used/window, ↑↓, cache and $.
  - Unique: `/why` and inline verify.
  - Gaps: no per-turn elapsed, no branch, no running-agent view.
  - The mode is truncated by a long path.
  - Esc and Ctrl+U don't clear typed input: Esc interrupts, Ctrl+U pages up. The generic menu probe therefore couldn't run, and ternly was probed with direct commands from its registry.

## Method caveats

- The first, generic probe typed `/name` without Enter and decided "listed" when the name appeared at least twice on screen. That heuristic gave false positives: Claude's "Unknown command: /jobs" and Pi's `/jobs`, which was sent to the model as a prompt. Its follow-up `q` keystroke also typed into some inputs. Every cell above was decided by reading the screens, not from `commands.txt`.
- Two probe commands changed state: `/plan` enabled plan mode in Claude Code and Codex. Later screens show that mode.
- For ternly, the first reach session ended mid-turn, and its menu probe typed into the input. Both were archived, not committed. The session in `results/ternly/` used `REACH_DIRECT` (each read-only registry command sent with Enter) and the chords C-o C-b C-k C-l.
- The model drove the mid-session state. Codex, Crush, OpenCode and CodeWhale made no edit in their reach turn, so their diff and files-changed cells show an empty but working view.
