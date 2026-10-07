# ADR 018 — Phase A: route on the expected cost of finishing, not on token price

Status: **accepted** (reviewed 2026-10-07, with the decisions and refinements below). v0.2 plan,
Phase A. Ships as v0.1.1 with issues #1 and #2.

## Symptom
Signed in to Ollama with cloud access, ternly routes to the local `qwen3.6:latest`. On this
machine that model runs mostly on the CPU, and a task can take up to 30 minutes (ADR 015 §1: 17
and 29 minutes in M8 dogfooding).

## Root cause (measured 2026-10-06 on v0.1.0 + the plan commit, `2b4eff0`)
**Machine:** Ryzen 7 6800H (16 threads), 62 GB RAM, RTX 3060 Laptop with 6 GB VRAM, Ollama
0.34.4. Ten T3 Ollama Cloud models are signed in.

**What the router does.** A probe test called the real `Router` with this machine's discovered
models and the shipped measurements (`internal/eval/defaults.json`):

| Call | Result |
|---|---|
| `Pick(diff=1)` | `ollama/qwen3.6:latest` ("T1 task → cheapest T1+") |
| `Pick(diff=2)` | `ollama/qwen3.6:latest` |
| `Pick(diff=3)` | `ollama/qwen3.6:latest` |
| `Utility()` (titles, summaries, compaction) | `ollama/qwen3.6:latest` |
| `Escalate(qwen3.6)` | **`qwen3.6`, ok=false**: no escalation possible |

`ternly --models` agrees: qwen3.6 is `T3 local measured(…3runs;pass=97%[85-99])`, alongside ten
`T3 cloud·quota name` models.

**The four suspected factors, each checked:**

1. **Local models cost 0: confirmed.** `catalog.enrich` sets `In = Out = 0` for local models
   (`discover.go:524`), and `price()` returns `Blended()` = 0. `cheaper()` compares price
   *before* anything else (`router.go:182`), and `Pick` takes the cheapest model at or above the
   wanted tier. A free model at the wanted tier always wins.
2. **qwen3.6's measured tier is T3: confirmed, and decisive.**
   - The shipped `defaults.json` gives qwen3.6 T3 (3 runs, pass lower bound 0.85, ADR 013).
   - The user has no capability records of their own (`~/.local/share/ternly/capability` doesn't
     exist), so the shipped default applies.
   - By name alone qwen3.6 would be T2. Then `Pick(3)` returns `ollama/glm-5.1:cloud`: re-running
     the probe without the measurement gave exactly that.
   - So the measured promotion moved *hard* tasks to the local model. The eval that measured it
     scores correctness only and never looks at time.
3. **Nothing accounts for latency or throughput: confirmed.**
   - `discover.Model` has no speed, latency or placement field.
   - `Router` reads only `Tier`, `Tools`, `Ctx`, `Cloud`, `Priced`, `In` and `Out`.
   - The turn time limit stops a slow model, but nothing routes away from one.
4. **Subscription cloud models are "quota-priced" and lose on price: confirmed.**
   - `price()` gives `m.Cloud` 0.01 (`router.go:191`), above local's 0 and below every paid API.
   - Within T3, local qwen (0) therefore beats every Ollama Cloud model (0.01).
   - The quota is treated as a small fixed price, not as near-free prepaid capacity.

**Two more defects the probe exposed:**

5. **Escalation is a dead end from a free model.** `Escalate` moves on only to a higher tier or a
   higher `Blended()` price (`router.go:126`).
   - qwen3.6 is already T3, and every cloud model's `Blended()` is also 0, because `enrich`
     zeroes cloud prices too (`discover.go:528`). So it returns `qwen3.6, false`.
   - The agent ignores `ok=false` (`agent.go:384`, `:427`). Repeated verification failures and
     no-progress loops stay on the same slow model.
   - `Failover` needs a *different provider*. Local and cloud Ollama models share provider
     `ollama`, so there is no failover either.
6. **Utility calls use the slow model too.** `Utility` takes the cheapest model that is cloud or
   T2+ local, which is qwen3.6 here, for titles, summaries and compaction.

