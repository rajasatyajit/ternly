// Command nopython is a Claude Code PreToolUse hook for this repository: it
// rejects a Bash command that runs Python (python, python3, python3.12, pypy,
// a path ending in one of them), wherever in the command line it runs: after
// ;, &&, ||, |, a subshell or command substitution, behind env, sudo, nohup,
// timeout, xargs and other wrappers, or inside bash -c / sh -c / eval. The
// owner's rule: performant languages only (Go, C, Rust), shell as glue.
//
// It reads the hook's JSON on stdin and exits 2 with the reason on stderr to
// block (Claude Code shows the reason to the model), 0 to allow. A command
// carrying the marker "# owner-approved-python" is allowed: the owner asked
// for Python explicitly.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

const marker = "# owner-approved-python"

func main() {
	var in struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return // not ours to judge
	}
	if in.ToolName != "Bash" && in.ToolName != "" {
		return
	}
	if w, ok := runsPython(in.ToolInput.Command); ok {
		fmt.Fprintf(os.Stderr, "blocked: this command runs Python (%q). The owner's rule: no Python for tooling or analysis; use Go, C or Rust, with shell only as glue. Python is allowed only when the owner asks for it explicitly.\n", w)
		os.Exit(2)
	}
}

var pythonName = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9.]*)$`)

// wrappers run the command that follows them; the value is how many
// arguments of their own they take before it (after their -options).
var wrappers = map[string]int{
	"env": 0, "sudo": 0, "doas": 0, "exec": 0, "nohup": 0, "time": 0, "command": 0, "builtin": 0,
	"nice": 0, "ionice": 0, "stdbuf": 0, "chrt": 1, "taskset": 1, "timeout": 1, "flock": 1,
	"xargs": 0, "watch": 0, "strace": 0, "ltrace": 0, "unbuffer": 0, "setsid": 0, "systemd-run": 0,
}

// runsPython reports whether cmd runs Python, and the word that does.
func runsPython(cmd string) (string, bool) {
	if strings.Contains(cmd, marker) {
		return "", false
	}
	for _, simple := range splitCommands(cmd) {
		if w, ok := simpleRunsPython(words(simple)); ok {
			return w, true
		}
	}
	return "", false
}

func simpleRunsPython(ws []string) (string, bool) {
	i := 0
	for i < len(ws) && isAssignment(ws[i]) {
		i++
	}
	for i < len(ws) {
		w := ws[i]
		base := path.Base(w)
		switch {
		case pythonName.MatchString(base):
			return w, true
		case base == "uv" || base == "pipx" || base == "poetry" || base == "pdm" || base == "conda" || base == "mamba":
			return w, true // Python's own launchers
		case base == "uvx":
			return w, true
		case (base == "bash" || base == "sh" || base == "zsh" || base == "dash" || base == "ksh") && i+1 < len(ws):
			for j := i + 1; j < len(ws); j++ { // bash -c "…", also bash -lc "…"
				if strings.HasPrefix(ws[j], "-") && strings.Contains(ws[j], "c") && j+1 < len(ws) {
					return runsPython(ws[j+1])
				}
			}
			return "", false
		case base == "eval":
			return runsPython(strings.Join(ws[i+1:], " "))
		}
		n, ok := wrappers[base]
		if !ok {
			return "", false
		}
		i++
		for i < len(ws) && (strings.HasPrefix(ws[i], "-") || isAssignment(ws[i])) { // the wrapper's options, env's assignments
			opt := ws[i]
			i++
			if (base == "nice" && opt == "-n") || (base == "sudo" && (opt == "-u" || opt == "-g")) || (base == "timeout" && (opt == "-s" || opt == "-k")) {
				i++ // an option with a value
			}
		}
		i += n
	}
	return "", false
}

func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	return ok && k != "" && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k)
}

// splitCommands splits a command line at ; & | && || newlines, ( ) { },
// and inside $( … ) and backquotes, keeping quoted text whole. A command
// substitution's text is returned as a command of its own.
func splitCommands(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			cur.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '$' && i+1 < len(s) && s[i+1] == '(' { // a substitution inside double quotes runs
				end := matchParen(s, i+1)
				out = append(out, splitCommands(s[i+2:end])...)
				cur.WriteString(s[i : end+1])
				i = end
				continue
			}
			if c == '`' {
				end := strings.IndexByte(s[i+1:], '`')
				if end >= 0 {
					out = append(out, splitCommands(s[i+1:i+1+end])...)
					i += end + 1
					continue
				}
			}
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			end := matchParen(s, i+1)
			out = append(out, splitCommands(s[i+2:end])...)
			cur.WriteString("SUBST")
			i = end
		case c == '`':
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				end = len(s) - i - 1
			}
			out = append(out, splitCommands(s[i+1:i+1+end])...)
			cur.WriteString("SUBST")
			i += end + 1
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\n'):
			for i < len(s) && s[i] != '\n' { // a comment
				i++
			}
			flush()
		case strings.IndexByte(";&|\n(){}", c) >= 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// matchParen is the index of the ')' closing the '(' at open (len-1 if none).
func matchParen(s string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}

// words splits a simple command into words, removing quotes.
func words(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			in = true
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
