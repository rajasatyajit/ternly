#!/usr/bin/env bash
# box.sh <workdir> <home> -- <command…>      run a harness inside the boundary
# box.sh --check <workdir> <home>             the canary self-check, same boundary
#
# The boundary every F2 harness runs in (ternly included, for parity). The
# rivals run with their own permission prompts off (--dangerously-skip-
# permissions, --yolo, allow-all), so the boundary, not a temp HOME, is what
# keeps them away from the owner's files:
#   - bubblewrap: --die-with-parent --unshare-all --share-net (network only
#     for the local Ollama proxy on 127.0.0.1; the host network stays
#     reachable, noted in README.md)
#   - a tmpfs over /home/satyajit: the real home is invisible except the binds
#   - read-only: /usr, a minimal /etc, the harness installs, the Node runtime,
#     Claude Code's version dir, the ternly binary
#   - writable: only the cell's workspace and its temp HOME
#   - a fresh /tmp, /run, /proc and /dev
# Fails closed: no bwrap, no run.
set -u
if ! command -v bwrap >/dev/null 2>&1; then echo "box.sh: bwrap not found; refusing to run a harness unsandboxed" >&2; exit 97; fi
REALHOME=/home/satyajit
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}
H=/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/harnesses
NODE=/home/satyajit/.nvm/versions/node/v22.22.2
CLAUDE=/home/satyajit/.local/share/claude/versions
check=0; [ "${1:-}" = --check ] && { check=1; shift; }
wd=$1 home=$2; shift 2; [ "${1:-}" = -- ] && shift
for d in "$wd" "$home"; do case $d in "$REALHOME"/.claude/jobs/*/tmp/f2/*) ;; *) echo "box.sh: $d is outside the survey's runtime dir; refusing" >&2; exit 98 ;; esac; done
args=(--die-with-parent --unshare-all --share-net
  --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib /lib64 --symlink usr/bin /sbin
  --ro-bind /etc/passwd /etc/passwd --ro-bind /etc/group /etc/group --ro-bind /etc/hosts /etc/hosts
  --ro-bind /etc/resolv.conf /etc/resolv.conf --ro-bind /etc/nsswitch.conf /etc/nsswitch.conf
  --ro-bind /etc/ssl /etc/ssl --ro-bind /etc/ca-certificates /etc/ca-certificates
  --symlink "$(readlink /etc/localtime)" /etc/localtime
  --proc /proc --dev /dev --tmpfs /tmp --tmpfs /run
  --tmpfs "$REALHOME"
  --ro-bind "$H" "$H" --ro-bind "$NODE" "$NODE" --ro-bind "$CLAUDE" "$CLAUDE"
  --ro-bind "$R/ternly" "$R/ternly"
  --bind "$wd" "$wd" --bind "$home" "$home"
  --chdir "$wd")
if [ $check = 0 ]; then exec bwrap "${args[@]}" -- "$@"; fi
# --check: what the harness could reach, from inside the same boundary
canary=$REALHOME/.ternly-f2-canary
[ -f "$canary" ] || { echo "f2 canary: if you can read this from inside a harness sandbox, the boundary leaked" > "$canary"; chmod 600 "$canary"; }
escape=$REALHOME/.ternly-f2-escape-$$
bwrap "${args[@]}" -- /bin/sh -c '
  r() { if eval "$2" >/dev/null 2>&1; then echo "FAIL $1"; else echo "ok   $1"; fi; }
  w() { if eval "$2" >/dev/null 2>&1; then echo "ok   $1"; else echo "FAIL $1"; fi; }
  r "canary under the real home is unreadable"            "cat '"$canary"'"
  r "the ternly repo is invisible"                        "cat '"$REALHOME"'/workspace/ternly/go.mod"
  r "~/.ssh is invisible"                                 "ls '"$REALHOME"'/.ssh"
  r "the harness install is read-only"                    "touch '"$H"'/.f2-write-test"
  r "/usr is read-only"                                   "touch /usr/.f2-write-test"
  r "the ternly binary is read-only"                      "touch '"$R"'/ternly"
  w "the workspace is writable"                           "touch '"$wd"'/.f2-box-check && rm '"$wd"'/.f2-box-check"
  w "the temp HOME is writable"                           "touch '"$home"'/.f2-box-check && rm '"$home"'/.f2-box-check"
  touch '"$escape"' 2>/dev/null; echo "info escape attempt written inside the sandbox at '"$escape"'"
  w "the local model proxy is reachable"                  "exec 3<>/dev/tcp/127.0.0.1/11435"
'
if [ -e "$escape" ]; then echo "FAIL a write under the real home persisted outside the sandbox ($escape)"; rm -f "$escape"; else echo "ok   a write under the real home did not persist outside the sandbox"; fi
