#!/usr/bin/env bash
# Reproduces the benchmarks cited in docs/adr (007, 008, 009), and runs the
# real-model end-to-end checks (e2e). See bench/README.md.
# Usage: bench/run.sh [graph|tokens|graphmem|store|memory|eval|all|e2e [quick]|fuzz|fabrication|perf [base]|baseline]   (default: all but eval and e2e)
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD

# isolate DIR: HOME and every XDG directory inside DIR (the harness tripwire,
# eval.HarnessIsolated, refuses anything else); Go's caches stay where they are.
isolate() {
  export GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)" GOENV="$(go env GOENV)"
  mkdir -p "$1/home/.config" "$1/home/.local/share" "$1/home/.cache" "$1/home/.local/state"
  export HOME="$1/home" XDG_CONFIG_HOME="$1/home/.config" XDG_DATA_HOME="$1/home/.local/share" \
    XDG_CACHE_HOME="$1/home/.cache" XDG_STATE_HOME="$1/home/.local/state" TERNLY_HARNESS=1
}

e2e() { # every real-model check in bench/e2e_checks.txt, against a fresh static build ("e2e quick": security, 1 run)
  if [ "${1:-}" = quick ]; then export TERNLY_E2E_QUICK=1; fi
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
  isolate "$E2E_WORK"
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

fabrication() { # ADR 012: ternly --eval per model (MODELS, RUNS), isolated HOME; records → bench/results/fabrication
  local models=${MODELS:-qwen3.6} runs=${RUNS:-1}
  FAB_WORK=$(mktemp -d "$ROOT/.e2e-work-XXXXXX") # global: the EXIT trap runs after this function returns
  local work=$FAB_WORK
  trap 'rm -rf "$FAB_WORK"' EXIT
  CGO_ENABLED=0 go build -o "$work/ternly" .
  mkdir -p bench/results/fabrication
  isolate "$work"
  for m in ${models//,/ }; do
    echo "== $m"
    "$work/ternly" --eval --model "$m" --local-only --eval-runs "$runs" || true
  done
  cp "$work"/home/.local/share/ternly/capability/*.json bench/results/fabrication/ 2>/dev/null || true
}

fuzz() { # every go test -fuzz target, FUZZTIME each (default 60s); new crashers land in testdata/fuzz
  local t=${FUZZTIME:-60s}
  for spec in internal/llm:FuzzSSE internal/llm:FuzzOpenAIStream internal/llm:FuzzAnthropicStream \
    internal/tools:FuzzMCPRead internal/tools:FuzzMCPToolList internal/tools:FuzzMCPCallResult internal/tools:FuzzSchemaValidate \
    internal/plugins:FuzzLoadClaude internal/plugins:FuzzLoadGemini internal/plugins:FuzzFrontmatter \
    internal/session:FuzzLogRead internal/session:FuzzReplay; do
    echo "== ${spec#*:} (./${spec%%:*}, $t)"
    go test -run '^$' -fuzz "^${spec#*:}\$" -fuzztime "$t" "./${spec%%:*}" 2>&1 | tail -3
  done
}

BENCHSTAT=golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68 # pinned (ADR 017)

# perfbins SIDE DIR: test binaries of every bench/perf.json suite, built from checkout DIR
perfbins() {
  local side=$1 dir=$2 pkg re bt
  mkdir -p "$PERF_WORK/$side"
  while IFS=$'\t' read -r pkg re bt; do
    [ "$pkg" = count ] || [ "$pkg" = alpha ] && continue
    # a package or benchmark that doesn't exist at the base yet is reported "new", not a failure
    (cd "$dir" && go test -c -o "$PERF_WORK/$side/${pkg//\//_}.test" "./$pkg" >/dev/null 2>&1) ||
      echo "  ($side: ./$pkg has no test binary)"
  done <<<"$SUITES"
}

# perfrun SIDE DIR: one round of every suite, appended to SIDE.txt
perfrun() {
  local side=$1 dir=$2 pkg re bt bin
  while IFS=$'\t' read -r pkg re bt; do
    [ "$pkg" = count ] || [ "$pkg" = alpha ] && continue
    bin="$PERF_WORK/$side/${pkg//\//_}.test"
    [ -x "$bin" ] || continue
    local try=1 # up to 3 tries: a flaky benchmark at the base mustn't decide the gate; one that always fails does
    until (cd "$dir/$pkg" && "$bin" -test.run '^$' -test.bench "$re" -test.benchmem -test.count 1 -test.benchtime "$bt") >"$PERF_WORK/run.txt" 2>&1; do
      echo "perf: ./$pkg benchmarks failed ($side, try $try):" >&2; tail -20 "$PERF_WORK/run.txt" >&2
      [ $((try++)) -lt 3 ] || return 1
    done
    cat "$PERF_WORK/run.txt" >>"$PERF_WORK/$side.txt"
  done <<<"$SUITES"
}

perf() { # ADR 017: the CI performance gate. Interleaved A/B of bench/perf.json at BASE (default origin/main) and the working tree
  local base=${1:-origin/main} count alpha i bsize hsize
  PERF_WORK=$(mktemp -d "$ROOT/.perf-work-XXXXXX")
  trap 'git -C "$ROOT" worktree remove --force "$PERF_WORK/src" 2>/dev/null; rm -rf "$PERF_WORK"' EXIT
  PERF_OUT=${PERF_OUT:-$ROOT/bench/results/perf}
  mkdir -p "$PERF_OUT"
  git worktree add -q --detach "$PERF_WORK/src" "$base"
  echo "perf gate: $(git rev-parse --short "$base") (base) vs $(git rev-parse --short HEAD)$(git diff --quiet HEAD || echo -dirty) (head)"
  SUITES=$(go run ./bench/perfgate suites)
  count=$(awk -F'\t' '$1=="count"{print $2}' <<<"$SUITES")
  alpha=$(awk -F'\t' '$1=="alpha"{print $2}' <<<"$SUITES")
  GOBIN="$PERF_WORK/bin" go install "$BENCHSTAT"
  perfbins base "$PERF_WORK/src"
  perfbins head "$ROOT"
  for i in $(seq "$count"); do # alternate which side goes first, so drift hits both equally
    echo "  round $i/$count"
    if [ $((i % 2)) = 1 ]; then perfrun base "$PERF_WORK/src"; perfrun head "$ROOT"
    else perfrun head "$ROOT"; perfrun base "$PERF_WORK/src"; fi
  done
  bsize=$(cd "$PERF_WORK/src" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$PERF_WORK/ternly.base" . && stat -c %s "$PERF_WORK/ternly.base")
  hsize=$(CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$PERF_WORK/ternly.head" . && stat -c %s "$PERF_WORK/ternly.head")
  cp "$PERF_WORK/base.txt" "$PERF_WORK/head.txt" "$PERF_OUT/"
  (cd "$PERF_WORK" && bin/benchstat -alpha "$alpha" base.txt head.txt) >"$PERF_OUT/benchstat.txt"
  (cd "$PERF_WORK" && bin/benchstat -alpha "$alpha" -format csv base.txt head.txt 2>/dev/null) >"$PERF_OUT/benchstat.csv"
  # CPU and allocation profiles of the head, one file per suite (hot-path changes attach these)
  while IFS=$'\t' read -r pkg re bt; do
    [ "$pkg" = count ] || [ "$pkg" = alpha ] && continue
    local bin="$PERF_WORK/head/${pkg//\//_}.test" n=${pkg//\//_}
    (cd "$ROOT/$pkg" && "$bin" -test.run '^$' -test.bench "$re" -test.benchtime "$bt" \
      -test.cpuprofile "$PERF_OUT/$n.cpu.pprof" -test.memprofile "$PERF_OUT/$n.mem.pprof" >/dev/null)
  done <<<"$SUITES"
  set +e
  go run ./bench/perfgate check -csv "$PERF_OUT/benchstat.csv" -size "$bsize,$hsize" | tee "$PERF_OUT/summary.md"
  local status=${PIPESTATUS[0]}
  set -e
  echo "details: $PERF_OUT/benchstat.txt; profiles: $PERF_OUT/*.pprof"
  return "$status"
}

baseline() { # ADR 017: bench/baseline.json for HEAD (run on a quiet machine; BASELINE_E2E=<e2e report json> adds e2e)
  local tmp
  PERF_WORK=$(mktemp -d "$ROOT/.perf-work-XXXXXX")
  trap 'rm -rf "$PERF_WORK"' EXIT
  tmp=$PERF_WORK
  SUITES=$(go run ./bench/perfgate suites)
  GOBIN="$tmp/bin" go install "$BENCHSTAT"
  perfbins head "$ROOT"
  for i in $(seq "$(awk -F'\t' '$1=="count"{print $2}' <<<"$SUITES")"); do echo "  round $i"; perfrun head "$ROOT"; done
  (cd "$tmp" && bin/benchstat -format csv head.txt 2>/dev/null) >"$tmp/one.csv"
  go run ./bench/perfgate summary -csv "$tmp/one.csv" >"$tmp/micro.json"
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$tmp/ternly" .
  go build -o "$tmp/fake" ./bench/fakeprovider
  go run ./bench/macro -bin "$tmp/ternly" -fake "$tmp/fake" -n 15 >"$tmp/macro.json"
  local tti
  tti=$(go test -count=1 -run '^TestResumeLargeSessionTimeToInteractive$' -v . 2>&1 | sed -n 's/.*interactive with transcript in \([0-9.]*m\?s\).*/\1/p')
  jq -n --arg sha "$(git rev-parse HEAD)" --arg tag "$(git describe --tags --always)" --arg date "$(date -u +%FT%TZ)" \
    --arg go "$(go version | cut -d' ' -f3-)" --arg cpu "$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs)" \
    --arg threads "$(nproc)" --arg tti "$tti" --slurpfile micro "$tmp/micro.json" --slurpfile macro "$tmp/macro.json" \
    '{commit: $sha, describe: $tag, measured: $date, machine: {cpu: $cpu, threads: ($threads|tonumber), go: $go},
      micro: $micro[0], macro: ($macro[0] + {resume_1000_turns_to_interactive: $tti})}' >"$tmp/baseline.json"
  if [ -n "${BASELINE_E2E:-}" ]; then
    jq --slurpfile e "$BASELINE_E2E" '. + {e2e: {report: $e[0].git_sha, model: $e[0].model, runs_per_check: $e[0].runs_per_check,
      wall_seconds: $e[0].wall_seconds, ok: $e[0].ok, took_bait_runs: $e[0].took_bait_runs, attack_runs: $e[0].attack_runs,
      checks: [$e[0].checks[] | {name, class, threshold, runs, passes, pass_rate, median_ms, took_bait_runs, ok}]}}' "$tmp/baseline.json" >"$tmp/b2.json" && mv "$tmp/b2.json" "$tmp/baseline.json"
  fi
  cp "$tmp/baseline.json" bench/baseline.json.new
  echo "wrote bench/baseline.json.new (merge the slow numbers — graph, memory, fabrication — by hand; see ADR 017)"
}

case "${1:-default}" in
  e2e) e2e "${2:-}" ;;
  fuzz) fuzz ;;
  fabrication) fabrication ;;
  perf) perf "${2:-}" ;;
  baseline) baseline ;;
  graph|graphmem|tokens|store|memory|eval) "$1" ;;
  all) graph; graphmem; tokens; store; memory; eval ;;
  default) graph; graphmem; tokens; store; memory ;;
  *) echo "usage: $0 [graph|tokens|graphmem|store|memory|eval|all|e2e [quick]|fuzz|fabrication|perf [base]|baseline]" >&2; exit 2 ;;
esac
