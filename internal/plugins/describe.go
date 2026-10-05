package plugins

import (
	"fmt"
	"sort"
	"strings"
)

// Review is the text shown before a plugin is approved: where it comes
// from, its trust label (from the source), exactly what it will run and
// with which access, what it adds and costs per request, what isn't loaded,
// and for an update the diff.
func Review(p *Pending) string {
	m := p.Manifest
	var b strings.Builder
	head := fmt.Sprintf("%s %s (%s) from %s", m.Name, m.Version, m.Format, p.Source.URL)
	if p.Source.Path != "" {
		head += " (" + p.Source.Path + ")"
	}
	if p.Commit != "local" {
		head += " @ " + shortCommit(p.Commit)
	}
	b.WriteString(head + "\n")
	fmt.Fprintf(&b, "trust: %s — %s\n", p.Trust.Level, p.Trust.Why)
	if m.Description != "" {
		fmt.Fprintf(&b, "says: %q%s\n", clip(m.Description, 160), map[bool]string{true: " (by " + m.Author + ", self-declared)", false: ""}[m.Author != ""])
	}
	if p.Prev != nil {
		fmt.Fprintf(&b, "update from %s:\n", shortCommit(p.Prev.Commit))
		if p.Diff.Empty() {
			b.WriteString("  no changes\n")
		} else {
			b.WriteString(p.Diff.Text() + "\n")
			if p.Diff.Executes {
				b.WriteString("  ⚠ what this plugin executes changed — review the lines above\n")
			}
		}
	}
	if len(p.Surface.Lines) > 0 {
		b.WriteString("will run (confined: no access to your home, keys or ternly's data; no inherited environment):\n")
		hooks, mcp := DefaultHookScope, DefaultMCPScope
		if p.Prev != nil {
			hooks, mcp = p.Prev.Hooks, p.Prev.MCP
		}
		for _, l := range p.Surface.Lines {
			sc := hooks
			if strings.HasPrefix(l, "mcp ") {
				sc = mcp
			}
			fmt.Fprintf(&b, "  %s   [workspace %s, network %s]\n", l, sc.Workspace, map[bool]string{true: "on", false: "off"}[sc.Network])
		}
	} else {
		b.WriteString("runs no code (prompt text only)\n")
	}
	counts := map[Kind]int{}
	tokens := 0
	for _, c := range m.Components {
		counts[c.Kind]++
		switch c.Kind {
		case KSkill, KAgent:
			tokens += estTokens(fmt.Sprintf("\n- %s: %s", c.Name, clip(c.Description, 200)))
		}
	}
	var adds []string
	for _, k := range []Kind{KSkill, KCommand, KAgent, KRule, KContext, KHook, KMCP} {
		if n := counts[k]; n > 0 {
			adds = append(adds, fmt.Sprintf("%d %s%s", n, k, map[bool]string{true: "s", false: ""}[n > 1]))
		}
	}
	if len(adds) > 0 {
		fmt.Fprintf(&b, "adds: %s · ~%d tokens per request for skill and agent listings (bodies load only when used; MCP tool schemas counted once running)\n", strings.Join(adds, ", "), tokens)
	}
	if len(m.Skipped) > 0 {
		sk := append([]Skip(nil), m.Skipped...)
		sort.Slice(sk, func(i, j int) bool { return sk[i].What < sk[j].What })
		b.WriteString("not loaded:\n")
		for _, s := range sk[:min(len(sk), 12)] {
			fmt.Fprintf(&b, "  %s — %s\n", s.What, s.Why)
		}
		if len(sk) > 12 {
			fmt.Fprintf(&b, "  … %d more (/plugin info %s)\n", len(sk)-12, m.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
