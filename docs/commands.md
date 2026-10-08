# Slash commands: compatibility table

Surveyed on 2026-10-05 from each harness's current official documentation. Only commands seen on
these pages are listed:

| Code | Harness | Source |
|---|---|---|
| CC | Claude Code | https://code.claude.com/docs/en/commands; custom commands: https://code.claude.com/docs/en/skills |
| CX | OpenAI Codex CLI | https://developers.openai.com/codex/cli/slash-commands (redirects to learn.chatgpt.com/docs/developer-commands); custom prompts: https://developers.openai.com/codex/custom-prompts |
| GM | Gemini CLI | https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/commands.md; custom commands: docs/cli/custom-commands.md |
| AI | Aider | https://aider.chat/docs/usage/commands.html (+ modes.html, git.html, lint-test.html, images-urls.html) |
| OC | OpenCode | https://opencode.ai/docs/tui/, https://opencode.ai/docs/commands/ |
| CR | Crush | https://github.com/charmbracelet/crush/blob/main/README.md (Crush documents a ctrl+p palette, not slash commands) |

**Rules.**
- Where harnesses disagree, ternly follows the most common semantics and adds the others' names as
  aliases.
- A command is implemented when it fits ternly's model: a terminal coding agent with
  cost-aware routing, sessions, checkpoints, memory and a sandbox.
- Account, cloud and vendor-app commands (login, upgrade, mobile apps, cloud review, etc.) are
  out of scope and listed once at the end.

Status: **yes** (built in), **alias** (another name for a ternly command), **M6** (plugins, skills,
agents, hooks), **no** (a reason is given).

## Session and conversation
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/help` (`/?`) | CC CX GM AI OC | commands, with descriptions; `/help <cmd>` for one | yes |
| `/clear` (`/reset`) | CC GM AI OC; `/reset` CC AI | new conversation in this session (history cleared, files kept). CC also aliases `/new` to it, but ternly's `/new` is a new *session* (CX, OC) | yes |
| `/new` | CX OC | start a new session in place | yes |
| `/compact [instructions]` (`/compress`, `/summarize`) | CC CX OC; `/compress` GM; `/summarize` OC | summarise older turns with the cheapest model, optionally steered by instructions | yes |
| `/resume [id]` (`/continue`, `/chat`, `/sessions`) | CC CX GM OC; `/continue` CC OC; `/chat` GM | pick or switch to a saved session | yes |
| `/fork [n\|id]` (`/branch`) | CX; `/branch` CC | a new session from this one, optionally from before turn n | yes |
| `/rename <title>` | CC CX | rename the session | yes |
| `/delete <id>` | CX | delete a session (with confirmation) | yes |
| `/export [md\|json] [file]` | CC GM OC | export the conversation | yes |
| `/undo` | AI OC; alias of `/rewind` in CC | revert the last turn (files and conversation) | yes |
| `/rewind [n] [both\|code\|chat]` (`/checkpoint`) | CC GM | restore files and/or conversation to before turn n | yes |
| `/btw <question>` (`/side`) | CC; `/side` CX | a side question answered with the current context, not added to the history | yes |
| `/copy [n]` | CC CX GM AI | copy the last (or nth-latest) answer to the clipboard (OSC 52, else wl-copy/xclip/pbcopy) | yes |
| `/exit` (`/quit`, `/q`) | CC CX GM AI OC | exit (the session is saved as paused) | yes |
| `/pause`, `/stop` | ternly (requirement 10) | stop at a safe point and save / save as stopped and exit. CC's and CX's `/stop` stop background work, which ternly doesn't run | yes |

## Models, cost and context
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/model [id\|auto]` | CC CX GM AI | pin a model, or return to cost-aware routing | yes |
| `/models [filter]` | AI OC | list discovered models (tier, price, context); with routing v2, each model's rank for T1–T3, p(success) and a T2 task's expected time and cost; `/models why [model]` spells out every term and its source | yes |
| `/cost` (`/usage`, `/stats`, `/tokens`) | CC (alias of `/usage`); `/usage` CC CX; `/stats` CC GM; `/tokens` AI | session tokens, cache hits, spend, guard counters | yes |
| `/context` | CC | context usage: system prompt, tools, history, memory notes, against the model's window | yes |
| `/status` | CC CX | version, model, mode, verify command, limits, budget, sandbox, code graph, memory | yes |
| `/budget <usd>`, `/limits …` | ternly | spend cap and per-turn limits | yes |

