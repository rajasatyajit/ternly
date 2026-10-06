#!/usr/bin/env bash
# Release smoke test (M9): bench/smoke.sh <ternly> <fakeprovider>
# Each argument is a command, so an emulator prefix works:
#   bench/smoke.sh "qemu-aarch64-static dist/ternly" "qemu-aarch64-static dist/fakeprovider"
# Checks --version, then one scripted headless turn against the fake
# provider, in a throwaway HOME and workspace.
set -euo pipefail
read -ra bin <<<"$1"
read -ra fake <<<"$2"
work=$(mktemp -d); trap 'kill "${pid:-0}" 2>/dev/null || true; rm -rf "$work"' EXIT
"${bin[@]}" --version
mkdir -p "$work/home/.config/ternly" "$work/home/.cache/ternly" "$work/ws"
"${fake[@]}" SMOKE-OK > "$work/url" & pid=$!
for _ in $(seq 50); do [ -s "$work/url" ] && break; sleep 0.1; done
url=$(cat "$work/url")
printf '{"no_local":true,"suggestions":false,"memory":false,"providers":[{"id":"fake","kind":"openai","base_url":"%s","key_env":"FAKE_KEY"}]}' "$url" > "$work/home/.config/ternly/config.json"
chmod 600 "$work/home/.config/ternly/config.json"
echo '{"m1":{"Tools":true,"Ctx":100000}}' > "$work/home/.cache/ternly/catalog.json"
out=$(HOME="$work/home" FAKE_KEY=k "${bin[@]}" --model fake/m1 -C "$work/ws" -p "say the word" 2>&1)
echo "$out" | tail -3
grep -q SMOKE-OK <<<"$out" || { echo "smoke: the scripted reply didn't come back" >&2; exit 1; }
echo "smoke: ok ($("${bin[@]}" --version))"
