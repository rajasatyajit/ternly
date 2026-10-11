#!/usr/bin/env bash
# One measurement cell (thin glue): a byte-counting proxy and a sampler
# (started outside the lock), then tuiprobe under flock -o, then cleanup by
# PID and by the cell's HOME. Resumable: a cell with result.json is skipped.
#   cell.sh NAME BIN HOME 'STEPS' LIMIT_S PORT [EXTRA_ENV]
set -u
M=${MEAS:?set MEAS (the measurement directory: ws/, cells/)}
name=$1 bin=$2 home=$3 steps=$4 limit=$5 port=$6 extra=${7:-}
out=$M/cells/$name
[ -f "$out/result.json" ] && { echo "skip $name"; exit 0; }
mkdir -p "$out" "$home"
ws=$out/ws; rm -rf "$ws"; cp -r "$M/ws" "$ws"
"${COUNTPROXY:?}" -listen 127.0.0.1:$port -out "$out/bytes.json" >"$out/proxy.log" 2>&1 &
proxy=$!
( t0=$(date +%s); while sleep 5; do printf '%s %s\n' $(( $(date +%s) - t0 )) "$(jq -r .down_total "$out/bytes.json" 2>/dev/null)"; done ) >"$out/timeline.txt" &
sampler=$!
env_spec="HOME=$home,XDG_CONFIG_HOME=$home/.config,XDG_DATA_HOME=$home/.local/share,XDG_CACHE_HOME=$home/.cache,XDG_STATE_HOME=$home/.local/state,TERM=xterm-256color,COLORTERM=truecolor,LANG=C.UTF-8,LC_ALL=C.UTF-8,HTTPS_PROXY=http://127.0.0.1:$port"
[ -n "$extra" ] && env_spec="$env_spec,$extra"
date -Is >"$out/started"
flock -o "${HEAVY_LOCK:?set HEAVY_LOCK}" timeout -s TERM -k 30 "$limit" "${TUIPROBE:?}" -clean-env -cols 120 -rows 40 -dir "$ws" -env "$env_spec" \
  -steps "$steps" -out "$out/probe.json" -raw "$out/raw.gz" -- "$bin"
st=$?
"${TUIPROBE:?}" -reap "$home" >"$out/reap.log" 2>&1
sleep 2
kill "$sampler" 2>/dev/null; kill "$proxy" 2>/dev/null; wait "$proxy" 2>/dev/null
jq -n --arg name "$name" --argjson st "$st" --slurpfile probe "$out/probe.json" --slurpfile bytes "$out/bytes.json" \
  '{name:$name, exit:$st, probe:$probe[0], bytes:$bytes[0]}' >"$out/result.json" 2>/dev/null || echo "{\"name\":\"$name\",\"exit\":$st}" >"$out/result.json"
echo "done $name exit=$st"
