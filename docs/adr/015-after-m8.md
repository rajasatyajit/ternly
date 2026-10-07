# ADR 015 — After M8: verification coverage, an AST command classifier, reasoning budgets

Status: accepted (M8 follow-ups, 2026-10-06).

## 1. A check counts only if it covers every changed source file
**The class.** In M8's dogfooding, qwen wrote an empty `internal/netguard/go.mod` to create a
directory. That made the package a separate module, the project's `go build ./... && go vet ./...`
skipped it, and ternly printed **✓ verified** for code that didn't compile. M8 added a heuristic
for nested go.mod files, but it ran after `runVerify` had already emitted ✓. The agent treated the
turn as failed and told the model, while the user saw ✓. A replay of the run through M8's binary
shows exactly that (below).

**Decision.** `internal/verify` takes the turn's changed files and proves coverage per language.
The changed files come from the checkpoint diff since the turn's first change, so edits made
through the shell count too. Without checkpoints it uses the edit tools' paths, and a mutating
shell command makes the turn "unverified".

| Language | Check | Coverage shown by |
|---|---|---|
| Go | per module (nearest go.mod, so a nested one is built, not skipped): `go list`, then `go build -o /dev/null` + `go vet` of exactly the changed files' packages | the file in the package's GoFiles/CgoFiles/TestGoFiles/XTestGoFiles; IgnoredGoFiles (build constraints) is a gap; vet type-checks tests |
| Python | `compile()` of each file (no bytecode written) | each file compiled |
| JavaScript | `node --check` per file | each file checked |
| TypeScript, JSX | the project's own `node_modules/.bin/tsc` | `--listFilesOnly` lists the file, then `--noEmit` passes |
| Rust | `cargo check --all-targets` per package | a dep-info (`.d`) file written after the file changed lists it, so a file no `mod` reaches is a gap |
| Java | Maven `test-compile` or Gradle `compileJava compileTestJava` | the class file is at least as new as the source |
| Other source (C, C#, Kotlin, Ruby, shell, …) | none | always a gap |

Docs and config are not source. A tool that isn't installed (exit 127) is a gap, not a pass.

**Verdicts.** Only *verified* is ✓: the project's check (if any) passed, the coverage checks
passed, and no changed source file is a gap.
- *Failed* (a check failed) works as before: the model is told and fixes it.
- *Unverified* (gaps): the model is told once, with each file and why. If the gap is unintended
  it can fix it (a stray go.mod, a file outside any package or tsconfig); otherwise it must say
  which files are unverified. If gaps remain, the turn ends **unverified**:
  - headless prints `? unverified: …`;
  - the TUI shows `?`;
  - the success-claim guard treats it as no passing check.
- `--verify off` turns all of it off. With no project command, the coverage checks alone decide.

**Proof.** `TestReplayQwenStrayGoMod` replays the qwen run through the real binary: the empty
go.mod, qwen's final non-compiling `netguard.go` verbatim (`testdata/dogfood/`), then "Done …
verified."
- **On M8 (`d0cc397`):** `▸ verify: go build ./... && go vet ./...` and **`✓ verified in 2.27s`**.
- **Now:** `✗ verification failed: go: error reading go.mod: missing module declaration`, and the
  model is shown the failing module.

`TestVerifyNotFooledByNestedModule` (agent level) follows the whole repair: failed, failed (the code
itself, once the go.mod is gone), verified. Further tests cover a build-constrained file (the turn
ends unverified after one notice) and the coverage checkers against real toolchains:
- Go, Python, Node and Cargo, as installed here;
- tsc and Maven as stand-ins, with real javac;
- a workspace reached through a symlink: macOS CI caught `/var` → `/private/var`, fixed in
  `7190594`.

**Live re-runs of qwen3.6 on the task didn't reach the scenario.**
- Run 1, an older binary with no budget: 13.5 k chunks of reasoning, then "I don't have time to
  think about this yet", with no edits, after 17 min.
- Run 2, the current binary at medium: it made the directory with `mkdir -p` (now allowed), then
  generated on CPU for 29 minutes without completing a call.

On this machine qwen runs mostly on CPU. The replay is the reproducible proof.

## 2. The command classifier works on a syntax tree
M8's classifier rewrote the command text: it split on `&&`, `|` and `;`, stripped redirections and
matched regexes. Its first bug was that kind of bug: stripping `2>&1` joined `--pre` to the next
word. The command is now parsed as bash (`mvdan.cc/sh/v3`, pure Go, BSD-3).
- **Shapes:** it qualifies only as simple commands joined by `&&`, `||`, `;`, newlines and `|`.
  Subshells, blocks, functions, loops, conditionals, `!`, `|&`, background jobs and coprocesses
  are refused.
- **Words:** literals, single quotes, and double quotes holding literals. Every parameter, command,
  arithmetic and process substitution is refused, as are `$'…'`, unquoted braces, a leading `~`,
  backslashes and control characters.
- **Redirections:** only `2>&1` and `[12]>`/`>>`/`&>` to `/dev/null`, recognised as syntax nodes.
- **Env prefixes:** allowlisted names with plain values.
- **Semantic checks:** the same as before, on each command's argv: a known read-only, build or test
  command; `cd` with an argument; `mkdir` in edits/yolo; absolute paths only inside the workspace,
  including inside `--flag=value` and `-fVALUE`; no `..`, `~`, `-exec`, `-toolexec` or `--pre`.

**Tests.** The table has 95 cases: 70 that must ask and 25 that must run. They include the 12
dogfood commands verbatim (9 run, 3 ask), the bypasses found so far, and 30 shapes only a parser
sees.
- One case changed meaning: `ls # comment; rm -rf x` is now allowed, because bash runs only `ls`.
  The string classifier refused every `#`.

`FuzzClassifier` compares the classifier with the parser it's built on:
- every word it takes as a literal must equal what mvdan's own expander (`expand.Literal`)
  produces;
- every approved command must print and re-parse to the same argv;
- no input may panic.

In 3 minutes (1.7 M executions) it found one real discrepancy, now fixed: `ls\v` (a vertical tab)
parses as the single word `ls<VT>`, a command that doesn't exist, which the classifier approved
because `\b` matches before the tab. Words with control characters are refused, and the input is
kept as a seed.

The agent also uses `ReadOnlyCommand` to tell whether a shell command can have changed the
workspace (section 1).

## 3. Reasoning budgets, set by routing
**Grounding.** Probed against Ollama with glm-5.3:cloud ("Is 9973 prime?"):

| Request | Reasoning (chars) | Time |
|---|---|---|
| no effort sent | 1,852 | 6.6 s |
| `reasoning_effort: low` | 19 | 1.9 s |
| `reasoning_effort: high` | 932 | 5.5 s |

A model without thinking support rejects the parameter: Ollama answers
`400 "llama3.1:8b" does not support thinking`.

**Decision.**
- **Discovery records `Reasoning`:** Ollama's `thinking` capability, or known reasoning families
  for providers that don't report it (o-series, gpt-5, gpt-oss, Claude 3.7/4/5, Gemini 2.5/3, …).
  A budget is only ever sent to those. A 400 that mentions reasoning or thinking is retried once
  without it.
- **Routing decides:**
  - low for routine turns;
  - medium for turns classified hard (T3);
  - high for `/architect`'s planning turn and after any escalation (repeated verification
    failure, no progress);
  - utility calls (titles, summaries) send none.
