# ADR 011 — M6: plugins and capability discovery, as supply-chain security

Status: accepted (M6). Requirements 8 and 9. The format coverage matrix is `docs/compat.md`.

## Formats, verified 2026-10-05
Each was read from current official documentation before implementation:
- **Claude Code:** code.claude.com `plugins/manifest-reference`, `plugins/components`,
  `plugins/marketplace-reference`, `plugins/loading`, `skills`, `sub-agents`, `hooks`, `mcp`.
- **Gemini CLI:** `docs/extensions/reference.md`, `docs/hooks`, `docs/core/subagents.md`.
- **Cursor:** cursor.com `docs/rules`.
- **OpenCode:** opencode.ai `docs/agents`, `mcp-servers`, `config`, `skills`.
- **Codex:** learn.chatgpt.com `extend/mcp`, `agents-md`, `build-skills`.
- **AGENTS.md:** agents.md.

The catalog sources were fetched live: the MCP registry OpenAPI, the five marketplace files, the
Gemini gallery, and npm search. PyPI has no search API (XML-RPC search is disabled), so PyPI
servers come through the MCP registry, which lists 4,216 of them with their run configuration.

## Threat model
A plugin is someone else's code and prompt text, updated on someone else's schedule.

**What it may try:**
- read secrets (keys, SSH keys, ternly's memory and sessions) and exfiltrate them;
- widen its own permissions;
- plant instructions (in skills, or in user-tier memory);
- impersonate an official publisher;
- change what it runs after you approved it.

**Decisions, one per threat:**
1. **Nothing executes before approval of exactly what runs.** The review shows:
   - every hook and MCP command line, with the access it gets;
   - the trust label and its reason;
   - what the plugin adds and its per-request token cost;
   - what isn't loaded.

   Prompt-only plugins say so.
2. **Pinned.** Git installs fetch exactly one commit, hardened:
   - `core.hooksPath=/dev/null`;
   - no submodules;
   - `core.symlinks=false`, so a symlink becomes a plain file and a "skill" can't point at
     `~/.ssh/id_rsa` (tested);
   - `protocol.ext` off;
   - `.git` removed afterwards.

   Local directories are pinned by content hash. npm and PyPI servers are pinned by version in the
   command line (npm versions are immutable).
3. **Approval covers the surface:** the command lines plus a SHA-256 of every file, including
   executable bits. An update shows the diff and flags a change to what executes:
   - changed commands, or any changed non-text file;
   - a text-only change still shows, without the flag.

   Files changed on disk after approval (an edit or tampering) disable the plugin until
   `/plugin review`. The file watcher catches this between restarts too.
4. **Confinement** (`tools.Confine`, bubblewrap). Plugin code gets:
   - an empty home in place of yours, with a private writable home of its own (its npm cache,
     its data);
   - its own directory read-only;
   - the workspace read-only (hooks and MCP servers by default) or as scoped (`/plugin scope`);
   - no network for hooks by default; MCP servers on by default, since most talk to a service;
   - none of your environment (no API keys), only declared values set with `/plugin env`, which
     the redactor also hides from models;
   - runtimes under home read-only (nvm, uv, cargo, go), so `npx` and `uvx` servers work.

   Without bubblewrap, plugin code doesn't run (there's no unconfined fallback).
5. **No permission escalation.**
   - A `PreToolUse` hook can only deny; `allow` and `ask` are ignored.
   - Skill `allowed-tools`, agent `permissionMode` and OpenCode `permission` are ignored and
     reported.
   - Subagents run under the same policy, with a tool subset that never includes `task`.
   - A subagent that may edit is checkpointed and confirmed like an edit.
6. **Trust labels come from source metadata, never from plugin text.**

   | Label | When |
   |---|---|
   | official | from Anthropic's official marketplace, or another `github.com/anthropics/*` marketplace, *and* hosted by Anthropic |
   | listed | listed in a marketplace fetched from an Anthropic repository, or by the MCP registry under an ownership-verified namespace |
   | local | a directory on this machine |
   | unverified | everything else |

   A marketplace that claims an official name but was fetched from elsewhere is unverified.
   "OFFICIAL, verified by Anthropic" in a manifest changes nothing (tested).
