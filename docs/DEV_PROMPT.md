# Mission

You are the lead engineer on **ternly** (this repository; formerly named *vane*): a single-binary Go coding-agent harness that
auto-discovers paid, free and local LLMs and routes each task to the cheapest model capable of it.
Extend it to meet the eight requirements below. Read the whole codebase first
(`internal/llm`, `discover`, `tools`, `agent`, `tui`, `main.go`) and preserve what already works:
cost-aware routing, failover/escalation, prompt caching, the bubblewrap sandbox, path confinement,
permission policy, secret redaction, auto-verification, and the static `CGO_ENABLED=0` build.

<working_rules>
- Ground every claim in something you read or ran this session. When unsure whether an API, flag,
  library version, file format or command exists, check it (docs, registry, source, `go doc`, a quick
  experiment) before relying on it. Say "unverified" instead of guessing.
- Plan before coding. For each milestone write a short design note in `docs/adr/NNN-*.md`: the problem,
  options considered, decision, trade-offs, and how it will be measured. Then implement.
- Every milestone ends green: `gofmt`, `go vet ./...`, `go test -race ./...`, a static build, and a
  benchmark or end-to-end check that proves the milestone's claim with numbers. Report the actual
  numbers. Never write "faster", "secure" or "complete" without evidence.
- Keep diffs focused and match the existing style. Prefer the standard library; add a dependency only
  when it clearly beats writing it, it is pure Go (no cgo), and it is maintained. Record why in the ADR.
- When a requirement is impossible as literally stated, say so plainly, explain why, and deliver the
  strongest achievable version. Do not pretend.
</working_rules>

# Requirements

## 0. Name: ternly (formerly vane) — do this first
The project has been renamed from **vane** to **ternly**. Before any other work, confirm the rename
is complete and consistent. Run `grep -rIni vane .` and fix every remaining hit, except the
deliberate legacy-migration code. Everything below must use the new name:

| Item | Name |
|---|---|
| Go module path | `github.com/rajasatyajit/ternly` |
| Binary | `ternly` |
| Config dir | `~/.config/ternly` |
| Cache dir | `~/.cache/ternly`, including the graph cache paths below |
| Theme env var | `TERNLY_THEME` |
| Env var set inside the sandbox | `TERNLY=1` |
| Temp-file prefix | `.ternly-*` |
| Project instruction file | `TERNLY.md` (alongside `AGENTS.md` / `CLAUDE.md`) |
| OpenRouter `X-Title` / `HTTP-Referer` | `ternly` / `https://github.com/rajasatyajit/ternly` |
| Name in docs, README, examples, system prompt, TUI and help text | ternly |

- **Brand:** the name refers to the Arctic tern (long-distance, energy-efficient migration) and to
  the Latin *terni*, "three each" (the three routing tiers). The TUI logo/wordmark and README may use
  the tagline "Code, ternly." Keep the ◆ wordmark style unless you propose a better one in an ADR.
- **Legacy migration:** keep the one-time migration of `~/.config/vane` → `~/.config/ternly` and
  `~/.cache/vane` → `~/.cache/ternly`. It must never overwrite an existing ternly directory. Add a
  test for it.
- **Release metadata:** add release metadata under the new name: goreleaser config producing static
  linux/darwin amd64+arm64 binaries, plus an AUR `PKGBUILD` for `ternly-bin`.

Gate before M1: build, vet and race tests green, with zero unintended `vane` references.

## 1. Language policy for code ternly writes
ternly's own code stays in Go. For code ternly generates for users, encode this policy in its system
prompt and routing:
- **Existing project:** use the project's existing language and conventions. Never rewrite a
  codebase into another language unless asked.
- **New code, no language specified:** default to Go. Choose Rust or C when the workload genuinely
  needs it (no-GC latency, SIMD/embedded/FFI, memory layout control), and state the reason in one line.
- **Python or other slower runtimes:** only when the user explicitly asks, or when the target
  ecosystem requires it (e.g. a Python-only ML library). Even then, say so.
