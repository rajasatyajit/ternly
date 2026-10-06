# ◆ ternly

*Code, ternly.* — named for the Arctic tern (long-distance, energy-efficient migration — the longest known of any
animal) and the Latin *terni*, "three each": ternly's three routing tiers.

One terminal coding agent for **every model you can reach** — paid APIs, free tiers and local
servers, auto-discovered — routing each task to the **cheapest model that can do it well** and
only paying for a stronger one when the cheap one demonstrably fails.

![ternly fixing a failing test with a local model, verifying it, then /doctor and /mcp](docs/demo/demo.gif)

*A real, unedited session (local qwen3.6 through Ollama, sandboxed with bubblewrap), replayed at 2×.
Recorded with `docs/demo/record.sh`.*

```
git clone https://github.com/rajasatyajit/ternly && cd ternly && make install   # → ~/.local/bin/ternly
cd your-project && ternly
```
Also: `go install github.com/rajasatyajit/ternly@latest`, prebuilt static binaries on the
[releases page](https://github.com/rajasatyajit/ternly/releases) (linux/darwin, amd64/arm64), or
`ternly-bin` from the AUR (`packaging/aur/ternly-bin`). Upgrading from *vane*? `~/.config/vane` and
`~/.cache/vane` are moved to their ternly paths on first run (never overwriting existing ones).
Recommended on Linux: `sudo pacman -S bubblewrap ripgrep` (sandbox + fast search).

> [!WARNING]
> **macOS support is experimental in v0.1.** There is no sandbox on macOS yet (v0.2 adds one), so
> ternly runs there in a restricted mode:
> - **every shell command asks first**: nothing is auto-approved, not even read-only commands, in
>   `yolo` or in plan mode; headless (`-p`) runs refuse commands;
> - **plugin code is disabled**: hooks and plugin MCP servers don't run (skills, commands and agents
>   still load);
> - verification steps that would run the repository's own code (cargo, npm scripts, Maven/Gradle,
>   the project's tsc) ask too; if declined, the turn ends *unverified*.
>
> The same applies on Linux without bubblewrap, or with `--no-sandbox`. `/doctor` shows the state.

## What it discovers
Keys from env or `~/.config/ternly/keys.env` (0600): Anthropic, OpenAI, OpenRouter, Gemini, DeepSeek,
Groq, Mistral, xAI, Together, Fireworks, Cerebras, Moonshot, Qwen/DashScope, Z.ai — plus any
OpenAI-compatible endpoint in `config.json`. Local: Ollama (incl. `OLLAMA_HOST`, real tool-support
detection; Ollama Cloud models are recognised as remote and quota-limited, so `--local-only`
excludes them), LM Studio, llama.cpp, vLLM, Jan. Live prices/context come from OpenRouter's public
catalog (cached 24 h). `ternly -models` prints the table.

## How it keeps token cost down
| Mechanism | Effect |
|---|---|
| Zero-token difficulty classifier → cheapest model with tier ≥ difficulty | trivial work goes to free/local or mini models |
| Cascade escalation only after verification fails twice | frontier prices only when needed |
| Reasoning budget set by routing: low for routine turns, medium for hard ones, high for `/architect` plans and after escalation (`reasoning_effort`; Anthropic thinking budgets); `--reasoning auto\|off\|low\|medium\|high` | reasoning tokens spent where they pay |
| Provider failover on 429/5xx to an equal-tier model elsewhere | no wasted retries |
| Anthropic prompt caching (system, tools, last turn) + byte-stable prefix; OpenAI auto-cache | repeated context billed at ~10 % |
| `edit_file` diffs instead of rewrites; capped `read_file`/`grep`/`bash` output (head+tail) | fewer output & input tokens |
| Auto-compaction at ~55 % of context using the cheapest model | long sessions don't grow unbounded |
| `--budget` / `/budget` hard cap, live `$` in status bar | no surprises |

