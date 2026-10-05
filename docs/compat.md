# Compatibility: other harnesses' extensions in ternly

"Any and all" isn't achievable, so this page says exactly what loads. Formats were checked against
each harness's current documentation on 2026-10-05; the sources are in ADR 011. Whatever a plugin
contains that isn't loaded is listed, with the reason, when you review it (`/plugin add`) and in
`/plugin info <name>`.

**Security model, for every format:**
- Every plugin is untrusted code until you approve exactly what it will run.
- Installs are pinned to a commit, or a content hash for local directories.
- Hooks and MCP servers run confined:
  - none of your home, keys, ternly's data or your environment;
  - the workspace read-only and the network as scoped (`/plugin scope`);
  - nothing at all without bubblewrap, so on macOS plugins are prompt text only.
- An update, or a change on disk, that alters what runs needs approval again with a diff.
- Trust labels come from where a plugin was fetched, never from what it says about itself.
- Plugins can't grant permissions: a hook can deny but never allow, and `allowed-tools`
  (skills), `permissionMode` (agents) and `permission` (OpenCode) are ignored.

## Claude Code
| Feature | ternly | Notes |
|---|---|---|
| Plugin `.claude-plugin/plugin.json` (name, version, description, author, …) | yes | optional, as in Claude Code; unknown keys ignored |
| Standard layout without a manifest | yes | |
| `skills/<name>/SKILL.md`, root `SKILL.md`, manifest `skills` paths | yes | listed for the model by description; the body loads only through `use_skill`; `/plugin:skill` runs it as a command |
| Skill frontmatter `name`, `description`, `when_to_use`, `argument-hint`, `disable-model-invocation` | yes | |
| Skill `allowed-tools` | **ignored** | a skill can't grant permissions |
| Skill `hooks`, `context: fork`, `agent`, `model`, `effort`, `shell`, `paths` | no | reported per skill |
| `${CLAUDE_SKILL_DIR}`, `${CLAUDE_PLUGIN_ROOT}`, `${CLAUDE_PROJECT_DIR}` in bodies | yes | |
| `commands/*.md` (and manifest command paths) | yes | `/plugin:name`; arguments as Claude Code (0-based `$0`); inline command maps aren't supported |
| `agents/*.md` (`name`, `description`, `tools`, `model`) | yes | a `task` tool delegates to them: own system prompt, the listed tools mapped to ternly's, never `task` itself, same permission policy; `model` is advisory (routing decides) |
| Agent `permissionMode`, `hooks`, `mcpServers`, `disallowedTools`, `isolation`, `memory`, `skills` | **ignored** / no | reported |
| `hooks/hooks.json` and manifest `hooks`: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart` | yes | command hooks, confined, Claude Code stdin JSON (tool names mapped: `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`); exit 2 or `permissionDecision: deny` blocks; `allow`/`ask` are ignored; `additionalContext` and plain output reach the model framed as untrusted |
| Other hook events (`Stop`, `PreCompact`, `Notification`, … 30 more) | no | reported |
| `http`, `prompt`, `mcp_tool`, `agent` hook types; `async` | no / sync | reported |
| `.mcp.json` and manifest `mcpServers` (stdio: `command`, `args`, `env`) | yes | confined; tools named `mcp__plugin_<plugin>_<server>__<tool>`, registered only after `initialize` + `tools/list` |
| Remote MCP in plugins (`http`), `sse`, `ws`, `.mcpb` bundles | no | reported; a plugin's `http` server can be added to `~/.config/ternly/mcp.json` (remote servers there are supported, ADR 014) |
| `userConfig` | no | values a server needs: `/plugin env <name> KEY=VALUE` |
| `lspServers`, `outputStyles`, `workflows`, `themes`, `monitors`, `channels`, `bin/`, `dependencies`, `settings` | no | reported |
| Marketplaces (`.claude-plugin/marketplace.json`) | yes | `/plugin marketplace add owner/repo`; sources: relative path, `github`, `url`, `git-subdir` (with `ref`/`sha`); `npm`, `archive`, `command` sources aren't supported; `strict: false` entries without `plugin.json` act as the manifest (name and listed skills) |
| Plugins installed for Claude Code (`~/.claude/plugins/cache`) | import | `/plugin import <name>`: same review and approval |
| Personal `~/.claude/skills`, `~/.claude/agents`; project `.claude/skills`, `.claude/agents` | yes | prompt text only, loaded automatically |
| `.claude/commands`, `~/.claude/commands` | yes | M5 (`docs/commands.md`) |
| Hooks in `.claude/settings.json` | no | repository hooks never run automatically; package them as a plugin to review them |
| `.mcp.json` in the workspace | opt-in | `--project-mcp` (M1) |

## Gemini CLI extensions
| Feature | ternly | Notes |
|---|---|---|
| `gemini-extension.json` (`name`, `version`, `description`) | yes | |
| `mcpServers` (stdio), `${extensionPath}`, `${workspacePath}` | yes | confined |
| `contextFileName` / `GEMINI.md` | yes | added to the instructions at start-up |
| `commands/**/*.toml` | yes | `{{args}}`, `!{…}` (through the sandbox and policy) |
| `skills/<name>/SKILL.md`, `agents/*.md` | yes | as Claude Code |
| `hooks/hooks.json` `BeforeTool` / `AfterTool` | yes | mapped to `PreToolUse` / `PostToolUse`; timeouts in ms honoured |
| Other hook events | no | reported |
| `excludeTools`, `settings`, `themes`, `plan`, `policies/` | no | reported |
| Installed extensions (`~/.gemini/extensions`) | import | `/plugin import <name>` |
| Agents in `.gemini/agents`, `~/.gemini/agents` | yes | prompt text only |

## Cursor
| Feature | ternly | Notes |
|---|---|---|
| `.cursor/rules/**/*.mdc` with `alwaysApply: true` | yes | added to the instructions at start-up |
| Rules with `description` or `globs` | yes | offered through `use_skill` by description; globs are shown, not auto-attached |
| `~/.cursor/rules` | yes | |
| Legacy `.cursorrules` | yes | treated as an always-apply rule |
| `@file` references inside rules | no | |

## OpenCode
| Feature | ternly | Notes |
|---|---|---|
| Agents in `.opencode/agents/`, `~/.config/opencode/agents/` (and singular `agent/`) | yes | subagents (`mode: subagent` or `all`); the file name is the agent's name |
| `mode: primary` agents | no | they replace the main agent |
| Agent `permission`, `tools`, `temperature`, `steps` | **ignored** / no | permissions can't be changed by an agent |
| Skills in `.opencode/skills`, `~/.config/opencode/skills` | yes | |
| Commands (`.opencode/commands`) | yes | M5 |
| `opencode.json` `mcp` | no | add servers to `~/.config/ternly/mcp.json` |

## Codex
| Feature | ternly | Notes |
|---|---|---|
| Skills in `.agents/skills`, `~/.agents/skills` | yes | |
| `~/.codex/prompts` | yes | M5 (`/prompts:name`) |
| `config.toml` `[mcp_servers.*]` | read | listed by `/plugin import codex-mcp`; copy them into `~/.config/ternly/mcp.json` (they are your own config) |
| `AGENTS.md` | yes | read since M1 (with `CLAUDE.md`, `TERNLY.md`) |

## AGENTS.md
Read at the workspace root (M1). Nested `AGENTS.md` files in subdirectories aren't merged yet.
