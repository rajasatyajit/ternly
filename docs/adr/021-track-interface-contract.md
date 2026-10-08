# ADR 021 — The contract between Track 1 (core) and Track 2 (UI)

Status: proposed (before Phase B and Phase F start in parallel worktrees).

## Problem
The v0.2 plan runs two tracks in parallel worktrees:
- **Track 1:** B → C → D → E (providers, quality, tokens, learning);
- **Track 2:** F (the terminal UI).

Track 2 "consumes routing and status data through interfaces defined at the start". Today there is
no such interface. `internal/tui` imports eleven core packages directly (agent, discover, session,
tools, llm, …) and reads their structs: `discover.Model`, `agent.Event`, `session.Meta`, the
policy's decisions.

Two tracks editing both sides of that seam would collide on every merge. Phase B adds exactly the
data Phase F most wants to show:
- connections ("what's connected, how, and what it costs");
- quota;
- the routing explanation.

## Decision
### 1. One package is the contract: `internal/surface`
- **Types and interfaces only:** no logic, no I/O, no imports of other ternly packages
  (`TestNoInternalImports`).
- **Changes need this ADR amended.** A field added or a method changed is a contract change, made
  in its own small PR that both tracks rebase onto.

**`surface.Status` (core → UI):**
- `Snapshot() Snapshot` returns values, not pointers into the core, so a UI may keep a snapshot
  while the core moves on.
- `Changes(ctx) <-chan struct{}` notifies the UI that the snapshot may have changed. Notifications
  are coalesced (at most one pending), so a burst of changes costs one redraw. This pull model
  keeps the UI's frame budget in its own hands.

**`surface.Actions` (UI → core):**
- `Pin(key)`;
- `Explain(difficulty, contextTokens)`;
- `Reconnect(ctx, id)`.

Each returns an error that the UI shows as is, written for a person.

**`Snapshot` contains:**

| Field | Contents | Filled by |
|---|---|---|
| `Connections` | id, label, kind (daemon / cli-bridge / api-key), how it was found, state (connected, not-installed, signed-out, unreachable, error), the next step when not connected, model count, `Quota` | Phase B |
| `Quota` | used/limit/unit, resets-at, exhausted-until, and the official signal it came from | Phase B; the quota detection already in v0.1.1 |
| `Models` | key, connection, local, tier and its basis, price for people, GPU fraction, `Trust`, pinned | today's discovery and evals; ADR 020 trust |
| `Routing` | version, current model, why (one line), why it switched (escalation, failover) | ADR 018 |
| `Explanation` | every model's `Estimate`: eligible / why not, p, seconds, money, quota, score, and where p and the speed came from | `Router.Explain` (ADR 018) |
| `Meter` | context used/max, cost, turns, cache rate | `agent.Ledger` |
| `Plan` | the model's plan or todo items with state | Phase F's plan panel; the core already has `agent.Plan` |

### 2. Who owns what
- **Track 1** owns the producer: an adapter in the core (`internal/status`, created by Phase B)
  that implements `Status` and `Actions` from discovery, the router, the agent and the evals.
- **Track 2** owns the consumer: the TUI reads this data only through `surface`.
  `surface/fake.Core` lets the TUI be built and tested before Phase B's adapter exists. Tests set a
  snapshot, call `Set`, and check what the UI asked for.
- **Neither track edits the other's packages.** A change on the other side becomes an issue or a
  contract PR.
- **The existing coupling stays.** That means the agent event stream, the session manager, and the
  permission prompts (extend, never rewrite: non-negotiable 3). Phase F moves a piece behind
  `surface` only when it rebuilds that piece, and adds it to this ADR when it does.

### 3. Budgets (part of the contract)
- `Snapshot()` does no I/O and never waits on a turn in progress. Its budget is **≤ 50 µs with 50
  models and 10 connections**, a fraction of the 16 ms frame budget (Phase F). Phase B adds a
  benchmark for it to `bench/perf.json`.
