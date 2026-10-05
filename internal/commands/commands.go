// Package commands loads user-defined slash commands from markdown (and
// Gemini CLI TOML) files, in the formats of the harnesses ternly is
// compatible with, and expands their arguments (docs/commands.md).
package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Origin is the harness whose file format a command uses; it decides the
// argument syntax.
type Origin string

const (
	Ternly   Origin = "ternly"
	OpenCode Origin = "opencode"
	Claude   Origin = "claude" // $0 is the first argument (0-based)
	Gemini   Origin = "gemini" // TOML; {{args}}
	Codex    Origin = "codex"  // /prompts:name; $NAME from NAME=value
)

// Command is a user-defined command.
type Command struct {
	Name        string // without the slash, e.g. "review" or "git:commit"
	Description string
	ArgHint     string
	Project     bool // from the workspace (repository content) rather than the user's home
	Origin      Origin
	Path        string
	Template    string
}

// Dir is one place commands are read from.
type Dir struct {
	Path    string
	Origin  Origin
	Project bool
}

// Dirs are the command locations for a workspace and home directory, in
// precedence order: project before user; within each, ternly, OpenCode,
// Claude Code, Gemini CLI, Codex.
func Dirs(root, home string) []Dir {
	return []Dir{
		{filepath.Join(root, ".ternly", "commands"), Ternly, true},
		{filepath.Join(root, ".opencode", "commands"), OpenCode, true},
		{filepath.Join(root, ".claude", "commands"), Claude, true},
		{filepath.Join(root, ".gemini", "commands"), Gemini, true},
		{filepath.Join(home, ".config", "ternly", "commands"), Ternly, false},
		{filepath.Join(home, ".config", "opencode", "commands"), OpenCode, false},
		{filepath.Join(home, ".claude", "commands"), Claude, false},
		{filepath.Join(home, ".gemini", "commands"), Gemini, false},
		{filepath.Join(home, ".codex", "prompts"), Codex, false},
	}
}

// Load reads every command in dirs. The first definition of a name wins
// (dirs are in precedence order); names in reserved (built-ins) are skipped
// with an error, as are unreadable files.
func Load(dirs []Dir, reserved func(string) bool) ([]*Command, []error) {
	var out []*Command
	var errs []error
	seen := map[string]bool{}
	for _, d := range dirs {
		var files []string
		_ = filepath.WalkDir(d.Path, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil // a missing directory is normal
			}
			if e.IsDir() {
				if p != d.Path && d.Origin == Codex { // Codex reads only top-level prompts
					return filepath.SkipDir
				}
				return nil
			}
			ext := ".md"
			if d.Origin == Gemini {
				ext = ".toml"
			}
			if strings.HasSuffix(p, ext) && e.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			rel, _ := filepath.Rel(d.Path, p)
			name := strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel)), "/", ":")
			if d.Origin == Codex {
				name = "prompts:" + name
			}
			if seen[name] {
				continue
			}
			if reserved != nil && reserved(name) {
				errs = append(errs, fmt.Errorf("%s: /%s is a built-in command and can't be redefined", p, name))
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			c := &Command{Name: name, Project: d.Project, Origin: d.Origin, Path: p}
			if d.Origin == Gemini {
				err = parseTOML(c, string(b))
			} else {
				parseMarkdown(c, string(b))
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
				continue
			}
			seen[name] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// parseMarkdown reads optional YAML-style frontmatter (description,
// argument-hint) and the template body.
func parseMarkdown(c *Command, s string) {
	s = strings.TrimPrefix(s, "\uFEFF")
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if fm, body, ok := strings.Cut(rest, "\n---"); ok {
			for _, l := range strings.Split(fm, "\n") {
				k, v, ok := strings.Cut(l, ":")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				switch strings.TrimSpace(k) {
				case "description":
					c.Description = v
				case "argument-hint":
					c.ArgHint = v
				}
			}
			s = strings.TrimPrefix(strings.TrimPrefix(body, "\r"), "\n")
		}
	}
	c.Template = strings.TrimSpace(s)
	if c.Description == "" {
		c.Description = firstLine(c.Template)
	}
}

// parseTOML reads the two keys Gemini CLI defines: prompt (required) and
// description. Strings may be basic ("…"), literal ('…') or multi-line
// ("""…""" / ”'…”').
func parseTOML(c *Command, s string) error {
	vals := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		for _, q := range []string{`"""`, `'''`} {
			if strings.HasPrefix(v, q) {
				body := strings.TrimPrefix(v, q)
				for !strings.Contains(body, q) && sc.Scan() {
					body += "\n" + sc.Text()
				}
				body, _, _ = strings.Cut(body, q)
				vals[k] = strings.TrimPrefix(body, "\n")
				v = ""
				break
			}
		}
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, `"`):
			u, err := strconv.Unquote(v)
			if err != nil {
				return fmt.Errorf("%s: %v", k, err)
			}
			vals[k] = u
		case strings.HasPrefix(v, `'`):
			vals[k] = strings.TrimSuffix(strings.TrimPrefix(v, `'`), `'`)
		}
	}
	if strings.TrimSpace(vals["prompt"]) == "" {
		return errors.New("no prompt")
	}
	c.Template, c.Description = vals["prompt"], vals["description"]
	if c.Description == "" {
		c.Description = "custom command"
	}
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	l = strings.TrimLeft(l, "# ")
	if r := []rune(l); len(r) > 70 {
		l = string(r[:69]) + "…"
	}
	return l
}

