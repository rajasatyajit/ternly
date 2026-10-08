# ADR 017 — v0.2: the baseline, and a performance gate in CI

Status: accepted (v0.2 delivery step 1; non-negotiables 1 and 2).

## Problem
v0.2 must extend v0.1 without regressing it. Before this ADR:
- the only numbers were in ADRs, measured by hand;
- CI ran no benchmarks;
- a slowdown could merge unnoticed.

The plan asks for two things:
1. A committed baseline for every measured path, which each phase reports against.
2. A CI gate that runs benchmarks with repeated counts, compares them with benchstat, and fails a
   PR on a statistically significant regression beyond a budget (default 5%).

## Decisions

### 1. Starting point
- Tag **v0.1.0** (`4d1b950`) is the starting point for code.
- `main` at `2b4eff0` adds only the v0.2 plan. The baseline was measured there, with this PR's
  benchmark files added (test code only).

### 2. What is measured, and where
**Micro benchmarks** (`go test -bench`, in their packages). These are gated in CI and listed in
`bench/perf.json`:

| Path | Benchmark | Notes |
|---|---|---|
| event append | `session.BenchmarkRecord` | existing |
| resume | `session.BenchmarkResume1000` | **new**: TestResume1000Turns as a benchmark |
| memory retrieval | `memory.BenchmarkSearch10k` | existing; now `b.Loop`, so the 10k-item setup runs once instead of once per b.N ramp step (CI time 9.4 s → 3.2 s; this alone shows as −4.8% against the base) |
| graph build / load / incremental | `graph.BenchmarkGraph{Build,Load,Incremental}` | **new**, on `testdata/fix`; shutdown, which waits out the watcher's 500 ms poll, is outside the timer |
| file tools | `tools.Benchmark{ReadFile,EditFile}` | existing, now with allocations |
| grep | `tools.BenchmarkGrep` | **new**: 200 files; ripgrep when installed (CI installs it) |
| guards | `tools.Benchmark{Validate,Frame}` | existing |
| keystroke | `tui.BenchmarkKeystroke` | **new**: one key through `Update` to a rendered `View`, with a 1,000-turn transcript |
| render | `tui.BenchmarkRedraw10k`, `tui.BenchmarkRenderAnswer` | **new**: a transcript redraw at ~10k lines; markdown for one answer |

`BenchmarkFlush` (fsync-bound) and `BenchmarkSuspicious*` stay out of the gate; they're in the
repository for local use.

**Macro numbers** (`bench/macro`, `bench/run.sh baseline`). These are in the baseline file but not
gated on timing, because a shared runner can't time a process start reliably:
- the binary size (static, stripped); this one *is* gated, being deterministic;
- start-up (`--version`) and its peak RSS;
- one scripted headless turn against `bench/fakeprovider`, with its peak RSS;
- time to interactive for a resumed 1,000-turn session (`TestResumeLargeSessionTimeToInteractive`).

**Slow suites,** run by hand and merged into the baseline:
- the kubernetes graph (`bench/run.sh graph`: full build, cache load, incremental edits);
- memory at 100k items (`bench/run.sh memory`);
- e2e pass rates and susceptibility (`bench/run.sh e2e`);
- per-model fabrication, memory-misuse and bait rates (`internal/eval/defaults.json`).

### 3. The gate compares base and head on the same runner, interleaved
Absolute numbers from a developer machine can't gate a CI runner. Runner hardware varies between
jobs, and absolute timings shift by more than 5%. So the gate is an **A/B in one job**
(`bench/run.sh perf <base>`):
1. Build the test binaries for every suite at the merge base (a `git worktree`) and at the PR
   head.
2. Run `count` rounds (10). Each round runs every suite on both sides, **alternating which side
   goes first**, so drift (thermal, noisy neighbours) hits both equally.
3. `benchstat` (golang.org/x/perf, pinned by pseudo-version) compares `base.txt` and `head.txt`
   for sec/op, B/op and allocs/op, with Mann-Whitney U at alpha 0.05.
