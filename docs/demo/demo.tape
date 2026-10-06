# The README demo (M9). A real session — a local model through Ollama, not a
# script — replayed at 2× speed. Re-record: docs/demo/record.sh
Output docs/demo/demo.gif

Require ternly
Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 760
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 35ms
Set PlaybackSpeed 2
Set WaitTimeout 300s

Hide
Type "cd /tmp/ternly-demo/calc && export HOME=/tmp/ternly-demo/home PS1='$ ' && clear" Enter
Show

Type "go test ./... | tail -2" Enter
Sleep 2s
Type "ternly --mode edits" Enter
Wait+Screen /Ask ternly/
Sleep 1s

# 1. A fix, routed to the cheapest capable model, and proven.
Type "the tests fail — fix it" Enter
Wait+Screen /covered/
Sleep 8s

# 2. What protects you: the sandbox, the tools, the models.
Type "/doctor" Enter
Wait+Screen /sandbox/
Sleep 5s

# 3. A remote MCP server: protocol era, tools, network grant.
Type "/mcp" Enter
Wait+Screen /deepwiki/
Sleep 4s
Ctrl+C
Sleep 1s
