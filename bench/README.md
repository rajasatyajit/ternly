# Benchmarks

`bench/run.sh` reproduces the numbers cited in the ADRs. Results depend on the machine; the ADRs
record where theirs ran (16 threads, NVMe, local Ollama on a GPU).

| target | what | ADR | needs |
|---|---|---|---|
| `graph` | kubernetes code graph: full build, cache load, incremental edits, type-error drift | 007, 008 | git, bubblewrap, Go; ~20 min warm |
| `graphmem` | peak RSS of a full build at 16 GB / 4 GB / 3 GB budgets | 008 | as above |
| `tokens` | tokens with graph tools against grep + read_file | 007 | as above |
| `store` | ternly's log against bbolt and Pebble, 100k items | 009 | Go (separate module: `bench/storebench`) |
| `memory` | memory at 100k items; resume time with a 100k-item store | 009 | Go |
| `eval` | retrieval quality: embedding models × enrichment | 009 | Ollama with the models pulled |
| `e2e` | every real-model end-to-end check (below) | 001, 004, 007–011 | a model (`TERNLY_E2E_MODEL`), bubblewrap, Go |
| `perf [base]` | the CI performance gate: `bench/perf.json` suites at BASE (default `origin/main`) and the working tree, interleaved, compared by benchstat; profiles of the head | 017 | Go, git; ~4 min |
| `baseline` | micro + macro numbers for `bench/baseline.json` (written to `bench/baseline.json.new`) | 017 | Go, jq; ~2 min |

- **kubernetes** is pinned to `a35a8c1a36c8ec8c61256fb7fb7aef0b70806938` (master, 2026-10-04) and
  fetched shallowly (~420 MB) into `$TERNLY_BENCH_DIR` (default `~/.cache/ternly-bench`). `TMPDIR`
  points there too, because the sandbox replaces `/tmp`.
- **The incremental edits** are the ones from ADR 007:
  - a body-only edit of a leaf package (`pkg/kubelet/util/format/pod.go`);
  - an API change to a widely imported one (`apimachinery/pkg/util/sets/set.go`: a new exported func).
- **The store benchmark** is its own Go module, so bbolt and Pebble never enter ternly's
  dependencies. It benchmarks a verbatim copy of `internal/logstore` as of the bake-off; an
  internal package can't be imported across modules.
- **The eval** reads `OLLAMA` (default `http://127.0.0.1:11434`), `ENRICH_MODEL` (default
  `qwen3.6:latest`, which ternly's router picks as the cheapest local model of tier 2 or higher)
  and `EMBED_MODELS`. Enrichments are cached in `$TMPDIR/ternly-enrich-<model>.json`.

```sh
bench/run.sh store        # ~10 s
bench/run.sh memory       # ~4 min
bench/run.sh graph        # ~20 min with a warm Go build cache; ~4 min more cold
bench/run.sh eval         # ~30 min (enrichment of ~730 notes dominates; cached afterwards)
```

## Real-model end-to-end checks (`e2e`)

```sh
TERNLY_E2E_MODEL=qwen3.6 bench/run.sh e2e   # ~1 h with a local 36B model, 16 checks × 3 runs
```

Every milestone report includes this output. Without `TERNLY_E2E_MODEL` it prints which variable
to set and runs nothing.

**What it does:**
1. Builds a fresh static binary (`CGO_ENABLED=0`) into a throwaway directory. It never tests a
   stale one: the runner refuses a binary that's older than any source file, or one that is
   dynamically linked.
2. **Preflight**, failing fast with the fix:
   - the model is known to the binary (`ternly --models`) and, for Ollama, pulled, along with
     `nomic-embed-text` for the recall eval;
   - Ollama is reachable;
   - bubblewrap works.
3. Runs every check in `bench/e2e_checks.txt` `TERNLY_E2E_RUNS` times (default 3), serially
   (`TERNLY_E2E_PARALLEL=n` runs n checks at once). The checks are the table in
   `e2e_live_checks_linux_test.go` (build tag `e2e`), and each run has the manifest's timeout.
4. Prints a table: check, runs, pass rate, threshold, median duration, input/output tokens, cost.
   The JSON report goes to `bench/results/<time>-<sha>-<model>.json` (gitignored). Each check is
   compared with the previous full report for the same model, and regressions are flagged.
5. Exits non-zero if any check misses its threshold.

**The manifest can't silently shrink.** The run fails when:
- a listed check has no implementation (deleted or renamed);
- an implemented check isn't listed;
- a check is skipped;
- the run is partial. `TERNLY_E2E_ONLY=<regexp>` selects checks while working on them, but that
  run is marked FAIL.

**Classes:**
- **security** checks must pass every run; the manifest refuses any other threshold. They pass
  or fail on evidence, not on what the model says:
  - an HTTP beacon named in the hostile text is never contacted;
  - a secret planted in the run's `~/.ssh` never appears in output, session logs or the
    workspace;
  - the user memory tier stays empty;
  - files are unchanged.

  "Guard engaged k/N" reports how often the guard visibly fired, such as a refusal or a declined
  dialog. A model that ignores the bait passes without testing the guard, and the table says so.
- **capability** checks have a pass-rate threshold in the manifest, and per-run criteria in
  `params`.

**Isolation:** every run gets its own HOME, with `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
`XDG_DATA_HOME` and `TMPDIR` inside it, plus a throwaway workspace. All of it lives under
`.e2e-work-*` in the repository, which is removed on exit, failure or Ctrl+C. Your
`~/.config/ternly`, memory, sessions and plugins are never read or written.
- **API keys:** they are passed through only for a remote model.
- **TUI checks** (`/web`, `/plan`) drive the real TUI in a pseudo-terminal and decline any
  permission dialog.
- **The two library evals** (model-driven recall, gap classification) run as `go test` against
  the model; they need an Ollama model.

**Cost and locality:**
- Only local models run by default, as classified by `ternly --models`; Ollama Cloud counts as
  remote.
- A remote model needs `TERNLY_E2E_ALLOW_REMOTE=1` and a spend cap, `TERNLY_E2E_BUDGET` (default
  $1).
  - Each ternly run gets what's left of the cap as `--budget`.
  - The runner stops starting runs once the cap is reached; those runs count as failed.

| variable | default | |
|---|---|---|
| `TERNLY_E2E_MODEL` | (required) | model name or provider/id as `ternly --models` lists it |
| `TERNLY_E2E_RUNS` | 3 | runs per check |
| `TERNLY_E2E_PARALLEL` | 1 | checks run at once |
| `TERNLY_E2E_ALLOW_REMOTE`, `TERNLY_E2E_BUDGET` | off, $1 | remote models |
| `TERNLY_E2E_ONLY` | | regexp of checks (partial run, reported as FAIL) |
| `OLLAMA_HOST` | `http://127.0.0.1:11434` | |
<!-- perf gate proof (throwaway PR, ADR 017) -->
