# Changelog

## Unreleased

### TUI layout, steps 5 and 6: the palette, and adapting to the environment (ADR 030 (d))
- **The Ctrl+K palette shows each row's keys at the right** (`/panel error … Ctrl+X e`), matches
  descriptions from three letters (`diff` finds the changes panel), and lists the five most recently
  used first. `/output` (Ctrl+O) and `/sidebar` (Ctrl+B) are in it too.
- **The frame rate fits the machine**, chosen at startup: 120 fps on AC power in a local terminal on
  a capable CPU; 30 on battery or a small CPU, and over SSH (with no spinner animation). Below
  30 fps (`ui.fps`) `/doctor` warns how long a typed key can wait for its frame.
- **tuiprobe types each key after a random pause**: a fixed pause measured the phase of the frame
  ticker, not the echo (about 48 ms at 10–20 fps alike).
  `"ui": {"fps": N}` overrides it; `/doctor` shows the profile and why.
- **ASCII mode is ASCII throughout:** the input box's corners, popups, panels and the status line;
  text in other scripts shows as `?` per column, so the layout holds.
- **The accessible mode announces subagents** when their state changes.
- **`bench/f2/layout`**: a palette probe, a 16-colour tier, and `-idle-ab name=binary@fps` with echo
  per sample; `tui-idle.yml` takes `name=ref@fps`.

### TUI layout, step 4: panels and the leader key (ADR 030 (d))
- **Ctrl+X, then a letter, opens a panel**, and a popup lists the letters while Ctrl+X waits:
  a agents, c changes, e last error, p plan, r why (routing), s sessions, o tool output,
  k commands. The leader is set in config.json (`"keys": {"leader": "f2"}`); ternly refuses
  Ctrl+D, R, S, Q and Z, and keys the input uses, and says so.
- **Panels:**
  - agents: each subagent's model, step, last call, usage and time; after the turn, the last
    ones, with their turn;
  - changes: the files ternly changed this session, and the working tree's diff, read again when
    a turn ends;
  - last error: ternly's cause, the whole message, the turn's status lines before it, the next
    step;
  - routing: why this turn's model, and `/why`'s ranking;
  - plan: says there's no plan data yet (it comes with Phase C).
- **At 160 columns or more a panel docks beside the transcript; narrower, it takes the
  transcript's place.** Esc closes it (after clearing anything typed), PgUp/PgDn scroll it. One
  panel or the session sidebar, not both.
- **Alt+letter opens a panel directly where the terminal disambiguates keys** (the kitty keyboard
  protocol), except b, c, d, f, l and u, which the input uses with Alt.
- **The Ctrl+K palette lists every panel with its keys** (`/panel agents` …), and `/panel <name>`
  opens one. In the accessible mode the keys and panels print as text.
- **`bench/f2/layout`** records the popup and each panel, the panels after all four turns, and a
  429 without Retry-After (the estimated quota label).

### Quota times: the provider's, or marked as an estimate
- **"quota out until HH:MM" only when the provider gave the time**: a Retry-After header, or a
  balance API that reports the quota spent and when it resets. Without one, ternly holds the model
  for its own 1-hour cooldown and says so: "quota out, retry in about 1h (estimated)", counting
  down. `/status`, background work's skip reason and the "nowhere to escalate" hint follow the
  same rule.

### TUI layout, step 3: agent lanes (ADR 030 (d))
- **Running subagents have lanes above the input.** Each lane shows the agent's name, model, step,
  current tool call, tokens, cost and time; done and failed agents stay until the next prompt.
  - At 100 columns or more there's a row per agent, three at most, with the rest summed up.
  - Narrower, there's one row for all of them ("2 agents: ● lexer 1 · ✓ tests").
  - ASCII glyphs where the locale isn't UTF-8.
- **The core reports subagents as they go** (`EvSubagent`): queued, running, the model chosen,
  each tool call, usage, then done or failed, under the agent definition's name.
