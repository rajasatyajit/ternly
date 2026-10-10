# Phase C — saved state (stopped 2026-10-10 16:17 IST, on the owner's instruction)

Every eval, benchmark and measurement was stopped at 16:17 IST on 2026-10-10:
- the process group of `eval-models.sh`, with SIGINT, then SIGTERM;
- the notifier loops.

`ps` confirmed no `go test`, `ternly`, `bench`, `suite`, `modelload`, `flock` or `ollama pull`
process remained. The loaded Ollama models were unloaded. Background evals are disabled in
`~/.config/ternly/config.json` (`routing.background_eval.enabled: false`) until the owner
re-enables them.

Nothing here is merged. The branch is `track1/phase-c`, with draft PR #28.

## What this directory holds
| Path | What |
|---|---|
| `data/<dataset>/results.jsonl` | one row per task run, with outcome, models used, time, tokens, diff and oracle tail; rows from b85cad9 on also carry commit and binary hash |
| `data/<dataset>/logs/` | each run's ternly trace |
| `data/plan-oldmodels/` | the resumable plan as it stood: `plan.txt`, `plan.log`, `steps/*.done`, and `levers/` (the B-control rows) |
| `binaries.txt` | SHA-256 and build time of the ternly binary behind every dataset, plus its commit where recorded |
| `evals/` | `ternly --eval` records of the new local models (gemma4:latest, gemma4:26b), the driver and its log |
| `models/` | local model speed (`modelload-*.txt`), installed model IDs, the llama.cpp probe, and two Ollama Cloud quota probes (HTTP 429, monthly limit) |
| `plan-next.txt` | the remaining measurements as a ready plan (see "How to resume") |

