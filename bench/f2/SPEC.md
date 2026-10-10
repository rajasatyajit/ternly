# Phase F2 survey: the shared specification (2026-10-10)

Both survey agents and the rubric follow this file. Changing it is a decision recorded in ADR 030.

## Harnesses (each verified against its registry on 2026-10-10)
| Harness | Version to measure | Source | Runs the shared model? |
|---|---|---|---|
| ternly | main 1b0085a (static build) | this repo | yes (Ollama) |
| Claude Code | 2.1.296 (current; installed) | npm @anthropic-ai/claude-code | yes: `ANTHROPIC_BASE_URL=http://127.0.0.1:11434`, dummy `ANTHROPIC_AUTH_TOKEN`, model `gemma4:latest` |
| Codex CLI | 0.162.1 (installed copy 0.157.1 is stale) | npm @openai/codex | yes: custom provider, `base_url = "http://127.0.0.1:11434/v1"`, `wire_api = "responses"` (or `chat`) |
| Gemini CLI | 0.63.0 (not installed) | npm @google/gemini-cli | **no**: Google API/auth only. Measure the UI up to the auth prompt and score the rest from dated docs, marked second-hand |
| Crush | v0.98.1 (installed copy v0.97.1 is stale) | github charmbracelet/crush release | yes: OpenAI-compatible provider |
| OpenCode | v1.18.35 stable (the installed `opencode-beta` 2.0.22 is the beta channel; measure stable, note beta) | github anomalyco/opencode / npm opencode-ai | yes: OpenAI-compatible provider |
| Pi | pi-mono v1.1.0 (2026-10-07) | github badlogic/pi-mono | yes: OpenAI-compatible provider |
| CodeWhale (ex DeepSeek-TUI) | v0.10.1 (2026-10-08) | github Hmbown/DeepSeek-TUI / npm codewhale | if it accepts an OpenAI-compatible base URL; else docs only |
| Cursor agent | — | cursor.com/install (curl \| bash), account sign-in | **docs only** (closed, needs an account) |

**Excluded as unverifiable (2026-10-10):** Antigravity CLI and Muse Code, which have no registry
entry. Continue's `cn` is stale: its last release was June 2026.

**Installs:**
- Use the latest versions in a temporary prefix (`npm install --prefix <tmp>` or the release
  tarball). Nothing goes system-wide, and nothing is installed with sudo.
- Verify a release's checksum where one is published.

**Isolation:**
- Each harness runs with `HOME` and `XDG_*` pointing at a fresh temp directory.
- Its folder-trust prompts, onboarding and config writes stay there, and nothing touches the
  owner's real configs.
- No subscription sign-in and no paid API key. Everything uses the local Ollama.

**The shared model:** `gemma4:latest` (Ollama 0.35.1 on 127.0.0.1:11434). It serves
`/v1/messages` (Anthropic), `/v1/chat/completions` and `/v1/responses` (OpenAI). gemma4 is fast
here (49 tokens/s warm, 4.7 s for a turn with a 3.8k-token prompt). Record the model and its
digest (dc35e8d9c606) with every result.

**Timing:** every run whose numbers are reported takes the shared lock:
`flock /home/satyajit/.claude/jobs/f10e4ff6/tmp/heavy.lock …`.

**No Python anywhere.** Tools are Go (or C/Rust); shell only as glue. This is the owner's rule.

## The 5 scenarios (identical prompts and fixtures in every harness)
Fixtures are copies of the Phase C suite's pinned repositories (bench/suite), reset before every
run.

| | Scenario | Fixture | Prompt | Exercises |
|---|---|---|---|---|
| S1 | explain | golang-lru | "Explain what simplelru/lru.go does and how eviction works." | streaming text, read tools |
| S2 | fix a failing test | golang-lru, lru-resize task | the task's prompt | tool calls, edits, a test run, diff display, verify status |
| S3 | plan and parallel work | mitt (TS) | "Plan, then implement once() and emit-count tracking; use parallel sub-agents if you can." | plan display, sub-agent lanes |
| S4 | review | the Phase F review fixture (two seeded bugs) | "Review this change and list the bugs with file:line." | long structured output, diffs |
| S5 | long session and failure | any | 30 turns of scripted follow-ups (a long transcript), then a turn that errors (stop Ollama mid-turn, or point at a missing model) | scrollback, memory growth, the last-error display, recovery |

Recordings are a timeline of screen snapshots (a terminal emulator in the probe, or
`tmux capture-pane` at fixed intervals) plus the raw output bytes, saved per harness and scenario.

## The 20 key facts (information reach)
For each fact: the minimum keystrokes from the idle prompt, mid-session, to see it. 0 means always
visible. "∞" means it is not available in the UI. Record the keys, with a screen as evidence.

1. current model
2. why this model (routing or selection reason)
3. cost so far (session)
4. quota or usage remaining
5. context used / window
6. tokens this turn
7. files changed this session
8. the diff of a change
9. current plan step
10. the whole plan
11. running agents and their status
12. last error
13. verify / test status of the last change
14. session name / id
15. permission mode
16. pending approval (what, and why it asks)
17. git branch / dirty state
18. elapsed time / latency of the turn
19. undo / checkpoint availability
20. workspace / working directory

## Performance tiers (the probe measures each, for every harness)
| Axis | Tiers |
|---|---|
| colour | truecolor (`COLORTERM=truecolor`, `TERM=xterm-256color`); 256 (`TERM=xterm-256color`, no COLORTERM); 16 (`TERM=xterm`); none (`NO_COLOR=1`, and `TERM=dumb` where it starts) |
| glyphs | Unicode (`LANG=C.UTF-8`) vs ASCII (`LANG=C`) |
| size | 80×24, 120×40, 250×70; plus a live resize from 120×40 to 80×24 (does it reflow?) |

Metrics, each taken over N runs with medians:
- startup to first stable paint (ms);
- keystroke to echo (ms);
- bytes per keystroke frame;
- bytes per frame while streaming, and total bytes for S1. This is the SSH payload: count the pty
  output bytes, compressed and uncompressed (zstd/gzip as a proxy for ssh -C);
- frames per second while streaming;
- idle CPU % over 30 s (process tree);
- RSS after S5;
- whether it uses synchronized output (DECSET 2026), the alternate screen, a cursor-addressed
  diff or full repaints.