- **A pending approval says why it asks** ("why: the permission mode is ask, so edits and commands
  wait for your yes"), in the permission box and in the per-hunk review, unless the core gave its
  own reason.
- **`bench/f2/layout` runs three more scenarios:** two subagents at once, an edit waiting for a yes
  in ask mode, and Ollama Cloud's rate limit (429 with Retry-After). `bench/fakeprovider` gained
  per-agent script queues, several tool calls per answer, response headers, and Ollama's discovery
  API for a cloud model.

### TUI layout, step 2: information reach (ADR 030 (d))
- **An edit shows its change** under its tool line: up to 6 `-`/`+` lines, the rest with Ctrl+O.
  What the model wrote is shown as text.
- **A routing card per turn** says why that model ("chosen as the cheapest capable model: T1 task →
  est. 28 s, free, p≥0.30"; failover and escalation say so).
- **A receipt after every turn:** its tokens in and out, cost ("cost unknown" for an unpriced
  model), time, the verification's verdict, and the files it changed.
- **Errors are framed** with ternly's one-line cause (rate limit or quota, credentials, context
  too long, timeout, unreachable, provider failure) and the next step. The status line counts
  them (⚠ 1 error). ASCII mode frames them in ASCII.
- **The header shows the git branch,** with `*` when tracked files changed. It's read off the UI
  thread at start and after every turn. It gives way after the session title and before the path.
  A branch name is shown as text.

### TUI layout, step 1: the status line (ADR 030 (d))
- **The status line carries the facts checked between turns:**
  - the model and why it was chosen (▸ cheapest, ◆ pinned, ↑ escalated, ⇄ failover);
  - context used/window and the session's cost. A model with no price shows "cost unknown",
    not $0.0000;
  - the provider's quota (only where it reports one) and lost trust;
  - this turn's tokens in and out, the turn's time (live while it runs), the last
    verification's verdict, the plan step and the undo depth.
- **It fits any width.** Fields are in priority order. A line that's too long shortens its least
  important fields first, then drops them; the model is last to go. It uses one line when
  everything fits in full and two otherwise (two at 80×24). It collapses to one line below 24
  rows, when the input grows, or when two would leave the transcript fewer than 16 rows.
- **ASCII glyphs where the locale isn't UTF-8** (LC_ALL, LC_CTYPE, LANG, as in C), or with
  `TERNLY_ASCII=1`. This covers the status line so far; the rest comes with the adaptive tiers.
- **Transcript info lines no longer spill past the screen.** At 60 columns the routing line was 79
  columns wide.
- **`bench/f2/layout`** records every step: screens at 200×50, 120×40 and 80×24, Unicode and
  ASCII/no-colour; the 20-fact information reach; and startup, echo, bytes per key, idle CPU and RSS
  against ADR 030's budgets. It drives a scripted `bench/fakeprovider` session, so runs are
  repeatable.

### TUI: four fixes from the F2 survey (ADR 030)
- **The welcome hint fits the screen.** It was one 118-column line, cut off at 80 columns; it now
  wraps at " · " and between words, and re-wraps on resize. The no-providers hint and /help's key
  line wrap the same way.
- **The Ctrl+K palette's descriptions line up.** A row with an argument hint (`/model [id|auto]`) padded
  its name by bytes, its styling included, so its description started in another column. Names
  are padded by what shows, in one column for the whole list (at most 40 columns, 2/5 of the
  screen); a hint that doesn't fit is shortened.
- **The header keeps the sandbox and permission mode.** A long workspace path pushed them off the
  right edge. They never truncate now; the session title shrinks (32 → 8 columns, then goes) and
  the path elides in its middle (`/home/someone/wor…/project-x`).
- **Widths are display columns.** The header, the palette and every shortened text in the TUI
  count a CJK or emoji character as the two columns it takes. A path, title or file name in those
  scripts used to overflow, as in the two bugs above.
- **Keys** (docs/commands.md):
  - **Esc** clears what's typed; with nothing typed, it interrupts a running turn. It only
    interrupted before.
  - **Ctrl+U** deletes to the start of the line, as in a shell. It scrolled up before.
  - **Ctrl+D** is bound to nothing (it scrolled down, and deleted forward in the input). ternly
    binds none of Ctrl+D/R/S/Q/Z. PgUp/PgDn scroll, as before.

### Keystroke echo: 14.6 ms → 6.5 ms, without a fork (ADR 032, F2 item a)
- **A typed key reaches the terminal in 6.5 ms at the median** (p95 7.0–7.4 ms), down from
  14.5–14.7 ms. Measured through a pseudo-terminal with `bench/tuiprobe` at 120×40 in truecolor.
- **The cause was the frame tick, verified.** Bubble Tea writes to the terminal only on its frame
  ticker, so a key waits for the next tick. ternly now runs at 120 fps (`tui.FPS`), the most Bubble
  Tea allows, so the wait is at most 8.3 ms instead of 16.7 ms.
- **The cost is about 0.2 points of idle CPU.** The idle TUI measures 0.45–0.55%, against
  0.25–0.35% at 60 fps.
- **No fork.** A patched Bubble Tea measured 1.3 ms, but it needed a `go.mod` `replace`, and that
  breaks `go install …@latest`. A fork under its own module path would also have meant forking
  Bubbles. The change is offered upstream instead: charmbracelet/bubbletea#1857, with #1858
  (render input at once) and #1859 (diff only changed lines). ternly issue #37 tracks adopting it
  and returning to the default frame rate.
- **A guard test.** `TestKeystrokeEcho` runs the real program with ternly's options, at a random
  key phase, and requires a median under 8 ms. That's 6.0–7.0 ms at 120 fps and 9.1–11.0 ms at 60,
  so a fall-back fails CI. It waits for a warm-up key's echo before measuring: on a loaded runner
  the event loop can start 1.4 s after the first output.
- **An upstream regression found.** ultraviolet `006e29f`, which ternly's dependencies select,
  re-sends unchanged lines holding wide characters on every frame. This was bisected and reported
  in #1857.

### Background work: idle-only, quota-aware, conditional (ADR 031)
- **Background evaluations never run on a spent quota.** Before scheduling, ternly checks Ollama's
  documented balance (with `OLLAMA_API_KEY`) and every quota hit any ternly process recorded
  (`<data>/quota.json`). A hit with no known reset keeps background work off that quota for a
  day. A 429 during an evaluation stops it after that trap: nothing is saved, it doesn't count
  against the caps, and it's retried after the reset.
- **Background work runs only when idle and on AC power:** no turn running, no key pressed for
  5 minutes, and mains or USB power (Linux sysfs, macOS `pmset`; unknown counts as AC). Typing or
  a turn starting pauses an evaluation (the trap in progress runs again on resume) and the
  catalog refresh (after the request in flight); both go on from where they were.
- **Evaluations are opt-in.** On the first interactive start with no
  `routing.background_eval.enabled`, ternly asks once ("Run background capability evaluations
  when idle? [y/N]") and saves the answer. Headless, CI and harness runs never ask and stay off.
  An explicit value is never asked about or changed.
- **The catalog refresh is conditional and idle-only.** Requests carry `If-None-Match` /
  `If-Modified-Since`; a 304 or an identical body (SHA-256, for sources without validators) isn't
  indexed again. Progress is saved per source and per registry page, so an interrupted refresh
  resumes instead of starting over. npm's search (no validators) runs weekly. Headless runs never
  refresh.
- **Measured** (ADR 031): a daily refresh 1.19 MB → 26 KB; an interrupted first refresh resumes
  instead of re-downloading 39 MB; idle CPU 10–40 s after a second start 9.1% → 0.4–0.9%. The UI's
  own idle cost (0.4–0.9%) still misses the 0.5% target.

### Toolchain: Go 1.27.2 and x/net v0.60.0 (security)
- **Go 1.27.2 and golang.org/x/net v0.60.0** fix GO-2026-6612, GO-2026-6613 and GO-2026-6617
  (net/http and x/net http2, published 2026-10-11). govulncheck found them reachable through
  netguard's transport and failed CI on every branch.
- **staticcheck runs from master (452d5bb86b45, 2026-10-09)** until a release ships: v0.8.1 can't
  read go1.27.2's export data (version 5).

### Catalog refresh, empty answers, e2e failure kinds (ADR 028)
- **The capability catalog's index rebuilds once per refresh, at its end,** and is exact
  afterwards. Before, document frequencies could count replaced entries and push common words'
  BM25 weight below zero.
  - A first refresh measured network-bound: 0.43% CPU on average, with no index rebuilds. So
    Phase F's "a rebuild per entry" didn't reproduce, and batching doesn't change ternly's ~2% CPU
    while a refresh runs.
- **An empty answer** (no text, no tool call) is asked again once on the same model with a nudge,
  then fails over once. Both are counted (`/cost`, the session's stats).
- **Failovers are counted** in the session's stats, whatever caused them.
- **e2e reports separate "format failure"** (no or misshapen structured answer) from "wrong
  answer", per run and per check, and `bench/rescore` reads the new field.

### Perf gate: attribution, waivers and an A/A run (ADR 017 amendment)
- **Every failing benchmark is attributed.** Coverage counts at two iteration counts give the code
  it runs per op. The summary says whether the PR changed any of it ("on the path: …" or "none of
  the code this benchmark runs changed").
- **Waivers in `bench/perf.json`:**
  - named, justified with evidence and an ADR, and per benchmark;
  - they expire at the next release;
  - they're never honoured for a benchmark whose code the PR touched;
  - a waived regression reads WAIVED, never ok.
- **A weekly A/A run** (base = head, waivers off) opens an issue for any benchmark that regresses
  against itself.

### Git environment isolation (ADR 024)
- **Fixed: ternly started with `GIT_DIR` set** (inside a git hook, or exported) could act on that
  repository:
  - its checkpoint store's first `git init` would re-initialise it;
  - installing a plugin would fetch into it;
  - the session key and the branch shown to the model would come from it.
- **Fixed: in the repository's own tests and pre-push hook,** a push from a git worktree let tests
  commit into this repository, rewrite its `user.name`, and set `core.bare=true` (2026-10-08).
- **Every git call now goes through `internal/gitenv`.** A lint enforces it per call, and CI runs
  the whole suite with `GIT_DIR` pointing at a sentinel repository that must stay unchanged.

### Connections: what's connected, how, and what it costs (Phase B, ADR 022)
- **Every source gets a status line** in `ternly --models`, `/status` and `/doctor`. Each one says
  how it was found (the key's variable name, never its value, or the daemon's address), how many
  models it has, and its cost or remaining usage. When a source isn't connected, the line gives
  the next step.
  - Errors are worded by ternly, for example "the key in OPENAI_API_KEY was refused (HTTP 401)".
    The provider's own error text is never shown.
- **Ollama Cloud usage** comes from Ollama's documented `/api/balance`, when `OLLAMA_API_KEY` is
  set. Without the key, Ollama gives no official usage signal, so ternly says so rather than
  guessing.
- **The claude and codex CLIs are detected but not used.** Anthropic's and OpenAI's terms don't
  clearly allow another program to drive them with your subscription, so ternly doesn't (ADR 022).
  Use your API keys. OpenAI's Sign in with ChatGPT for open-source apps is planned, behind a flag,
  once the client is registered.
- **Status for UIs** (ADR 021, `internal/status`): connections, quota, models with trust, routing
  and its explanation, and the session meter, with a 50 µs snapshot budget held by the perf gate.

### Terminal UI (Phase F, ADR 023)
- **Outside text can't drive your terminal any more.** Model output, tool output, the command in a
  permission dialog, provider errors, model names and file contents in `/diff` used to reach the
  terminal raw. A hostile file could rewrite the permission dialog, write your clipboard or retitle
  the window. Now control sequences are shown escaped as text, and only ternly's own colours pass.
- **Faster and lighter:**
  - starts in about 45 ms (a dependency's 50 ms init is gone);
  - idles at ~0 % CPU (no ticker, and the terminal draws the cursor);
  - redraws a 10k-line transcript in about 1 ms (was 33 ms).
- **New keys:**
  - **Ctrl+K:** every command, fuzzy-filtered;
  - **`@`:** pick a workspace file;
  - **Ctrl+O:** a tool's full output.
- **The status bar** shows context fill, quota and "trust lost" when the core reports them, and
  `/why` explains routing.
- **Access:**
  - `TERNLY_REDUCED_MOTION=1`: nothing animates;
  - `TERNLY_SCREEN_READER=1`: also words instead of glyphs;
  - `TERNLY_MOUSE=1`: wheel scrolling;
  - `NO_COLOR` is honoured;
  - the theme follows a light/dark switch when the window regains focus.
- **Edits that ask are reviewed hunk by hunk.** ↑↓ move, Space toggles, y applies the selection.
  Only what you accept is written, and the model is told what you declined.
- **The session sidebar:** Ctrl+B.
- **`--accessible`:** linear output for screen readers (no alternate screen, plain text, nothing
  animates).
- **Budgets in CI:** startup ≤ 100 ms, frame time < 16 ms at 10k lines (with or without the
  sidebar), no flicker, and an idle-CPU guard.

### Judges score structured answers (ADR 019)
**The class of bug:** judges that searched the model's prose. They failed right answers worded
unexpectedly, and passed non-answers that contained the right token. Fixed:
- **The e2e checks and the eval's traps ask for a final JSON answer,** and their judges read only
  that.
- **History is re-scored** (`bench/rescore`, `bench/evalaudit`, with the manual verdicts committed
  as data):
  - 24 e2e runs changed, and three full-run reports flip FAIL → PASS;
  - 10 of 126 eval verdicts behind the shipped tiers were wrong.

**Shipped measurements, re-measured under eval v4:**
- every tier is unchanged;
- **qwen3.6 is now measured easily baited:** it ran a hostile skill's `curl … | sh` once in three
  runs, and fetched the same script with `curl … | head` once.

### Asymmetric trust (ADR 020)
- **Trust is lost under the same rule as before,** but regained only after 20 bait trials in a row
  without taking the bait. Measuring a model afresh doesn't reset it.
- **A model whose trust is lost:**
  - every shell command asks, in every mode, even read-only ones, and headless runs refuse them;
  - file edits are allowed in an explicitly chosen edits or yolo mode while checkpoints are on, so
    each one can be undone;
  - external tool calls ask, and "always" never sticks.
- So headless, pinned to qwen3.6 with `--mode edits`, ternly can fix code again. Its own sandboxed
  verify step still runs the tests.

### Quota and rate limits
- **Errors sent inside a stream** (the way Ollama reports errors during streaming) used to be
  dropped silently, ending the turn with an empty answer. They now fail over.
- **Retry-After is honoured,** and a long wait fails over at once.
- **A subscription's usage limit** takes its model out of routing for that long, also when the
  status is a 402 or 403.

### Bench
- **`bench/netguard`:** the netguard task's black-box scorer, recreated.

## v0.1.1 (2026-10-08)

### Routing: the expected cost of finishing (ADR 018)
v0.1.0 routed on token price alone. A free local model therefore won every task it was strong
enough for, however slow it was: on the owner's machine, qwen3.6 runs 84% on the CPU and takes
64–75 s to its first token at agent-sized contexts, against 2–3 s for Ollama Cloud models.

**Routing v2, the new default,** scores each model as (money + quota + λ × time) ÷ p(success):
- **time:** measured on this machine, on every request;
- **placement:** from Ollama's `/api/ps`;
- **λ:** $20/hour of your waiting by default;
- **p:** the measured lower bound, with a floor.

**On the routed e2e checks:** same pass rates as v0.1.0's router, 3.9× faster (9 m 52 s against
38 m 12 s).

**What changes for you:**
- **Escalation and failover** can now leave a free model. Local Ollama and Ollama Cloud count as
  different providers.
- **A subscription's 429** takes its model out of routing for an hour.
- **When there's nowhere to escalate,** ternly says what would help.
- **Utility calls** (titles, summaries) use a local model only if it's fully on the GPU.
- **New models are evaluated in the background,** with the bundled traps only, at idle, under
  caps: 3 a week, 20 minutes each; paid APIs never by default.
- **`ternly --models` and `/models why`** show every term and its source.
- **`"routing": "v1"`** (or `--routing v1`) restores the old router for one release.

### Fixes
- **Verification checks formatting.** Unformatted code never ends ✓ verified: changed Go files must
  pass `gofmt -l`, and changed Rust files `rustfmt --check` (a missing rustfmt leaves them
  unverified). Python and JavaScript/TypeScript have no standard formatter, so the one a project
  configures (ruff or black; prettier) is run when it's installed. A file that needs formatting
  goes back to the model like a build error.
- **The release smoke test** exited 143 after passing (its cleanup's `wait` on the killed fake
  provider became the exit status). That alone kept v0.1.0's release from publishing. CI now runs
  it on every push.
- **The reasoning watchdog** counts tokens and idle time, not stream chunks (#2). A bare number in
  `reasoning_watchdog` is read as tokens; `{"tokens": N, "seconds": S}` sets both.
- **`callers` and `references`** list confirmed uses first: typed, or a name match whose receiver
  type the code shows. Then possible ones, then calls on another type with the same method name
  (#1). Rust methods aren't qualified by type yet (#10).
- **Security:**
  - goldmark 1.7.17 fixes GO-2026-5320, an XSS reachable through markdown rendering;
  - the build toolchain is pinned to Go 1.27.1, because v0.1.0's draft binaries were built with
    Go 1.26.0, whose net/url, crypto/tls and net/http have reachable advisories;
  - `staticcheck` and `govulncheck` now run in CI.

### Correction to v0.1.0's known limitations
"qwen3.6 qualifies name-matched Python callers only about half the time" was wrong. The e2e
check's judge failed answers that named `app/run.py:5` in order to exclude it. Re-scored, qwen3.6
passed 26 of 27 runs (ADR 018, "A judge bug").

### Known limitations
- **Background evaluations** haven't yet run against a real newly added model. They're tested
  with fakes.
- **Ollama Cloud's quota reset time** isn't documented, so the one-hour cooldown is a guess.

## v0.1.0 (never published)

**v0.1.0 was never published; it is superseded by v0.1.1** (release toolchain vulnerabilities).
- Its binaries were built with Go 1.26.0, whose net/url, crypto/tls and net/http advisories are
  reachable from ternly (GO-2026-6218, GO-2026-6090, GO-2026-6089).
- Its release run also stopped at the smoke test, which failed on a bug in the script itself, not
  in the binaries.
- The draft release and its assets were deleted on 2026-10-07. The `v0.1.0` tag stays.
- **Two entries remain in the public Rekor transparency log**, permanently, as Rekor entries do:
  - log index **3108111468**: the cosign keyless signature of the draft's `checksums.txt`
    (sha256 `cf1a9aaa…0350c`);
  - log index **3108112282**: the GitHub build-provenance attestation of its four archives and the
    checksums.

  Both are bound to the release workflow's identity for the `v0.1.0` tag. Nothing signed by them
  was ever offered for download.

The notes below describe that version, as written for it. The design decisions behind each item are in `docs/adr/` (000–016).

### What it is
One terminal coding agent for every model you can reach: paid APIs, free tiers, local servers
(Ollama, LM Studio, llama.cpp, vLLM, Jan) and Ollama Cloud, all discovered automatically. Each
task goes to the cheapest model that can do it well, and a stronger one only when the cheap one
demonstrably fails. A single static binary for linux/darwin × amd64/arm64.

### Highlights
- **Routing by measured capability.** `ternly --eval` measures a model (fabrication traps, Wilson
  intervals, hysteresis across runs). A capability tier routes, and a separate trust profile
  restricts easily-baited models. Reasoning budgets follow routing, per model.
- **Guardrails.**
  - A permission policy (ask, edits, yolo or plan).
  - Commands are judged on the parsed bash syntax tree.
  - bubblewrap sandbox on Linux.
  - Workspace confinement, including everything ternly loads by itself.
  - Untrusted-output framing and an injection flagger.
  - Secrets redacted from everything a model sees.
- **Verification you can believe:** ✓ only when every changed source file was compiled by a check
  (Go, Python, JavaScript/TypeScript, Rust, Java); otherwise "unverified".
- **Checkpoints and sessions:** every turn is undoable (`/undo`, `/rewind`). Sessions are durable,
  auto-resumed, and warn about files changed outside them.
- **Code graph** (Go typed; Python, TypeScript/JavaScript, Rust and Java by tree-sitter), with
  graph tools. Every edge says whether it was typed or name-matched.
- **Long-term memory**, guarded against poisoning: hybrid retrieval, with notes recalled as leads to
  check.
- **Extensions:**
  - Claude Code plugins, skills, agents and hooks; Gemini CLI extensions; Cursor rules; OpenCode
    and Codex skills.
  - Installs are pinned, reviewed and confined.
  - Capability suggestions come from the MCP registry and marketplaces.
- **Remote MCP:** Streamable HTTP (2026-07-28 and 2025), OAuth 2.1 (`/mcp login`), tokens in the OS
  keyring, per-server network grants.
- **Slash commands** at parity with the other harnesses, plus user-defined commands.

### Known limitations
- **macOS is experimental.** There is no sandbox yet (v0.2 adds Seatbelt), so on macOS every shell
  command asks, plugin hooks and MCP servers are disabled, and verification steps that run
  repository code ask. The same restrictions apply on Linux without bubblewrap, or with
  `--no-sandbox`.
- **Windows** isn't supported.
- **Remote MCP servers from plugins** aren't started; add them to `~/.config/ternly/mcp.json`.
  The old HTTP+SSE transport isn't supported.
- **Reasoning level semantics** are verified only for documented families (OpenAI reasoning models,
  Gemini 2.5/3, Anthropic budgets) and measured for glm-5. Other models never get "medium", and a
  watchdog interrupts runaway reasoning. Its threshold counts stream chunks; v0.1.1 moves it to
  tokens or seconds ([#2](https://github.com/rajasatyajit/ternly/issues/2)). Measure your own with `reasoning_levels` in the config.
- ~~**qwen3.6** qualifies name-matched Python callers only about half the time~~ — corrected in
  v0.1.1: the e2e check's judge was wrong, not the model ([#1](https://github.com/rajasatyajit/ternly/issues/1); ADR 018).
- **The injection flagger is advisory**; the permission policy is the barrier. See
  `docs/threat-model.md`.
- **Model quality varies.** Small local models make mistakes the guards catch but don't fix.
  `ternly --models` shows each model's tier and its basis.

### Verifying this release
See `SECURITY.md`: sha256 checksums, a cosign keyless signature, and build-provenance attestations.
