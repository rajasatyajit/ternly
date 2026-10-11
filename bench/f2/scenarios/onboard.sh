#!/usr/bin/env bash
# onboard.sh <harness> <session>: answer first-run prompts (rule per screen text) until ready.
# Ready must hold on three consecutive polls (2 s apart) with no dialog between:
# Codex's folder-trust dialog appears a moment after its prompt glyph, and a
# single poll declared it ready too early (the first batch's Codex S1 typed
# its prompt into the dialog). Prints the steps taken; exit 1 if not ready in 30 steps.
hn=$1 id=$2; S="tmux -L ${TMUX_SOCK:-f2rec}"
streak=0
for step in $(seq 30); do
  sleep 2; scr=$($S capture-pane -p -t "$id" 2>/dev/null) || { echo "session gone"; exit 1; }
  k=""; ready=0
  case $hn in
  claude)
    if   grep -q "Yes, I accept" <<<"$scr"; then k="Down Enter"
    elif grep -q "Yes, I trust this folder" <<<"$scr"; then k="Down Enter"
    elif grep -qE "Syntax theme|Choose the text style" <<<"$scr"; then k="Enter"
    elif grep -qE "Press Enter to continue|Enter to confirm" <<<"$scr"; then k="Enter"
    elif grep -qE "for shortcuts|bypass permissions on" <<<"$scr"; then ready=1; fi ;;
  codex)
    if   grep -q "Trust and continue" <<<"$scr"; then k="Enter"
    elif grep -qE "Sign in with ChatGPT" <<<"$scr"; then echo "login screen: not runnable"; exit 1
    elif grep -qE "permissions:|Ask Codex|›" <<<"$scr" && ! grep -qE "Folder access|Trust this folder" <<<"$scr"; then ready=1; fi ;;
  crush)
    if   grep -q "Would you like to initialize" <<<"$scr"; then k="Right Enter"
    elif grep -qE "Ready|ctrl\+p commands|ctrl\+g more" <<<"$scr" && ! grep -q "initialize" <<<"$scr"; then ready=1; fi ;;
  pi)
    if   grep -q "Trust project folder" <<<"$scr"; then k="Enter"
    elif grep -qE "gemma4|ollama" <<<"$scr"; then ready=1; fi ;;
  ternly)    grep -q "Ask ternly" <<<"$scr" && ready=1 ;;
  opencode)  grep -q "Ask anything" <<<"$scr" && ready=1 ;;
  codewhale) grep -q "Type a message" <<<"$scr" && ready=1 ;;
  esac
  if [ -n "$k" ]; then streak=0; echo "step $step: $k"; for key in $k; do $S send-keys -t "$id" "$key"; sleep 0.4; done; continue; fi
  if [ $ready = 1 ]; then streak=$((streak+1)); [ $streak -ge 3 ] && { echo "ready after $step"; exit 0; }; else streak=0; fi
done
echo "not ready after 30 steps"; exit 1
