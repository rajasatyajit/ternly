#!/usr/bin/env bash
# reach-all.sh: reach.sh for every harness, resumable (a harness with final.txt
# is skipped), each under `flock -o` on the shared lock with a 30-minute limit.
#   start: setsid nohup bench/f2/reach/reach-all.sh > $R/reach.log 2>&1 < /dev/null &
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}; export R
D=$(cd "$(dirname "$0")" && pwd)
LOCK=/home/satyajit/.claude/jobs/f10e4ff6/tmp/heavy.lock
echo $$ > "$R/reach.pid"
pidf=$R/reach-proxy.pid
# the model proxy on :11435 may already run for the scenario batch; start one only if the port is free
if ! (exec 3<>/dev/tcp/127.0.0.1/11435) 2>/dev/null; then setsid nohup "$R/bin/proxy" >/dev/null 2>&1 < /dev/null & echo $! > "$pidf"; sleep 0.5; fi
trap '[ -f "$pidf" ] && kill "$(cat "$pidf")" 2>/dev/null; rm -f "$pidf"; tmux -L f2reach kill-server 2>/dev/null' EXIT
for hn in ${HARNESSES:-ternly claude codex crush opencode pi codewhale}; do
  [ -f "$R/reach/$hn/final.txt" ] && { echo "skip $hn"; continue; }
  echo "== $hn $(date -u +%FT%TZ)"
  flock -o "$LOCK" timeout --kill-after=30 1800 "$D/reach.sh" "$hn" 2>&1 | tail -3
  tmux -L f2reach kill-session -t "reach-$hn" 2>/dev/null; "$R/bin/homekill" "$R/homes/reach-$hn" >/dev/null 2>&1
done
echo "REACH DONE $(date -u +%FT%TZ)"