## Setup
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/init` | CC CX GM OC (CR: "Initialize project") | analyse the repository and write `AGENTS.md` (the most common target: CX OC CR; CC writes CLAUDE.md, GM GEMINI.md; ternly reads all of them) | yes |
| `/memory [search\|forget\|edit\|add\|promote]` (`/memories`) | CC GM; `/memories` CX | view and edit ternly's memory (ADR 009). CC and GM edit instruction files instead | yes |
| `/config [key value]` (`/settings`) | CC; `/settings` CC GM | show effective settings and the config file; set session settings | yes |
| `/permissions` (`/allowed-tools`) | CC CX GM; `/allowed-tools` CC | permission mode and the actions allowed for this session | yes |
| `/mode ask\|edits\|yolo\|plan` | ternly | permission mode (`plan` is read-only, as `/plan`) | yes |
| `/verify <cmd\|off>` | ternly | the check run after edits (auto-detected by default) | yes |
| `/doctor` | CC | check sandbox, git, ripgrep, Go, providers, models, local servers, graph and memory | yes |
| `/theme [dark\|light]` | CC CX GM OC CR | switch the colour theme | yes |
| `/about` | GM | version and build | yes |
| `/tools` | GM | the tools the model can call | yes |
| `/refresh` | ternly | re-discover providers and models | yes |
| `/why` | ternly | how routing ranks every model for the current context (p, time, money, quota, score; why a model is ineligible), from the status interface (ADR 021, 022) | yes |
| `/vim` | CX GM (removed from CC in v2.1.92) | vim editing mode | no: removed from CC, and textarea vim mode is out of scope |
| `/terminal-setup`, `/keybindings`, `/keymap`, `/statusline`, `/title`, `/editor` (GM: editor *selection*) | CC CX GM | terminal and UI customisation | no |

## Extensions
| ternly | sources | semantics | status |
|---|---|---|---|
| `/mcp [login\|logout <server>]` | CC CX GM | list MCP servers, their tools, and (remote) protocol era, auth state and granted hosts; OAuth login/logout | yes; plugin servers: `/plugin` |
| `/commands [reload]` | GM | list user-defined commands; reload them from disk | yes |
| `/plugin` (`/plugins`, `/extensions`) | CC CX GM (`/extensions`) | install (pinned, reviewed), update with a diff, remove, enable, disable, scope, info, marketplaces, catalog search, `suggestions` (what became of capability suggestions: precision and false-suggestion rate by detection path, from the local log); see `docs/compat.md` | yes |
| `/skills` | CC CX GM | skills, agents and rules available to the model, with their token cost | yes |
| `/agents` | CC GM | subagents the model can delegate to (`task`) | yes |
| `/hooks` | CC CX GM | lifecycle hooks: shown per plugin in `/plugin info` | no: managed through `/plugin` |

## Code review and changes
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/review` | CC CX | review uncommitted changes with the strongest model | yes |
| `/diff` | CC CX AI | working-tree changes: `git diff` plus untracked files; without git, changes since the session's first checkpoint | yes |
| `/commit [message]` | AI | commit all changes; without a message, the cheapest model writes one from the diff, shown for confirmation | yes |
| `/git <args>` | AI | run a git command in the workspace (output shown, not added to the conversation) | yes |
| `/redo` | OC | redo an undone turn | no: `/rewind` keeps every checkpoint, so going forward again is `/rewind` to a later turn of the fork |

