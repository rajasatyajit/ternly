# Changelog

## v0.1.1 (unreleased)

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

## v0.1.0 (unreleased)

The first public release. The design decisions behind each item are in `docs/adr/` (000–016).

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