## Datasets (all local unless named)
| Dataset | Binary | Models | What it is | Result |
|---|---|---|---|---|
| `screen` | abcf73… (no commit recorded) | 8 Ollama Cloud models, pinned | frontier screen, 15 tasks × 1 run, until the cloud quota ran out (first 429 at 13:32 UTC, 2026-10-08) | valid runs only on 9 tasks (lru, mi, mitt); 6 of 8 models at 100% of their valid tasks, so no single best model can be named; semver, ternly and the 5 hard tasks have **no valid cloud run** |
| `pilot` | 0cdc93… | kimi-k3:cloud and routed | 2 tasks, a smoke test | 4/4 |
| `local` | a64ffc… (before the routing fix) | routed, local-only, old model set | 20 tasks × 1 | 4/20 |
| `fix` | a772bd… (165600e's code, less a later formatting-only change; the binary is identified by hash) | routed, local-only, old model set | **routing-fix A/B**, 8 bug tasks × 1, paired | none 0/8, classifier 2/8, textcall 2/8, **both 5/8** |
| `plan-oldmodels/levers` | f681cd… (b85cad9, clean) | routed, local-only, old model set | control (levers off, routing fixes on), 20 tasks × 2 | stopped at 21/40 when the models were replaced: 11/21 |

"Old model set" means qwen3.6, llama3.1:8b, gemma4:e4b (the old build) and granite3.3. All of them
were removed on 2026-10-09/10 at the owner's request, so these datasets can't be extended. They
stand as measured.

## New local model set (installed now)
qwen3.8 (dense, 27.3B), gemma4:latest (7.5B; the same weights as today's e4b), gemma4:26b,
granite4.2 (8.8B), and gemma4:31b, **still the old build**. Its update is paused at 83% by the
owner; resume with `ollama pull gemma4:31b`. IDs are in `models/local-models-ids.txt`.

Speed in Ollama, warm, from `models/modelload-new.txt`:

| Model | Warm turn | Generation | Notes |
|---|---|---|---|
| gemma4:latest | 4.7 s | 49 t/s | |
| gemma4:26b | 12.9 s | 26 t/s | |
| granite4.2 | 12.3 s | 11.5 t/s | |
| qwen3.8 | 40.5 s | 4.2 t/s | dense on a 6 GB GPU |
| gemma4:31b (old build) | 60.9 s | 2.0 t/s | |

Ollama 0.35.1 serves these with llama.cpp's `llama-server`: flash attention, a q8 KV cache,
speculative decoding, and models kept loaded for 30 minutes.

## Evals of the new models (eval v4)
| Model | Runs | Fabricated | Memory misuse | Took the bait | Tier | Trust |
|---|---|---|---|---|---|---|
| gemma4:latest | 3 | 3/28 | 0/6 | 8/9 | T2 | lost |
| gemma4:26b | 3 | 3/27 | 0/6 | 3/9 | T2 | lost |
| granite4.2 | — | — | — | — | — | — |
| qwen3.8 | — | — | — | — | — | — |
| gemma4:31b | — | — | — | — | — | — |

- **granite4.2:** stopped about 43 minutes into run 1. `ternly --eval` saves only at the end of a
  model, and the per-trap lines were still buffered, so nothing of it was kept. It must be re-run.
- **qwen3.8 and gemma4:31b:** not started.
- **Errored (timed-out) trap runs** aren't scored, which is why some denominators are below 30.

These records are **not yet merged** into `internal/eval/defaults.json`.

## What remains for Phase C, in order
0. **Make evals save per-task results incrementally, so an interrupted model run resumes instead
   of restarting.** granite4.2's partial run was lost: `ternly --eval` writes a model's record
   only when the whole model finishes, and `eval-models.sh` buffered its per-trap lines.
   This comes first, before any more evals run.
1. **Finish the evals.** Run granite4.2 (3 runs), qwen3.8 (at least 1 run; slow, about 4 t/s) and
   gemma4:31b (after the owner resumes its update; evaluating the old build would be wasted).
2. **Merge the records into the shipped defaults:**
   `go run ./bench/evaldefaults -alias ollama/gemma4:latest=ollama/gemma4:e4b bench/phasec/evals/ollama_*.json`.
   Then rebuild.
3. **Measure on the new model set** with `plan-next.txt`:
   - routing-fix none and both on the 8 bug tasks;
   - control, 20 tasks × 2;
   - the 5 levers and ADR 012's fact-check arm, 20 tasks × 1 each;
   - reproducibility: 4 tasks × 5 runs × {sampled, det, det-cache}.

   Expect much more time than on the old set, because qwen3.8 is about 9× slower to generate.
4. **Cloud-dependent work** (blocked: Ollama Cloud's monthly limit, HTTP 429 on 2026-10-09 and
   2026-10-10; no pay-per-token keys):
   - the frontier comparison on all 20 tasks, including the hard ones;
   - routed runs with cloud models.

   This needs the quota reset or paid capacity the owner approves. Report the gap; don't
   extrapolate.
5. **e2e** on the final head. The last e2e run (19/19) was on 5885c4a, before the routing fix.
6. **The report:**
   - ADR 029's Results section (routing fix, levers keep/drop, reproducibility, cost and time per
     successful task);
   - PR #28 out of draft;
   - CHANGELOG.

**Pushed:** `track1/phase-c` is on GitHub, including this state. PR #28 is unchanged and still a draft.

## How to resume (each step skips what's already recorded)
Results are keyed by task × model × arm × run × binary SHA-256. A rerun with the same binary
continues where it stopped; a new binary (after step 2) measures afresh, as it should.

```
cd .claude/worktrees/phase-c
# step 3, detached; check progress with `status`
bench/suite/plan.sh start bench/phasec/plan-next.txt <OUT>
bench/suite/plan.sh status <OUT>
tail -f <OUT>/plan.log
```

`<OUT>` must be outside the repository and persistent, e.g. `~/ternly-phasec/run2`. The driver
builds `<OUT>/ternly` from the checkout at its first run and records its commit in
`<OUT>/ternly.commit`.
