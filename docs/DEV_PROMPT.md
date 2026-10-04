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

# Delivery

Work in milestones, each one shippable:

| Milestone | Scope |
|---|---|
| M0 | rename to ternly + release metadata (requirement 0) |
| M1 | guardrails + git checkpoints/undo + schema validation |
| M2 | code graph (Go first) + graph tools + central dependency cache |
| M3 | memory subsystem + `/memory` |
| M4 | slash-command parity |
| M5 | plugin/skill/agent adapters |
| M6 | multi-language graph + fabrication eval + measured routing tiers |

Start by reading the code and completing M0. Then post the M1 plan with ADRs, implement M1 fully,
and stop for review before M2.

After each milestone, report:
1. what changed (files)
2. test and benchmark results with numbers
3. what remains unverified or out of scope
4. any requirement you could only partially meet, and why
