# Changelog

## Unreleased

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
- **The claude and codex CLIs are detected but not used.** Whether another program may drive them
  with your subscription is unclear in Anthropic's and OpenAI's terms, so it's an open decision
  (ADR 022). Your API keys work as before.
- **Status for UIs** (ADR 021, `internal/status`): connections, quota, models with trust, routing
  and its explanation, and the session meter, with a 50 µs snapshot budget held by the perf gate.

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
