#!/usr/bin/env bash
# Runtime dir: R (default the F2 job temp dir). See README.md.
# launch.sh <harness> <session-id> <workdir> [cols rows]: an isolated HOME with
# the harness's documented config for the local gemma4, then rec.sh start.
set -u
R=${R:-/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/reach}
H=/home/satyajit/.claude/jobs/f10e4ff6/tmp/f2/harnesses
hn=$1 id=$2 wd=$3 cols=${4:-120} rows=${5:-40}
[ -d "$wd" ] || { echo "launch: workdir $wd does not exist; refusing (would start in HOME)"; exit 1; }
h=$R/homes/$id; rm -rf "${h:?}"; mkdir -p $h/.config $h/.local/share $h/.cache $h/.local/state $h/tmp
M=gemma4:latest
base=(env -i HOME=$h XDG_CONFIG_HOME=$h/.config XDG_DATA_HOME=$h/.local/share XDG_CACHE_HOME=$h/.cache XDG_STATE_HOME=$h/.local/state TMPDIR=$h/tmp
  PATH=/home/satyajit/.nvm/versions/node/v22.22.2/bin:/usr/local/bin:/usr/bin:/bin TERM=xterm-256color COLORTERM=truecolor LANG=C.UTF-8
  GOCACHE=$(go env GOCACHE) GOMODCACHE=$(go env GOMODCACHE) GOPATH=$(go env GOPATH) GOTOOLCHAIN=local GOFLAGS=-mod=mod)
case $hn in
ternly)  cmd=("${base[@]}" TERNLY_BACKGROUND_EVAL=1 OLLAMA_HOST=http://127.0.0.1:11435 $R/ternly --model $M --mode yolo --local-only) ;;
claude)  cmd=("${base[@]}" ANTHROPIC_BASE_URL=http://127.0.0.1:11435 ANTHROPIC_AUTH_TOKEN=ollama CLAUDE_CODE_MAX_CONTEXT_TOKENS=131072 ANTHROPIC_MODEL=$M ANTHROPIC_DEFAULT_HAIKU_MODEL=$M ANTHROPIC_DEFAULT_SONNET_MODEL=$M ANTHROPIC_DEFAULT_OPUS_MODEL=$M CLAUDE_CODE_SUBAGENT_MODEL=$M DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_TELEMETRY=1 /home/satyajit/.local/bin/claude --dangerously-skip-permissions) ;;
codex)   mkdir -p $h/.codex; printf 'model = "%s"\nmodel_provider = "ollama-local"\n[model_providers.ollama-local]\nname = "Ollama (local)"\nbase_url = "http://127.0.0.1:11435/v1"\nwire_api = "responses"\n' "$M" > $h/.codex/config.toml; cmd=("${base[@]}" CODEX_HOME=$h/.codex $H/codex/node_modules/.bin/codex --dangerously-bypass-approvals-and-sandbox) ;;
crush)   mkdir -p $h/.config/crush; cat > $h/.config/crush/crush.json <<J
{"providers":{"ollama":{"name":"Ollama (local)","type":"openai","base_url":"http://127.0.0.1:11435/v1","api_key":"ollama","models":[{"id":"$M","name":"gemma4 (local)","context_window":32768,"default_max_tokens":4096}]}},
 "models":{"large":{"provider":"ollama","model":"$M"},"small":{"provider":"ollama","model":"$M"}},"options":{"disable_metrics":true}}
J
         cmd=("${base[@]}" $H/crush/crush --yolo) ;;
opencode) mkdir -p $h/.config/opencode; cat > $h/.config/opencode/opencode.json <<J
{"\$schema":"https://opencode.ai/config.json","provider":{"ollama":{"npm":"@ai-sdk/openai-compatible","name":"Ollama (local)","options":{"baseURL":"http://127.0.0.1:11435/v1"},"models":{"$M":{"name":"gemma4"}}}},
 "model":"ollama/$M","small_model":"ollama/$M","permission":{"edit":"allow","bash":"allow","webfetch":"allow"},"autoupdate":false,"share":"disabled"}
J
         cmd=("${base[@]}" OPENCODE_DISABLE_AUTOUPDATE=1 $H/opencode/node_modules/.bin/opencode) ;;
pi)      mkdir -p $h/.pi/agent; cat > $h/.pi/agent/models.json <<J
{"providers":{"ollama":{"baseUrl":"http://127.0.0.1:11435/v1","api":"openai-completions","apiKey":"ollama","models":[{"id":"$M"}]}}}
J
         cmd=("${base[@]}" $H/pi/pi/pi --provider ollama --model $M) ;;
codewhale) cat > $h/codewhale.toml <<J
provider = "custom"
model = "$M"
approval_policy = "never"
sandbox_mode = "workspace-write"
telemetry = false
[providers.custom]
kind = "openai-compatible"
base_url = "http://127.0.0.1:11435/v1"
api_key = "ollama"
J
         cmd=("${base[@]}" CODEWHALE_TELEMETRY=0 $H/codewhale/node_modules/.bin/codewhale --config $h/codewhale.toml --skip-onboarding --fresh -C $wd) ;;
gemini)  cmd=("${base[@]}" $H/gemini/node_modules/.bin/gemini) ;;
*) echo "unknown harness $hn"; exit 2 ;;
esac
printf '%q ' "${cmd[@]}" > $R/homes/$id.cmd
"$(dirname "$0")/rec.sh" start "$id" "$cols" "$rows" "$wd" "${cmd[@]}"
echo "started $hn as $id in $wd (home $h)"
