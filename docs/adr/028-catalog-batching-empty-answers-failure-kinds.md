# ADR 028 — Catalog refresh batching, empty-answer retry, and e2e failure kinds

Status: accepted (the core decisions from the Phase B/F review, 2026-10-08).

Decisions, verbatim from the review: "batch catalog upserts so the memory index rebuilds once per
refresh. Empty answer: retry once on the same model with a nudge, then fail over; count retries.
e2e: report 'format failure' separately from 'wrong answer'."

## 1. Catalog refresh: one index rebuild per refresh

### What was measured first
Phase F reported (ADR 023, findings) that the capability catalog's refresh rebuilds the whole
memory index per upserted entry, costing 1–3% CPU for minutes after start. Measured here before
changing anything:

| Scenario | Wall | CPU | Index rebuilds |
|---|---|---|---|
| A real first refresh, default sources, empty catalog (marketplaces, Gemini, MCP registry, npm; 17,517 entries) | 3m27s | 0.89 s (0.43% average) | **0** |
| Opening the owner's real catalog log (85,522 records, 45,815 live) | 0.73 s | — | 1 |
| A refresh re-upserting all 45,815 live entries of that catalog | 1.58 s | — | **1** |
| ternly's TUI, fresh default-config home, CPU from 15 to 75 s after start (refresh running) | — | ~2% (1.9–2.3%, 2 runs) | — |

- **The per-entry rebuild doesn't reproduce.** A rebuild clears every dead slot, and one runs only
  once dead slots outnumber live ones (and exceed 1024). Rebuilds can repeat within one refresh
  only when the same entries are replaced many times while few are live: a registry listing every
  version of a server.
- **The first refresh is network-bound.** Its CPU goes to JSON decoding of the responses, TLS, log
  encoding, and `index.add` (tokenising), not to rebuilds.
- **The ~2% while it runs** matches Phase F's 2.5%. Where the in-binary time beyond the 0.43%
  in-process refresh goes is profiled below.

### What the golden comparison found
Comparing search results against an index built fresh from the final items exposed a ranking
defect, older than this change:
- BM25's document frequency is the length of a term's posting list, **including postings of dead
  slots** (removal is lazy), while `N` counts only live items.
- After replacements, `df` can exceed `N`, so `log(1 + (N−df+0.5)/(df+0.5))` goes **negative**,
  and common words lower a match's score.
- The skew lasts until a rebuild, which happens only once dead slots outnumber live ones.

### Decision
- **`memory.Store.HoldIndex()`** defers rebuilds until it is released. On release, the index is
  rebuilt **once if anything was replaced or removed**, so it is exact afterwards. Holds nest.
  Searches meanwhile skip dead slots, as before.
- **`Catalog.Refresh`** holds the index for the whole refresh: at most one rebuild per refresh, and
  exactly one when it replaced anything.
- **`Store.UpsertAll`** applies a page under one lock; `Catalog.Put` uses it.
- **`Store.IndexRebuilds()`** counts rebuilds, for tests and diagnostics.

### Proof
- `TestHoldIndexRebuildsOnce`: a refresh-shaped burst (100 entries, each replaced 30 times) rebuilds
  **exactly once**, at release, over two refreshes. The same updates unheld rebuild at least twice,
  which checks the premise. Searches while held return results.
- `TestUpsertAllGolden`: after held, batched refreshes, every search returns exactly what a fresh
  index over the final items returns, in order, with scores to 4 decimals. The test also checks the
  premise that per-item upserts drift from it.
- `TestRefreshRebuildsIndexOnce`: a real `Catalog.Refresh` against a fake MCP registry (30 pages,
  the same 100 servers on each) rebuilds once per refresh, over two refreshes.
- `BenchmarkCatalogPut`, 1000 entries × 8 replacements in pages of 100, A/B against main (n=10):
  77.0 ms → 71.3 ms (~, p = 0.25; ±44–55% fsync noise); B/op +1.1%, allocs +0.7%. **No measurable
  CPU win**, as the profile predicts. The gains are a bounded rebuild count and an exact index after
  every refresh.
