#!/bin/sh
# Claude Code PreToolUse hook (Bash): blocks commands that run Python (the
# owner's rule: Go, C or Rust; shell only as glue). The check is Go,
# bench/devhooks/nopython, built once into the cache and again when its source
# changes. If it can't be built, a coarse check blocks any command naming
# python as a word. "# owner-approved-python" in a command lets it through:
# only when the owner asked for Python.
set -u
root=$(cd "$(dirname "$0")/../.." && pwd)
src="$root/bench/devhooks/nopython/main.go"
bin="${XDG_CACHE_HOME:-$HOME/.cache}/ternly-devhooks/nopython"
if [ ! -x "$bin" ] || [ "$src" -nt "$bin" ]; then
	mkdir -p "$(dirname "$bin")"
	if ! (cd "$root" && go build -buildvcs=false -o "$bin" ./bench/devhooks/nopython) >/dev/null 2>&1; then
		input=$(cat)
		if printf '%s' "$input" | grep -Eq '(^|[^A-Za-z0-9_.-])python[0-9.]*([^A-Za-z0-9_]|$)' &&
			! printf '%s' "$input" | grep -q 'owner-approved-python'; then
			echo "blocked: this command names Python (the nopython check couldn't be built, so this is the coarse check). The owner's rule: no Python; use Go, C or Rust." >&2
			exit 2
		fi
		exit 0
	fi
fi
exec "$bin"
