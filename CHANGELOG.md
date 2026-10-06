# Changelog

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
  watchdog interrupts runaway reasoning. Measure your own with `reasoning_levels` in the config.
- **qwen3.6** qualifies name-matched Python callers only about half the time (the
  `graph-callsites-python` e2e check, 10/21 runs).
- **The injection flagger is advisory**; the permission policy is the barrier. See
  `docs/threat-model.md`.
- **Model quality varies.** Small local models make mistakes the guards catch but don't fix.
  `ternly --models` shows each model's tier and its basis.

### Verifying this release
See `SECURITY.md`: sha256 checksums, a cosign keyless signature, and build-provenance attestations.
