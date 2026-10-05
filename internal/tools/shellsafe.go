package tools

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Commands that run without confirmation (ADR 014, from dogfooding: every
// model's first exploration command was a chain like `ls -la && cat go.mod`,
// and a refusal there costs a turn and a retry). A command qualifies when
// every segment of its &&, ||, ; or | chain is a known read-only, build or
// test command; quoting is understood, and anything that expands or
// redirects ($, backquotes, <, >, braces, parentheses, a lone &) still asks.

// envOK are the variables a segment may set in front of its command
// (`CGO_ENABLED=0 go build`): build knobs that can't load code or change
// where it comes from (not GOFLAGS, LD_PRELOAD, PATH, …).
var envOK = map[string]bool{"CGO_ENABLED": true, "GOOS": true, "GOARCH": true, "GOARM": true, "GOAMD64": true, "GOEXPERIMENT": true,
	"NODE_ENV": true, "RUST_BACKTRACE": true, "CI": true, "TZ": true, "LANG": true, "LC_ALL": true, "NO_COLOR": true}

var (
	reAssign           = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=([\w.,:/+-]*)$`)
	reCd               = regexp.MustCompile(`^cd\s+[^-~\s]\S*$`) // into the workspace (paths are checked above); a bare cd would go home
	reMkdir            = regexp.MustCompile(`^mkdir(\s+-p)?(\s+[^-\s]\S*)+$`)
	reHarmlessRedirect = regexp.MustCompile(`\s(2>&1|[12]?>\s?/dev/null)(\s|$)`)
	reExecArg          = regexp.MustCompile(`(^|\s)(--pre|--pre-glob|-toolexec|--exec)(=|\s|$)`)
)

// safeCommand reports whether cmd may run without confirmation; mkdir (in
// the workspace) counts only when writes is set (edits and yolo modes).
func (p *Policy) safeCommand(cmd string, writes bool) bool {
	cmd = reHarmlessRedirect.ReplaceAllString(cmd, "") // merging stderr into stdout, or discarding output, writes nothing
	segs, ok := splitChain(cmd)
	if !ok {
		return false
	}
	for _, seg := range segs {
		words, ok := shellWords(seg)
		if !ok || len(words) == 0 {
			return false
		}
		for len(words) > 1 {
			m := reAssign.FindStringSubmatch(words[0])
			if m == nil {
				break
			}
			if !envOK[m[1]] {
				return false
			}
			words = words[1:]
		}
		for i, w := range words {
			if strings.HasPrefix(w, "/") {
				if !p.inRoot(w) {
					return false
				}
				words[i] = "./" + w // in the workspace: as safe as a relative path
			}
		}
		s := strings.Join(words, " ")
		if reForbidden.MatchString(s) || reDanger.MatchString(s) || reUnsafeArg.MatchString(s) || reExecArg.MatchString(s) {
			return false
		}
		if !reSafe.MatchString(s) && !reCd.MatchString(s) && !(writes && reMkdir.MatchString(s)) {
			return false
		}
	}
	return true
}

func (p *Policy) inRoot(path string) bool {
	if p.Root == "" {
		return false
	}
	rel, err := filepath.Rel(p.Root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// splitChain splits cmd at unquoted &&, ||, ; and |. It fails on anything
// that expands, substitutes, redirects or backgrounds, quoted or not
// (except inside single quotes, where nothing expands).
func splitChain(cmd string) ([]string, bool) {
	var segs []string
	var cur strings.Builder
	var q byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if q == '\'' {
			if c == '\'' {
				q = 0
			}
			cur.WriteByte(c)
			continue
		}
		if q == '"' {
			switch c {
			case '`', '$', '\\', '!':
				return nil, false
			case '"':
				q = 0
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'', '"':
			q = c
			cur.WriteByte(c)
		case '`', '$', '<', '>', '(', ')', '{', '}', '\n', '\r', '\\', '!', '#':
			return nil, false
		case ';', '&', '|':
			if c != ';' && i+1 < len(cmd) && cmd[i+1] == c {
				i++
			} else if c == '&' {
				return nil, false // background
			}
			if strings.TrimSpace(cur.String()) == "" {
				return nil, false
			}
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if q != 0 || strings.TrimSpace(cur.String()) == "" {
		return nil, false
	}
	return append(segs, cur.String()), true
}

// shellWords splits a segment (already checked by splitChain) into words,
// removing quotes. A word that was quoted keeps a marker-free value, which
// only makes the checks above stricter (a quoted "-delete" still matches).
func shellWords(seg string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	var q byte
	in := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case q != 0 && c == q:
			q = 0
		case q != 0:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			q, in = c, true
		case c == ' ' || c == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if q != 0 {
		return nil, false
	}
	if in {
		words = append(words, cur.String())
	}
	return words, true
}