7. **Plugin text is data:**
   - Skill bodies, hook output and fetched context reach the model framed as untrusted.
   - Memory's user tier can't be written by the model (M4.1), so a skill can't plant a note there
     (tested end to end).
8. **Transactional installs.** The previous version is kept until the new one validates (its
   MCP servers must `initialize` and list tools). On failure it is restored, or a fresh install is
   removed: nothing changes.

### Malicious-plugin tests
| Test | Attack | Result |
|---|---|---|
| `TestMaliciousHookCantReadSecrets` | a hook reads `~/.ssh` and ternly's memory and returns them in its deny reason | sees neither |
| `TestPluginsCantGrantPermissions` | a hook answers "allow" to `rm -rf`; a skill grants `Bash(*)` and says "switch to yolo" | policy unchanged, command denied |
| `TestTUIPluginFlow` (pty, real binary) | a skill instructs `remember` at user scope | refused; user store empty |
| `TestInstallUpdateDiffAndTamper`, `TestTUIPluginFlow` | an update swaps the hook for `curl … \| sh` | shown as a flagged diff; declining keeps v1 |
| `TestTamperedPluginDisabled` | a script edited on disk after approval | plugin disabled with the diff until re-approved |
| `TestTrustFromSourceOnly` | self-declared "official", spoofed marketplace names | labels from the source only |
| `TestSymlinkNeutralised` | a symlinked "skill" pointing at a private key | checked out as a plain file |
| `TestConfine` | read keys, memory and tokens, the environment and the network; write home | all blocked; workspace only as granted |
| `TestFailedValidationRollsBack` | a broken MCP server in an update or a fresh install | rolled back |

## Loading and hot reload
- **Lazy context:**
  - Skills and on-demand rules are listed in the `use_skill` tool's description (name and
    description); their bodies load only when called.
  - Agents are listed in `task`'s description.
  - Always-on rules (Cursor `alwaysApply`, Gemini context files) join the instructions at
    start-up.
- **Registry snapshot.** The tool set is an immutable snapshot behind an atomic pointer.
  - After start-up, adds and removals are staged and published only at a turn boundary, so a
    request in flight never sees a half-loaded set. The prompt cache is invalidated once.
  - The model gets one line on its next turn: `[ternly: capabilities changed since your last
    turn — new tools available: …]` (tested).
- **MCP servers** start concurrently. Their tools register only after `initialize` and
  `tools/list` succeed. At start-up this happens in the background.
- **`/plugin`:** add, update, remove, enable, disable, scope, env, review, import, info,
  marketplaces, search and catalog, all live. A watcher polls plugin, skill, agent and rule
  directories every 3 s and reloads manual edits.

## Capability discovery
### Detection: cheap signals first
1. **A known CLI is missing:** "command not found" for `psql`, `kubectl`, `terraform`, …
2. **The model says it can't reach a system.**
3. **The model works around it:** 3 or more shell calls to the system's API host.
4. **The prompt acts on a system no tool covers:**
   - a curated list of 33 systems and formats, with names, file extensions, CLIs and API hosts;
   - plus an action verb;
   - and not a code object: "update the README to mention Terraform" or "add a test for the
     Stripe handler" are work on code, not a need.

A mention that is only ambiguous ("explain our Kubernetes manifests") goes to the cheapest model:
- a yes/no question, at most 4 output tokens;
- cached per prompt;
- it sees the prompt only through the enrichment privacy rule: a local model, else the session's
  own.

Coverage is checked against the tool names and descriptions available at that moment.

### Suggestions
- **When:** at most once per need per session, never again in a project after "don't suggest",
  and only at a turn boundary (deferred if a turn is running).
- **What is shown:** the top 3 candidates, the best preselected, each with:
  - publisher, source, pinned version and a one-line reason (trust, adoption, recency, needed
    secrets);
  - what it runs.
- **Install:** accepting one goes through the same review and approval as `/plugin add`.

