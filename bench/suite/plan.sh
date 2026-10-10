#!/usr/bin/env bash
# A resumable measurement plan (Phase C, ADR 029). It survives the session
# that started it: run it detached, check it with `status`, and rerun the same
# command after an interruption — it continues where it stopped.
#
#   bench/suite/plan.sh start  PLAN OUT   # detached; log: OUT/plan.log
#   bench/suite/plan.sh status OUT
#   bench/suite/plan.sh run    PLAN OUT   # in the foreground (what start runs)
#
# PLAN has one step per line: `<name> <suite run arguments…>` (# comments and
# blank lines ignored). {OUT} expands to OUT and {BIN} to OUT/ternly, the one
# ternly binary every step measures (built at the first run, with its commit
# in OUT/ternly.commit; delete both to measure a new build). A step runs under
# the shared heavy lock, resumes at task granularity (`suite run -resume`),
# and is marked done in OUT/steps/<name>.done. Per-step progress is in that
# step's -out directory, state.json.
set -uo pipefail
cd "$(dirname "$0")/../.."
LOCK=${HEAVY_LOCK:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/heavy.lock}
cmd=${1:-}; shift || true
case "$cmd" in
start)
  plan=$(realpath "$1"); out=$(realpath -m "$2"); mkdir -p "$out"
  setsid nohup "$0" run "$plan" "$out" >>"$out/plan.log" 2>&1 </dev/null &
  echo "started (pid $!); log: $out/plan.log; status: $0 status $out"; exit 0 ;;
status)
  out=$(realpath -m "$1")
  echo "plan: $(cat "$out/plan.state" 2>/dev/null || echo 'not started')"
  if pid=$(cat "$out/plan.pid" 2>/dev/null) && kill -0 "$pid" 2>/dev/null; then echo "running: pid $pid"; else echo "not running"; fi
  for s in "$out"/*/state.json; do [ -f "$s" ] && "$out/suite" status "$s"; done
  tail -3 "$out/plan.log" 2>/dev/null; exit 0 ;;
run) ;;
*) sed -n '2,20p' "$0"; exit 2 ;;
esac
plan=$1; out=$2; mkdir -p "$out/steps"
echo $$ >"$out/plan.pid"
if [ ! -x "$out/suite" ]; then go build -o "$out/suite" ./bench/suite || exit 1; fi
if [ ! -x "$out/ternly" ]; then
  CGO_ENABLED=0 go build -o "$out/ternly" . || exit 1
  c=$(git rev-parse HEAD); d=false; [ -n "$(git status --porcelain --untracked-files=no)" ] && d=true
  echo "$c dirty=$d" >"$out/ternly.commit"
fi
total=$(grep -cvE '^\s*(#|$)' "$plan"); n=0
while read -r name args; do
  case "$name" in ''|'#'*) continue ;; esac
  n=$((n+1))
  if [ -f "$out/steps/$name.done" ]; then echo "[$(date -u +%FT%TZ)] step $n/$total $name: already done"; continue; fi
  echo "step $n/$total: $name (started $(date -u +%FT%TZ))" >"$out/plan.state"
  echo "[$(date -u +%FT%TZ)] step $n/$total $name: suite run $args"
  args=${args//\{OUT\}/$out}; args=${args//\{BIN\}/$out/ternly}
  # shellcheck disable=SC2086
  if flock "$LOCK" "$out/suite" run $args; then
    date -u +%FT%TZ >"$out/steps/$name.done"
    echo "[$(date -u +%FT%TZ)] step $n/$total $name: done"
  else
    st=$?; echo "[$(date -u +%FT%TZ)] step $n/$total $name: FAILED (exit $st); rerun the plan to resume"
    echo "step $n/$total: $name FAILED (exit $st)" >"$out/plan.state"; exit "$st"
  fi
done <"$plan"
echo "all $total steps done ($(date -u +%FT%TZ))" >"$out/plan.state"
echo "[$(date -u +%FT%TZ)] plan done"
