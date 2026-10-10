#!/usr/bin/env bash
# batch.sh: every scenario × harness, resumable and detached-safe.
#   start:  setsid nohup bench/f2/scenarios/batch.sh > $R/batch.log 2>&1 < /dev/null &
#   rerun:  the same command; a cell with result.txt is skipped.
# Each cell: `flock -o` on the shared lock (nothing started inherits it), a
# hard timeout (a hung harness becomes outcome=timeout), then cleanup of every
# process with that cell's HOME. The model proxy runs outside the lock and
# is stopped at the end. Progress: $R/out/state.txt.
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}; export R
D=$(cd "$(dirname "$0")" && pwd)
LOCK=/home/satyajit/.claude/jobs/f10e4ff6/tmp/heavy.lock
HARNESSES=${HARNESSES:-"ternly claude codex crush opencode pi codewhale"}
SCENARIOS=${SCENARIOS:-"S1 S2 S4 S3 S5"}
mkdir -p "$R/out"; echo $$ > "$R/batch.pid"
setsid nohup "$R/bin/proxy" >/dev/null 2>&1 < /dev/null & echo $! > "$R/proxy.pid"; sleep 0.5
trap 'kill "$(cat "$R/proxy.pid")" 2>/dev/null; tmux -L f2rec kill-server 2>/dev/null; "$R/bin/homekill" "$R/homes" >/dev/null 2>&1' EXIT
state() { echo "$(date -u +%FT%TZ) $*" >> "$R/out/state.txt"; }
for sc in $SCENARIOS; do for hn in $HARNESSES; do
  r=$R/out/$hn/$sc/result.txt
  if [ -f "$r" ]; then echo "skip $hn $sc: $(cat "$r")"; continue; fi
  case $sc in S5) lim=4500 ;; S3) lim=1800 ;; *) lim=1200 ;; esac
  echo "== $hn $sc $(date -u +%FT%TZ) (limit ${lim}s)"; state "start $hn $sc"
  flock -o "$LOCK" timeout --kill-after=30 "$lim" "$D/run.sh" "$hn" "$sc" > "$R/out/$hn-$sc.run.log" 2>&1; st=$?
  tmux -L f2rec kill-session -t "$hn-$sc" 2>/dev/null; "$R/bin/homekill" "$R/homes/$hn-$sc" >/dev/null 2>&1
  if [ $st = 124 ] || [ $st = 137 ]; then mkdir -p "$R/out/$hn/$sc"; echo "$hn $sc outcome=timeout limit=${lim}s" > "$r"; fi
  [ -f "$r" ] || { mkdir -p "$R/out/$hn/$sc"; echo "$hn $sc outcome=harness-error exit=$st" > "$r"; }
  echo "   $(cat "$r")"; state "done $hn $sc: $(cat "$r")"
done; done
echo "BATCH DONE $(date -u +%FT%TZ)"; state "batch done"
