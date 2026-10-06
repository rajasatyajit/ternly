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

## Gate proof (CI)
To be filled in from the CI runs on this PR:
- the A/A false-alarm check: the gate run against itself;
- the deliberate regression: a throwaway PR stacked on this branch that slows a gated path,
  shown failing, then closed.

## Not done here
- `staticcheck` and `govulncheck` in CI (non-negotiable 6) are a separate small PR, so that this
  one stays about performance.
- TUI performance targets (16 ms keystroke-to-render, idle CPU, 10k-line smoothness) are Phase F.
  The baseline already shows the first gap: **`BenchmarkRedraw10k` takes ~33 ms**, because
  `refresh` rebuilds the whole transcript string on every redraw.
