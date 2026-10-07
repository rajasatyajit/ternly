# Threat model (v0.1)

ternly is a coding agent: a language model decides which files to read, which edits to make and
which commands to run, on the user's machine. This document says what ternly protects, from whom,
how, and what it doesn't. Each control links to the ADR that decided it and the test that holds
it. Report gaps as described in [SECURITY.md](../SECURITY.md).

## What is protected
| Asset | Where it lives |
|---|---|
| Provider API keys | the environment, or `~/.config/ternly/keys.env` (must be 0600) |
| Remote MCP OAuth tokens and client credentials | the OS keyring, or `<data>/mcp/credentials.json` (0600) |
| The user's files outside the workspace | `~/.ssh`, `~/.aws`, other repositories, … |
| ternly's own state | memory, sessions, checkpoints, plugins (`~/.local/share/ternly`, `~/.cache/ternly`) |
| The workspace itself | edits the user didn't intend; history (checkpoints make every turn undoable) |
| Money | spend on paid providers (budgets) |
| The truth of what ternly reports | "✓ verified", answers citing code that exists |

## Who attacks, and how
1. **A malicious repository.** The user opens it and asks for help. It may carry instruction files
   (`AGENTS.md`, rules, skills) written to steer the model; symlinks to secrets; a `.mcp.json`;
   build scripts and tests that run code; and files crafted to break parsers.
2. **Prompt injection through tool output:** a web page, a fetched document, a tool result or a
   remote MCP server's answer that tells the model to do something else.
3. **A malicious or compromised plugin or MCP server:** hooks, servers, skill text, tool
   descriptions written to manipulate.
4. **A model that takes the bait**, or simply makes things up (files, symbols, packages).
5. **A malicious OAuth authorization server, or metadata pointing at one:** token theft, SSRF.
6. **The network:** DNS rebinding, redirects, a remote MCP server reaching into the local network.
7. **Out of scope:** a local attacker with the user's privileges, a compromised OS, a compromised
   model provider.