**Why it's slow: measured on this machine.**
- **Placement:** Ollama's `/api/ps` reports qwen3.6 (36B MoE, Q4_K_M) at `size` 25.1 GB,
  `size_vram` 4.0 GB, so **16% on the GPU and 84% on the CPU**. `ollama ps` says the same. A cold
  load took 26.8 s.
- **Speed:** time to first token (TTFT) and output speed through Ollama's OpenAI endpoint, as
  ternly calls it. Streaming, 400 output tokens, reasoning off (`scratchpad ttft.py`, 1 run
  each):

| Prompt tokens | qwen3.6 (local) TTFT | qwen3.6 tok/s | glm-5.3:cloud TTFT | glm tok/s | kimi-k3:cloud TTFT | kimi tok/s |
|---|---|---|---|---|---|---|
| ~2.3 k | 8.0 s | 36.4 | 1.3 s | 88.9 | 1.0 s | 85.4 |
| ~20 k | **63.7 s** | 32.0 | 1.9 s | 81.3 | 1.8 s | 82.4 |
| ~40 k | **74.6 s** | 30.3 | 2.5 s | 87.7 | 3.0 s | 115.2 |

An agent turn is many steps, each with a growing context: 20–60 steps of 10–60 k tokens each in
the e2e and dogfood logs. Ollama keeps a KV cache, so a step re-reads only the new tail of the
context, but qwen3.6 still pays tens of seconds per step plus reasoning at ~30 tok/s. The ADR 015
runs (17 and 29 minutes) fit that profile.

**Conclusion.** The router's objective is *the lowest token price at a sufficient tier*. On this
machine that objective selects a free model that is 25–30× slower to first token at agent-sized
contexts, with no way out once it's chosen. Each of the four suspected factors contributes:
- (1) and (4) order free above quota;
- (2) makes the free model eligible for hard tasks;
- (3) means nothing pushes back.

Defect (5) removes the safety net.

## Options
1. **Patch the order:** rank quota cloud before local. Rejected: it fixes this machine and
   breaks one with a fast local GPU, where a local model is the right choice. It's still price
   alone.
2. **A time limit per model:** exclude models whose measured speed is under a threshold.
   Rejected as the main mechanism: a fixed threshold is wrong for easy versus hard tasks, and it
   ignores success probability. It's kept as a hard guard (below).
3. **Expected cost to finish successfully (chosen).** It's what the plan asks for, and it
   degrades to today's behaviour when speed is unknown and the user's time is valued at 0.

## Review decisions (2026-10-07)
These override the proposal text below wherever the two differ.

1. **λ** defaults to **$20/hour** and is configurable. **λ = 0 must reproduce price-only routing**,
   and a test proves it.
2. **The default flips to routing v2 in v0.1.1** once it's proven on:
   - the e2e suite;
   - the baseline (benchmarks);
   - a **routing replay test on the owner's real model list**: hard tasks go to cloud, and so on.

   `routing: "v1"` stays as an escape hatch for one release.
3. **Background evals may spend Ollama Cloud quota**, under two caps: per new model and per week.
   - They are visible in `/models`, and there's an off switch.
   - Paid APIs default to $0.
   - Evals use **only ternly's bundled tasks**, never the user's repositories.

**Refinements:**
- **P(success)** is the Wilson lower bound, **with a floor**, never the raw rate.
- **Re-rank every turn on the current context size,** since prefill cost grows with context.
  **Hysteresis** keeps a task from flapping between models mid-task and losing the prompt cache.
- **Background utility calls** (titles, summaries, compaction) use a local model **only if it's
  fully on the GPU**; otherwise the cheapest cloud model.
- **Provider identity for failover is endpoint + locality,** so local Ollama ≠ Ollama Cloud.
- **When escalation has nowhere higher to go,** ternly tells the user what would help: a
  stronger model, an API key, or a quota reset.
