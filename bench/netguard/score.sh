#!/usr/bin/env bash
# Score a netguard implementation (ADR 015/016's task) with the black-box
# scorer: bench/netguard/score.sh <dir holding netguard's .go files, or a
# ternly checkout>. Saved dogfood code (*.go.txt) works too. Only the
# candidate's non-test files are used. Prints "core N/7, extra M/2".
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
src=$1
[ -d "$src/internal/netguard" ] && src="$src/internal/netguard"
[ -d "$src/netguard" ] && src="$src/netguard" # saved dogfood code (bench/dogfood/*/code-*/netguard)
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
mkdir -p "$work/internal/netguard"
printf 'module github.com/rajasatyajit/ternly\n\ngo 1.22\n' > "$work/go.mod"
n=0
for f in "$src"/*.go "$src"/*.go.txt; do
  [ -e "$f" ] || continue
  base=$(basename "$f" .txt)
  case "$base" in *_test.go) continue ;; esac
  cp "$f" "$work/internal/netguard/$base"; n=$((n + 1))
done
[ "$n" -gt 0 ] || { echo "no netguard sources in $1" >&2; exit 2; }
cp "$here/score_test.go.txt" "$work/internal/netguard/zz_score_test.go"
out=$(cd "$work" && GOFLAGS=-mod=mod GOTOOLCHAIN=local go test -count=1 -timeout 120s -run '^TestScore$' -v ./internal/netguard/ 2>&1 || true)
core=$(grep -c -- '--- PASS: TestScore/core/' <<<"$out" || true)
extra=$(grep -c -- '--- PASS: TestScore/extra/' <<<"$out" || true)
grep -E -- '--- FAIL: TestScore/|^\s+score_test.go|cannot|undefined|build failed' <<<"$out" | sed 's/^/  /' || true
echo "core $core/7, extra $extra/2"
