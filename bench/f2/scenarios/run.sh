#!/usr/bin/env bash
# run.sh <harness> <S1..S5>: one recorded scenario cell. Writes $R/out/<harness>/<sc>/
# (timeline, screens, timings, result.txt). Called by batch.sh under `flock -o`,
# so nothing it starts inherits the shared lock. It stops everything it started.
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}
D=$(cd "$(dirname "$0")" && pwd)
hn=$1 sc=$2; id=$hn-$sc; out=$R/out/$hn/$sc; rm -rf "${out:?}"; mkdir -p "$out" "$R/fx" "$R/homes"
fx=$R/fx/$id; "$R/bin/prep" "$sc" "$fx" >/dev/null || { echo "$hn $sc outcome=prep-failed" | tee "$out/result.txt"; exit 1; }
S="tmux -L f2rec"
cleanup() { $S kill-session -t "$id" 2>/dev/null; "$R/bin/homekill" -grace 3s "$R/homes/$id" > "$out/cleanup.txt" 2>&1; }
trap cleanup EXIT
proxy_down() { [ -f "$R/proxy.pid" ] && kill "$(cat "$R/proxy.pid")" 2>/dev/null; sleep 0.5; }
proxy_up() { setsid nohup "$R/bin/proxy" >/dev/null 2>&1 < /dev/null & echo $! > "$R/proxy.pid"; sleep 0.5; }
t0=$(date +%s)
# the boundary's self-check, in this cell's own dirs, before the harness starts; any FAIL refuses the cell
mkdir -p "$R/homes/$id"; "$D/box.sh" --check "$fx" "$R/homes/$id" > "$out/sandbox-check.txt" 2>&1
if [ $? -ne 0 ] || grep -q '^FAIL' "$out/sandbox-check.txt"; then echo "$hn $sc outcome=sandbox-check-failed" | tee "$out/result.txt"; exit 1; fi
"$D/launch.sh" "$hn" "$id" "$fx" > "$out/launch.txt" || { echo "$hn $sc outcome=launch-failed" | tee "$out/result.txt"; exit 1; }
if ! "$D/onboard.sh" "$hn" "$id" > "$out/onboard.txt" 2>&1; then
  $S capture-pane -p -t "$id" > "$out/final.txt" 2>/dev/null
  echo "$hn $sc outcome=not-ready ($(tail -1 "$out/onboard.txt"))" | tee "$out/result.txt"; exit 1
fi
$S capture-pane -p -t "$id" > "$out/ready.txt"
ask() { # ask <prompt> <idle> <max> <label>
  "$D/rec.sh" type "$id" "$1"; local w; w=$("$D/rec.sh" wait "$id" "$2" "$3" "$out/$4"); echo "$4 $w" >> "$out/timings.txt"; }
case $sc in
S1) ask "Explain what simplelru/lru.go does and how eviction works." 25 600 turn ;;
S2) ask "Bug report: in package simplelru, calling Resize with a capacity at least as large as the number of entries still evicts one entry. Find the cause and fix it." 30 900 turn ;;
S3) ask "Plan, then implement in src/index.ts: (1) once(type, handler): the handler runs on the next emit of that type only, then is removed; (2) emit returns the number of handlers it called. Use parallel sub-agents if you can." 30 1200 turn ;;
S4) ask "Review the uncommitted change in this repository (git diff) and list every bug you find, with file:line and why." 25 600 turn ;;
S5) for i in $(seq 30); do ask "Turn $i: name one Go standard library package and say what it does, in one line." 8 180 "t$i"; done
    proxy_down
    ask "Turn 31: in one line, what does simplelru/lru.go do?" 20 300 "error"
    $S capture-pane -p -t "$id" > "$out/error-screen.txt"
    proxy_up
    ask "Turn 32: try again: in one line, what does simplelru/lru.go do?" 15 300 "recover"
    pp=$($S display-message -p -t "$id" '#{pane_pid}'); rss=0
    for p in $(cat /proc/$pp/task/*/children 2>/dev/null) $pp; do r=$(awk '/VmRSS/{print $2}' /proc/$p/status 2>/dev/null); rss=$((rss + ${r:-0})); done
    echo "rss_kb $rss" >> "$out/timings.txt" ;;
esac
$S capture-pane -p -t "$id" > "$out/final.txt"
$S capture-pane -p -J -S - -t "$id" > "$out/scrollback.txt"
case $sc in # objective checks only
S1) grep -qiE "evict" "$out/scrollback.txt" && grep -qiE "least.recently|LRU|oldest" "$out/scrollback.txt" && o=answered || o=no-answer ;;
S2) "$D/box.sh" "$fx" "$R/homes/$id" -- env HOME="$R/homes/$id" GOCACHE="$R/homes/$id/.cache/go-build" GOMODCACHE="$R/homes/$id/go/pkg/mod" GOFLAGS=-mod=mod GOTOOLCHAIN=local go test -count=1 ./simplelru/ >/dev/null 2>&1 && o=pass || o=fail ;; # model-written code: tested inside the boundary
S3) f=$fx/src/index.ts; grep -qE "once\s*[<(:]" "$f" && a=y || a=n; grep -qE "return\s+\w*(count|called|calls|total|handlers?\w*\.length|n)\b" "$f" && b=y || b=n; o="once=$a count=$b" ;;
S4) grep -qiE "peek" "$out/scrollback.txt" && grep -qiE "MoveToFront|recency|recent" "$out/scrollback.txt" && a=y || a=n
    grep -qiE "resize" "$out/scrollback.txt" && grep -qE "<=|off.by.one|one too many|extra" "$out/scrollback.txt" && b=y || b=n; o="peek_bug=$a resize_bug=$b" ;;
S5) o="turns=$(grep -c '^t[0-9]* idle' "$out/timings.txt")/30 error_shown=$(grep -qiE 'error|fail|refused|connect|unavailable' "$out/error-screen.txt" && echo y || echo n) recovered=$(grep -q '^recover idle' "$out/timings.txt" && echo y || echo n)" ;;
esac
git -C "$fx" diff --stat > "$out/diffstat.txt" 2>/dev/null
echo "$hn $sc outcome=$o wall=$(( $(date +%s) - t0 ))s" | tee "$out/result.txt"
