# ternly

One terminal coding agent for **every model you can reach** — paid APIs, free tiers and local
servers, auto-discovered — routing each task to the **cheapest model that can do it well** and
only paying for a stronger one when the cheap one demonstrably fails.

```
git clone https://github.com/rajasatyajit/ternly && cd ternly && make install   # → ~/.local/bin/ternly
cd your-project && ternly
```
(After the first `go mod tidy` is committed, `go install github.com/rajasatyajit/ternly@latest` also works.)
Recommended on Linux: `sudo pacman -S bubblewrap ripgrep` (sandbox + fast search).

## What it discovers
Keys from env or `~/.config/ternly/keys.env` (0600): Anthropic, OpenAI, OpenRouter, Gemini, DeepSeek,
Groq, Mistral, xAI, Together, Fireworks, Cerebras, Moonshot, Qwen/DashScope, Z.ai — plus any
OpenAI-compatible endpoint in `config.json`. Local: Ollama (incl. `OLLAMA_HOST`, real tool-support
detection), LM Studio, llama.cpp, vLLM, Jan. Live prices/context come from OpenRouter's public
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
Strict engineering system prompt (+ `AGENTS.md`/`CLAUDE.md`), auto-detected post-edit verification
(`go build && go vet`, `cargo check`, `npm run typecheck|lint|build`, `ruff`) fed back to the model,
`/review` with the strongest available model, parallel read-only tool calls.

## Security model
Workspace path confinement (symlink-safe, `.git` internals blocked) · permission prompts for edits,
shell and MCP (`/mode ask|edits|yolo`) · read-only commands auto-approved only without shell
metacharacters/escaping args · forbidden list (e.g. `rm -rf /`) even in yolo · shell runs in
**bubblewrap**: read-only root, writable workspace + toolchain caches, `~/.ssh ~/.aws ~/.gnupg …`
masked, optional `--no-net` · provider keys stripped from the shell env and redacted from all tool
output · keys file must be 0600 · repo-supplied `.mcp.json` is not started without `--project-mcp`.

## Usage
`ternly` (TUI) · `ternly -p "fix the failing test"` (headless, CI-friendly) · `--model`, `--mode`,
`--budget`, `--local-only`, `--no-local`, `--verify`, `--no-net`, `-C dir`.
TUI: `/models /model /review /cost /compact /mode /verify /budget /refresh /clear` ·
Enter send · Alt+Enter newline · Esc interrupt · PgUp/PgDn scroll · ↑↓ history.

Config examples in `examples/` → copy to `~/.config/ternly/`.
