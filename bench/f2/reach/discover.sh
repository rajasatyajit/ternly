#!/usr/bin/env bash
# discover.sh <harness>: capture a harness's command surfaces (slash menu,
# palette, help) without sending any prompt, for designing reach probes.
# Runs at nice 19 on its own tmux socket, without the shared lock (no model
# calls). Output: $R/reach/discover/<harness>/*.txt
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}; export R
export TMUX_SOCK=f2disc
D=$(cd "$(dirname "$0")/../scenarios" && pwd)
hn=$1; id=disc-$hn; out=$R/reach/discover/$hn; rm -rf "${out:?}"; mkdir -p "$out" "$R/fx" "$R/homes"
S="tmux -L $TMUX_SOCK"
cleanup() { $S kill-session -t "$id" 2>/dev/null; "$R/bin/homekill" "$R/homes/$id" >/dev/null 2>&1; }
trap cleanup EXIT
"$R/bin/prep" S2 "$R/fx/$id" >/dev/null
nice -n 19 "$D/launch.sh" "$hn" "$id" "$R/fx/$id" > "$out/launch.txt" || exit 1
"$D/onboard.sh" "$hn" "$id" > "$out/onboard.txt" 2>&1 || { $S capture-pane -p -t "$id" > "$out/not-ready.txt"; exit 1; }
snap() { sleep "${2:-1.5}"; $S capture-pane -p -t "$id" > "$out/$1.txt"; }
key() { $S send-keys -t "$id" "$@"; }
snap ready
key -l "/"; snap slash; for i in 1 2 3 4 5 6; do key PageDown; snap "slash-pg$i" 0.8; done; key Escape; snap after-slash
key C-p; snap ctrl-p; key Escape; snap after-ctrl-p
key -l "/help"; key Enter; snap help 2; for i in 1 2 3; do key PageDown; snap "help-pg$i" 0.8; done; key Escape; key Escape; snap end
echo "captured $(ls "$out" | wc -l) screens for $hn"