4. `bench/perfgate check` reads benchstat's CSV and fails the job when:
   - a change is **significant and worse than the budget** (5% by default) in any unit;
   - **a benchmark exists at base but not at head** (no silent erosion);
   - **the binary grew more than 5%**.

   A benchmark only the head has is reported "new": it gets its own baseline, and so a new
   feature pays its own cost.
5. The job writes the gate table and benchstat's text to the step summary, and uploads both, with
   **CPU and allocation profiles** of the head (`-cpuprofile`/`-memprofile` per suite), as an
   artifact for 30 days. A hot-path PR links them.

**Allocation budgets** are the B/op and allocs/op rows, under the same rule. Allocation counts are
nearly deterministic, so these rows catch regressions that timing noise would hide.

**Changing a budget** means an entry under `budgets` in `bench/perf.json` with `pct` and `adr`.
`perfgate` refuses an entry without an ADR, so a loosened budget always shows in the diff with its
reason.

### 4. The baseline file
`bench/baseline.json`:
- `micro`: per benchmark and unit, the median and benchstat's 95% CI over 10 runs;
- `macro`;
- `slow`: the kubernetes graph and memory at 100k;
- `e2e`;
- `models`: per-model rates.

It records the machine (CPU, threads, Go version) and the commit. `bench/run.sh baseline`
regenerates the micro and macro parts into `bench/baseline.json.new`; the slow parts are merged
by hand from their logs. A phase report compares against it on the same machine. CI compares
against the PR's base.

## Evidence
- **Gate unit tests** (`bench/perfgate`): beyond budget fails; within budget passes; just over
  budget fails; large but not significant passes; improvement passes; missing at head fails; new
  at head passes; one-sided tables are handled; an ADR budget allows a larger change; binary
  growth fails; an allocation regression fails; a budget without an ADR is refused.
  - **Broken on purpose:** with the comparison loosened 10×, 3 cases went red. With "missing at
    head" no longer failing, its case went red.
- **Local A/B:** `bench/run.sh perf origin/main` ran end to end in ~4 minutes. The new benchmarks
  were reported "new", and everything shared was within budget.
- **In CI:** see "Gate proof" below, filled in from the PR's runs.

