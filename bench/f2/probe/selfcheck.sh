#!/usr/bin/env bash
# Sandbox self-check, once per harness (bench/f2/SPEC.md; the parent's
# security requirement): inside that harness's own bubblewrap wrapper,
#   - a canary file under the real home must be unreadable,
#   - a write under the real home must not reach the real disk,
#   - writes to /usr and /etc must fail,
#   - the real ~/workspace must not be visible,
#   - the cell's workspace and HOME must be writable,
#   - the harness binary must be present.
# Also times the wrapper itself (bwrap … true vs true, 20 runs each).
#   selfcheck.sh OUTDIR [harness …]   → OUTDIR/selfcheck.<harness>.json, OUTDIR/wrapper-overhead.txt
set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=harnesses.sh
source "$here/harnesses.sh"
P=${PROBE_TMP:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/probe}
out=$1; shift
names=${*:-$HARNESS_NAMES}
mkdir -p "$out"
for name in $names; do
  work=$(mktemp -d -p "$P" "chk-$name-XXXXXX"); home=$work/home; ws=$work/ws; mkdir -p "$home" "$ws"
  SETUP_CMDS=() RO_BINDS=()
  "setup_$name" "$home" "$ws"
  sandbox_prefix "$work" "$ws" || exit 3
  canary=$REAL_HOME/.f2-canary-$$-$RANDOM; escape=$REAL_HOME/.f2-escape-$$-$RANDOM
  echo secret >"$canary"
  r=$(env -i PATH="$NODE_DIR/bin:/usr/bin" HOME="$home" "${SBX[@]}" sh -c '
    c=$1 e=$2 ws=$3 home=$4 bin=$5 rh=$6
    cat "$c" >/dev/null 2>&1 && echo "canary_read=FAIL" || echo "canary_read=ok"
    touch "$e" 2>/dev/null; echo "home_write_attempted=1"
    touch /usr/.f2-escape 2>/dev/null && echo "usr_write=FAIL" || echo "usr_write=ok"
    touch /etc/.f2-escape 2>/dev/null && echo "etc_write=FAIL" || echo "etc_write=ok"
    ls "$rh/workspace" >/dev/null 2>&1 && echo "workspace_visible=FAIL" || echo "workspace_visible=ok"
    touch "$ws/.ok" "$home/.ok" 2>/dev/null && echo "work_write=ok" || echo "work_write=FAIL"
    [ -e "$bin" ] && echo "binary=ok" || echo "binary=FAIL"
  ' sh "$canary" "$escape" "$ws" "$home" "${CMD[0]}" "$REAL_HOME" 2>&1)
  if [ -e "$escape" ]; then esc=FAIL; rm -f "$escape"; else esc=ok; fi
  rm -f "$canary"; rm -rf "$work"
  pass=true
  for kv in $r "home_write_reached_disk=$esc"; do case $kv in *=FAIL) pass=false ;; esac; done
  { printf '{"harness":"%s","pass":%s,"checks":{' "$name" "$pass"
    first=1; for kv in $r "home_write_reached_disk=$esc"; do k=${kv%%=*} v=${kv#*=}; [ $first = 1 ] || printf ','; printf '"%s":"%s"' "$k" "$v"; first=0; done
    printf '},"at":"%s"}\n' "$(date -u +%FT%TZ)"; } >"$out/selfcheck.$name.json"
  echo "$name: pass=$pass ($r home_write_reached_disk=$esc)" | tr '\n' ' '; echo
done
# the wrapper's own cost: bwrap … true, vs true (20 runs each, median ms)
RO_BINDS=("$TERNLY_BIN"); work=$(mktemp -d -p "$P" chk-ovh-XXXXXX); mkdir -p "$work/ws"
sandbox_prefix "$work" "$work/ws"
med() { sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}'; }
t() { local s e; s=$(date +%s%N); "$@" >/dev/null 2>&1; e=$(date +%s%N); echo $(((e - s) / 1000)); }
w=$(for i in $(seq 20); do t "${SBX[@]}" true; done | med)
b=$(for i in $(seq 20); do t true; done | med)
rm -rf "$work"
printf 'bwrap wrapper startup: median %s µs (bwrap … true), vs %s µs (true alone), 20 runs each, %s\n' "$w" "$b" "$(date -u +%FT%TZ)" | tee "$out/wrapper-overhead.txt"
