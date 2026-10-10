#!/usr/bin/env bash
# Capability evals (ternly --eval, eval v4) of the local models, one model per
# bench/run.sh call so each record is saved as soon as it is done; a rerun skips
# models whose record exists. Detached: survives the session.
cd /home/satyajit/workspace/ternly/.claude/worktrees/phase-c || exit 1
T=/home/satyajit/.claude/jobs/f10e4ff6/tmp
R=bench/results/fabrication
for spec in gemma4:latest:3 gemma4:26b:3 granite4.2:latest:3 qwen3.8:latest:1 gemma4:31b:1; do
  m=${spec%:*}; runs=${spec##*:}; f=$R/ollama_${m//:/_}.json
  if [ -f "$f" ] && [ "$f" -nt "$T/eval-models.started" ]; then echo "== $m: done already ($f)"; continue; fi
  echo "== $m runs=$runs $(date -u +%FT%TZ)"
  MODELS=$m RUNS=$runs flock $T/heavy.lock bench/run.sh fabrication 2>&1 | grep -E "^[✓·✗!] |fabrication |capability:|trust:|E2E-METRIC|^==|error" 
  echo "== $m finished $(date -u +%FT%TZ); record: $(ls -la $f 2>/dev/null)"
done
echo "EVALS DONE $(date -u +%FT%TZ)"