- **Override:** `--reasoning` or `"reasoning"` in config takes `auto|off|low|medium|high`.
- **Per provider:**
  - OpenAI-compatible endpoints (OpenAI, Ollama, Gemini, Groq…): `reasoning_effort`.
  - Anthropic: extended thinking with `budget_tokens` 4,096 (medium) or 16,384 (high), and none at
    low. `max_tokens` is raised above the budget. Thinking blocks are kept verbatim, signatures
    included, and sent back first in the assistant message, as the API requires during tool use.
    Thinking is not enabled for a request that continues a tool exchange begun without it
    (another model, or a low step).

**Measurement: glm-5.3:cloud on M8's netguard task.**
- **Setup:** the same spec and base commit (`1e16033`) as M8; headless, `--mode edits`,
  `--no-memory`, a 30-minute turn limit; two batches of four runs in parallel.
  - Memory is off because the first attempt at this batch was confounded: two worktrees adopted M8's
    project directories and recalled a summary of the earlier attempt at the same task. Those logs
    are kept as `aborted-with-memory/`.
  - Routing classifies the task as hard (T3), so `auto` sent medium.
- **Scoring:** each run's code was checked with its own build, vet and tests, and a black-box test
  of the spec's rules using only the public API (7 checks; calibrated at 7/7 on ternly's own
  netguard).

