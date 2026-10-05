#!/usr/bin/env bash
# Reproduces the benchmarks cited in docs/adr (007, 008, 009), and runs the
# real-model end-to-end checks (e2e). See bench/README.md.
# Usage: bench/run.sh [graph|tokens|graphmem|store|memory|eval|all|e2e]   (default: all but eval and e2e)
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD

e2e() { # every real-model check in bench/e2e_checks.txt, against a fresh static build
  if [ -z "${TERNLY_E2E_MODEL:-}" ]; then
    echo "bench/run.sh e2e: set TERNLY_E2E_MODEL to the model to test, e.g. TERNLY_E2E_MODEL=qwen3.6 bench/run.sh e2e" >&2
    exit 2
  fi
  E2E_WORK=""
  E2E_WORK=$(mktemp -d "$ROOT/.e2e-work-XXXXXX") # not /tmp: the sandbox replaces /tmp
  trap 'rm -rf "$E2E_WORK"' EXIT
  trap 'rm -rf "$E2E_WORK"; exit 130' INT TERM
  echo "building a static binary…"
  CGO_ENABLED=0 go build -o "$E2E_WORK/ternly" . || { echo "static build failed: fix the build, then rerun" >&2; exit 1; }
  local sha
  sha=$(git rev-parse --short HEAD 2>/dev/null || echo nogit)
  git diff --quiet HEAD 2>/dev/null || sha="$sha-dirty"
  set +e
  TERNLY_E2E_BIN="$E2E_WORK/ternly" TERNLY_E2E_WORK="$E2E_WORK/runs" TERNLY_E2E_SHA="$sha" \
    go test -tags e2e -count=1 -run '^TestE2E$' -v -timeout "${TERNLY_E2E_TIMEOUT:-6h}" . 2>&1 |
    grep --line-buffered -v -E '^(=== RUN|--- (PASS|FAIL): TestE2E|PASS$|FAIL$|ok |FAIL\s)'
  local status=${PIPESTATUS[0]}
  set -e
  return "$status"
}
WORK=${TERNLY_BENCH_DIR:-$HOME/.cache/ternly-bench}
K8S_COMMIT=a35a8c1a36c8ec8c61256fb7fb7aef0b70806938 # kubernetes master, 2026-10-04 (ADR 007/008)
mkdir -p "$WORK/tmp"
export TMPDIR="$WORK/tmp" # off /tmp: the sandbox replaces /tmp, and builds need the space

k8s() {
  if [ ! -d "$WORK/kubernetes/.git" ]; then
    git init -q "$WORK/kubernetes"
    git -C "$WORK/kubernetes" fetch -q --depth 1 https://github.com/kubernetes/kubernetes "$K8S_COMMIT"
    git -C "$WORK/kubernetes" checkout -q FETCH_HEAD
  fi
  test "$(git -C "$WORK/kubernetes" rev-parse HEAD)" = "$K8S_COMMIT" || { echo "kubernetes clone is not at $K8S_COMMIT" >&2; exit 1; }
}

graph() { # ADR 007/008: full build, cache load, incremental edits, first pass
  k8s
  TERNLY_GRAPH_BENCH="$WORK/kubernetes" \
  BENCH_LEAF_FILE=pkg/kubelet/util/format/pod.go BENCH_LEAF_FROM='"%s_%s(%s)"' BENCH_LEAF_TO='"%s_%s[%s]"' \
  BENCH_API_FILE=staging/src/k8s.io/apimachinery/pkg/util/sets/set.go BENCH_API_FROM='// Len returns the size of the set.' \
  BENCH_API_TO=$'func PingUNIQ() int { return 1 }\n\n// Len returns the size of the set.' \
    go test -count=1 -run 'TestLargeRepoBenchmark|TestIncrementalPrecision' -v -timeout 90m ./internal/graph
}

graphmem() { # ADR 008: peak RSS per worker count / budget (one build per process)
  k8s
  for cfg in "16 16384" "16 4096" "1 3072"; do
    set -- $cfg
    TERNLY_GRAPH_BENCH="$WORK/kubernetes" TERNLY_GRAPH_MEM=1 TERNLY_GRAPH_WORKERS=$1 TERNLY_GRAPH_BUDGET_MB=$2 \
      go test -count=1 -run TestBuildMemory -v -timeout 60m ./internal/graph
  done
}

tokens() { # ADR 007: tokens with graph tools vs grep + read_file
  k8s
  TERNLY_TOKEN_BENCH="$WORK/kubernetes" go test -count=1 -run TestTokenReduction -v -timeout 60m ./internal/graph
}

store() { # ADR 009: ternly log vs bbolt vs Pebble, 100k items
  (cd bench/storebench && go test -count=1 -run TestStores -v -timeout 30m .)
}

memory() { # ADR 009: memory at 100k items, and resume time with a 100k-item store
  TERNLY_MEM_SCALE=100000 go test -count=1 -run TestScale -v -timeout 30m ./internal/memory
  TERNLY_E2E_BIGMEM=100000 go test -count=1 -run TestResumeWithLargeMemory -v -timeout 10m .
}

eval() { # ADR 009: retrieval quality; needs Ollama with the models below pulled
  : "${OLLAMA:=http://127.0.0.1:11434}"
  go test -count=1 -run TestEval$ -v ./internal/memory
  TERNLY_MEM_MATRIX=1 TERNLY_MEM_OLLAMA=$OLLAMA TERNLY_MEM_ENRICH=${ENRICH_MODEL:-qwen3.6:latest} \
  TERNLY_MEM_EMBED_MODELS=${EMBED_MODELS:-nomic-embed-text,mxbai-embed-large,bge-m3,qwen3-embedding:0.6b} \
    go test -count=1 -run TestEvalMatrix -v -timeout 4h ./internal/memory
}

case "${1:-default}" in
  e2e) e2e ;;
  graph|graphmem|tokens|store|memory|eval) "$1" ;;
  all) graph; graphmem; tokens; store; memory; eval ;;
  default) graph; graphmem; tokens; store; memory ;;
  *) echo "usage: $0 [graph|tokens|graphmem|store|memory|eval|all|e2e]" >&2; exit 2 ;;
esac
