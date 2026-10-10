#!/usr/bin/env bash
# reach.sh <harness>: the information-reach session for one harness.
# 1. A fresh S2 fixture; the harness fixes the bug (a real mid-session state:
#    an edit, a diff, a test run, tokens and cost); then the idle screen = the
#    facts at 0 keys.
# 2. Each candidate slash command is typed WITHOUT Enter and the autocomplete
#    menu captured; it runs only if the menu lists it (an unknown command is
#    never sent to the model). State-changing or model-calling commands
#    (review, init, compact, undo, rewind) are existence-checked only.
# 3. Each key chord is pressed, captured, and undone.
# Every screen is saved under $R/reach/<harness>/. The session holds the
# shared lock (`flock -o`, taken by reach-all.sh) so it never overlaps a timed
# scenario cell. Keystrokes are counted as typed: "/cost⏎" = 6, a chord = 1.
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}; export R
export TMUX_SOCK=f2reach
D=$(cd "$(dirname "$0")/../scenarios" && pwd)
hn=$1; id=reach-$hn; out=$R/reach/$hn; rm -rf "${out:?}"; mkdir -p "$out" "$R/fx" "$R/homes"
T="tmux -L $TMUX_SOCK"
cleanup() { tmux -L "$TMUX_SOCK" kill-session -t "$id" 2>/dev/null; "$R/bin/homekill" "$R/homes/$id" > "$out/cleanup.txt" 2>&1; }
trap cleanup EXIT
cap() { sleep "${2:-1.5}"; tmux -L "$TMUX_SOCK" capture-pane -p -t "$id" > "$out/$1.txt"; }
key() { tmux -L "$TMUX_SOCK" send-keys -t "$id" "$@"; }
clear_input() { key Escape; sleep 0.3; key C-u; sleep 0.3; }
"$R/bin/prep" S2 "$R/fx/$id" >/dev/null || { echo "prep failed"; exit 1; }
"$D/launch.sh" "$hn" "$id" "$R/fx/$id" > "$out/launch.txt" || exit 1
"$D/onboard.sh" "$hn" "$id" > "$out/onboard.txt" 2>&1 || { cap not-ready 0; echo "not ready"; exit 1; }
cap ready 0.5
"$D/rec.sh" type "$id" "Bug report: in package simplelru, calling Resize with a capacity at least as large as the number of entries still evicts one entry. Find the cause and fix it."
"$D/rec.sh" wait "$id" 30 900 "$out/warmup" > "$out/warmup.txt"
cap mid 2
# 2. slash commands. REACH_DIRECT (a list) is for a harness whose input Esc and C-u
#    don't clear (ternly: Esc interrupts, C-u pages up): each read-only command from
#    its registry is sent with Enter, captured, then Esc; no menu stage, no "q".
if [ -n "${REACH_DIRECT:-}" ]; then
  for c in $REACH_DIRECT; do
    key -l "/$c"; key Enter; cap "run-$c" 3; echo "$c run (direct)" >> "$out/commands.txt"; key Escape; sleep 0.5
  done
else
for c in cost status context usage stats tokens model models diff changes agents tasks todos plan jobs fleet session sessions resume history permissions approvals mode why errors logs debug checkpoints timeline tree undo rewind review init compact; do
  clear_input; key -l "/$c"; cap "menu-$c" 1.2
  n=$(grep -c -- "/$c" "$out/menu-$c.txt")
  if [ "$n" -ge 2 ]; then echo "$c listed" >> "$out/commands.txt"
    case $c in review|init|compact|undo|rewind) echo "$c not run (state-changing)" >> "$out/commands.txt"; clear_input; continue ;; esac
    key Enter; cap "run-$c" 3; key Escape; sleep 0.4; key Escape; sleep 0.4; key -l q; sleep 0.3; clear_input; cap "after-$c" 0.5
  else echo "$c not listed" >> "$out/commands.txt"; fi
done
fi
clear_input
# 3. key chords (each pressed, captured, pressed again to toggle off, then Escape)
for k in ${REACH_KEYS:-C-o C-t C-g C-b C-] F1 F2 Left}; do
  clear_input; key "$k"; cap "key-$k" 1.5; key "$k"; sleep 0.4; key Escape; sleep 0.4; cap "after-key-$k" 0.5
done
clear_input; key -l "?"; cap "key-question" 1.5; key BSpace; clear_input
cap final 1
echo "reach $hn: $(ls "$out" | wc -l) files"
