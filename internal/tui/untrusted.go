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
// names never reach it as control sequences (ADR 022, threat model).
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