- **Concurrency where it helps:** use concurrency and parallelism only where the workload benefits
  (I/O fan-out, independent CPU-bound work). Bound it with worker pools, contexts and cancellation.
  Don't add goroutines that only add overhead.

## 2. Thorough but minimal code
Teach the agent, through its system prompt and through `/review` checks, to produce the smallest
complete implementation. That means:
- Handle every stated requirement and the real edge cases: errors, empty input, cancellation,
  concurrency safety.
- No speculative abstractions, unused options, dead code, TODO stubs or duplicate helpers.
- Reuse existing project code before writing new code.
- Prefer `edit_file` diffs over rewrites.

## 3. Guardrails for the LLMs
Harden the harness against model failure and adversarial input:
- **Prompt-injection defence:** tool outputs, file contents, web and MCP results are untrusted data.
  Wrap them in clearly delimited blocks. Tell the model they are never instructions, and keep the
  permission system authoritative regardless of what text says.
- **Tool argument validation:** validate arguments against each tool's JSON schema before execution.
  Return precise errors the model can act on.
- **Loop protection:** detect repeated identical tool calls or no-progress loops. Enforce
  per-turn step, time and spend limits, plus a session budget. Degrade gracefully when a limit hits.
- **Destructive operations:** git checkpoints before edits, with `/undo` and `/rewind`. Keep the
  existing confirmation and forbidden-command layers.
- **Output checks:** reject malformed tool calls with a corrective message. Detect claims of success
  not backed by a passing verification run, and challenge them.
- **Hostile-input tests:** injected instructions in files, symlink escapes, oversized outputs and
  malformed tool calls.

## 4. Built-in, model-independent memory
Build a memory subsystem that works identically whatever model is active.

**Be precise about the goal.** Nothing beats tokens already in the context window for the model's own
recall. The real goal is that storing and retrieving memory is far cheaper and faster than the
alternatives: re-reading files, re-sending history, or asking the model to re-derive facts. Design
for:
- **Tiers:**
  - working context: the current turn
  - session memory: summaries, decisions, files touched
  - project memory: durable facts, conventions, architecture, past failures and fixes
  - user memory: preferences across projects
- **Store:** an embedded, pure-Go, crash-safe store. Benchmark candidates (e.g. bbolt, Pebble, a
  custom append-only log with an index) and pick by measured p50/p99 read and write latency.
  Targets: sub-millisecond point reads, single-digit-millisecond ranked retrieval on ~100k items.
- **Retrieval:** hybrid ranking — lexical (BM25 / inverted index), structural (code-graph
  neighbourhood, see requirement 5) and recency. Use optional vector similarity only when a local
  embedding model is discovered. It must work fully without embeddings.
- **Writes:** writes happen at turn boundaries (decisions, verified fixes, user corrections) and must
  never block the UI. Deduplicate, version and expire entries. Record provenance (session, file,
  commit) on every entry.
- **Retrieval budget:** retrieve under a token budget, and inject only what the current task needs.
  Measure and report tokens saved versus re-reading.
- **Privacy:** never store secrets or redacted values. Give the user `/memory` to view, edit and
  forget entries.

## 5. Local code graph, built first, updated incrementally
When ternly enters a codebase, its first action is to build or load a code graph and use it for
retrieval.
- **Nodes and edges:** files, packages/modules, symbols (types, functions, methods, fields), and
  references, calls, imports, implements and test-of relationships. Keep spans so answers can cite
  `file:line`.
- **Parsing:** use native parsers where available (`go/parser` + `go/types` for Go). For other
  languages, evaluate pure-Go options before choosing: tree-sitter grammars compiled to WASM run on
  wazero, existing SCIP/LSIF indexers, or language servers. Document the choice in an ADR. Keep the
  binary static.