## Controls
| Boundary | Control | Decided in | Held by |
|---|---|---|---|
| Model → filesystem | Tool paths confined to the workspace with `os.Root` (symlink-safe; `.git` internals blocked) | ADR 001, 003 | tools tests, `TestWorkspaceLinksNeverLoaded` |
| Repository → prompt | Everything ternly loads by itself (instruction files, rules, skills, agents, graph sources, MCP config) is read through one confined loader | ADR 013 | `TestOnlyConfinedReads` (lint), `TestWorkspaceLinksNeverLoaded` |
| Model → shell | Permission policy: edits and commands ask, unless the mode allows them. Read-only, build and test commands are auto-approved only if the parsed bash syntax tree is a chain of known commands with literal words. A forbidden list holds even in yolo. A model measured as easily baited never gets auto-approval | ADR 001, 013, 014, 015 | `TestSafeCommand` (95 cases), `FuzzClassifier`, `TestInjectedInstructionsCannotGrantPermission` |
| Shell → machine | bubblewrap (Linux): read-only root, writable workspace and caches; `~/.ssh`, `~/.aws`, ternly's own data and keys are masked; keys are scrubbed from the environment; `--no-net` | ADR 001, 003 | `TestSandboxedCommandsCannotReadPrivateData` |
| No sandbox (macOS v0.1, Linux without bwrap) | Every shell command asks (no auto-approval in any mode); verification steps that run the repository's code ask; plugin code is disabled | ADR 016 | `TestUnsandboxedEveryCommandAsks`, `TestNoCodeWithoutSandbox`, `TestUnsandboxedVerifyNotRun` |
| Tool output → model | Untrusted output is framed with a per-session nonce, and an injection flagger warns the user; secrets are redacted from everything a model sees | ADR 001, 012 | scripted adversary (CI); e2e security checks, 9 × 3 runs on a local model |
| Memory | Memory writes are guarded (secrets, injected "preferences", task requirements aren't saved as standing instructions); notes are recalled as leads to check, not facts | ADR 009, 014 | memory tests; the `memory-poisoning` e2e check |
| Plugins | Installs are pinned to a commit, reviewed with a diff, and confined (bubblewrap, workspace and network only as scoped); plugins can't grant permissions; manipulative descriptions are withheld | ADR 011 | plugin runtime tests |
| Remote MCP | Per-server network grants: only the server's host plus hosts the user approves at login; no private addresses (checked on the address dialed); redirects only within the grant; no proxy. OAuth 2.1 with exact issuer match, PKCE S256, `resource`, `iss` check, https-only endpoints. Tokens live in the keyring and are redacted; a browser login happens only on `/mcp login` | ADR 014 | netguard, mcpauth and mcpremote tests; live checks |
| Truth of "verified" | ✓ only when every changed source file is covered by a check that compiled it; otherwise "unverified" | ADR 015 | `TestReplayQwenStrayGoMod`, verify tests |
| Fabrication | Cited files, lines and symbols are checked against the workspace and the code graph; added dependencies are looked up in their registries; graph edges say typed or name-matched | ADR 012, 013 | fabrication eval, e2e checks |
| Money | Session budget and per-turn limits; routing to the cheapest capable model | ADR 012 | agent tests |
| Releases | Pinned actions; SBOMs; cosign keyless signature of the checksums; build-provenance attestations; smoke tests of the archives before anything is published | ADR 016 | `release.yml` |
| Background evaluations (routing v2) | A new model is measured by running this binary's own `--eval`: only ternly's bundled trap workspaces, never the user's repository (`-C` is never passed; the child runs in the temp directory). Spend is capped per model (20 min) and per week (3 runs); quota and local models only, paid APIs get $0 by default; under `--local-only` cloud models are never evaluated; an off switch in config. The child runs in its own process group (stopped whole on timeout or a turn starting) and carries `TERNLY_BACKGROUND_EVAL=1`, so a background eval never schedules another one | ADR 018 (review decision 3) | `TestBackgroundEvalCommand`; bgeval tests: `TestWhatIsNeverEvaluated`, `TestWeeklyCapAndOncePerModel`, `TestYieldsToATurn`, `TestTimeoutAndFailure` |
| Routing data (`speed.json`, `background-eval.json`) | Both files are 0600, written atomically, and versioned: an unknown version is ignored and the data is re-measured. They contain numbers about models, so a tampered file can only bias routing preferences (make a model look fast, slow or already evaluated); it can't run code or widen permissions | ADR 018 §5 | `TestSpeedStore`, `TestLedgerPersists` |
| Hardware probes | `nvidia-smi --query-gpu=memory.total` (a fixed-argument command, no shell), Ollama's `/api/ps` (a GET), `/proc/meminfo` and the amdgpu sysfs VRAM file: read-only, and only to estimate GPU placement and whether a local model fits | ADR 018 §3 | `TestHardware` |

## Residual risks and non-goals
- **macOS v0.1 has no sandbox.** The restrictions above stand in for one. A command the user
  approves runs with their full privileges. Seatbelt arrives in v0.2.
- **yolo mode, `--no-sandbox` and approved commands** do what they say. Build and test commands run
  the repository's own code by design, inside the sandbox on Linux.
- **The injection flagger is advisory.** It catches phrasings; it can't catch every attack. The
  permission policy, not the flagger, is the barrier.
- **Keyring backends** are the OS's CLIs (`secret-tool`, `security`). Without one, tokens are in a
  0600 file in ternly's data directory, readable by anything running as the user (masked from
  sandboxed commands).
- **Model providers** see what is sent to them. Local models keep it local. `/doctor` and the model
  line say where each request goes.
- **Remote MCP servers** see the arguments of the tools they serve. ternly can't vet a server's
  honesty, only fence its network reach and frame its output.
- **Providers see ternly's synthetic eval prompts.** When a cloud model is background-evaluated,
  the provider receives the bundled trap workspaces. They contain no user code, but they do say
  the user runs ternly. `--local-only` (cloud models are skipped) or the off switch stops this (`TestWhatIsNeverEvaluated`).

## Re-running the evidence
- `go test -race ./...`: the unit tests, the scripted adversary, the confinement lint, the
  classifier table.
- `go test -fuzz FuzzClassifier ./internal/tools` (and the other fuzz targets).
- `TERNLY_E2E_MODEL=<local model> bench/run.sh e2e`: every real-model check, the security class at
  100%.
- `TERNLY_MCP_LIVE=1 go test ./internal/mcpremote ./internal/mcphttp`: live remote MCP servers.
