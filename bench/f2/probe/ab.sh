#!/usr/bin/env bash
# ab.sh OUT A_BIN B_BIN: a paired A/B of two ternly builds' output bytes, in
# the perf matrix's cells (bench/f2/probe/matrix.sh): the S1 stream ×3 and the
# 10-turn long session ×2, at 120×40 truecolor, the builds alternating so the
# model and the machine drift alike. Each cell takes the shared lock
# (`flock -o`). Thin glue; measuring is bench/tuiprobe. Analyse with
# `tuiprobe -rowbytes OUT/<cell>.raw.gz -rows 40` (ADR 030, F2.1).
#   PROBE_TMP, HARNESSES, FIXTURE, LOCK as for matrix.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
out=$1 a=$2 b=$3
LOCK=${LOCK:-${TMPDIR:-/var/tmp}/ternly-f2/heavy.lock}
S1='Explain what simplelru/lru.go does and how eviction works.'
STREAM="until 60000 @READY@; @PRE@; stable 1500 30000; type ${S1// /\\s}; sleep 700; enter; stream 8000 300000 3000; snap answered; rss"
LONG="until 60000 @READY@; @PRE@; stable 1500 30000; rss"
for i in 1 2 3 4 5 6 7 8 9 10; do
  LONG="$LONG; type Turn\\s$i:\\sname\\sone\\sfunction\\sin\\ssimplelru/lru.go\\sand\\swhat\\sit\\sreturns,\\sin\\sone\\sline.; sleep 700; enter; stream 6000 300000 1500; rss"
done
mkdir -p "$out"
cell() { # NAME BIN KIND ROUND LIMIT
  local f=$out/$1.$3-r$4 steps=$STREAM
  [ "$3" = long ] && steps=$LONG
  [ -f "$f.json" ] && return
  TERNLY_BIN=$2 flock -o "$LOCK" "$here/run1.sh" ternly 120 40 truecolor "$steps" "$f" "$5" >/dev/null 2>"$f.err"
  echo "$(date -u +%T) $3 r$4 $1"
}
for r in 1 2 3; do cell a "$a" stream $r 600; cell b "$b" stream $r 600; done
for r in 1 2; do cell b "$b" long $r 3600; cell a "$a" long $r 3600; done
echo "AB DONE"