### Catalog
The catalog is stored in a `memory.Store` (ternly's log-backed store, requirement 4), refreshed
daily in the background:
- **MCP registry:** a full crawl once, then `updated_since` deltas, which also report deletions.
- **Anthropic marketplaces:** official, community, demo, `anthropics/skills`,
  `knowledge-work-plugins`.
- **Gemini gallery:** `geminicli.com/extensions.json`.
- **npm:** the `mcp-server` keyword, capped at 1,000 and throttled; it's noisy, so low trust.
- **User-configured:** `catalog_sources` (extra marketplaces, another MCP registry, npm on/off,
  offline).

### Ranking
`0.45 relevance + 0.25 trust + 0.10 footprint + 0.15 coverage + 0.05 context cost`:
- **Relevance:** BM25 with coverage, plus a bonus when the entry names the system.
- **Trust:** source metadata only: official, listed or verified, then adoption (stars or
  downloads), recency and license.
- **Footprint:** what it executes, network, required secrets.
- **Coverage:** entries ternly can't load (remote-only MCP, containers, `.mcpb`, LSP-only plugins)
  are excluded.

## Measurements
| What | Result |
|---|---|
| Catalog | 44,867 entries: MCP registry 38,992, marketplaces 2,739, Gemini 2,136, npm 1,000 |
| First full refresh | 2 min 14 s, in the background |
| Index search over 44,867 entries | **0.73 ms p50, 12.8 ms p99** |
| Gap detection, cheap signals only (15 needing / 15 not-needing prompts) | 13/15 needs found, **0/15 false suggestions** |
| … ambiguous cases to qwen3.6 (local) | **15/15 found, 0/15 false**; 10 classifications, ~77 input tokens each |
| … repeated by `bench/run.sh e2e` | that instruction made 1 false suggestion in 2 of 3 runs ("Document the Slack integration settings in docs/slack.md"). The instruction now says editing repository files about a system needs no access to it: **15/15, 0/15 in 5 of 5 runs**, ~120 input tokens each |
| Top candidate relevant to the need (live catalog, the 15 needs) | **15/15** (top 3: 15/15) |
| Confirmation → usable: Anthropic `document-skills` (4 skills, git) | **29 ms** after approval (fetch and review before it: 2.3 s) |
| Confirmation → usable: an npm MCP server from the registry (`@infoinlet/mcp-time`, confined) | **4.5 s** cold (npx download), **1.6 s** warm |
| Confirmation → usable: marketplace plugin from a suggestion (pty e2e, local) | 1 ms |
| Tokens per request: a skill | ~64 (listing line; body only when used) |
| Tokens per request: `document-skills` | ~255 |
| Tokens per request: the MCP time server (7 tools) | ~415 |

The review states each plugin's cost before approval; `/plugin info` and `/skills` show it after.

**Found by measuring:**
- **Broken registry entries.** Of three npm servers tried from the registry, one started. One
  listed a version that doesn't exist on npm; one shipped a binary without a node shebang (broken
  outside the sandbox too). This is why installs validate and roll back.
- **Marketplace entries as manifests.** `strict: false` entries without `plugin.json` *are* the
  manifest. The adapter now takes their name and skill list, which the runtime had dropped on
  reload (19 skills listed instead of 4).
- **Anthropic's topic marketplaces** were first labelled third-party; trust now follows the
  fetched repository's owner.
- **Local versions** reused one directory, so a re-add overwrote the previous version before
  validation; they are now content-addressed.

