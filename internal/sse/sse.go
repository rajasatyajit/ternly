// Package sse reads Server-Sent-Events streams (LLM providers, MCP servers).
package sse

import (
	"bufio"
	"io"
	"strings"
)

// Read parses r, calling fn(event, data) for each message (data lines
// joined by newlines; comment lines and ids ignored). fn returning false
// stops reading. A message without data is not dispatched.
func Read(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	var data strings.Builder
	flush := func() bool {
		if data.Len() == 0 {
			event = ""
			return true
		}
		ok := fn(event, data.String())
		event = ""
		data.Reset()
		return ok
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line[5:], " "))
		}
	}
	flush()
	return sc.Err()
}
