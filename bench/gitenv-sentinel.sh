#!/usr/bin/env bash
# ADR 024: run the test suite with git's environment pointing at a sentinel
# repository (as a hook in a worktree would: GIT_DIR, GIT_WORK_TREE,
# GIT_INDEX_FILE); the sentinel must be byte-for-byte unchanged afterwards.
# Usage: bench/gitenv-sentinel.sh [go test args…]   (default: ./...)
set -euo pipefail
cd "$(dirname "$0")/.."
S=$(mktemp -d "${TMPDIR:-/tmp}/ternly-sentinel-XXXXXX")
trap 'rm -rf "$S"' EXIT
clean() { env $(env | sed -n 's/^\(GIT_[A-Za-z0-9_]*\)=.*/-u \1/p') "$@"; }
clean git -C "$S" init -q
clean git -C "$S" -c user.name=sentinel -c user.email=sentinel@example.invalid commit -q --allow-empty -m sentinel
snap() {
  (cd "$S" && find .git -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
  clean git -C "$S" for-each-ref --format='%(refname) %(objectname)'
  clean git -C "$S" config --local --list
}
snap >"$S.before"
st=0
GIT_DIR="$S/.git" GIT_WORK_TREE="$S" GIT_INDEX_FILE="$S/.git/index" go test -count=1 "${@:-./...}" || st=$?
snap >"$S.after"
if ! diff -u "$S.before" "$S.after"; then
  echo "gitenv-sentinel: the test suite changed the sentinel repository (a git call inherited GIT_DIR): see the diff above" >&2
  rm -f "$S.before" "$S.after"; exit 1
fi
rm -f "$S.before" "$S.after"
[ "$st" = 0 ] || { echo "gitenv-sentinel: the suite failed (exit $st) with GIT_DIR set" >&2; exit "$st"; }
echo "gitenv-sentinel: sentinel unchanged"
