# ◆ ternly

*Code, ternly.* — named for the Arctic tern (long-distance, energy-efficient migration — the longest known of any
animal) and the Latin *terni*, "three each": ternly's three routing tiers.

One terminal coding agent for **every model you can reach** — paid APIs, free tiers and local
servers, auto-discovered — routing each task to the **cheapest model that can do it well** and
only paying for a stronger one when the cheap one demonstrably fails.

```
git clone https://github.com/rajasatyajit/ternly && cd ternly && make install   # → ~/.local/bin/ternly
cd your-project && ternly
```
Also: `go install github.com/rajasatyajit/ternly@latest`, prebuilt static binaries on the
[releases page](https://github.com/rajasatyajit/ternly/releases) (linux/darwin, amd64/arm64), or
`ternly-bin` from the AUR (`packaging/aur/ternly-bin`). Upgrading from *vane*? `~/.config/vane` and
`~/.cache/vane` are moved to their ternly paths on first run (never overwriting existing ones).
Recommended on Linux: `sudo pacman -S bubblewrap ripgrep` (sandbox + fast search).

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
| Provider failover on 429/5xx to an equal-tier model elsewhere | no wasted retries |
| Anthropic prompt caching (system, tools, last turn) + byte-stable prefix; OpenAI auto-cache | repeated context billed at ~10 % |
| `edit_file` diffs instead of rewrites; capped `read_file`/`grep`/`bash` output (head+tail) | fewer output & input tokens |
| Auto-compaction at ~55 % of context using the cheapest model | long sessions don't grow unbounded |
| `--budget` / `/budget` hard cap, live `$` in status bar | no surprises |

## How it keeps quality up
Strict engineering system prompt (+ the first of `TERNLY.md`, `AGENTS.md`, `CLAUDE.md`), auto-detected post-edit verification
(`go build && go vet`, `cargo check`, `npm run typecheck|lint|build`, `ruff`) fed back to the model,
`/review` with the strongest available model, parallel read-only tool calls.

## Security model
Workspace path confinement (symlink-safe, `.git` internals blocked) · permission prompts for edits,
shell and MCP (`/mode ask|edits|yolo`) · read-only commands auto-approved only without shell
metacharacters/escaping args · forbidden list (e.g. `rm -rf /`) even in yolo · shell runs in
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
Success claims with no passing build or test since the last edit are challenged. Before a turn's first
change, the workspace is checkpointed into a per-session private git repo under
`~/.cache/ternly/checkpoints` (including shell side effects; your `.git` is never touched). Secret-like
files (`.env*`, `*.pem`, `*.key`, `id_*`, `*credentials*`, `*.p12`) are never captured, and you are told
which were skipped. `/undo` and `/rewind` restore it, and the repo is deleted when the session ends.
File tools access the workspace through Go's `os.Root`, so a path swapped to a symlink mid-call can't
escape. ternly's config, cache and session dirs are hidden from sandboxed commands.

## Code graph (Go)
On entering a Go workspace, ternly loads or builds a code graph in the background: packages, files,
symbols, references, calls, and test links, with `file:line` spans. It is type-checked with
`go/types` from `go list -export` data, which runs in the sandbox. The model gets `find_symbol`,
`references`, `callers`, `callees`, `implementations`, `related_files` and `impact`, and is told to
use them before grep/read. On kubernetes they answer "who calls X" with ~13× fewer tokens. The graph
is shared by a project's sessions under `~/.cache/ternly/graphs`, with dependencies' exported API
cached per module version. It updates incrementally as files change (~0.1 s for a body edit).
`code_graph: false` turns it off.

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

## License
Apache-2.0 — see `LICENSE` and `NOTICE`.

## Usage
`ternly` (TUI) · `ternly -p "fix the failing test"` (headless, CI-friendly) · `--model`, `--mode`,
`--budget`, `--local-only`, `--no-local`, `--verify`, `--no-net`, `-C dir`, `-c`, `--resume [id]`, `--new`.
TUI: `/models /model /review /cost /compact /mode /verify /budget /limits /undo /rewind /refresh /clear` ·
Enter send · Alt+Enter newline · Esc interrupt · PgUp/PgDn scroll · ↑↓ history.

Config examples in `examples/` → copy to `~/.config/ternly/`.