- Real binary, idle 15–75 s with the refresh running: ~2% before and after. Batching doesn't change
  it. What the 2% is: see the profile below.

**Not changed:** the IDF drift outside catalog refreshes. The memory store's own replacements, and
reopening a log that has history, keep the old behaviour (exact only after a rebuild). That is a
retrieval-quality question for Phase C's evals, not a CPU one.

## 2. Empty answers: one retry with a nudge, then one failover
When a step ends with no text and no tool call:
1. The empty reply isn't committed, so no empty assistant message is ever sent back to a provider.
2. **First time in a turn:** the same model is asked again with a short nudge ("Your last reply was
   empty: no text and no tool call. Continue the task: call a tool if there is more to do,
   otherwise give your final answer."). It is counted in `Stats.EmptyRetry`.
3. **Second time:** fail over through ADR 018's `FailoverFor` (models not tried this turn), counted
   in `Stats.Failovers`. The nudge stays in the conversation the next model sees, which is harmless
   because the instruction applies to it too.
4. **Bounded:** one retry and one failover per turn. After that, the turn ends without an answer,
   and the UI says so (ADR 023).

- **`Stats.Failovers`** now counts every failover: errors and quota hits too, not just empty
  answers.
- **Both counters** are in the session's stats record (the ledger), and in `/cost`'s guard line.

**Proof:**
- `TestEmptyAnswerRetriedOnce`: one retry, same model, the nudge sent, the empty reply not sent
  back.
- `TestEmptyAnswerTwiceFailsOver`: two requests to the empty model, one to the backup, counted.
- `TestEmptyAnswerBounded`: a lone empty model gets 2 requests. Two empty models get 2 + 1. Three
  empty models still fail over once (3 requests in all).

## 3. e2e: format failure vs wrong answer
- **`e2ejudge` classifies a failed run:**
  - `format`: no final ```json block, or one of the wrong shape (`ErrNoAnswer`);
  - `wrong-answer`: `ErrWrongAnswer`, attached by `e2ejudge.Wrong(err)`, which keeps the message
    byte-identical (the judge audit and rescore match on it);
  - `other`: timeouts, harm, a run that failed before answering.
- **The five structured checks** mark their content mismatches as wrong answers.
- **Reports gain** `failure` per run and `format_failures` / `wrong_answers` per check. The summary
  prints a "failed runs by kind" table. Thresholds are unchanged: this is reporting.
- **`bench/rescore`** reads the new field and tallies kinds. For reports from before this ADR it
  classifies only what the error text says for certain (format, timeout, failed run) and counts the
  rest as "unclassified". Over the saved history that is 19 unclassified, and nothing guessed.

**Proof:** `TestFailureKind` and `TestFailureKindsCounted` (e2e-tagged, no model; added to CI's
tagged run).

## Mutation runs
Each guard was broken in turn. All 14 were caught. The first run had one survivor, "failover
unbounded": with two models the tried-set alone stopped it, so the bounded test gained a
three-model case.

| Broken | Caught by |
|---|---|
| refresh doesn't hold the index | TestRefreshRebuildsIndexOnce |
| release doesn't rebuild | TestHoldIndexRebuildsOnce, TestUpsertAllGolden |
| rebuilds while held | TestHoldIndexRebuildsOnce |
| UpsertAll skips the version bump | TestUpsertAllGolden |
| no retry on an empty answer | all three TestEmptyAnswer* |
| retry unbounded | TestEmptyAnswerTwiceFailsOver, TestEmptyAnswerBounded |
| no failover after a second empty | TestEmptyAnswerTwiceFailsOver, TestEmptyAnswerBounded |
| failover unbounded | TestEmptyAnswerBounded (three models) |
| retries not counted | all three TestEmptyAnswer* |
| empty failover not counted | TestEmptyAnswerTwiceFailsOver, TestEmptyAnswerBounded |
| the empty reply kept in the conversation | TestEmptyAnswerRetriedOnce |
| wrong answers classed as other | TestFailureKind |
| Wrong changes the message | TestFailureKind |
| finish doesn't count kinds | TestFailureKindsCounted |
