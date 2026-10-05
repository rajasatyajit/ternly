package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/plugins"
	"github.com/rajasatyajit/ternly/internal/tools"
)

const pluginHelp = `  /plugin                          installed, local and importable plugins
  /plugin add <git-url[#ref]|dir|name@marketplace>   review, approve, install (pinned)
  /plugin update [name]            fetch the latest; review the diff; approve
  /plugin review <name>            re-approve a plugin whose files changed on disk
  /plugin import <name>            adopt a plugin installed for Claude Code or Gemini CLI
  /plugin remove|enable|disable <name>
  /plugin scope <name> hooks|mcp workspace=none|ro|rw network=on|off
  /plugin info <name>              components, what it runs, what isn't loaded, tokens
  /plugin marketplace add <owner/repo|git-url|dir> · /plugin marketplace list`

type pluginDoneMsg struct {
	text   string
	reload bool
}

// pluginReviewMsg carries a fetched plugin to show before asking.
type pluginReviewMsg struct {
	p   *plugins.Pending
	t0  time.Time
	err error
}

func (m *Model) cmdPlugin(arg string) tea.Cmd {
	rt := m.App.Plugins
	if rt == nil || rt.Store == nil {
		m.addInfo(sDim.Render("  plugins are off"))
		return nil
	}
	f := strings.Fields(arg)
	sub, rest := "", ""
	if len(f) > 0 {
		sub, rest = f[0], strings.TrimSpace(strings.TrimPrefix(arg, f[0]))
	}
	st := rt.Store
	ctx := context.Background()
	switch sub {
	case "", "list":
		m.addInfo(m.pluginList())
	case "help":
		m.addInfo(pluginHelp)
	case "add", "install":
		if rest == "" {
			m.addInfo(sErr.Render("  usage: /plugin add <git-url[#ref]|dir|name@marketplace>"))
			return nil
		}
		m.addInfo(sDim.Render("  fetching " + rest + "…"))
		return func() tea.Msg {
			t0 := time.Now()
			src, err := pluginSource(st, rest)
			if err != nil {
				return pluginReviewMsg{err: err}
			}
			p, err := st.Fetch(ctx, src)
			return pluginReviewMsg{p: p, t0: t0, err: err}
		}
	case "import":
		home, _ := os.UserHomeDir()
		for _, fd := range plugins.FoundElsewhere(home) {
			if fd.Name == rest {
				return func() tea.Msg {
					p, err := st.Fetch(ctx, plugins.Source{Kind: "local", URL: fd.Dir})
					return pluginReviewMsg{p: p, t0: time.Now(), err: err}
				}
			}
		}
		if rest == "codex-mcp" {
			m.addInfo(sErr.Render("  Codex MCP servers: copy them into ~/.config/ternly/mcp.json (they are your own config)"))
			return nil
		}
		m.addInfo(sErr.Render("  no plugin " + rest + " installed for Claude Code or Gemini CLI — /plugin lists them"))
	case "update":
		var names []string
		for _, p := range st.List() {
			if (rest == "" || p.Name == rest) && p.Source.Kind == "git" {
				names = append(names, p.Name)
			}
		}
		if len(names) == 0 {
			m.addInfo(sDim.Render("  nothing to update (local plugins update with /plugin add <dir> again)"))
			return nil
		}
		var cmds []tea.Cmd
		for _, n := range names {
			p, _ := st.Get(n)
			src := p.Source
			if src.Marketplace != "" { // the marketplace decides the pinned version: refresh it first
				src.Ref = ""
			} else {
				src.Ref = strings.TrimSuffix(src.Ref, p.Commit) // a ref (branch or tag) is followed; a pinned sha is re-fetched as is
			}
			cmds = append(cmds, func() tea.Msg {
				t0 := time.Now()
				if src.Marketplace != "" {
					if _, err := st.AddMarketplace(ctx, src.MarketplaceURL); err != nil {
						return pluginReviewMsg{err: err}
					}
					if s2, _, err := st.Resolve(p.Name, src.Marketplace); err == nil {
						src = s2
					}
				}
				np, err := st.Fetch(ctx, src)
				if err == nil && np.Commit == p.Commit && np.Diff.Empty() {
					st.Discard(np)
					return pluginDoneMsg{text: sDim.Render("  " + p.Name + " is up to date (" + p.Commit[:min(12, len(p.Commit))] + ")")}
				}
				return pluginReviewMsg{p: np, t0: t0, err: err}
			})
		}
		return tea.Sequence(cmds...)
	case "review":
		p, ok := st.Get(rest)
		if !ok {
			m.addInfo(sErr.Render("  no plugin " + rest))
			return nil
		}
		_, d, ok, err := st.Verify(p)
		switch {
		case err != nil:
			m.addInfo(sErr.Render("  " + err.Error()))
			return nil
		case ok:
			m.addInfo(sDim.Render("  " + rest + " matches what you approved"))
			return nil
		}
		m.addInfo(sWarn.Render("  "+rest+" changed on disk since you approved it:") + "\n" + d.Text())
		ask := m.App.Reg.Policy.Ask
		return func() tea.Msg {
			if ask == nil || ask(ctx, "re-approve plugin", rest+" as it is now on disk", d.Executes) == tools.Deny {
				return pluginDoneMsg{text: sDim.Render("  not approved — the plugin stays disabled")}
			}
			if err := st.Reapprove(rest); err != nil {
				return pluginDoneMsg{text: sErr.Render("  " + err.Error())}
			}
			return pluginDoneMsg{text: sOK.Render("  re-approved " + rest), reload: true}
		}
	case "remove", "uninstall":
		ask := m.App.Reg.Policy.Ask
		return func() tea.Msg {
			if ask == nil || ask(ctx, "remove plugin", "uninstall "+rest+" and delete its files and data", false) == tools.Deny {
				return pluginDoneMsg{text: sDim.Render("  remove cancelled")}
			}
			if err := st.Remove(rest); err != nil {
				return pluginDoneMsg{text: sErr.Render("  " + err.Error())}
			}
			return pluginDoneMsg{text: sOK.Render("  removed " + rest), reload: true}
		}
	case "enable", "disable":
		if err := st.SetEnabled(rest, sub == "enable"); err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
			return nil
		}
		return m.reloadPlugins(sOK.Render("  " + sub + "d " + rest))
	case "scope":
		return m.pluginScope(rest)
	case "info":
		m.addInfo(m.pluginInfo(rest))
	case "marketplace":
		mf := strings.Fields(rest)
		switch {
		case len(mf) >= 2 && mf[0] == "add":
			return func() tea.Msg {
				mk, err := st.AddMarketplace(ctx, mf[1])
				if err != nil {
					return pluginDoneMsg{text: sErr.Render("  " + err.Error())}
				}
				return pluginDoneMsg{text: sOK.Render(fmt.Sprintf("  marketplace %s: %d plugins (from %s @ %s) — /plugin add <name>@%s", mk.Name, len(mk.Plugins), mk.URL, mk.Commit[:min(12, len(mk.Commit))], mk.Name))}
			}
		default:
			all, err := st.Marketplaces()
			if err != nil || len(all) == 0 {
				m.addInfo(sDim.Render("  no marketplaces — /plugin marketplace add anthropics/claude-plugins-official"))
				return nil
			}
			var b strings.Builder
			for _, mk := range all {
				fmt.Fprintf(&b, "  %s %s\n", sAccent.Render(mk.Name), sDim.Render(fmt.Sprintf("%d plugins · %s", len(mk.Plugins), mk.URL)))
				for i, e := range mk.Plugins {
					if i == 15 {
						fmt.Fprintf(&b, "    … %d more\n", len(mk.Plugins)-15)
						break
					}
					fmt.Fprintf(&b, "    %-28s %s\n", e.Name, sDim.Render(truncate(e.Description, m.w-36)))
				}
			}
			m.addInfo(strings.TrimRight(b.String(), "\n"))
		}
	default:
		m.addInfo(pluginHelp)
	}
	return nil
}

