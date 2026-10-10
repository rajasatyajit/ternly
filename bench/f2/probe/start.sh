#!/usr/bin/env bash
# Start the matrix detached (it survives the session that started it).
#   bench/f2/probe/start.sh OUT [harness …]   → log OUT/matrix.log, state OUT/state.txt
set -u
here=$(cd "$(dirname "$0")" && pwd)
out=$1
mkdir -p "$out"
setsid nohup "$here/matrix.sh" "$@" >>"$out/matrix.log" 2>&1 </dev/null &
echo "started pid $! — log $out/matrix.log, state $out/state.txt"