- **Central dependency cache:** keep dependency graphs in a shared location
  (`~/.cache/ternly/graphs/<ecosystem>/<module>@<version>/<content-hash>`). Reuse a cached graph only
  when ecosystem, module, exact version and content hash all match (Go module cache, npm, cargo,
  PyPI). Otherwise build it once and cache it.
- **Incremental updates:** update via content hashes plus `git diff` and filesystem watching, so only
  changed files are re-parsed and edges are patched transactionally. Benchmark full-build and
  incremental update times on a large real repo (e.g. kubernetes or a big Go monorepo) and report
  them.
- **Tools:** expose the graph as `find_symbol`, `references`, `callers`, `callees`,
  `implementations`, `related_files` and `impact` (what breaks if X changes). Route the agent to use
  these before grep/read, and measure the token reduction.

## 6. Slash commands from the frontier harnesses
Survey the current official docs of Claude Code, OpenAI Codex CLI, Gemini CLI, Aider, OpenCode and
Crush. Build a compatibility table in `docs/commands.md`: command, source harness, semantics, and
ternly status. Verify each command against the docs. Do not include commands you cannot confirm.

Candidate starting points to verify:

- **Session:** `/help /clear /compact /resume /continue /export`
- **Models and cost:** `/model /cost /status /context`
- **Setup:** `/init /memory /config /permissions /doctor`
- **Extensions:** `/mcp /agents /hooks`
- **Code review and changes:** `/review /diff /undo /rewind /commit`
- **Planning:** `/plan`
- **Aider file context and modes:** `/add /drop /ask /architect /code`
- **Utilities:** `/test /lint /run /web /copy /theme /vim`

Implement all commands that fit ternly's model. Where two harnesses disagree, prefer the most common
semantics. Add aliases for the others. Support user-defined commands from markdown files with
arguments (see requirement 8). Add tab completion and fuzzy search in the TUI.

## 7. Minimal fabrication
Make hallucination structurally hard, not just discouraged. "Zero" cannot be guaranteed; build
mechanisms and measure the rate.
- **Code facts:** answers about the codebase must cite graph or tool results with `file:line`. A
  claim with no supporting tool output is flagged.
- **Symbols:** before the agent uses a symbol, API, package or version, check that it exists via the
  code graph, the compiler, or a registry lookup. Feed failures back automatically.
- **Unknowns:** the system prompt must make "I don't know / I need to check" the expected behaviour
  for unknowns. Verification gates must back any "it works" statement.
- **Eval:** add a small eval suite (`internal/eval`) with seeded traps: non-existent functions,
  outdated APIs, fake flags. Track the fabrication rate per model, and feed the results back into
  routing tiers. This replaces the stale name-based tier table with measured capability.

## 8. Plugins, skills, agents and personas from other harnesses
Design an adapter layer that normalises external ecosystems into ternly's internal model: commands,
skills, subagents/personas, hooks, MCP servers and rules. Verify each format against current docs
before implementing it.

Formats to cover:
- Claude Code:
  - plugins and marketplaces (`.claude-plugin/`)
  - `.claude/commands`
  - `.claude/agents`
  - skills (`SKILL.md` with frontmatter)
  - hooks
  - `.mcp.json`
- `AGENTS.md`
- Codex prompts/config
- Gemini CLI extensions and TOML commands
- Cursor rules (`.cursor/rules`)
- OpenCode agents

Requirements:
- **Discovery:** discover installed repositories in their standard locations and from a git URL
  (`/plugin add <url>`). Load them lazily: skill bodies load into context only when relevant.
- **Security:** treat every plugin as untrusted code. Show what it will run, require approval, pin
  it by commit hash, run hooks and MCP servers in the sandbox, and allow per-plugin permission
  scopes.
- **Unsupported features:** when an external feature has no ternly equivalent, load what is possible
  and report exactly what was skipped. "Any and all" is not literally achievable, so make coverage
  explicit in `docs/compat.md`.

## 9. Capability discovery: suggest, confirm, install, hot-reload
ternly should notice when a task needs a capability it doesn't have — a skill, plugin, MCP server or
subagent — find the best candidates, and let the user install one without restarting.