// pluginSource parses what /plugin add was given.
func pluginSource(st *plugins.Store, s string) (plugins.Source, error) {
	if name, market, ok := strings.Cut(s, "@"); ok && !strings.Contains(s, "://") && !strings.HasPrefix(s, "git@") {
		src, _, err := st.Resolve(name, market)
		return src, err
	}
	if fi, err := os.Stat(s); err == nil && fi.IsDir() {
		abs, _ := filepath.Abs(s)
		return plugins.Source{Kind: "local", URL: abs}, nil
	}
	url, ref, _ := strings.Cut(s, "#")
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") && strings.Count(url, "/") == 1 {
		url = "https://github.com/" + url
	}
	return plugins.Source{Kind: "git", URL: url, Ref: ref}, nil
}

// onPluginReview shows a fetched plugin and asks for approval.
func (m *Model) onPluginReview(r pluginReviewMsg) tea.Cmd {
	if r.err != nil {
		m.addInfo(sErr.Render("  plugin: " + r.err.Error()))
		return nil
	}
	p := r.p
	m.addInfo(sAccent.Render("  Review") + "\n" + indent(plugins.Review(p)))
	ask := m.App.Reg.Policy.Ask
	st := m.App.Plugins.Store
	danger := len(p.Surface.Lines) > 0 && (p.Prev == nil || p.Diff.Executes)
	verb := "install plugin"
	if p.Prev != nil {
		verb = "update plugin"
	}
	return func() tea.Msg {
		ctx := context.Background()
		if ask == nil || ask(ctx, verb, fmt.Sprintf("%s @ %s — see the review above", p.Manifest.Name, p.Commit[:min(12, len(p.Commit))]), danger) == tools.Deny {
			st.Discard(p)
			return pluginDoneMsg{text: sDim.Render("  not installed — nothing changed")}
		}
		t1 := time.Now()
		in, err := st.Accept(p)
		if err != nil {
			st.Discard(p)
			return pluginDoneMsg{text: sErr.Render("  install failed, nothing changed: " + err.Error())}
		}
		ws := m.App.Plugins.Apply(ctx)
		text := sOK.Render(fmt.Sprintf("  %s %s @ %s — usable from your next prompt (%s after approval)", map[bool]string{true: "updated", false: "installed"}[p.Prev != nil], in.Name, in.Commit[:min(12, len(in.Commit))], time.Since(t1).Round(time.Millisecond)))
		for _, w := range ws {
			text += "\n" + sWarn.Render("  ! "+w)
		}
		return pluginDoneMsg{text: text, reload: true}
	}
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

func (m *Model) reloadPlugins(msg string) tea.Cmd {
	return func() tea.Msg {
		ws := m.App.Plugins.Apply(context.Background())
		for _, w := range ws {
			msg += "\n" + sWarn.Render("  ! "+w)
		}
		return pluginDoneMsg{text: msg, reload: true}
	}
}

func (m *Model) pluginScope(arg string) tea.Cmd {
	f := strings.Fields(arg)
	if len(f) < 3 || (f[1] != "hooks" && f[1] != "mcp") {
		m.addInfo(sErr.Render("  usage: /plugin scope <name> hooks|mcp workspace=none|ro|rw network=on|off"))
		return nil
	}
	st := m.App.Plugins.Store
	p, ok := st.Get(f[0])
	if !ok {
		m.addInfo(sErr.Render("  no plugin " + f[0]))
		return nil
	}
	sc := &p.Hooks
	if f[1] == "mcp" {
		sc = &p.MCP
	}
	for _, kv := range f[2:] {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "workspace" && (v == "none" || v == "ro" || v == "rw"):
			sc.Workspace = v
		case k == "network" && (v == "on" || v == "off"):
			sc.Network = v == "on"
		default:
			m.addInfo(sErr.Render("  bad setting " + kv))
			return nil
		}
	}
	if err := st.SetScopes(p.Name, p.Hooks, p.MCP); err != nil {
		m.addInfo(sErr.Render("  " + err.Error()))
		return nil
	}
	return m.reloadPlugins(sOK.Render(fmt.Sprintf("  %s %s: workspace %s, network %v (servers restart with it)", p.Name, f[1], sc.Workspace, sc.Network)))
}

