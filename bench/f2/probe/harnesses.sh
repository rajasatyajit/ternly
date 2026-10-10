#!/usr/bin/env bash
# Harness profiles for the F2 survey (bench/f2/SPEC.md): each writes its
# documented config into an isolated HOME and prints the command to run.
# Thin glue only — the measuring is bench/tuiprobe (Go).
#
#   source bench/f2/probe/harnesses.sh
#   setup_<name> HOME WORKSPACE      # write config; echo the command line
#
# Every harness talks to the local Ollama (gemma4:latest) through its own
# custom-endpoint setting; nothing signs in, nothing is paid, nothing touches
# the owner's real configs. Telemetry is turned off where a setting exists.

H=${HARNESSES:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/harnesses}
OLLAMA=http://127.0.0.1:11434
MODEL=gemma4:latest
TERNLY_BIN=${TERNLY_BIN:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/probe/ternly}
NODE_DIR=${NODE_DIR:-/home/satyajit/.nvm/versions/node/v22.22.2}
CLAUDE_BIN=${CLAUDE_BIN:-$(readlink -f "$(command -v claude)")}
REAL_HOME=${REAL_HOME:-/home/satyajit}

# sandbox_prefix WORK WS: the bubblewrap command every harness runs under
# (ternly too, for parity). Only WORK (the cell's workspace and temp HOME) is
# writable; system dirs, the harness install (RO_BINDS) and Node are read-only;
# the real home is a tmpfs, so nothing of the owner's is visible; fresh /tmp;
# network shared (the local Ollama on 127.0.0.1:11434). Fails closed without
# bwrap. Prints nothing; sets the SBX array.
sandbox_prefix() { # WORK WS
  command -v bwrap >/dev/null || { echo "bwrap not found: refusing to run a harness unsandboxed" >&2; return 1; }
  SBX=(bwrap --die-with-parent --unshare-all --share-net
    --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib /lib64 --symlink usr/bin /sbin
    --ro-bind /etc /etc --ro-bind-try /opt /opt --ro-bind-try /sys /sys
    --proc /proc --dev /dev --tmpfs /tmp --tmpfs "$REAL_HOME"
    --ro-bind "$NODE_DIR" "$NODE_DIR")
  local b
  for b in "${RO_BINDS[@]}"; do SBX+=(--ro-bind "$b" "$b"); done
  SBX+=(--bind "$1" "$1" --chdir "$2" --)
}

# common_env: the environment every harness gets (besides HOME/XDG).
common_env() { # HOME
  local h=$1
  echo "HOME=$h,XDG_CONFIG_HOME=$h/.config,XDG_DATA_HOME=$h/.local/share,XDG_CACHE_HOME=$h/.cache,XDG_STATE_HOME=$h/.local/state,DO_NOT_TRACK=1"
}

setup_ternly() { # HOME WS
  mkdir -p "$1/.config/ternly"
  printf '{"routing": {"background_eval": {"enabled": false}}}\n' >"$1/.config/ternly/config.json"
  ENV_EXTRA="TERNLY_BACKGROUND_EVAL=1"
  READY='Ask ternly to build' PRE=''
  CMD=("$TERNLY_BIN" --model "ollama/$MODEL")
  RO_BINDS=("$TERNLY_BIN")
}

