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