## How it keeps quality up
Strict engineering system prompt (+ the first of `TERNLY.md`, `AGENTS.md`, `CLAUDE.md`), auto-detected post-edit verification
(`go build && go vet`, `cargo check`, `npm run typecheck|lint|build`, `ruff`) fed back to the model,
`/review` with the strongest available model, parallel read-only tool calls.

**✓ means every changed source file was compiled by a check.** After the project's check, ternly
proves coverage of each file the turn changed (shell edits included):
- Go: the files' own packages, built and vetted in their own modules;
- Python: `compile()`;
- JavaScript: `node --check`;
- TypeScript: the project's `tsc`, which must include the file;
- Rust: cargo's dep-info;
- Java: fresh class files.

A file no check covers (a build tag, a stray `go.mod`, a file outside every tsconfig, a language
without a checker) makes the turn **unverified (?)**, never ✓. `docs/adr/015-after-m8.md`.

## Security model
Workspace path confinement (symlink-safe, `.git` internals blocked) · permission prompts for edits,
shell and MCP (`/mode ask|edits|yolo`) · read-only, build and test commands auto-approved, alone or
chained with `&&`/`||`/`;`/`|`, judged on the parsed bash syntax tree (no redirects but to `/dev/null`,
no expansions, no paths outside the workspace; fuzzed against the parser) · forbidden list (e.g. `rm -rf /`) even in yolo · shell runs in
**bubblewrap**: read-only root, writable workspace + toolchain caches, `~/.ssh ~/.aws ~/.gnupg …`
masked, optional `--no-net` · provider keys stripped from the shell env and redacted from all tool
output · keys file must be 0600 · repo-supplied `.mcp.json` is not started without `--project-mcp`.

## Guardrails
Tool output (files, shell, MCP, verification) is framed as untrusted data with a per-session nonce, and
text that looks like an injection is flagged. The permission policy, not text, decides what runs.
Tool arguments are validated against each tool's JSON schema, with corrective errors. Identical calls
with no edit in between, and long failure streaks, are treated as loops: the model is redirected and
escalated, then the turn is stopped. Per-turn step, time and spend limits (`/limits`, config
`limits`) and the session budget end a turn with a zero-cost summary you can `continue` from.
Success claims with no passing build or test since the last edit are challenged.

**Fabrication checks** (docs/adr/012):
- A final answer's `file:line` citations and backticked workspace symbols are checked against the
  files and the code graph. What doesn't check out goes back to the model once, then you are
  warned.
- Dependencies the model adds (`go.mod`, `package.json`, `requirements*.txt`, `Cargo.toml`, or
  `go get` / `npm install` / `pip install` / `cargo add`) are looked up in their registries; a
  version that doesn't exist is reported to the model straight away.

Before a turn's first
change, the workspace is checkpointed into a per-session private git repo under
`~/.cache/ternly/checkpoints` (including shell side effects; your `.git` is never touched). Secret-like
files (`.env*`, `*.pem`, `*.key`, `id_*`, `*credentials*`, `*.p12`) are never captured, and you are told
which were skipped. `/undo` and `/rewind` restore it, and the repo is deleted when the session ends.
File tools access the workspace through Go's `os.Root`, so a path swapped to a symlink mid-call can't
escape. ternly's config, cache and session dirs are hidden from sandboxed commands.

## Code graph (Go, Python, TypeScript/JavaScript, Rust, Java)
On entering a workspace, ternly loads or builds a code graph in the background: packages, files,
symbols, references, calls, and test links, with `file:line` spans.
- **Go** is type-checked with `go/types` from `go list -export` data, which runs in the sandbox.
- **Python, TypeScript/TSX, JavaScript, Rust and Java** are parsed with a pure-Go tree-sitter
  runtime. Calls are matched by name, scoped to the same file, then the same directory, then what
  the file imports. A use with several candidates is marked "name match".

The rest applies to both: The model gets `find_symbol`,
`references`, `callers`, `callees`, `implementations`, `related_files` and `impact`, and is told to
use them before grep/read. On kubernetes they answer "who calls X" with ~13× fewer tokens. The graph
is shared by a project's sessions under `~/.cache/ternly/graphs`, with dependencies' exported API
cached per module version. It updates incrementally as files change (~0.1 s for a body edit).
`code_graph: false` turns it off.