func (m *Model) pluginList() string {
	rt := m.App.Plugins
	var b strings.Builder
	ins := rt.Store.List()
	tokens := rt.Tokens()
	b.WriteString("  " + sAccent.Render("Installed") + "\n")
	if len(ins) == 0 {
		b.WriteString(sDim.Render("    none — /plugin add <git-url|dir|name@marketplace>") + "\n")
	}
	for _, p := range ins {
		state := sOK.Render("on")
		if !p.Enabled {
			state = sDim.Render("off")
		}
		if _, _, ok, _ := rt.Store.Verify(p); !ok {
			state = sErr.Render("changed on disk — /plugin review " + p.Name)
		}
		t := 0
		for k, v := range tokens {
			if strings.HasPrefix(k, p.Name+":") {
				t += v
			}
		}
		fmt.Fprintf(&b, "    %-22s %s %s\n", p.Name, state, sDim.Render(fmt.Sprintf("@%s · %s · %s trust · %d things it runs · ~%d tokens/request", p.Commit[:min(12, len(p.Commit))], p.Format, p.Trust.Level, len(p.Approved.Lines), t)))
	}
	for _, mf := range rt.Active() {
		if mf.Format != "local" {
			continue
		}
		var names []string
		for _, c := range mf.Components {
			names = append(names, string(c.Kind)+" "+c.Name)
		}
		label := map[string]string{"user": "From your home (~/.claude, ~/.cursor, ~/.config/opencode, …)", "project": "From this workspace (.claude, .cursor, .opencode, .gemini)"}[mf.Name]
		fmt.Fprintf(&b, "  %s %s\n    %s\n", sAccent.Render(label), sDim.Render("(prompt text only)"), truncate(strings.Join(names, ", "), max(40, m.w-6)))
	}
	home, _ := os.UserHomeDir()
	if fs := plugins.FoundElsewhere(home); len(fs) > 0 {
		b.WriteString("  " + sAccent.Render("Installed for other tools") + sDim.Render(" — /plugin import <name> to review and adopt") + "\n")
		for _, f := range fs {
			fmt.Fprintf(&b, "    %-22s %s\n", f.Name, sDim.Render(f.Harness))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) pluginInfo(name string) string {
	rt := m.App.Plugins
	for _, mf := range rt.Active() {
		if mf.Name != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "  %s %s (%s) %s\n", sAccent.Render(mf.Name), mf.Version, mf.Format, sDim.Render(mf.Dir))
		if p, ok := rt.Store.Get(name); ok {
			fmt.Fprintf(&b, "  trust: %s — %s\n  source: %s %s @ %s\n  hooks: workspace %s, network %v · mcp: workspace %s, network %v\n",
				p.Trust.Level, p.Trust.Why, p.Source.URL, p.Source.Path, p.Commit, p.Hooks.Workspace, p.Hooks.Network, p.MCP.Workspace, p.MCP.Network)
			for _, l := range p.Approved.Lines {
				b.WriteString("  runs: " + l + "\n")
			}
		}
		tokens := rt.Tokens()
		for _, c := range mf.Components {
			fmt.Fprintf(&b, "  %-8s %-30s %s%s\n", c.Kind, c.Name, sDim.Render(truncate(c.Description, max(20, m.w-50))), sDim.Render(map[bool]string{true: fmt.Sprintf(" (~%d tok)", tokens[c.Name]), false: ""}[tokens[c.Name] > 0]))
		}
		for _, s := range mf.Skipped {
			fmt.Fprintf(&b, "  %s %s — %s\n", sWarn.Render("not loaded:"), s.What, s.Why)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if p, ok := rt.Store.Get(name); ok && !p.Enabled {
		return sDim.Render("  " + name + " is disabled — /plugin enable " + name)
	}
	return sErr.Render("  no active plugin " + name)
}

// PluginsChanged is sent when plugins reload in the background (the file
// watcher): the TUI refreshes its commands.
func PluginsChanged() tea.Msg { return pluginDoneMsg{reload: true} }
