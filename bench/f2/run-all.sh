#!/usr/bin/env bash
# run-all.sh: the whole F2 survey job: the scenario batch, then the reach
# sessions. Detached and resumable: rerun the same command after any
# interruption; finished cells and harnesses are skipped.
#   R=<runtime> setsid nohup bench/f2/run-all.sh > $R/run-all.log 2>&1 < /dev/null &
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}; export R
D=$(cd "$(dirname "$0")" && pwd)
echo $$ > "$R/run-all.pid"
echo "== scenarios $(date -u +%FT%TZ)"; "$D/scenarios/batch.sh" > "$R/batch.log" 2>&1
echo "== reach $(date -u +%FT%TZ)";     "$D/reach/reach-all.sh" > "$R/reach.log" 2>&1
echo "ALL DONE $(date -u +%FT%TZ)"
