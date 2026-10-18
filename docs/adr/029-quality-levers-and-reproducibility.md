# ADR 029 — Phase C's quality levers and reproducibility, behind feature flags; measurement blocked by quota

Status: implemented and tested; **not yet measured**. The keep/drop decisions wait for the task
suite (ADR 025) to run with cloud quota.

## Problem
The plan lists levers to add "in order, keeping each only if it measurably helps":
1. strong-model planning with cheap-model execution;
2. tests or the compiler as the judge, with best-of-n only for hard tasks and small n;
3. escalation when verification fails;
4. retrieval instead of reading whole files;
5. structured outputs and tool schemas.

It also asks for *outcome* reproducibility: pinned sampling, versioned prompts, cached responses,
and verification gates deciding success.

## Decision: every lever is a feature flag, off by default
They are set by config `"levers"` or by `TERNLY_LEVERS`, which the suite's A/B arms use. Unknown
names are an error. A lever becomes a default only after the suite shows it helps.

| Lever | Flag | What it does |
|---|---|---|
| 1. planning | `plan_first` | The strongest tool-capable model plans in plan mode (read-only) and ends with `{"plan": [...]}`. Its steps become `PlanSteps`, which feed ADR 021's `Snapshot.Plan`, the data Phase F's plan panel was waiting for. Then the implementer is routed afresh at one tier below the task. |
| 2. tests as the judge | `best_of=N` (N ≤ 5) | A task that ends with its check failing is rewound (code and conversation) and run again from scratch, up to N attempts. A failed check is what marks a task as "hard", so easy tasks never pay. The check is the judge. |
| 3. escalation | `no_verify_escalation` | The control arm. Escalation on repeated verification failure has been on since ADR 018; this measures it. |
| 4. retrieval | `outline_reads` | `read_file` of a file of 300+ lines without a range returns its declarations with line numbers, so the model reads a range or asks the graph. |
| 5. schemas | `no_schema_repair` | The control arm. Tool arguments are already validated against their schemas, and near-misses (`3.0` for an integer) are repaired; this turns the repair off. |

**Found while building them (each has a test):**
- **Routing hysteresis (ADR 018) kept the planner, the most expensive model, as the implementer.**
  The "kept, within 25% of the best" rule saw it as the current model. The implementation turn
  now starts with no current model. `TestPlanFirstV2RoutesImplementerAfresh` builds a v2 case
  where the cheap model wins by less than the keep factor.
- **The implementation prompt classified as the hardest tier** ("implement", "architect" are hard
  words), so only the strongest model qualified. The implementer's difficulty is set explicitly to
  the task's minus one. The same two effects apply to the TUI's `/architect`, which isn't changed
  here.
- **The verify gate counts failures that existed before the change.** mitt's `npm run typecheck`
  fails in this environment before any edit (a deprecated tsconfig option, test files needing
  uninstalled packages). A routed run that fixed the bug correctly still escalated twice, through
  three models (pilot run). Escalation on verification failure therefore has a cost the suite can
  measure. Comparing against the check's result before the change is a candidate fix, not built
  here.

## Decision: reproducibility
- **Pinned sampling:** config `"deterministic"` or `TERNLY_DETERMINISTIC=1` sends temperature 0
  and seed 20261008 on every request. OpenAI-compatible endpoints, including Ollama, honour both.
  Anthropic takes temperature only, and only without extended thinking.
- **A response cache** answers byte-identical deterministic requests from disk (`<cache>/responses`,
  or `TERNLY_RESPONSE_CACHE_DIR`; `TERNLY_RESPONSE_CACHE=0` turns it off).
  - Only complete, successful streams are stored; errors are never replayed.
  - The key is the endpoint (not its key) plus the whole request.
  - The system prompt contains the workspace path, so reruns hit the cache only on the same path.
    The suite's `-stable-ws` reuses one path per task.
- **Prompt versions:** every headless run prints `prompt <hash>`, a hash of the system prompt
  without its per-run lines (date, workspace path, branch) plus every tool spec. The suite records
  it with each result, so a prompt change is visible in any comparison.
- **Success is decided by the verification gate and the hidden oracle,** never by the model's
  claim.

## Proof
- **Tests:**
  - `TestParseLevers`, `TestBestOfRetriesFromScratch`, `TestBestOfOffOrPassing`, `TestPlanFirst`,
    `TestPlanFirstV2RoutesImplementerAfresh`, `TestNoVerifyEscalation`;
  - `TestOutlineReads`, `TestNoSchemaRepair`;
  - `TestCacheDeterministicOnly`, `TestCacheNeverStoresErrors`, `TestCacheSkipsInStreamErrors`,
    `TestBodyPinsSampling`;
  - `TestDeterministicPinsSampling`, `TestDeterministicRerunCached`, `TestPromptVersion`,
    `TestSnapshotPlan`.
- **Mutations: 15, all caught.** Two needed new tests first:
  - hysteresis keeping the planner survived on the v1 test router; it's now caught under v2;
  - "the cache stores errored streams" survived, because an HTTP error never reports done; an
    in-stream error that still reports done now catches it.

## Measurement: blocked
The Ollama Cloud subscription hit its **monthly usage limit** at 13:00 UTC on 2026-10-08, halfway
through the suite's first screen. Every cloud request after that returned HTTP 429, and ternly
failed over to local llama3.1:8b. No pay-per-token key is set here. The lever A/B arms and the
reproducibility runs therefore haven't run. When quota returns, the plan is:

```
S=bench/suite; O=<dir>
go run ./$S run -models auto -runs 3 -out $O/control
for L in plan_first best_of=2 no_verify_escalation outline_reads no_schema_repair; do
  go run ./$S run -models auto -runs 2 -arm $L -env TERNLY_LEVERS=$L -out $O/levers
done
go run ./$S run -models auto -runs 2 -arm nofactcheck -env TERNLY_NO_FACT_CHECKS=1 -out $O/levers   # ADR 012's A/B
for A in sampled det; do  # 5 runs, same tasks
  E=""; [ $A = det ] && E="TERNLY_DETERMINISTIC=1,TERNLY_RESPONSE_CACHE=0"
  go run ./$S run -models auto -runs 5 -tasks '<4 tasks>' -arm $A -env "$E" -out $O/repro
done
go run ./$S run -models auto -runs 5 -stable-ws -tasks '<4 tasks>' -arm det-cache -env TERNLY_DETERMINISTIC=1,TERNLY_RESPONSE_CACHE_DIR=$O/cache -out $O/repro
go run ./$S report $O/*/results.jsonl; go run ./$S repro $O/repro/results.jsonl
```

**Decision rule:** a lever is kept if its pass@1 over the 20 tasks is higher than the control's,
paired by task, and the intervals and per-task differences support it, at no more than a
proportionate cost in tokens and time. A null result means dropped, not "probably fine".