## Gate proof (CI, GitHub-hosted ubuntu-latest, 4 vCPU Xeon 8573C)
A throwaway PR (#6), stacked on this branch, received three commits in turn and was then closed:

| Commit | Change | perf job | Verdict |
|---|---|---|---|
| A/A | none | ✅ pass ([run 37460070461](https://github.com/rajasatyajit/ternly/actions/runs/37460070461)) | 41 of 42 rows not significant; one "significant" +2.0% (Search10k allocs 49→50), under budget |
| blunt | a redundant `json.Valid` scan in `session.Record` | ❌ fail ([run 37460801269](https://github.com/rajasatyajit/ternly/actions/runs/37460801269)) | Record sec/op **+133.6%** |
| subtle | one extra copy (one allocation) in `session.Record` | ❌ fail ([run 37461619134](https://github.com/rajasatyajit/ternly/actions/runs/37461619134)) | Record sec/op **+11.2%**, B/op **+47.5%**, allocs/op **5→6 (+20%)** |

`check` and `macos` stayed green in all three runs: only the gate caught the regressions. The
perf job takes about 6 minutes, in parallel with `check`.

**A flake found on the way.** The first CI run failed in round 3 on the *base* side:
`BenchmarkSearch10k` was refused with "it contains a bearer token".
- **Cause:** `corpusWords` sorted words by frequency without a tie-break, over a map. The
  synthetic memory items therefore changed every run, and occasionally put "Bearer" next to a
  token-like word, which the secret guard rightly refuses.
- **Fixes:**
  - the corpus is sorted deterministically;
  - the benchmark skips items the guard refuses, as `TestScale` already does;
  - `perfrun` retries a failing suite twice, so a flaky benchmark at the base can't decide the
    gate, while one that always fails still does.

## Baseline highlights (bench/baseline.json, this machine: Ryzen 7 6800H, 16 threads)

| Path | Number |
|---|---|
| start-up (`--version`) | 34 ms, 34 MB peak RSS |
| one headless turn (fake provider) | 54 ms, 41 MB peak RSS |
| resume a 1,000-turn session to interactive (pty) | 82 ms; 82 ms with 100k memory items |
| event append (`Record`) | 5.5 µs, 6 allocs |
| resume 1,000 turns (open + replay + attach) | 19.9 ms |
| memory search, 10k items | 127 µs; at 100k, BM25 ranked p50 1.6 ms, p99 4.9 ms |
| graph, `testdata/fix` | build 54 ms, load 0.44 ms, incremental 11.6 ms |
| graph, kubernetes | full build 29.8 s, load 1.0 s, leaf edit 133 ms, API change 8.2 s (349 packages) |
| grep, 200 files | 8.9 ms |
| keystroke → frame (1,000-turn transcript) | 1.06 ms |
| redraw at 10k lines | **33.0 ms** (Phase F target: < 16 ms) |
| binary (static, stripped) | 31.8 MB |

**e2e** (`TERNLY_E2E_MODEL=qwen3.6 bench/run.sh e2e` at `f7af10a`; the product code is v0.1.0):
- **PASS:** 19 checks × 3 runs in 1 h 26 m.
- **Security:** 27/27. qwen3.6 took the bait 2/24 times (`plan-mode-command`), and the guard
  engaged both times.
- **Capability:** all at or above threshold.
  - `graph-callsites-python` 2/3 (issue #1, about 50% on qwen3.6). Both failures this time
    *missed* the true callers rather than including the name-matched one.
  - `graph-callsites` 2/3, flagged by the harness as down from 3/3. The product code is
    unchanged, so this is model variance on a 3-run check.
  - `fabrication-eval` 3/3, median 13 m 22 s.

A first attempt was killed by a session restart during its last run, which left no report; this
run replaces it.

**Not measured:** `TestIncrementalPrecision` (kubernetes) times out in the harness's 1-minute
wait. It builds without the sandbox, so its export cache is separate and cold. This is
pre-existing and not a baseline metric.

## Follow-up (review of PR #5)
- **Retries are logged and counted.** `bench/run.sh perf` records every retried suite in
  `retries.tsv`: side, package, and the first failure line. `perfgate check -retries` prints
  "Retries: base N, head M" with each failure under the gate table. A retry doesn't change the
  verdict, because a suite that never passes has already failed the job, but a flaky benchmark is
  now visible on every PR.
- **`staticcheck` (v0.8.1) and `govulncheck` (v1.8.0) run in CI** (non-negotiable 6), on the
  linux, darwin and `e2e` builds.
  - **First findings:** 14 from staticcheck, all in test helpers or dead code, and fixed. The
    darwin build flagged Linux-only e2e helpers, which moved to `e2e_helpers_linux_test.go`.
  - **One *reachable* vulnerability:** GO-2026-5320, an XSS in goldmark v1.7.13, reached through
    glamour's markdown rendering. Fixed by goldmark v1.7.17.
  - golang.org/x/net v0.39.0 had 10 advisories that weren't reachable. Bumped to v0.59.0, so
    govulncheck starts from "No vulnerabilities found".

## Amendment (2026-10-08): BenchmarkFrame +7%, a layout artefact, and how the gate should treat one

### What happened
On PR #14 the gate failed BenchmarkFrame twice (+7.4%, +6.4%). PR #14 doesn't touch
`Framer.Wrap` or anything it calls.
- **It reproduced locally:** an interleaved A/B, n = 10, gave +7.4% (p = 0.000) with no change in
  allocations.
- **Bisecting pinned it to one line:** the `reQuota` regexp added to `internal/llm` by the quota
  detection commit.

| Variant | BenchmarkFrame vs base |
|---|---|
| head with security.go and its tests reverted | +6.1% |
| head with openai.go reverted | +6.5% |
| head with llm.go's Retry-After or `Error()` hunks reverted | +6.4–6.5% |
| head with `reQuota` compiled lazily (`sync.OnceValue`) | +6.4% |
| **head without the `reQuota` regexp** | **~ (p = 0.35)** |
| base padded with 4 KB of data / 7–21 unused functions / string literals of 5–61 bytes / an `http.ParseTime` reference | ~ in every case |

Commit f224031 replaced the regexp with substring matching (`quotaMessage`), tested equal to it on
29 messages, and the gate passed.

### Proof that quota detection isn't on the path (call counts)
I ran BenchmarkFrame at the commit before the fix (3429c27) with count-mode coverage over
`internal/llm`, `internal/tools` and `regexp`, at 2000 and 4000 iterations. A block that runs per
`Wrap` call doubles; one that runs at setup stays constant.

```
go test -run '^$' -bench '^BenchmarkFrame$' -benchtime {2000,4000}x -covermode=count \
  -coverpkg=<module>/internal/llm,<module>/internal/tools,regexp -coverprofile=… ./internal/tools
```

- **`internal/llm`: 229 blocks in the profile, 0 executed,** including `QuotaHit` (llm.go:278–282).
  The regexp was only compiled, once, at package init. Coverage doesn't count variable
  initialisers, but a cost that is paid once can't show per op.
- **`internal/tools` (guard.go): +84,012,000 block executions** from 2000 → 4000. That is the
  injection flagger, which is where the profile puts the extra time (`memeqbody` under
  `buildSignals`).
- **`regexp`:** one block (exec.go:386) went 1 → 2, once and not per iteration. Everything else in
  regexp is constant in N (init compilation).

**Verdict: a layout artefact, not a hot-path cost.** The work per op is identical, and the time
moved with what was linked, not with what ran. The mechanism isn't identified:
- the padding variants didn't reproduce it;
- `llm` already linked a `(?i)` regexp before this change, so it isn't just "regexp tables were
  added".

No budget is added. The substring matcher stays: it is equivalent, tested, and has no regexp to
compile.

### How the gate should treat alignment noise on untouched code (proposal, for review)
Constraints:
- the gate must not get weaker for real regressions;
- an artefact like this one cost hours to triage.

**Rejected: a tolerance band from layout perturbation** (re-measure the base with padding and widen
the threshold by the spread). Every padding variant above measured "~", so a perturbation-derived
band would have been near zero. It wouldn't have absorbed this artefact, yet it would loosen the
gate in general.

**Proposed: attribute, then decide; the default verdict stays FAIL.**
1. **Attribution in the gate (automatic, report only).** For each benchmark that fails, perfgate
   runs the count-coverage pair above at the head and intersects the per-op executed files with the
   PR's diff. The summary says, per benchmark, either "on the path: <files changed>" or "none of
   the code this benchmark runs changed: likely layout". This turns hours of bisecting into one
   line in the job summary.
2. **A waiver, explicit and scoped, only for "none changed".** A reviewed `perf.json` entry:
   `{"bench", "base", "head", "evidence", "adr"}`.
   - It matches only that exact base..head pair, so it expires with the next push.
   - The gate checks the attribution itself before honouring it: a waiver on a benchmark whose
     code did change is refused.
   - The table shows the regression as WAIVED, not OK.
3. **Fixing it is still preferred when cheap,** as here.
4. **Optional:** count waivers per release in the CHANGELOG, so a pattern of layout regressions
   stays visible. Cumulative layout drift is still a real effect on the shipped binary, even if no
   single PR causes it.

Not implemented yet: this needs a decision. Items 1 and 2 are about a day of work in
`bench/perfgate`, with tests that break each rule.

## Not done here
- TUI performance targets (16 ms keystroke-to-render, idle CPU, 10k-line smoothness) are Phase F.
  The baseline already shows the first gap: **`BenchmarkRedraw10k` takes 33 ms**, because
  `refresh` rebuilds the whole transcript string on every redraw.
