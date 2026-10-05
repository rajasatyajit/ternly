# Backlog

Known gaps, each found while building or measuring a milestone. They are scheduled before the
milestone named, or noted as open.

## Code graph (before M7: multi-language graph)
- **Generics in `implementations`.** Method sets of generic types and interfaces are skipped, so
  `type Set[T comparable]` never matches an interface. It needs instantiation-aware matching, or at
  least matching on the generic method shapes. (ADR 007)
- **Calls through function values.** `f := pkg.Func; f()`, method values, and callbacks passed as
  arguments are not call edges. Static assignment tracking within a function would cover the common
  cases. (ADR 007)
- **Approximate first pass** (M3.1): name-based references miss uses through aliases and dot
  imports, and shadowing can produce false references. It is replaced by the typed graph as soon as
  that build finishes, so this only matters during the first minutes on a cold Go cache.
- **Type-error drift after incremental API changes** is now bounded by the idle rebuild (M3.1).
  While a session stays busy, the inexact graph can persist.

## Code graph, other languages (M7)
- **Name matching.** Python, TypeScript/JavaScript, Rust and Java calls are matched by name,
  scoped by file, directory and imports. On real repositories, 5% (zod) to 65% (Guava) of call
  edges still have several candidates and are marked "name match". Java's overloads and common
  method names (`get`, `size`) dominate.
  - **Fix:** an optional SCIP layer when an indexer is installed (scip-python, scip-typescript,
    scip-java, rust-analyzer), read with the pure-Go SCIP binding.
- **No implementations or references (only calls)** for those languages; no test links.
- **Timeouts are silent.** A parse that exceeds 10 s returns what was tagged so far.
- **gotreesitter is pinned (v0.55.1).** It is young with one main author: it is wrapped behind
  the graph's own types so it can be replaced.

## Models
- **Retired or removed models** (seen 2026-10-04: `HTTP 410: glm-5.1 was retired` from Ollama
  Cloud): drop the model from routing for the session on 404/410 and fail over, instead of ending
  the turn with an error.

## Memory (M4)
- **Paraphrases need vectors, and vectors have a ceiling.** Lexical retrieval finds paraphrased
  questions 10% of the time. `nomic-embed-text` raises that to 70% among 40 notes, but only to 28%
  among 700 same-domain sentences, which is the model's own pure-cosine ceiling (25%). A stronger local
  embedding model, or asking the model to `recall` with its own keywords, would help. (ADR 009)
- **Notes after `/compact`.** A note is injected once per session, because it is then in the context.
  Compaction can summarise it away, and it isn't re-offered until a new session.
- **Structure outside Go.** The structural signal uses files and symbols named in the prompt and
  files changed in the session; code-graph neighbours exist only for Go (M7 adds languages).
- **Automatic turn summaries** take their "outcome" from the first sentence of the model's answer,
  which is sometimes a weak summary.

## Plugins and capabilities (after M6)
- **Remote MCP servers** (streamable HTTP, SSE): about 25k of the registry's 39k servers are remote
  only. They are excluded from suggestions until ternly has an HTTP MCP client.
- **Containers** (`oci` packages) can't run inside bubblewrap; **macOS** has no plugin sandbox,
  so plugins are prompt text only there.
- **Registry quality.** Of three npm servers tried from the MCP registry, one started: one listed a
  version that doesn't exist on npm, one shipped a broken binary. Install-time validation catches
  this (nothing changes), but ranking could also learn from failures.
- **Cursor glob rules** are offered by description, not auto-attached when a matching file is in
  context; **nested AGENTS.md** files aren't merged.
- **Plugin secrets** set with `/plugin env` are stored in ternly's data directory (0600, masked
  from every sandbox), not in the OS keychain.

## Infrastructure
- **CI** was blocked by a GitHub account billing lock during M3. It is resolved: both jobs (Linux
  check, macOS) ran green on `085fe9d`.