- **The perf gate logs and counts base retries** (done in PR #9).

**Sequencing:** the staticcheck/govulncheck PR (#9) lands before the Phase A fix merges.

## Decision (as proposed; amended by the review above)

### 1. The objective
For a task of difficulty *d* (Classify, unchanged), each tool-capable candidate *m* gets:

```
score(m, d) = ( money(m, d) + quota(m, d) + λ · time(m, d) ) / p(m, d)
```

Routing picks the lowest score, excluding models that can't finish within the turn limit.
Dividing by *p* is the expected cost of retrying until it succeeds (a geometric number of
attempts). That rewards a model that succeeds first time, and it is what makes escalation
consistent with the pick.

- **`money`**, for pay-per-token models: expected input and output tokens for *d* × the prices
  (cache-adjusted). Local models: 0.
- **`quota`**, the marginal subscription cost:
  - prepaid quota is close to free, so a small shadow price applies while there's headroom;
  - after a quota or rate-limit signal from the provider (HTTP 429 / a documented quota error),
    the model counts as **exhausted** until its documented reset or a cooldown.
  - Only official signals count (Phase B §3). Ollama Cloud's quota signals are **unverified**
    as of this ADR; the 429 path is what M8's failover already handles.
- **`time`**: expected wall time = steps(*d*) × (prefill of the new context ÷ prefill rate +
  output per step ÷ output rate) + a cold-load penalty if the model isn't loaded.
  - The rates are **measured on this machine**: passively from every streamed request (TTFT,
    tokens/s, prompt size), and actively by a short calibration probe when a model has no data.
  - Stored per model under `<data>/speed/`, as a versioned file with an EWMA and a sample count.
  - Steps and tokens per difficulty start from priors taken from the e2e reports, then come from
    the user's own session ledger.
- **`λ`**: the value of the user's time.
  - Config `routing.time_value_usd_per_hour`; the proposed default is **$20/h**.
  - At $20/h, a minute of waiting costs about the same as ~$0.33 of tokens.
  - 0 restores price-only routing. **This default is a policy choice for review.**
- **`p`**: probability of success at *d*.
  - From the measured capability: the Wilson lower bound (ADR 013) when the model's tier ≥ *d*,
    discounted per tier short of *d*.
  - Unmeasured models get a prior (§4).
  - Phase C replaces this with per-class pass@1 from the task suite.
- **Hard guard:** if the expected time exceeds the turn time limit, the model is not eligible
  for that turn.

**Ties and transparency.** The pick's reason line names the winning terms, e.g.
`T2 task → glm-5.3:cloud (est. 1.4 min, quota) over qwen3.6 (est. 19 min, local CPU 84%)`.

### 2. Escalation and failover use the same ranking
- **`Escalate`** returns the best-scoring model, *re-scored at d+1*, among those not yet tried
  this turn. It no longer requires a higher tier or a higher price, so a free model always has
  a way out.
- **Failover** keys on *model*, not provider. On a retryable error, the next model by score,
  excluding the failed one, is used, whatever its provider.
- **Utility** uses the same score at *d* = 1 with a small output, so summaries go to the fastest
  adequate model.
- **A slow step is a failed step.** When a step's elapsed time exceeds 3× its expected time (the
  seconds limit from issue #2), the step is treated like the no-progress case: escalate. Issue
  #2's watchdog work lands in the same PR series.

### 3. Hardware awareness
- **Placement:** from Ollama's `/api/ps` (`size_vram / size`, verified above), refreshed at
  discovery and after each model load.
- **Models that aren't loaded:** an estimate from `/api/show` size versus free VRAM. Total VRAM
  isn't in Ollama's API (**unverified**; to check against the 0.34 API docs). It comes from
  `nvidia-smi` or the amdgpu sysfs files when present; otherwise it's unknown, and only measured
  speed counts.
- **RAM headroom:** `/proc/meminfo`. A model larger than available RAM + VRAM is ineligible.
- Placement is a *prior* for speed. Measured rates replace it once they exist.

### 4. "Latest and greatest" = best measured
- **Prior for unmeasured models:** the family position, i.e. the version within a family
  (`glm-5.3` > `glm-5.1`, `kimi-k3` > `kimi-k2.7`), plus recency (`modified_at` is when it was
  pulled, not when it was released, so it's a weak signal).
  - It maps to a prior *p* *below* any measured model's lower bound at the same tier, so
    evidence wins.
- **Background eval:** when a new model appears, ternly runs the fabrication eval on it in the
  background, at most once a day per model, with a fixed budget. Then it routes on the result.
  - **Budget:** the eval's 14 traps, 1 run, at most 20 minutes.
  - **Quota and local models only.** Pay-per-token models need an explicit budget in config
    (default $0).
  - **Never under `--local-only`.** It sends only the eval's synthetic workspaces, never user
    code.
  - It runs at idle, never during a turn, and yields to a turn.
- **`/models` and `ternly --models`** show each model's score terms: *p* with its basis, the
  expected time with the measured rates and placement, money, quota state, and the resulting
  rank for T1–T3.

### 5. Integration (non-negotiables 4 and 5)
- **Behind a flag first.** The new ranking sits behind `routing: "v2"`, alongside the current
  `Router` logic (`"v1"`, still the default). The router keeps its interface (`Pick`,
  `Escalate`, `Failover`, `Utility`).
- **Flip the default** in the same release, once v2 is equal or better on the e2e capability
  checks (pass rate), wall time per check, and the routing microbenchmarks. Remove v1 one
  release later.
- **Data:** `<data>/speed/*.json` is new, with `{"version":1}`. Unknown versions are ignored,
  and the model is re-measured. No v0.1 format changes, and config keys are additive.
- **Security:** the background eval and calibration probe are new surfaces, so the threat model
  gets them. They send synthetic prompts only, run in the sandbox like `--eval`, and are off
  under `--local-only`. A scripted-adversary scenario checks that a hostile model reply during
  calibration can't trigger tools: the probe offers none.

## How it will be measured
- **Unit tests (table-driven).** On this machine's model set (fixtures of `/api/tags`,
  `/api/ps`, `/api/show`):
  - T1–T3 picks go to a cloud model;
  - with the cloud quota exhausted, qwen3.6 is picked;
  - on a fast-GPU fixture (100% VRAM, measured 80 tok/s), a local model wins T1/T2;
  - `Escalate` from a free model always moves;
  - λ = 0 reproduces v1's picks.

  Each guard is proven by breaking it.
- **e2e, routed rather than pinned.** The capability checks run with routing on (a new
  `TERNLY_E2E_ROUTED=1` mode), v1 against v2.
  - Reported per check: pass rate, wall time, and the model chosen.
  - Target: wall time on this machine well under v1's (qwen3.6), with pass rates no worse.
- **Benchmark:** `BenchmarkPick` (40 models) joins the perf gate. Budget: under 50 µs, so routing
  never shows up in a turn.
- **Dogfood:** a Phase A task done with ternly on routed v2, reported with its timings.

## Not in Phase A
- Claude/Codex bridges and subscription detection (Phase B).
- Per-class pass@1 (Phase C). Phase A's *p* uses the existing capability measurement.

## Implementation (v0.1.1, PR #11)
- **`internal/discover`:**
  - `cost.go`: the score, estimates, `PickFor`, `EscalateFor` and `FailoverFor`, utility,
    `Explanations`, family ranks and `StepSeconds`;
  - `speed.go`: `<data>/speed.json`, versioned, 0600, EWMA;
  - `hardware.go`: `/api/ps` placement, nvidia-smi or amdgpu VRAM, `/proc/meminfo`.
- **The router keeps v1's methods.** v2 is a cost model set on it, and λ = 0 delegates to v1's
  `Pick`.
- **`internal/agent`:**
  - per-turn re-rank with hysteresis (×1.25);
  - a tried set per turn, for escalation and failover;
  - a 429 on a subscription marks it exhausted for an hour;
  - "nowhere to go" is shown once per turn;
  - speeds are observed on every stream.
- **`internal/bgeval`:** the scheduler, ledger and caps. `routing_config.go` holds the config, the
  child command and the `--models` explanation.
- **Issue #2:** the watchdog counts tokens (bytes ÷ 4) and idle time, checked on a ticker. Under
  v2 the idle limit is at least 3× the expected step time, and a slow step escalates.
- **Issue #1:** `internal/graph/receiver.go` checks a name-matched call's receiver against local
  evidence of its type. Rust stays "possible" until its methods are qualified by their type (#10).

## Results (2026-10-07, the owner's machine; logs in `bench/dogfood/2026-10-07-phase-a/`)

### The replay test: the bug, and its fix, on the recorded model list
`routing_replay_test.go` replays the owner's Ollama responses (`/v1/models`, `/api/tags`,
`/api/ps`, `/api/show`), recorded today, with this machine's hardware:
- **v1:** qwen3.6 for T1, T2 and T3. That is the bug.
- **v2:** Ollama Cloud for T1–T3, a T3 model for hard tasks, in each of three variants:
  - priors only;
  - qwen3.6 loaded at 16% GPU;
  - the measured speeds.
- **v2 escalation and failover from qwen3.6:** reach a cloud model.
- **Utility calls:** never qwen3.6, which isn't fully on the GPU.
- **Quota exhausted:** qwen3.6 is picked, and escalation says what would help.
- **λ = 0:** identical to v1 at every difficulty and context size. Also on 500 random model sets
  (`TestLambdaZeroEqualsV1`).

**Every guard was broken on purpose and its test went red:**
- the time term;
- identity = endpoint only;
- utility ignoring the GPU;
- no hysteresis;
- raw pass rate;
- no floor;
- the escalation dead end;
- ignoring exhaustion;
- λ = 0 not being v1;
- no family prior (this one needed a new test: the first mutation survived);
- agent side: escalation off, the hint never shown or repeated, a 429 not recorded, speed not
  observed;
- watchdog: chunks instead of bytes, no idle trip, no 3× expectation, a slow step not escalated;
- callers grouping: other-type calls not excluded, receiver evidence ignored,
  `include_unverified` ignored, ambiguous counted as sure.

### Routed e2e: v1 against v2
These are the checks that route a task: 9 security and 7 capability, 3 runs each, from a fresh
HOME, so v2 starts on priors and has no measured speeds.

| Router | Models used (all 39 runs) | Pass, every check | Wall time | Bait taken (guard engaged) |
|---|---|---|---|---|
| v1 | `qwen3.6:latest` | 16/16 checks at threshold | **38 m 12 s** | 2/24 (2) |
| v2 | `kimi-k3:cloud` | 16/16 checks at threshold | **9 m 52 s** (3.9× faster) | 2/24 (2) |

**Every check's median is lower under v2:**
- memory-poisoning 1 m 39 s → 12 s;
- subagent-fanout 1 m 37 s → 24 s;
- graph-callsites 42 s → 10 s.

**"Pass" uses the fixed call-site judge** (below). The old judge printed
`graph-callsites-python` 2/3 for v1 and 0/3 for v2. The v2 failures were all correct answers
that said "app/run.py:5 … is excluded".

**Dogfood:** ternly on v2 drafted this ADR's threat-model update.
- It picked kimi-k3:cloud, and the turn took 46 s with ✓ verified.
- Review against the code corrected three claims:
  - `--local-only` skips cloud models only, not every evaluation;
  - it had left out the `/proc/meminfo` and sysfs reads;
  - it gave vague test names.

### A judge bug, found by issue #1's re-measurement
`graph-callsites-python`, and `graph-callsites` (Go), scored any mention of a wrong site as an
inclusion. Every "wrongly included" failure of the Python check since 2026-10-05 was a correct
answer naming `app/run.py:5` in order to exclude it ("I excluded `app/run.py:5` because that calls
`b.flush()` on a `Buffer` instance").
- **The fix:** a wrong site counts only on a line that doesn't say it was left out.
  - "Buffer" alone isn't enough: a list line annotated `# Buffer` still counts as included.
  - Seven answers from the reports, verbatim, are regression cases in `TestCallsiteVerdict`.
  - Broken in either direction, the test goes red.
- **The thresholds are unchanged.**

**Re-scored from the saved transcripts (`bench/results`):**

| Runs | Old judge | Fixed judge | 95% interval (fixed) |
|---|---|---|---|
| qwen3.6 pinned, v0.1 era (2026-10-05 → the baseline) | 14/27 (0.52) | **26/27 (0.96)** | [0.82, 0.99] |
| Phase A, pinned, 9 runs (#1's re-measurement) | 5/9 | **8/9** | [0.56, 0.98] |
| Phase A, full e2e | 1/3 | 3/3 | |

**The two genuine failures:**
- the model searched only `app/` and missed `server/` (v0.1 era);
- the model ended its turn without giving the list (Phase A).

**What the bug means for v0.1.0:**
- CHANGELOG v0.1.0 and ADR 016 §6/§9 said qwen3.6 reports name-matched callers as real callers
  about half the time. **That was the judge, not the model:** qwen3.6 passes this check 96% of the
  time.
- The withdrawn qwen3 reasoning rule (ADR 016 §6) was tested against the same judge, so its
  "no difference" result means nothing either way.

**Re-measured live with the fixed judge** (9 runs each; thresholds unchanged):

| Run | graph-callsites (Go) | graph-callsites-python | Median per run |
|---|---|---|---|
| qwen3.6 pinned (`9af790d`) | 8/9 [0.56, 0.98] | 8/9 [0.56, 0.98] | 1 m 05 s / 1 m 15 s |
| routed v2, kimi-k3:cloud (`bcdcee8`) | 9/9 [0.70, 1.00] | 9/9 [0.70, 1.00] | 10 s / 9 s |

**The qwen3.6 misses are genuine:**
- one run missed all four Go callers;
- one missed the two `server/` callers.

The name-matched-callers trap still passes: 3/3, fabrication 0/3.

### Issue #2: the watchdog on ADR 015/016's runaway case
Protocol: glm-5.3:cloud, `--reasoning off`, the netguard spec on base `1e16033`, `--no-memory`,
`--mode edits`, a 30-minute turn limit, default watchdog (6000 tokens, 5 min idle).

| Run | Watchdog | Result | Wall | Tokens in / out | Code |
|---|---|---|---|---|---|
| 1 | tripped at ~6001 tokens → retried at low | ✓ verified | 5 m 09 s | 519 k / 21 k | build, vet and own tests pass |
| 2 | tripped at ~6002 tokens → retried at low | ✓ verified | 3 m 36 s | 244 k / 17 k | build, vet and own tests pass; **not gofmt'd** |

**Compared with earlier runs of the same task:**
- **ADR 015** (no watchdog): off finished 0 of 2 within 30 minutes.
- **ADR 016** (chunk watchdog): off took 8 m 08 s (step limit) and 8 m 43 s.

ADR 015's 7-check black-box scorer wasn't saved, so it wasn't re-run. The code above is judged by
ternly's verification and its own tests only.

The `name-matched-callers` fabrication trap still passes: 3/3, fabrication 0/3.

### Performance (ADR 017 gate, local A/B against main, 10 rounds)
- **No regressions.** The only significant changes are improvements from the deterministic
  memory corpus.
- **The binary:** +0.37% (31.77 → 31.89 MB).
- **`BenchmarkPick`, new:**
  - v1: 1.5 µs, 13 allocs;
  - v2: 26.6 µs, 251 allocs. Under the 50 µs budget, after computing family ranks once per
    discovery; the first version took 90 µs and 862 allocs.
- **CI's perf job passed on the PR.**
- **Macro numbers against the baseline, re-measured interleaved with the baseline binary
  (`eff3df7`), the same hour:**
  - `--version`: 36.0 ms against 36.1–37.4 ms;
  - a headless turn: 52.7–53.0 ms against 52.5–53.9 ms;
  - peak RSS: 38–40 MB both;
  - resume a 1,000-turn session to interactive: 81 or 101 ms, bimodal, in both trees.
  - **No regression.** A one-off comparison with yesterday's `baseline.json` showed +9% and +23%:
    that was machine state, and the interleaved runs don't reproduce it.

### Full e2e (pinned qwen3.6, `d4db227`; 19 checks × 3 runs, 1 h 18 m)
- **18/19 checks at threshold** as printed. `graph-callsites-python` printed 1/3: both failures
  were the judge bug above, and it is 3/3 re-scored.
- **Security:** 27/27. The model took the bait 1/24 times (`plan-mode-command`), and the guard
  engaged.
- **A full `go test -race ./...` overlapped about 5 minutes of this run,** so some medians in that
  window are slightly high.

### Not done, or not verified
- **Ollama Cloud quota signals:**
  - 429 handling is tested with a fake.
  - No real quota exhaustion was observed, so the one-hour cooldown is a guess.
  - Ollama documents no reset time (unverified).
- **Total VRAM** comes from nvidia-smi or sysfs, not Ollama's API, which doesn't report it
  (unverified for other GPU vendors).
- **Task-shape priors** (steps, output per step) are from the e2e reports, not measured per task
  class (Phase C).
- **No background evaluation has run on a real new model yet.** The scheduler is tested with
  fakes, and the child command and its isolation are tested. The first real one runs when the
  owner's TUI sits idle.
