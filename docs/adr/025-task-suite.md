# ADR 025 — Phase C's task suite: hidden oracles, pinned repositories, and what it measures

Status: accepted (Phase C).

## Problem
Phase C's target is results close to the best frontier model at a fraction of the cost, measured
on our own suite rather than asserted. Before Phase C, ternly had:
- the fabrication eval (ADR 012/019): traps that test honesty, not task success;
- the e2e checks: capability and security, at 3 runs each.

Neither measures **whether a coding task gets done**, by which model, at what cost and in how
long. The levers the plan lists (planning, best-of-n, escalation, retrieval, schemas) can only be
kept "if they measurably help" against a measure like that.

## Decision
`bench/suite` holds coding tasks on pinned repositories. Each task has a **hidden test oracle**
that the model never sees: it is copied into the workspace only after the run.

### Repositories
| Repository | Language | Commit | Notes |
|---|---|---|---|
| hashicorp/golang-lru | Go | 9c13c57 | no dependencies |
| more-itertools/more-itertools | Python | 81c21a8 | stdlib only; oracles use `unittest` |
| developit/mitt | TypeScript | 6b41670 | oracles use node's test runner and type stripping, so no npm installs |
| dtolnay/semver | Rust | 280ebcb | the optional serde dependency and the criterion bench are removed (prep), so `cargo test --offline` works |
| ternly | Go | 9dd40a8 | ternly's own code, from `git archive` |

### Tasks (20)
- **bugfix (8):** a seeded bug, described only by its symptom.
- **feature (7):** a specification.
- **hard (5):** subtler bugs that need reasoning across the code (pre-release ordering, `windowed`
  end padding, 2Q promotion, FinalJSON's last block), and one async contract (mitt `emitAsync`).

### Oracles are validated
`suite validate` builds every task twice:
- **as given:** the oracle must fail;
- **with a reference solution:** the oracle must pass. A bugfix's reference is its seed reversed;
  a feature's is a written solution in `suite.json`.

All 20 pass this check. A task whose oracle passes on the unsolved code would measure nothing,
and one that fails on a correct solution would measure the oracle.

### Each run
- **Workspace:** a fresh copy of the pinned tree with the task's seed, committed, so the model's
  change is the workspace's `git diff`.
- **Home:** an isolated home (ternly's harness tripwire), and an environment with no keys, no
  tokens and no inherited git environment.
- **Command:** `ternly --mode edits --new -p <prompt>`, routed (`auto`) or pinned with `--model`.
- **Recorded:** models used, switches and their reasons, verification verdict, cost, tokens,
  seconds, quota hits, the diff, the prompt version (ADR 029), and plans and retries for the lever
  arms.
- **The oracle** runs in bubblewrap: no network, the user's secrets and runtime directory masked,
  and a scrubbed environment. **Without bubblewrap the oracle refuses to run,** and so does `run`,
  before spending any quota (`-unsafe-no-sandbox` is the only override). That rule came from a
  security review of the harness's first version, which fell back to running unsandboxed.
- **Outcomes:** pass, oracle-fail, no-change, timeout, error.

### Reporting
- **pass@1 per model and per class,** with 95% Wilson intervals, median seconds, cost and tokens.
- **Routing:** which models routing actually used.
- **Reproducibility:** outcome agreement and diff similarity over repeated runs (`suite repro`).

## Limits
- **Cost is $0 for Ollama Cloud models,** which run on the subscription. Their marginal cost
  shows as tokens and quota hits, not dollars. No pay-per-token key is set on this machine, so
  closed frontier models (Claude, GPT) aren't in the suite. Comparing against them is an open
  decision.
- **Each run's ternly starts with an empty home:** no speed history, no memory, no measured tiers
  beyond the shipped defaults. Routing therefore uses its priors, the conservative case for v2.
- **20 tasks give wide intervals:** at 3 runs per task an arm has 60 trials, so ±10–12 points near
  the middle. Differences smaller than that are reported as "not measurable here", not as findings.

## Results
### First screen (2026-10-08): eight Ollama Cloud models, one run of the 15 original tasks
- **Halfway through,** at 13:00 UTC, the subscription hit its monthly usage limit (HTTP 429).
  ternly then failed over from every pinned cloud model to local llama3.1:8b.
- **Those runs are excluded.** A pinned run that switched model isn't a measurement of the pinned
  model.
- **What remains:** 8–9 valid runs per model, from the tasks that ran first.

| Model | bugfix | feature | median s |
|---|---|---|---|
| kimi-k3 | 5/5 | 4/4 | 30 / 109 |
| glm-5.3 | 5/5 | 4/4 | 26 / 42 |
| deepseek-v4-pro | 5/5 | 3/3 | 35 / 127 |
| kimi-k2.7-code | 5/5 | 3/3 | 43 / 94 |
| minimax-m3 | 5/5 | 3/3 | 79 / 127 |
| nemotron-3-ultra | 5/5 | 3/3 | 209 / 574 |
| gpt-oss:120b | 4/4 | 2/4 | 10 / 34 |
| mistral-large-3:675b | 4/5 | 2/4 | 39 / 74 |

**What this supports:**
- **The original 15 tasks saturate.** Six of the eight models passed every valid run, and the
  intervals (e.g. 5/5 is [0.57, 1.00]) can't separate them. The five hard tasks were added for
  this reason, but the limit was hit before they could be screened.
- **"The single best frontier model" can't be named yet.** glm-5.3 and kimi-k3 are the leading
  candidates: 9/9, and the fastest among the perfect scorers.
- **gpt-oss:120b and mistral-large-3 fail features.** The failures are a missing method, wrong
  results, and empty answers.

### Defects the screen found
- **`--model` doesn't hold.** After two empty answers (ADR 028), and on errors or quota (ADR 018),
  ternly fails over to the router's best other model, even when pinned. Here that was a T1 local
  model. Open decision.
- **Exhaustion isn't remembered across runs.** Each new ternly process retries the exhausted cloud
  model first (one 429 per run), because `MarkExhausted` lives in memory. Persisting it is a
  candidate fix.