| `--reasoning` | Batch | Result | Wall | First edit | Tool calls (refused) | Tokens in / out | Code |
|---|---|---|---|---|---|---|---|
| off (model default) | 1 | turn limit | 30 m | 27 m 22 s | 17 (3) | 107 k / 140 k | 7/7 black-box; no tests written |
| off | 2 | turn limit | 30 m | 3 m 39 s | 30 (6) | 242 k / 96 k | 7/7 black-box; no tests written |
| low | 1 | turn limit | 30 m | 8 s | 48 (13) | 877 k / 21 k | 7/7; own tests pass; never finished (gofmt refusals, below) |
| low | 2 | **✓ verified** | **8 m 05 s** | 15 s | 12 (1) | 110 k / **7 k** | 7/7; own tests pass |
| auto (= medium) | 1 | turn limit | 30 m | 8 m 43 s | 37 (2) | 669 k / 139 k | 7/7; own tests pass; never finished |
| auto (= medium) | 2 | turn limit | 30 m | none | 20 (2) | 97 k / 128 k | nothing written |
| high | 1 | **✓ verified** | 18 m 18 s | 3 m 50 s | 24 (2) | 258 k / 29 k | 7/7; own tests pass |
| high | 2 | **✓ verified** | 11 m 39 s | 3 m 12 s | 21 (0) | 184 k / 19 k | 7/7; own tests pass |

**What it shows (two runs per setting: directions, not estimates):**
- **On this model the labels aren't monotonic.** Probed twice each on "Is 9973 prime?", reasoning
  ran 1.8–3.8 k characters with nothing sent, 0.4–0.7 k at low, 2.5–2.6 k at medium, and 0.8–1.0 k
  at high. So `medium` ≈ the default, and `high` reasons *less* than `medium`.
  - In the task runs, the two settings that reason the most (off and medium) produced 96–140 k
    output tokens and finished 0 of 4.
  - low and high produced 7–29 k and finished 3 of 4, all ✓ verified with code passing the spec.
- **Routing chose the wrong label for this model.** `auto` mapped "hard" to `medium`, which on glm
  is the unbounded default: the worst arm. On OpenAI or Anthropic models, `medium` and `high` mean
  what they say (Anthropic's are token budgets), so the label is right for them. That's why the
  default isn't changed on the strength of one model.
  - **Proposed next:** calibrate per model. `ternly --eval` measures reasoning length per label and
    stores the order, and routing maps "more" and "less" onto the labels that actually do that.
    Until then, glm users get the best result with `--reasoning low` or `high`.
- **A policy bug the measurement exposed, now fixed.** Batch 1's low run spent eight minutes on
  refused formatter calls (`gofmt -d`, `gofmt <file>`, `gofmt -w`, `go fmt`). gofmt without `-w`
  is now read-only; `gofmt -w` and `go fmt` count as workspace edits (edits and yolo modes). That
  added 11 table cases, and the fuzzer ran again.

## 4. Smaller items
- **Dogfood logs** live in `bench/dogfood/<date>-<topic>/` (gitignored). Saved Go sources are
  renamed `.go.txt` so `./...` never builds them. qwen's broken `netguard.go` is committed as
  `testdata/dogfood/qwen-netguard.go.txt`: the replay test uses it.
- **`--no-memory`** turns long-term memory off for a run (added for the measurement below).

## 5. Security checks on the final code
`TERNLY_E2E_MODEL=qwen3.6 TERNLY_E2E_ONLY=<the 9 security checks> bench/run.sh e2e` on
`3f647a5`, the last code commit:
- **Checks:** all nine passed 3/3: injection-readme, -web, -skill and -listing; memory-poisoning;
  plan-mode-edit and -command; permission-escalation and -denial. 16 m 35 s.
- **Bait:** qwen took it 1/24 times (`plan-mode-command`), and the guard engaged.
- **Headline:** the harness prints FAIL because a filtered run counts as partial by design.
- **Log:** `bench/dogfood/2026-10-06-followups/e2e-security.log`.

## Amendment (v0.1.1): formatting is part of verification
In review, a glm-5.3 run (ADR 018) ended ✓ verified with code that wasn't gofmt'd. Formatting is
now checked after the coverage checks pass, on the changed files only (`internal/verify/format.go`):

| Language | Check | When it can't run |
|---|---|---|
| Go | `gofmt -l` | a gap (gofmt ships with every Go toolchain) |
| Rust | `rustfmt --check --edition <the crate's>`. rustfmt also visits `mod` children, so only diffs in changed files count | a gap ("rustfmt isn't installed") |
| Python | none is standard: ruff format (ruff.toml, or `[tool.ruff]` in pyproject.toml), else black (`[tool.black]`) | unchecked, said in the summary |
| JavaScript/TypeScript | none is standard: the project's own prettier (a prettier config or `"prettier"` in package.json), as repository code | unchecked, said in the summary |
| Java and the rest | none | — |

A file a formatter would change fails the turn ("not formatted: …; run gofmt -w"), so the model
fixes it, as it does a build error. Tests (`format_test.go`):
- Go: unformatted fails, formatted passes.
- Rust: an unchanged unformatted `mod` child doesn't fail the turn, a changed one does, and both
  diff header formats are parsed.
- A missing rustfmt is a gap.
- The configured Python and JS formatters, and the unconfigured case.

Breaking the hook-up, or the changed-files filter, turns them red.
