#!/usr/bin/env bash
# ADR 031's measurement plan, cell by cell (resumable; thin glue). Needs:
#   MEAS (a directory with ws/, a small workspace), OLD and NEW (ternly
#   binaries: main and this change), TUIPROBE (bench/tuiprobe, track2/f2-probe),
#   COUNTPROXY (bench/countproxy), HEAVY_LOCK (the shared timing lock).
# Each cell: a fresh or reused HOME, the UI in a pty, idle CPU windows of the
# process tree, and the bytes each host sent through the counting proxy.
set -u
here=$(cd "$(dirname "$0")" && pwd)
H=$MEAS/homes
back() { # the catalog's last refresh 25 h ago: a daily refresh is due
  local s=$1/.local/share/ternly/catalog/state.json
  jq --arg t "$(date -u -d '25 hours ago' +%Y-%m-%dT%H:%M:%SZ)" '.last=$t' "$s" >"$s.new" && mv "$s.new" "$s"
}
READY='until 30000 Ask\sternly'
Q='until 30000 [y/N]; type \n' # the first-run question (new binary, no config): answered N
c() { "$here/cell.sh" "$@"; }
c old-fresh  "$OLD" "$H/old"  "$READY; sleep 8000; idle 30; idle 300; snap end" 450 18101 TERNLY_BACKGROUND_EVAL=1
c old-second "$OLD" "$H/old"  "$READY; sleep 8000; idle 30; idle 60; snap end" 150 18102 TERNLY_BACKGROUND_EVAL=1
[ -f "$MEAS/cells/old-daily/result.json" ] || back "$H/old"
c old-daily  "$OLD" "$H/old"  "$READY; sleep 8000; idle 30; idle 120; snap end" 220 18103 TERNLY_BACKGROUND_EVAL=1
c old-int1   "$OLD" "$H/old2" "$READY; sleep 8000; idle 30" 100 18104 TERNLY_BACKGROUND_EVAL=1
c old-int2   "$OLD" "$H/old2" "$READY; sleep 8000; idle 30; idle 300; snap end" 450 18105 TERNLY_BACKGROUND_EVAL=1
c new-fresh  "$NEW" "$H/new"  "$Q; $READY; sleep 8000; idle 30; idle 255; idle 330; snap end" 700 18106
c new-second "$NEW" "$H/new"  "$READY; sleep 8000; idle 30; idle 255; idle 60; snap end" 420 18107
[ -f "$MEAS/cells/new-daily/result.json" ] || back "$H/new"
c new-daily  "$NEW" "$H/new"  "$READY; sleep 8000; idle 30; idle 255; idle 90; snap end" 450 18108
c new-int1   "$NEW" "$H/new2" "$Q; $READY; sleep 8000; idle 30; idle 255; idle 45; snap end" 420 18109
c new-int2   "$NEW" "$H/new2" "$READY; sleep 8000; idle 30; idle 255; idle 300; snap end" 680 18110
echo ALL-DONE
