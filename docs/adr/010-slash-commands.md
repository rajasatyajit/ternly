# ADR 010 — M5: slash-command parity, user-defined commands, completion

Status: accepted (M5). Bubble Tea v2 migration: ADR 006.

## Survey
On 2026-10-05, the built-in commands of six harnesses were collected from their current official
documentation: Claude Code, Codex CLI, Gemini CLI, Aider, OpenCode and Crush. Every command was
seen on a cited page; nothing came from memory or third-party sites. `docs/commands.md` maps each
one to ternly's status.

Two changes since the requirement was written:
- Claude Code's command reference moved to `code.claude.com/docs/en/commands`, its "custom
  commands" were merged into skills, and `/vim` was removed in v2.1.92.
- Codex's custom prompts are deprecated in favour of skills. Gemini's command reference moved to
  `docs/reference/commands.md`.

Crush documents a command palette (ctrl+p), not slash commands.

## Decisions
- **One table.** Each built-in has its name, aliases, argument hint, section and description. `/help`,
  completion and the docs check (`TestCommandsDocumented`) are all driven by it.
  - A docs row marked *yes* must resolve to a command.
  - Every built-in must appear in `docs/commands.md`.
- **Where harnesses disagree, the most common meaning wins, and the others become aliases.**
  - `/clear` = new conversation; `/new` = new *session* (CX, OC). CC aliases `/new` to `/clear`.
  - `/cost`, with aliases `/usage`, `/stats` and `/tokens`.
  - `/resume`, with aliases `/continue`, `/chat` and `/sessions`.
  - `/compact`, with aliases `/compress` and `/summarize`.
  - `/init` writes `AGENTS.md`, which is what CX, OC and CR write. ternly reads AGENTS.md,
    CLAUDE.md and TERNLY.md alike.
  - `/diff` = working tree plus untracked files (CC, CX). AI's "since the last message" is what
    `/rewind` shows.
- **Plan mode** is a permission mode, so it is enforced, not requested.
  - **What's allowed:** read-only tools and the commands that are safe in any mode (`git
    status/diff/log`, `go test/vet`, `rg`, …; no chaining or redirection).
  - **What's refused:** edits, other commands and MCP tools, with a reason that tells the model
    how the user leaves plan mode.
  - **The model is told too:** each prompt in plan mode carries a note saying so.
  - **Commands:** `/plan` and `/ask` enter it; `/code` and `/plan off` leave. `/ask <prompt>` uses it
    for one turn only.
  - **Tested:** in the pty test the scripted model asks to edit, the edit is refused and the file
    is untouched.
- **`/architect`** uses ternly's routing rather than Aider's two fixed models. The strongest
  available model plans in plan mode. Then the pin is released and the router picks the
  cheapest capable model for the implementing turn. If planning fails, implementing isn't started.
- **Context that isn't the prompt** goes with the user message but not into the turn's record, via
  `Agent.RunWith`, so titles, `/rewind` and memory see only what the user typed:
  - pinned files (`/add`, `/drop`, `/ls`);
  - `@path` mentions;
  - output collected with `!cmd`, `/run` or `/web`;
  - the plan-mode note.
- **File and page text is framed as untrusted data, and redacted:**
  - Pinned files are capped at 40 KB each and 120 KB in total.
  - Secret-like paths (the checkpoint patterns) are refused.
  - `/web` keeps a page's readable text, up to 60k characters, and warns if it looks like
    instructions.
- **Shell from commands goes through the registry's `bash` tool**, so the permission policy,
  sandbox, redaction and output cap all apply. That covers `!cmd`, `/run`, `/test`, `/lint` and a
  template's `` !`cmd` `` / `!{cmd}`.
  - `/test` and `/lint` hand a failure to the model as a fixing turn.
  - `/git`, `/diff` and `/commit` run git inside the sandbox, so repository config can't run code
    outside it. Calls pass `core.fsmonitor=false`, and `--no-ext-diff --no-textconv` for diffs.
- **`/commit`** always asks for confirmation, even in yolo mode. It commits tracked changes and new
  files, but never secret-like files (named in the dialog). Without a message, the cheapest model
  writes one from the diff, and its cost is counted.
- **`/btw`** asks the current model with the conversation as context and no tools. Nothing is added
  to the history, and its cost is counted (`TestAskAndRunWith`).

## User-defined commands
`internal/commands` reads each harness's documented locations and argument syntax:
- **ternly, OpenCode:** `$ARGUMENTS`, `$1…`, `` !`cmd` ``.
- **Claude Code:** `$0…` and `$ARGUMENTS[N]` (0-based, as its docs now specify), `\$`.
- **Codex:** `/prompts:name`, `$1…`, `$NAME` from `NAME=value`, `$$`.
- **Gemini CLI:** TOML `prompt` with `{{args}}`, and `!{cmd}` with the arguments shell-quoted.

Arguments split shell-style. With no placeholder, they're appended as `ARGUMENTS: …`.
- **Precedence:** project over user. A built-in can't be shadowed: such a file is reported at
  start-up and skipped.
- **Project commands are repository content.** They're labelled `(project)` in completion, and
  their text is sent as your prompt only when you run them.
- **`/commands reload`** re-reads them.

## Completion
Typing `/` opens a list of up to 8 built-in and user commands, filtered by a subsequence score:
- a prefix match ranks first, then word-start matches;
- aliases match at a small discount;
- shorter names win ties.

↑/↓ choose and Tab completes. Enter runs the highlighted command unless the typed name is already
exact; Esc closes the list.

## Tests
- `internal/commands`: precedence, the five formats, every argument syntax, shell hooks,
  shell-style splitting.
- `internal/tools`: `TestPlanMode`.
- `internal/tui`:
  - fuzzy ranking, HTML-to-text, lint detection;
  - docs and command table consistency.
- **`TestTUICommands`** (pty, real binary, scripted model):
  - completion;
  - a Claude-format user command;
  - `/add` and `/drop`;
  - `!cmd`;
  - plan mode refusing an edit;
  - `/commit` with its dialog, secret files skipped and the commit verified with git.
- **`TestTUIArchitectTestMention`** (pty): `/architect` runs two turns (the first read-only and with
  the plan note, the second without), `/test` failure leads to a fixing turn with the output, and
  `@path` is attached.

## Not done, and why
- **`/vim`:** removed from Claude Code, and a vim textarea is out of scope.
- **`/redo`:** `/rewind` keeps every checkpoint.
- **Aider's per-role model and mode switches:** routing replaces them.
- **Account, cloud and vendor-app commands:** out of scope; listed in `docs/commands.md`.
- **Requirements 8 and 9 (M6):** `/agents`, `/skills`, `/hooks`, `/plugins`. Skills, which Claude
  Code and Codex now use for custom commands, come with them.
