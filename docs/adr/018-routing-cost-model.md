# ADR 018 — Phase A: route on the expected cost of finishing, not on token price

Status: **proposed** (root cause and plan; awaiting review before implementation). v0.2 plan,
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

## Decision (proposed)

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