**Detecting a gap.** Use cheap signals first and spend model tokens last:
- the task names an external system or format with no matching tool (Postgres, Jira, Figma, Terraform, PDF…)
- a tool fails because the capability is missing
- the model says it lacks access
- the model repeatedly works around a missing integration

Only when the cheap signals are ambiguous, classify with the cheapest utility model under a tight
token cap, and cache the result. Suggest at most once per need. Remember dismissals per project, and
never interrupt a turn mid-stream: queue the suggestion for the next turn boundary.

**Sources.** Keep a local, offline-searchable index of catalogs, refreshed in the background (daily
by default), stored in ternly's own store (see requirement 4):
- the official MCP registry
- Claude Code plugin marketplaces (`marketplace.json` in git repos)
- public skills repositories (`SKILL.md`)
- Gemini CLI extensions
- npm/PyPI packages that ship MCP servers
- user-configured registries

Verify each source's current URL and format before integrating it. Do not hard-code endpoints you
haven't confirmed.

**Ranking.** Rank candidates on:
- relevance to the task
- trust: official or verified publisher, maintenance recency, adoption, license
- security footprint: what it executes, network access, permissions requested
- adapter coverage: how much of it ternly can actually load
- context cost: how many tokens its skill text or tool schemas add

Show a short ranked list (top 3) with the best one preselected. Each entry gets a one-line reason,
publisher, source, pinned version or commit, and what it will run. Nothing installs without explicit
confirmation. Installs are pinned by commit or hash, and hooks and MCP servers run sandboxed, per
requirement 8.

**Hot reload, no restart.**
- Install in the background with progress shown in the status bar. If install or validation fails,
  nothing changes.
- The tool, skill and command registry is an immutable snapshot behind an atomic pointer. Swap it
  only at a turn boundary, so a streaming request never sees a half-loaded registry. Accept that the
  tool list changing invalidates the prompt cache once.
- Start MCP servers concurrently and register their tools only after `initialize` and `tools/list`
  succeed.
- `/plugin` covers list, add, remove, enable, disable and update — all applied live. A file watcher
  on plugin directories reloads manual edits.
- Tell the model what was added, in one line, on the next turn.

Measure and report: index search latency, time from confirmation to usable, and tokens added per
installed capability.

## 10. Sessions: pause, stop, resume
- **Durable store.** Every session is persisted as a crash-safe, append-only event log: messages,
  tool calls and results, model choices, ledger, checkpoint refs, plan/todo state and settings.
  Store it under the user data dir, keyed by a stable repo identity: real path plus git root-commit
  hash, so renamed directories still match where possible.
  - Files are `0600`, already-redacted content only.
  - The directory is masked from the sandbox, like the config and cache dirs.
- **Pause and stop.**
  - Esc interrupts the current turn.
  - `/pause` stops at the next safe point (between tool calls) and persists.
  - `/stop` ends the session.
  - Ctrl+C and crashes must also leave a resumable log.
  - Tool calls interrupted mid-flight are recorded as cancelled with a synthetic result. A resumed
    history must always be valid for every provider (no `tool_call` without a result).
- **Resume.**
  - `ternly -c/--continue` resumes the latest session; `ternly --resume [id]` resumes a chosen one.
  - Restore the conversation (compacted if needed), pinned model, permission mode, budgets and
    ledger.
  - Then detect workspace drift since the session paused, by comparing the last checkpoint tree with
    the current one. Give the model a concise summary of files changed outside the session before it
    acts.
- **Auto-resume on startup.** When ternly starts in a directory that has previous sessions, it
  automatically resumes the most recent one by last activity. It shows a one-line banner: session
  title, age, turns, cost, `/sessions` to switch, `/new` for a fresh one. `--new` and the config key
  `auto_resume: false` opt out.
- **Concurrency.** Use a per-session lock (`flock`) so two ternly processes never write the same
  session. A second process on the same repo offers to fork the session or start a new one.