## Planning and modes
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/plan [prompt]` | CC CX GM | read-only mode: the model may read, search and run safe commands but not edit; with a prompt, starts the task. `/plan off` leaves | yes |
| `/ask [prompt]` | AI | one read-only turn (with a prompt), or switch to read-only mode | yes |
| `/code [prompt]` | AI | back to normal (editing) mode; with a prompt, one turn | yes |
| `/architect [prompt]` | AI | two models: the strongest plans (read-only), then the cheapest capable model makes the edits | yes |

## File context (Aider) and shell
| ternly | sources | semantics in ternly | status |
|---|---|---|---|
| `/add <files>` (`/mention`) | AI; `/mention` CX | pin files: their current contents go with every prompt until dropped | yes |
| `/drop [files]` | AI | unpin files (all when no argument) | yes |
| `/ls` | AI | list pinned files | yes |
| `@path` in a prompt | GM OC | include that file's contents with the prompt | yes |
| `/run <cmd>` (`!cmd`) | AI; `!` GM OC AI | run a shell command in the sandbox (permission rules apply); the output is added as context for the next prompt | yes |
| `/test [cmd]` | AI | run the tests (default: the verify command); on failure the model is asked to fix them | yes |
| `/lint [cmd]` | AI | run the linter (default: detected, e.g. `go vet ./...`); on failure the model fixes | yes |
| `/web <url>` | AI | fetch a page as text and add it as context for the next prompt | yes |
| `/editor` (`/edit`) | OC; AI | compose the prompt in `$EDITOR` | yes |
| `/read-only`, `/map`, `/map-refresh`, `/load`, `/save`, `/paste`, `/voice`, `/multiline-mode`, `/ok`, `/chat-mode`, `/context` (AI's *mode*), `/reasoning-effort`, `/think-tokens`, `/editor-model`, `/weak-model` | AI | Aider-specific modes and settings | no: the code graph replaces the repo map; routing replaces per-role model switches |

## User-defined commands
Markdown files become commands. The file name gives the name, and subdirectories give
`dir:name`. ternly reads, in order of precedence:

| location | origin | argument syntax |
|---|---|---|
| `.ternly/commands/`, `~/.config/ternly/commands/` | ternly | `$ARGUMENTS`; `$1`…`$9` (1-based) |
| `.opencode/commands/`, `~/.config/opencode/commands/` | OC | `$ARGUMENTS`; `$1`…`$9` (1-based) |
| `.claude/commands/`, `~/.claude/commands/` | CC | `$ARGUMENTS`; `$0`…`$9` and `$ARGUMENTS[N]` (**0-based**, as CC documents) |
| `~/.codex/prompts/` | CX (deprecated by CX in favour of skills) | invoked as `/prompts:<name>`; `$1`…`$9`, `$ARGUMENTS`, `$NAME` from `NAME=value` |
| `.gemini/commands/*.toml`, `~/.gemini/commands/*.toml` | GM | `prompt` field; `{{args}}` |

- **Frontmatter:** `description` and `argument-hint` (shown in completion).
- **Unused arguments:** if a template has no placeholder, the arguments are appended as
  `ARGUMENTS: …` (as CC does).
- **Shell output:** `` !`cmd` `` (OC) and `!{cmd}` (GM) run through the sandbox and the permission
  policy, and their output is inlined.
- **Untrusted text:** project commands are repository content and are labelled `(project)` in
  completion. Their text is sent as your prompt only when you run them.
- **Precedence:** a project command overrides a user command of the same name. No user-defined
  command can shadow a built-in.

## Completion
Typing `/` opens a list of commands (built-in and user-defined) with descriptions. It filters by
fuzzy match as you type; Tab completes and ↑/↓ choose.

## Out of scope (account, cloud and vendor-app features)
- **CC:** `/login`, `/logout`, `/upgrade`, `/usage-credits`, `/passes`, `/mobile`, `/desktop`,
  `/chrome`, `/remote-control`, `/remote-env`, `/teleport`, `/web-setup`, `/install-github-app`,
  `/install-slack-app`, `/artifacts`, `/design*`, `/slides`, `/radio`, `/stickers`, `/powerup`,
  `/privacy-settings`, `/rate-limit-options`, `/autofix-pr`, `/ultrareview`, `/schedule`,
  `/background`, `/tasks`, `/workflows`.
- **CX:** `/logout`, `/app`, `/apps`, `/archive`, `/feedback`, `/experimental`, `/approve`, `/pets`,
  `/personality`, `/ps`, `/raw`, `/fast`, `/goal`, `/setup-default-sandbox`,
  `/sandbox-add-read-dir`.
- **GM:** `/auth`, `/bug`, `/docs`, `/privacy`, `/upgrade`, `/setup-github`, `/ide`, `/directory`,
  `/shells`, `/policies`, `/restore` (ternly's `/rewind` covers it).
- **OC:** `/connect`, `/share`, `/unshare`, `/details`, `/thinking`, `/themes` (ternly: `/theme`).