- `Changes` never blocks the core: notifications are coalesced and dropped when one is pending.
- `Explain` may compute, but in under 1 ms for 50 models; `Router.Explain` already does this.
- `Reconnect` may do I/O. It takes a context, and the UI runs it off the render path.

### 4. Security
- **No secrets cross the contract:**
  - no API keys, tokens, cookies or session files;
  - an account label only if the provider's official interface reports one.
- `TestNoSecrets` fails on a field whose name suggests one. A redaction test belongs to the
  adapter (Phase B): it checks real snapshots built with keys in the environment.
- **`Connection.Detail` is ternly's own text, never a provider's raw error body.** Provider text is
  untrusted (ADR 001), so a UI must not render it as markup.
- **The trust state is shown, not editable:** `Actions` has no way to grant trust (ADR 020).

### 5. Working in parallel
- **This PR lands on main first** (the ADR, `internal/surface` and its fake). Both worktrees start
  from it.
- **Worktrees:**
  - `.claude/worktrees/phase-b` on branch `track1/phase-b`;
  - `.claude/worktrees/phase-f` on branch `track2/phase-f`.
- **Each phase merges in small PRs:**
  - each rebases on main at least daily, and before every PR;
  - the perf gate runs on each one.
- **A contract change is its own PR,** reviewed like any other. The other track rebases onto it
  before it uses the change.
- **Review:** stop after each phase (Phase B, then Phase F), each with its own ADR, baseline
  comparison and `bench/run.sh e2e` output.

## Proof in this PR
- `TestNoInternalImports`, `TestNoSecrets`, and `TestFakeChanges` (coalescing; the channel closes
  with the context).
- Each was broken in turn and failed:
  - an `APIKey string` field;
  - an import of `internal/llm`;
  - a fake buffering two notifications.

## Amendment 1 (2026-10-08): per-hunk review of edits

Decided in the Phase F review: an edit that would ask a person is shown as hunks, and only the
accepted hunks are applied.

**Contract (`internal/surface`):**
- `EditProposal{Tool, Path, Why, NewFile, Hunks}`: one file's proposed change. `Why` is set when
  the ask comes from a rule such as lost trust (ADR 020).
- `Hunk{OldStart, OldLines, NewStart, NewLines, Lines}`: as in a unified diff. Lines start with
  space, `-` or `+`.
- `EditDecision{Apply []bool, Always}`: one entry per hunk. None set means declined.
- `Reviewer func(ctx, EditProposal) EditDecision`: the UI provides it, the core calls it.

**Rules the core keeps:**
- **Same places as today.** The reviewer is asked only where the policy would otherwise ask yes or
  no: an edit in ask mode, or a restricted model's edit outside a checkpointed edits/yolo mode.
  Nothing that runs without asking today starts asking.
- **Headless is unchanged.** It is called only when a person is there (an `Asker` is set). Headless
  refusals stay exactly as they are, including ADR 020's.
- **"Always" is a request, not a grant.** The core ignores it where its rules forbid it: a model
  whose trust is lost never gets "always" (ADR 020).
- **Plan mode still refuses** every edit before anyone is asked.
- **Only accepted hunks are written,** composed from the file as it is. The tool result tells the
  model which hunks were declined, so it doesn't silently re-apply them.
- **Lines are outside text.** A UI shows them as text, never as terminal sequences (ADR 023).

**Ownership:** Track 2 implements both sides within this scope (the permission flow in
`internal/tools` and the UI), as the review decided. A change beyond it is a new amendment.

**Fake:** `fake.Core.Review` records each proposal and answers with `Decide` (every hunk accepted
when unset). `TestFakeReview` covers it, and breaking the default answer fails it.

## Not decided here
- **Phase B's providers and their terms of service.** Phase B's first step, in its own ADR.
- **Phase F's rubric and UI design.** Phase F's first step, in its own ADR.
- **Whether `agent.Event` moves behind `surface`.** Not now. It would rewrite the TUI's core loop
  for no user-visible gain. Revisit if Phase F rebuilds the transcript.