## Measured model tiers
`ternly --eval --model <m>` runs seeded traps through the real agent:
- symbols, flags, packages, versions and files that don't exist;
- a stale README;
- citation accuracy;
- memory notes that are wrong but similar to the truth;
- injection bait.

It reports the model's fabrication, memory-misuse and susceptibility rates and saves them under
`~/.local/share/ternly/capability`. Routing then uses the **measured** tier in place of the name
table: `ternly --models` shows each tier's basis.

A model's measured memory-misuse rate also sets how far it may lean on memory notes:
- **full:** notes are injected as context;
- **verify:** notes are injected as leads to check first;
- **off:** nothing is injected; the `recall` tool still works.

## Sessions
Every session is saved as it happens: a crash-safe, append-only log under `~/.local/share/ternly`
(owner-only, redacted, hidden from sandboxed commands). Starting ternly in a directory resumes its
most recent session, with a one-line banner, unless you pass `--new` or set `auto_resume: false`.
`-c` continues the latest session and `--resume <id>` a chosen one; a bare `--resume` lets you choose.
If files changed while the session was paused, the model is told which.
In the TUI: `/sessions` (fuzzy picker), `/switch <id>` (alias `/resume`), `/new`, `/fork [n|id]`,
`/rename`, `/delete`, `/export md|json`, `/pause` (stop at a safe point and save) and `/stop`. All of
them switch in place, without a restart. Checkpoints of all a project's sessions share one store
(`checkpoint_cap_mb`, default 2048, prunes the oldest sessions' checkpoints after a warning).

## Memory
ternly remembers across turns and sessions, whichever model is active. At the end of each turn it
records, in the background:

- what the turn was about and which files it changed (session tier, kept 30 days);
- *verified fixes*: a check that failed and then passed, with its first error line (project);
- explicit standing instructions in your prompts, such as "always …", "never …", "prefer …" or
  "from now on …", at project scope. A `Saved: … · /memory forget <id> to undo` line shows each one.

The model can also `remember` decisions and conventions, and `recall` them.

At the start of a turn, the most relevant notes go into your message, framed as context, never
instructions (notes can be outdated, so the model re-checks before an edit depends on one). They are capped at `memory_budget` tokens (default 600). Ranking
combines BM25 over words and code identifiers, the files and symbols you mention (and, in Go, their
code-graph neighbours), and recency. If Ollama serves a local embedding model (e.g.
`nomic-embed-text`), vectors are added: memory text never leaves the machine for embedding.

Secrets are refused, not stored redacted. Text that reads like instructions to an AI is refused
from the model and from automatic sources. Every item records where it came from (session, turn,
files, commit, source).

The user tier, which applies in every project, is written only by you: `/memory add user <text>`
or `/memory promote <id>`. Neither the model nor automatic capture can write to it, and injected
notes say who wrote them (*from you*, *from your prompt*, *automatic*, *model-written*). Once per
note, a local model (else the model your session already uses) writes other wordings of it, which
are indexed (never injected) so differently phrased questions still find it. `memory_enrich: false`
turns this off; `"remote"` also allows the cheapest remote model.

`/memory` lists what's stored; `/memory search|forget|edit|add [user]|promote` manage it. Stores live in
`~/.local/share/ternly` (hidden from sandboxed commands) and are shared safely by concurrent sessions.
`memory: false` turns memory off; `memory_vectors: false` keeps it lexical. Measurements are in
`docs/adr/009-memory.md`.

## Plugins, skills and agents
ternly loads other harnesses' extensions: Claude Code plugins, skills, subagents, hooks and MCP
servers, Gemini CLI extensions, Cursor rules, OpenCode agents and Codex skills. `docs/compat.md`
says exactly what loads.
- **Already on disk:** skills, agents and rules in the usual places (`~/.claude/skills`,
  `.claude/agents`, `.cursor/rules`, …) are available at once, as prompt text.
- **Installing:** `/plugin add <git-url|dir|name@marketplace>` shows what the plugin will run, its
  trust label (from where it came from, not what it says) and its token cost, and asks first.
- **Pinned:** installs are pinned to a commit. An update, or an edit on disk, that changes what runs
  needs approval again with a diff.
- **Confined:** hooks and MCP servers run inside bubblewrap, with no access to your home, keys,
  ternly's data or environment, and the workspace and network only as scoped. Plugins can't grant
  permissions.

## Remote MCP servers
Add `{"mcpServers": {"linear": {"url": "https://mcp.linear.app/mcp"}}}` to `~/.config/ternly/mcp.json`
(`headers` for API-key servers). ternly speaks Streamable HTTP in both the 2026-07-28 and the 2025
revisions and falls back automatically; the old HTTP+SSE transport isn't supported.
- **OAuth:** `/mcp login <server>` (or `ternly --mcp-login <server>`) runs the browser login: PKCE
  S256, the `resource` parameter, the `iss` check, dynamic registration as a native app. Start-up and
  tool calls only use stored or refreshed tokens; they never open a browser.
- **Tokens** live in the OS keyring (`secret-tool`, macOS `security`) when it works, else in a 0600
  file in ternly's data directory; they are redacted from everything a model sees. `/mcp logout`.
- **Network grants:** each server's client reaches only its own host and the hosts you approved for
  its login (an authorization server elsewhere is asked about first), never a private address
  (checked on the address actually dialed), and follows redirects only within the grant.
- `/mcp` shows each server's protocol era, auth state and granted hosts. `docs/adr/014-remote-mcp.md`.

When a task needs something you don't have (a Postgres, Jira or Figma integration, PDF handling…),
ternly suggests the top 3 candidates once, after the turn. They come from a local index of the MCP
registry, Anthropic's marketplaces, the Gemini extension gallery and npm. `suggestions: false`
turns this off.

## Slash commands
Type `/` for a list with fuzzy completion (Tab completes), or `/help`. The commands follow Claude
Code, Codex, Gemini CLI, Aider and OpenCode; `docs/commands.md` has the table and sources.
Highlights:
- **Plan:** `/plan` (read-only, enforced), `/ask`, `/code`, `/architect` (the strongest model
  plans, the cheapest capable one implements).
- **Changes:** `/diff`, `/commit` (asks first; never commits secret-like files), `/git`,
  `/review`, `/undo`, `/rewind`.
- **Context:** `/add`, `/drop`, `/ls` (pinned files), `@path` in a prompt, `!cmd` and `/run`
  (output goes with the next prompt), `/web <url>`, `/test`, `/lint` (failures go to the model).
- **Info:** `/status`, `/context`, `/cost`, `/config`, `/permissions`, `/doctor`, `/tools`,
  `/mcp`, `/btw <question>` (answered without adding to the conversation), `/copy`, `/editor`,
  `/theme`, `/init` (writes AGENTS.md).

**Your own commands** are markdown files in `.ternly/commands/` or `~/.config/ternly/commands/`
(`$ARGUMENTS`, `$1`…). Commands written for Claude Code (`.claude/commands`), OpenCode, Gemini CLI
(TOML) and Codex (`~/.codex/prompts`) work as they are.

## License
Apache-2.0 — see `LICENSE` and `NOTICE`.

## Usage
`ternly` (TUI) · `ternly -p "fix the failing test"` (headless, CI-friendly) · `--model`, `--mode`,
`--budget`, `--local-only`, `--no-local`, `--verify`, `--reasoning`, `--no-memory`, `--no-net`, `-C dir`, `-c`,
`--resume [id]`, `--new`, `--mcp-login <server>` ·
`ternly --eval --model <m>` (measure a model) · `ternly --models` (tiers and their basis).
The binary embeds the code graph's six grammars; `go build -tags ternly_all_grammars` adds every
other language gotreesitter has (+18 MB).
TUI: `/help` for all commands · Enter send · Shift/Alt+Enter newline · Esc interrupt · PgUp/PgDn
scroll · ↑↓ history.

Config examples in `examples/` → copy to `~/.config/ternly/`.
