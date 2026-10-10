#!/usr/bin/env bash
# rec.sh: record a TUI in a private tmux server.
#   rec.sh start <id> <cols> <rows> <workdir> <cmd...>   (env from the caller)
#   rec.sh keys  <id> <tmux send-keys args...>
#   rec.sh type  <id> <text>               (literal text, then Enter)
#   rec.sh snap  <id> <file>               (one capture, plain text)
#   rec.sh wait  <id> <idle-seconds> <max-seconds> <outdir>  (captures every 1 s while it changes; returns when idle)
#   rec.sh grep  <id> <regex> <max-seconds> (wait for text on screen)
#   rec.sh stop  <id>
set -u
S="tmux -L ${TMUX_SOCK:-f2rec}"
cmd=$1; shift
case $cmd in
start) id=$1 c=$2 r=$3 wd=$4; shift 4
  $S kill-session -t "$id" 2>/dev/null
  $S new-session -d -s "$id" -x "$c" -y "$r" -c "$wd" "$@"
  $S set-option -t "$id" -g history-limit 50000 >/dev/null ;;
keys) id=$1; shift; $S send-keys -t "$id" "$@" ;;
type) id=$1; shift; $S send-keys -t "$id" -l "$1"; sleep 0.3; $S send-keys -t "$id" Enter ;;
snap) $S capture-pane -p -t "$1" > "$2" ;;
wait) id=$1 idle=$2 max=$3 out=$4; mkdir -p "$out"; t0=$(date +%s.%N); last=""; lastchange=$t0
  while :; do
    now=$(date +%s.%N); cur=$($S capture-pane -p -t "$id" 2>/dev/null) || { echo "exited"; break; }
    if [ "$cur" != "$last" ]; then last=$cur; lastchange=$now
      printf '=== t=%.1f\n%s\n' "$(echo "$now - $t0" | bc)" "$cur" >> "$out/timeline.txt"; fi
    el=$(echo "$now - $t0" | bc); quiet=$(echo "$now - $lastchange" | bc)
    if [ "$(echo "$quiet >= $idle" | bc)" = 1 ]; then printf 'idle after %.1f s (last change at %.1f s)\n' "$el" "$(echo "$lastchange - $t0" | bc)"; break; fi
    if [ "$(echo "$el >= $max" | bc)" = 1 ]; then printf 'TIMEOUT at %.1f s\n' "$el"; break; fi
    sleep 1
  done ;;
grep) id=$1 re=$2 max=$3; t0=$(date +%s)
  until $S capture-pane -p -t "$id" 2>/dev/null | grep -qE "$re"; do [ $(( $(date +%s) - t0 )) -ge "$max" ] && { echo "NOMATCH $re"; exit 1; }; sleep 0.5; done; echo "match after $(( $(date +%s) - t0 )) s" ;;
stop) $S kill-session -t "$1" 2>/dev/null; true ;;
esac
