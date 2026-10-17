package tools

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// outlineMinLines: files at least this long are outlined (OutlineReads).
const outlineMinLines = 300

// reDecl matches a declaration line in the languages the suite covers: Go,
// Python, TypeScript/JavaScript, Rust (and close relatives).
var reDecl = regexp.MustCompile(`^\s*(func |type |const |var \(|def |async def |class |export |interface |function |pub |fn |impl |struct |enum |trait |mod )`)

// outline is read_file's answer for a large file read without a range: its
// declarations with line numbers, so the model reads the part it needs
// (offset/limit) or asks the graph (find_symbol), instead of the first 400
// lines (ADR 029, the retrieval lever). ok is false for a short file.
func outline(f io.Reader, path string) (string, bool) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var decls []string
	n := 0
	for sc.Scan() {
		n++
		if t := sc.Text(); reDecl.MatchString(t) && len(decls) < 400 {
			if len(t) > 160 {
				t = t[:160] + "…"
			}
			decls = append(decls, fmt.Sprintf("%6d\t%s", n, strings.TrimRight(t, " {")))
		}
	}
	if n < outlineMinLines {
		return "", false
	}
	return fmt.Sprintf("%s has %d lines; its declarations are below. Read the part you need with offset and limit (or find_symbol / references for Go symbols).\n%s\n", path, n, strings.Join(decls, "\n")), true
}