- **Targets.** Measure resume time for a 1,000-turn session (target under 100 ms to interactive) and
  the per-event append cost (target under 1 ms, never blocking the UI).

## 11. Switch sessions in place
`/sessions` opens an interactive, fuzzy-searchable picker of this directory's sessions: title,
last active, turns, cost, status. Titles are auto-generated from the first prompt by the cheapest
model and can be changed with `/rename`.

Selecting a session (or `/switch <id>`, alias `/resume <id>`) works in place, without restarting:
1. Pause and persist the current session at a safe point.
2. Release its lock.
3. Load the target session.
4. Swap the agent's state atomically.
5. Re-render the transcript.

Also provide `/new`, `/fork` (branch the current session from its latest turn or a chosen
checkpoint), `/delete` (with confirmation) and `/export` (markdown or JSON). Switching must never
lose data, even if the target fails to load. Prove this with a test that kills the process mid-switch.

# Delivery

## Status
M0 (rename and release) and M1 (guardrails, checkpoints, `/undo` and `/rewind`) are complete and
approved. Decisions:
- **License:** Apache-2.0 (`LICENSE` + `NOTICE`, PKGBUILD `license=('Apache-2.0')`).
- **Git:** `git init`, commit M0 and M1 as separate commits.
- **CI:** add `.github/workflows/ci.yml` running a gofmt check, `go vet`, `go test -race` and
  `goreleaser check` on every push and PR.

## M1.1 — hardening, one commit, before anything else
a) **Security.** The sandbox binds `~/.cache` read-write, so checkpoint objects — including copied
   untracked secrets — are readable by model-driven shell commands across projects.
   - Mask `~/.cache/ternly` (and the session data dir) in the bwrap profile.
   - Exclude secret-pattern files from snapshots by default (`.env*`, `*.pem`, `*.key`, `id_*`,
     `*credentials*`, `*.p12`) and report what was skipped.
   - Delete a session's shadow repo when the session is deleted. With requirement 10, checkpoint
     refs now outlive the process, so retention follows the session.
   - Add a test proving a sandboxed command cannot read checkpoint or session data.
b) **Path escape (TOCTOU).** Close it with `os.Root` (Go ≥1.24). Verify which `Root` methods exist
   in Go 1.26 first, then migrate the file tools and the grep fallback. Add a symlink-swap race test.
c) **`/verify` data race.** Fix it, with a `-race` test that changes it mid-turn.
d) **TUI test.** Add a pty-driven end-to-end test covering `/undo`, `/rewind` and `/limits` in the
   real TUI.
e) **Budget visibility.** Show the per-turn budget in the status bar once usage passes 75%.

## Milestones
Work in milestones, each one shippable. The order changed: sessions come early because memory,
capability discovery and in-place switching all build on durable session state.

| Milestone | Scope |
|---|---|
| M0 | rename to ternly + release metadata (requirement 0) |
| M1 | guardrails + git checkpoints/undo + schema validation |
| M1.1 | hardening (above) + license, git history, CI |
| M2 | sessions: durable log, pause/stop/resume, auto-resume, `/sessions` `/switch` `/new` `/fork` (requirements 10, 11) |
| M3 | code graph (Go first) + graph tools + central dependency cache (requirement 5) |
| M4 | memory subsystem + `/memory`, sharing the store with sessions where it measurably fits (requirement 4) |
| M5 | slash-command parity (requirement 6) |
| M6 | plugin/skill/agent adapters + capability discovery, ranking and hot reload (requirements 8, 9) |
| M7 | multi-language graph + fabrication eval + measured routing tiers (requirement 7) |

Next: complete M1.1 and stop for review. Then, after approval, post the M2 plan with ADRs,
implement it, and stop for review again. Follow the same plan → ADR → implement → measure → stop
cycle for every later milestone.

After each milestone, report:
1. what changed (files)
2. test and benchmark results with numbers
3. what remains unverified or out of scope
4. any requirement you could only partially meet, and why
