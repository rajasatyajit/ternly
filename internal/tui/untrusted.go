package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rajasatyajit/ternly/internal/agent"
)

// cleanEvent makes an agent event's outside text safe to show.
func cleanEvent(e agent.Event) agent.Event {
	e.Text, e.Tool, e.Reason = untrusted(e.Text), untrusted(e.Tool), untrusted(e.Reason)
	return e
}

// untrusted makes outside text safe to put on the terminal: model output,
// tool output, commands awaiting permission, provider errors and model
// names never reach it as control sequences (ADR 023, threat model).
// Every C0/C1 control except newline and tab, DEL, a lone carriage return
// and the bidi overrides (Trojan Source) is shown escaped instead — a
// sequence split across stream chunks is harmless too, since its ESC byte
// is already text. Invalid UTF-8 becomes U+FFFD.
func untrusted(s string) string {
	if isPlain(s) {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune('�')
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isPlain: nothing to escape (the common case: no allocation).
func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f || c >= 0x80 {
			return utf8.ValidString(s[i:]) && plainRunes(s[i:])
		}
	}
	return true
}

func plainRunes(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return false
		}
	}
	return true
}

// safeFrame is the last line of defence for a whole frame: ternly's own
// styling is only SGR (ESC [ params m, from lipgloss and glamour), so every
// other escape sequence and control character is escaped as visible text,
// whatever produced it — an info line quoting git, a plugin or an MCP
// server included. Bubble Tea positions the cursor itself, after View.
func safeFrame(s string) string {
	if isPlainFrame(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if j := sgrEnd(s, i); j > 0 {
				b.WriteString(s[i:j])
				i = j
				continue
			}
			b.WriteString(`\x1b`)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune('�')
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sgrEnd: if s[i:] starts an SGR sequence, the index after it, else 0.
func sgrEnd(s string, i int) int {
	if i+1 >= len(s) || s[i+1] != '[' {
		return 0
	}
	for j := i + 2; j < len(s); j++ {
		switch c := s[j]; {
		case c >= '0' && c <= '9', c == ';', c == ':':
		case c == 'm':
			return j + 1
		default:
			return 0
		}
	}
	return 0
}

// isPlainFrame: nothing but SGR and safe characters (the common case).
func isPlainFrame(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0x1b:
			j := sgrEnd(s, i)
			if j == 0 {
				return false
			}
			i = j - 1
		case c < 0x20 && c != '\n' && c != '\t', c == 0x7f:
			return false
		case c >= 0x80:
			return utf8.ValidString(s[i:]) && plainRunes(s[i:])
		}
	}
	return true
}
