#!/usr/bin/env bash
# The F2 performance matrix (bench/f2/SPEC.md "Performance tiers"), resumable:
# a cell whose result JSON exists (a measurement or a recorded timeout) is
# skipped, so rerunning continues where it stopped. Each cell takes the shared
# heavy lock with `flock -o` (nothing it starts can inherit the lock) and is
# bounded by a timeout. OUT/state.txt says where it is. Thin glue; measuring is
# bench/tuiprobe.
#
#   bench/f2/probe/matrix.sh OUT [harness …]        # foreground
#   bench/f2/probe/start.sh  OUT [harness …]        # detached (setsid nohup)
set -u
here=$(cd "$(dirname "$0")" && pwd)
out=$1; shift
names=${*:-ternly claude codex gemini crush opencode pi codewhale}
LOCK=/home/satyajit/.claude/jobs/f10e4ff6/tmp/heavy.lock
mkdir -p "$out"
S1='Explain what simplelru/lru.go does and how eviction works.'

BASE='until 60000 @READY@; @PRE@; stable 1500 30000; snap ready; keys abcdefghij; snap typed; idle 30; rss'
RESIZE='until 60000 @READY@; @PRE@; stable 1500 30000; snap at120x40; resize 80 24; snap at80x24; resize 250 70; snap at250x70'
STREAM="until 60000 @READY@; @PRE@; stable 1500 30000; type ${S1// /\\s}; enter; stream 8000 300000; snap answered; rss"
LONG="until 60000 @READY@; @PRE@; stable 1500 30000; rss"
for i in 1 2 3 4 5 6 7 8 9 10; do
  LONG="$LONG; type Turn\\s$i:\\sname\\sone\\sfunction\\sin\\ssimplelru/lru.go\\sand\\swhat\\sit\\sreturns,\\sin\\sone\\sline.; enter; stream 6000 300000; rss"
done

total=0 done_n=0
cells=()
add() { cells+=("$*"); total=$((total + 1)); }
for n in $names; do
  for r in 1 2 3; do add "$n base-120x40-truecolor-r$r 120 40 truecolor BASE 300"; done
  for t in 256 16 nocolor truecolor+ascii dumb; do add "$n base-120x40-$t 120 40 $t BASE 300"; done
  add "$n base-80x24-truecolor 80 24 truecolor BASE 300"
  add "$n base-250x70-truecolor 250 70 truecolor BASE 300"
  add "$n resize-120x40 120 40 truecolor RESIZE 240"
done
for n in $names; do
  [ "$n" = gemini ] && continue # no shared model (Google auth only)
  for r in 1 2 3; do
    add "$n stream-120x40-r$r 120 40 truecolor STREAM 600"
    add "$n stream-80x24-r$r 80 24 truecolor STREAM 600"
  done
  add "$n long-120x40 120 40 truecolor LONG 3600"
done

state() { printf 'cells %d/%d done; %s; updated %s\n' "$done_n" "$total" "$1" "$(date -u +%FT%TZ)" >"$out/state.txt"; }
for c in "${cells[@]}"; do
  read -r n tag cols rows tier kind limit <<<"$c"
  f=$out/$n.$tag
  if [ -f "$f.json" ]; then done_n=$((done_n + 1)); continue; fi
  case $kind in BASE) steps=$BASE ;; RESIZE) steps=$RESIZE ;; STREAM) steps=$STREAM ;; LONG) steps=$LONG ;; esac
  state "running $n $tag"
  echo "$(date -u +%T) run $n $tag (limit ${limit}s)"
  flock -o "$LOCK" "$here/run1.sh" "$n" "$cols" "$rows" "$tier" "$steps" "$f" "$limit" >/dev/null 2>"$f.err" || echo "  exit $? ($f.err)"
  [ -f "$f.json" ] || printf '{"outcome":"no-result","harness":"%s","tag":"%s"}\n' "$n" "$tag" >"$f.json"
  done_n=$((done_n + 1))
done
state "MATRIX DONE"
echo "MATRIX DONE $(date -u +%FT%TZ)"