// Hooks run the side effects a template may ask for. Shell runs a command
// (through the sandbox and permission policy) and returns its output.
type Hooks struct {
	Shell func(cmd string) (string, error)
}

var (
	reClaudePos  = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]|\$(\d)`)
	rePos        = regexp.MustCompile(`\$(\d)`)
	reNamed      = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)
	reBacktick   = regexp.MustCompile("!`([^`]+)`")
	reGeminiExec = regexp.MustCompile(`!\{`)
)

// Expand fills a template with the arguments typed after the command.
func (c *Command) Expand(args string, h Hooks) (string, error) {
	t := c.Template
	if c.Origin == Gemini {
		return expandGemini(t, args, h)
	}
	fields := SplitArgs(args)
	used := false
	esc := "\x00DOLLAR\x00"
	if c.Origin == Claude {
		t = strings.ReplaceAll(t, `\$`, esc)
	}
	if c.Origin == Codex {
		t = strings.ReplaceAll(t, "$$", esc)
		named := map[string]string{}
		var pos []string
		for _, f := range fields {
			if k, v, ok := strings.Cut(f, "="); ok && reNamed.MatchString("$"+k) && k == strings.ToUpper(k) {
				named[k] = v
			} else {
				pos = append(pos, f)
			}
		}
		fields = pos
		t = reNamed.ReplaceAllStringFunc(t, func(m string) string {
			k := m[1:]
			if k == "ARGUMENTS" {
				return m
			}
			used = true
			return named[k]
		})
	}
	if strings.Contains(t, "$ARGUMENTS") {
		used = true
	}
	if c.Origin == Claude {
		t = reClaudePos.ReplaceAllStringFunc(t, func(m string) string {
			used = true
			s := reClaudePos.FindStringSubmatch(m)
			n, _ := strconv.Atoi(s[1] + s[2])
			if n < len(fields) {
				return fields[n]
			}
			return ""
		})
	} else {
		t = rePos.ReplaceAllStringFunc(t, func(m string) string {
			used = true
			n, _ := strconv.Atoi(m[1:])
			if n >= 1 && n <= len(fields) {
				return fields[n-1]
			}
			return ""
		})
	}
	t = strings.ReplaceAll(t, "$ARGUMENTS", strings.TrimSpace(args))
	if c.Origin == OpenCode || c.Origin == Ternly {
		var err error
		t = reBacktick.ReplaceAllStringFunc(t, func(m string) string {
			if err != nil {
				return ""
			}
			var out string
			out, err = runShell(h, reBacktick.FindStringSubmatch(m)[1])
			return out
		})
		if err != nil {
			return "", err
		}
	}
	t = strings.ReplaceAll(t, esc, "$")
	if !used && strings.TrimSpace(args) != "" {
		t += "\n\nARGUMENTS: " + strings.TrimSpace(args)
	}
	return t, nil
}

// expandGemini: !{cmd} (with {{args}} shell-escaped inside), then {{args}}
// raw; with no {{args}} the arguments are appended after two newlines.
func expandGemini(t, args string, h Hooks) (string, error) {
	args = strings.TrimSpace(args)
	has := strings.Contains(t, "{{args}}")
	var b strings.Builder
	for {
		loc := reGeminiExec.FindStringIndex(t)
		if loc == nil {
			b.WriteString(t)
			break
		}
		b.WriteString(t[:loc[0]])
		depth, end := 1, -1
		for i := loc[1]; i < len(t); i++ {
			switch t[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			return "", errors.New("unbalanced braces in !{…}")
		}
		cmd := strings.ReplaceAll(t[loc[1]:end], "{{args}}", shellQuote(args))
		out, err := runShell(h, cmd)
		if err != nil {
			return "", err
		}
		b.WriteString(out)
		t = t[end+1:]
	}
	out := strings.ReplaceAll(b.String(), "{{args}}", args)
	if !has && args != "" {
		out += "\n\n" + args
	}
	return out, nil
}

func runShell(h Hooks, cmd string) (string, error) {
	if h.Shell == nil {
		return "", errors.New("this command runs shell commands, which aren't available here")
	}
	return h.Shell(strings.TrimSpace(cmd))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SplitArgs splits shell-style: whitespace separates, quotes group, and a
// backslash escapes the next character.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	in, q, esc := false, rune(0), false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc, in = false, true
		case r == '\\' && q != '\'':
			esc = true
		case q != 0:
			if r == q {
				q = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			q, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