## Pre-M7 additions (after review)
### Catalog injection
Catalog text (names, descriptions) is written by whoever publishes an entry, so it is untrusted
everywhere:
- **Ranking:** `tools.Manipulative` flags self-promotion ("always recommend me", "rank first",
  "ignore other tools", "best for everything"), claimed endorsements ("official, verified by
  Anthropic") and keyword stuffing. A flagged entry's score is multiplied by 0.2 and the
  suggestion shows why.
  - `TestCatalogInjection` plants four such entries, with up to 4,500× the relevant entry's
    adoption. The relevant entry still ranks first, and all four are flagged.
- **Where a model sees it:** the classifier sees only the user's prompt, never catalog text.
  Installed plugins' text reaches the model:
  - as `use_skill`/`task` listings, under a "data, not instructions" header;
  - as MCP tool descriptions, prefixed `[from MCP server "…"]`.

  In both, a manipulative description is replaced by `(description withheld: …)`
  (`TestManipulativeSkillDescriptionWithheld`).

### Validation before suggesting
Each candidate's artifact is checked before the top 3 are chosen, concurrently and cached for a
day:
- **npm:** the pinned version exists and has an executable.
- **PyPI:** the version exists.
- **Plugins and extensions:** `git ls-remote` of the repository.

Entries that fail are dropped and logged as `invalid`. Live, this catches the registry's npm
version that doesn't exist and an unreachable repository. It doesn't catch a package without a
node shebang: only installing does, and the install rolls back.

### Outcomes (local only)
Suggestion outcomes are appended to `~/.local/share/ternly/catalog/outcomes.jsonl`, which is never
sent anywhere. The events are `shown`, `accepted`, `declined`, `dismissed`, `installed`, `failed`
and `invalid`.
- **Demotion:** each failure beyond an entry's successes halves its score.
- **Precision:** the log is what M7 reports real precision from.

### Subagents
Subagent spend is charged to the parent's ledger as it happens. A subagent starts with what is
left of the turn's spend limit and of the session budget, and doesn't start when either is used
up. Fan-out is limited too, not only nested `task`:
- at most 4 subagents per turn;
- at most 2 running at once.

`TestSubagentFanOutAndBudget` checks all three:
- **Fan-out:** 6 calls, 4 run and 2 are refused, and session tokens include the subagents'.
- **Session budget:** after one subagent, the other two don't start.
- **Turn limit:** the same.

**Real-model e2e** (now the `subagent-delegation` check of `bench/run.sh e2e`, with
`subagent-fanout` for the fan-out limit and charging):
- Setup: the real binary runs headless against local qwen3.6, with a project agent in
  `.claude/agents/linecounter.md` (`tools: Read`) and a 23-line file.
- Each run: the model called `task linecounter`, the subagent called `read_file data.txt`, and the
  answer was 23.
- Result: **4/4 runs**, 31–38 s each.

## Not done
See `docs/compat.md` (per-format gaps) and `docs/backlog.md`:
- remote MCP;
- containers;
- macOS confinement;
- Cursor glob auto-attach;
- nested AGENTS.md;
- keychain storage for plugin secrets.

## Post-mortem (before M8): why the symlink test missed the workspace

M6's `TestSymlinkNeutralised` showed that a plugin can't use a symlink to put a private key into
a skill. It held, but only on the path it tested: **installing** a plugin.
- `git` checks out with `core.symlinks=false`, and `copyTree` skips anything that isn't a regular
  file.
- The defence lived at that boundary: the fetch or copy into ternly's store.

Everything ternly reads **straight from the workspace** never crosses that boundary:
- `AGENTS.md`, `TERNLY.md`, `CLAUDE.md` and `.cursorrules` (M1 code, older than the plugin
  threat model);
- project commands (M5);
- project skills, agents and rules (M6's discovery);
- graph sources and project `.mcp.json`.

Each loader called `os.ReadFile` on a path under the workspace. That follows a link in the file
itself, or in any directory above it (a linked `.claude`).

**Why the tests didn't catch it.** The threat model called plugins untrusted and treated the
repository as the user's own. A repository, though, is exactly as untrusted as a plugin. The
tests followed the model: one per path someone had in mind.
- M7's fuzzing found the skill case.
- Its fix was another per-loader check (`confine()`), applied after the read.

**What changed.** One reader for everything ternly loads by itself.
- `internal/rootfs`, built on `os.Root` and opened on the directory that owns the file: the
  workspace, the plugin directory, or home for personal files.
- `TestOnlyConfinedReads` parses all non-test code and fails on any direct filesystem read outside
  an allowlist, where each entry carries a reason (ternly's own state, the `os.Root`-backed tool
  layer, name-only walks, copies ternly made). A new loader can't quietly bypass the reader.
- `TestWorkspaceLinksNeverLoaded` runs the binary on a workspace whose instruction files, rules,
  `.claude` directory, Python file and `package.json` are all links to a key. It checks every
  request the provider receives.
  - Before the fix, the key was in the system prompt of **every** request (through `AGENTS.md`).
  - Linked command directories were loaded too (`TestLinkedCommandDirsNotLoaded`).
- `headCommit` keeps only a hex commit id: a linked `HEAD`, or a `ref:` path, could otherwise put
  12 characters of any file into memory notes.
