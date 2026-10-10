#!/usr/bin/env bash
# One probe cell: a fresh isolated HOME and a fresh copy of the workspace
# fixture, then bench/tuiprobe with the given size, environment tier and
# steps, bounded by a timeout. Afterwards every process that still has the
# cell's HOME is stopped (Codex leaves an app-server daemon in its own
# session) and the cell fails if any survive. Thin glue (bench/f2/SPEC.md).
#
#   run1.sh NAME COLS ROWS TIER 'STEPS' OUT_PREFIX TIMEOUT_S
#   TIER: truecolor | 256 | 16 | nocolor | dumb, optionally +ascii (LANG=C)
#   writes OUT_PREFIX.json (a timed-out cell: {"outcome":"timeout",…})
set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=harnesses.sh
source "$here/harnesses.sh"
P=${PROBE_TMP:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/probe}
FIXTURE=${FIXTURE:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/phase-c/repos/golang-lru}
name=$1 cols=$2 rows=$3 tier=$4 steps=$5 out=$6 limit=${7:-600}
work=$(mktemp -d -p "$P" "run-$name-XXXXXX")
home=$work/home ws=$work/golang-lru
cleanup() {
  local r
  r=$("$P/tuiprobe" -reap "$home" 2>&1) || echo "run1: SURVIVORS after reap: $r" >&2
  [ -n "$r" ] && echo "run1: $r" >&2
  rm -rf "$work"
}
trap cleanup EXIT
mkdir -p "$home"
cp -r "$FIXTURE" "$ws"
"setup_$name" "$home" "$ws"
env_spec=$(common_env "$home")
[ -n "${ENV_EXTRA:-}" ] && env_spec="$env_spec,$ENV_EXTRA"
case "${tier%%+*}" in
truecolor) env_spec="$env_spec,TERM=xterm-256color,COLORTERM=truecolor,NO_COLOR=" ;;
256) env_spec="$env_spec,TERM=xterm-256color,COLORTERM=,NO_COLOR=" ;;
16) env_spec="$env_spec,TERM=xterm,COLORTERM=,NO_COLOR=" ;;
nocolor) env_spec="$env_spec,TERM=xterm-256color,COLORTERM=,NO_COLOR=1" ;;
dumb) env_spec="$env_spec,TERM=dumb,COLORTERM=,NO_COLOR=1" ;;
esac
case "$tier" in
*+ascii) env_spec="$env_spec,LANG=C,LC_ALL=C" ;;
*) env_spec="$env_spec,LANG=C.UTF-8,LC_ALL=C.UTF-8" ;;
esac
steps=${steps//@READY@/$READY}
steps=${steps//@PRE@/$PRE}
# SIGTERM at the limit: tuiprobe then kills the program's group and reaps its HOME.
timeout -s TERM -k 30 "$limit" "$P/tuiprobe" -clean-env -cols "$cols" -rows "$rows" -dir "$ws" -env "$env_spec" \
  -steps "$steps" -out "$out.json" -raw "$out.raw.gz" -- "${CMD[@]}"
st=$?
if [ "$st" = 124 ] || [ "$st" = 143 ] || [ "$st" = 137 ]; then
  printf '{"outcome":"timeout","limit_s":%s,"harness":"%s","cols":%s,"rows":%s,"tier":"%s"}\n' "$limit" "$name" "$cols" "$rows" "$tier" >"$out.json"
fi
exit $st
