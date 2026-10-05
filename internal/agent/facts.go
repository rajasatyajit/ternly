package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rajasatyajit/ternly/internal/deps"
	"github.com/rajasatyajit/ternly/internal/llm"
)

// Facts (requirement 7): before a turn ends, the answer's references are
// checked against the workspace — every file:line it cites, and every
// backticked workspace symbol. What doesn't check out goes back to the model
// once; if it still doesn't, the user is told.

var (
	reCite = regexp.MustCompile("(?:^|[\\s(`\"'\\[])((?:[\\w.-]+/)*[\\w-][\\w.-]*\\.[A-Za-z][A-Za-z0-9]{0,7}):(\\d+)\\b")
	reTick = regexp.MustCompile("`([A-Za-z_]\\w*(?:\\.[A-Za-z_]\\w*)+)(?:\\(\\))?`")
)

// unsupported lists the answer's references that don't check out.
func (a *Agent) unsupported(answer string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reCite.FindAllStringSubmatch(answer, -1) {
		file, line := m[1], m[2]
		if seen[file+":"+line] || strings.Contains(file, "://") {
			continue
		}
		seen[file+":"+line] = true
		b, err := a.Reg.ReadFile(file)
		if err != nil {
			if strings.Contains(err.Error(), "outside the workspace") {
				if _, serr := os.Stat(file); serr == nil || !filepath.IsAbs(file) && !strings.HasPrefix(file, "~") {
					continue // a real file elsewhere, or a path relative to somewhere else: not judged
				}
			}
			out = append(out, fmt.Sprintf("%s:%s — no such file in the workspace", file, line))
			continue
		}
		n, _ := strconv.Atoi(line)
		lines := strings.Count(string(b), "\n")
		if len(b) > 0 && b[len(b)-1] != '\n' {
			lines++ // a last line without a newline
		}
		if n < 1 || n > lines {
			out = append(out, fmt.Sprintf("%s:%s — %s has %d lines", file, line, file, lines))
		}
	}
	if a.KnownSymbol == nil {
		return out
	}
	var evidence string // what tools showed or what the model wrote this session
	for _, ref := range reTick.FindAllStringSubmatch(answer, -1) {
		if seen[ref[1]] {
			continue
		}
		seen[ref[1]] = true
		exists, decidable := a.KnownSymbol(ref[1])
		if !decidable || exists {
			continue
		}
		if evidence == "" {
			evidence = a.evidence()
		}
		name := ref[1][strings.LastIndex(ref[1], ".")+1:]
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(evidence) {
			continue // shown by a tool, or written this session (the graph may not have caught up)
		}
		out = append(out, fmt.Sprintf("`%s` — not found in the code graph", ref[1]))
	}
	return out
}

// evidence is the session's tool results and tool-call arguments.
func (a *Agent) evidence() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var b strings.Builder
	for _, m := range a.state.History {
		if m.Role == "tool" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
		for _, c := range m.ToolCalls {
			b.WriteString(c.Args)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func msgFacts(probs []string) string {
	return "[ternly check] These references in your answer don't check out against the workspace:\n- " + strings.Join(probs, "\n- ") +
		"\nLook them up with tools and correct them, or say plainly that you couldn't confirm them."
}

// manifestBefore returns the path and current contents of a dependency
// manifest an edit is about to change ("" if the call isn't one).
func (a *Agent) manifestBefore(tc llm.ToolCall) (string, []byte) {
	if a.DepCheck == nil || tc.Name != "edit_file" && tc.Name != "write_file" {
		return "", nil
	}
	var x struct{ Path string }
	if json.Unmarshal([]byte(tc.Args), &x) != nil || !deps.Manifest(x.Path) {
		return "", nil
	}
	b, _ := a.Reg.ReadFile(x.Path)
	return x.Path, b
}

// checkDeps looks up what a call added — dependencies in an edited manifest,
// packages an install command names — and returns a note for the tool
// result when the registry says one doesn't exist.
func (a *Agent) checkDeps(ctx context.Context, tc llm.ToolCall, manifest string, before []byte) string {
	var ds []deps.Dep
	switch {
	case manifest != "":
		after, err := a.Reg.ReadFile(manifest)
		if err != nil {
			return ""
		}
		ds = deps.Added(manifest, before, after)
	case tc.Name == "bash":
		var x struct{ Command string }
		if json.Unmarshal([]byte(tc.Args), &x) == nil {
			ds = deps.FromCommand(x.Command)
		}
	}
	if len(ds) == 0 {
		return ""
	}
	probs := a.DepCheck.Check(ctx, ds)
	if len(probs) == 0 {
		return ""
	}
	a.Emit(Event{Kind: EvStatus, Text: "dependency check: " + strings.Join(probs, "; ")})
	return "\n[ternly dependency check — from the registry, not from this tool] " + strings.Join(probs, "; ") +
		". Fix the dependency (or tell the user it doesn't exist); don't assume it will resolve."
}
