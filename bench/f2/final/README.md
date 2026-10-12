# F2 final comparison (2026-10-12)

This compares ternly's final F2 build (main 79ebb29) with Claude Code 2.1.296 and Codex 0.162.1, the two best-scoring
rivals in the survey. It used the survey's method (`bench/f2/README.md`, `bench/f2/probe/README.md`) and its model,
gemma4:latest, digest dc35e8d9c606. Ollama is 0.40.3 now; the survey ran 0.35.1.

| File | What |
|---|---|
| `scores.tsv` | S1–S5 outcomes from `scenarios/tools/score` |
| `MATRIX.md` | the 20-fact reach for the three, with the evidence for each cell |
| `SUMMARY.md` | the perf matrix (`tuiprobe -summarize`, 54 cells) |
| `metrics.jsonl` | one line per perf cell: measurements only (no environment, command line, screens or process ids) |

**How it ran:**
- `F2_RUNTIME=<dir> F2_ONLY="ternly claude codex"` with `scenarios/batch.sh`, `reach/reach-all.sh`, then
  `probe/matrix.sh <out> ternly claude codex`, with `FIXTURE` set to the suite's golang-lru.
- Each cell took the shared lock, so the three harnesses' cells ran one at a time, interleaved, on the owner's desktop.

**What's local:**
- The screens and raw output are recordings of other harnesses and of this machine, so they stay local.
- The first attempt is discarded: it ran with the proxy bug that `scenarios/tools/proxy` now fixes.

The findings, budgets and limits are in ADR 030, "F2 closed".