setup_claude() { # HOME WS
  local ws=$2
  cat >"$1/.claude.json" <<EOF
{"hasCompletedOnboarding": true, "theme": "dark", "autoUpdates": false,
 "projects": {"$ws": {"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}}}
EOF
  ENV_EXTRA="ANTHROPIC_BASE_URL=$OLLAMA,ANTHROPIC_AUTH_TOKEN=ollama,ANTHROPIC_MODEL=$MODEL,ANTHROPIC_DEFAULT_HAIKU_MODEL=$MODEL,ANTHROPIC_DEFAULT_SONNET_MODEL=$MODEL,ANTHROPIC_DEFAULT_OPUS_MODEL=$MODEL,DISABLE_AUTOUPDATER=1,CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1,DISABLE_TELEMETRY=1"
  CMD=("$CLAUDE_BIN")
  RO_BINDS=("$CLAUDE_BIN")
  READY='❯' PRE=''
}

setup_codex() { # HOME WS
  mkdir -p "$1/.codex"
  cat >"$1/.codex/config.toml" <<EOF
model = "$MODEL"
model_provider = "ollama-local"
check_for_update_on_startup = false

[model_providers.ollama-local]
name = "Ollama (local)"
base_url = "$OLLAMA/v1"
wire_api = "responses"

[projects."$2"]
trust_level = "trusted"

[analytics]
enabled = false
EOF
  ENV_EXTRA="CODEX_HOME=$1/.codex"
  CMD=("$H/codex/node_modules/.bin/codex")
  RO_BINDS=("$H/codex")
  READY='Ask Codex to do anything' PRE=''
}

setup_gemini() { # HOME WS (no shared model: Google auth only — UI up to the auth prompt)
  mkdir -p "$1/.gemini"
  printf '{"privacy": {"usageStatisticsEnabled": false}, "general": {"disableAutoUpdate": true}}\n' >"$1/.gemini/settings.json"
  ENV_EXTRA=""
  CMD=("$H/gemini/node_modules/.bin/gemini")
  RO_BINDS=("$H/gemini")
  READY='Do you trust the files in this folder' PRE='' # stops at Google auth after this: no prompt without an account
}

setup_crush() { # HOME WS
  mkdir -p "$1/.config/crush"
  cat >"$1/.config/crush/crush.json" <<EOF
{
  "\$schema": "https://charm.land/crush.json",
  "options": {"disable_metrics": true, "disable_provider_auto_update": true},
  "providers": {
    "ollama": {"name": "Ollama", "type": "openai-compat", "base_url": "$OLLAMA/v1", "api_key": "ollama",
      "models": [{"id": "$MODEL", "name": "gemma4", "context_window": 32768, "default_max_tokens": 4096}]}
  },
  "models": {"large": {"provider": "ollama", "model": "$MODEL"}, "small": {"provider": "ollama", "model": "$MODEL"}}
}
EOF
  ENV_EXTRA="CRUSH_DISABLE_METRICS=1"
  CMD=("$H/crush/crush")
  RO_BINDS=("$H/crush")
  READY='Would you like to initialize now' PRE='type \e[C; enter; stable 1500 20000' # "Nope"
}

setup_opencode() { # HOME WS
  mkdir -p "$1/.config/opencode"
  cat >"$1/.config/opencode/opencode.json" <<EOF
{
  "\$schema": "https://opencode.ai/config.json",
  "autoupdate": false, "share": "disabled",
  "provider": {"ollama": {"npm": "@ai-sdk/openai-compatible", "name": "Ollama",
    "options": {"baseURL": "$OLLAMA/v1"}, "models": {"$MODEL": {"name": "gemma4"}}}},
  "model": "ollama/$MODEL"
}
EOF
  ENV_EXTRA="OPENCODE_DISABLE_AUTOUPDATE=1"
  CMD=("$H/opencode/node_modules/.bin/opencode")
  RO_BINDS=("$H/opencode")
  READY='Ask anything' PRE=''
}

setup_pi() { # HOME WS
  mkdir -p "$1/.pi/agent"
  cat >"$1/.pi/agent/models.json" <<EOF
{"providers": {"ollama": {"baseUrl": "$OLLAMA/v1", "api": "openai-completions", "apiKey": "ollama",
  "models": [{"id": "$MODEL", "name": "gemma4", "contextWindow": 32768, "maxTokens": 4096}]}}}
EOF
  ENV_EXTRA=""
  CMD=("$H/pi/pi/pi" --provider ollama --model "$MODEL")
  RO_BINDS=("$H/pi")
  READY='Trust project folder' PRE='enter; stable 1500 20000' # "Trust" (isolated HOME)
}

setup_codewhale() { # HOME WS (its config commands run inside the sandbox too: see SETUP_CMDS)
  local cw="$H/codewhale/node_modules/.bin/codewhale"
  SETUP_CMDS=("$cw config set telemetry false" "$cw config set provider ollama" "$cw config set model $MODEL")
  ENV_EXTRA=""
  RO_BINDS=("$H/codewhale")
  CMD=("$cw")
  READY='Type a message' PRE=''
}

HARNESS_NAMES="ternly claude codex gemini crush opencode pi codewhale"
