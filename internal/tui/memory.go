package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/memory"
)

const memoryHelp = `  /memory                     recent items per tier, with ids
  /memory search <query>      ranked, with score components
  /memory forget <id>         delete an item (an id prefix is enough)
  /memory edit <id> <text>    replace an item's text (a new version)
  /memory add [user] <text>   remember something (user: in every project)
  /memory promote <id>        apply a project note in every project (only you can)`

// memoryCommand handles /memory; ok=false means "not mine".
func (m *Model) memoryCommand(name, arg string) (tea.Cmd, bool) {
	if name != "/memory" {
		return nil, false
	}
	mem := m.App.Memory
	if mem == nil {
		m.addInfo(sDim.Render("  memory is off (config \"memory\": false, or it failed to open at start)"))
		return nil, true
	}
	if ok, err := mem.Ready(); !ok || err != nil {
		if err != nil {
			m.addInfo(sErr.Render("  memory failed to load: " + err.Error()))
		} else {
			m.addInfo(sDim.Render("  memory is still loading — try again in a moment"))
		}
		return nil, true
	}
	sub, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	switch sub {
	case "":
		m.addInfo(memoryList(mem))
	case "search":
		if rest == "" {
			m.addInfo(sErr.Render("  usage: /memory search <query>"))
			break
		}
		hits := mem.Search(context.Background(), memory.Query{Text: rest, Near: mem.Near(rest), Limit: 15, Vector: true})
		if len(hits) == 0 {
			m.addInfo(sDim.Render("  no matching notes"))
			break
		}
		var b strings.Builder
		now := time.Now()
		for _, h := range hits {
			fmt.Fprintf(&b, "  %s %s\n", sDim.Render(memory.Label(h.Item, now)), h.Item.Text)
			fmt.Fprintf(&b, "%s\n", sDim.Render(fmt.Sprintf("      score %.2f = lexical %.2f · structure %.2f · recency %.2f · vector %.2f", h.Score, h.Lex, h.Struct, h.Rec, h.Vec)))
		}
		m.addInfo(strings.TrimRight(b.String(), "\n"))
	case "forget", "rm", "delete":
		if err := mem.Forget(rest); err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sOK.Render("  forgotten " + rest))
		}
	case "edit":
		id, text, _ := strings.Cut(rest, " ")
		it, err := mem.Edit(id, text)
		if err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sOK.Render(fmt.Sprintf("  %s is now version %d", it.ID, it.V)))
		}
	case "promote":
		it, err := mem.Promote(rest)
		if err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sOK.Render("  " + it.ID + " now applies in every project (user tier)"))
		}
	case "add":
		scope := memory.Project
		if f, r, _ := strings.Cut(rest, " "); f == "user" {
			scope, rest = memory.User, strings.TrimSpace(r)
		}
		it, err := mem.Add(memory.Item{Scope: scope, Kind: "note", Text: rest, Source: "user"})
		if err != nil {
			m.addInfo(sErr.Render("  " + err.Error()))
		} else {
			m.addInfo(sOK.Render(fmt.Sprintf("  remembered as %s (%s)", it.ID, it.Scope)))
		}
	default:
		m.addInfo(memoryHelp)
	}
	return nil, true
}

func memoryList(mem *memory.Memory) string {
	var b strings.Builder
	now := time.Now()
	proj, user := mem.Project.List(), mem.User.List() // loaded: memoryCommand checked Ready
	tiers := []struct {
		name  string
		items []memory.Item
	}{{"session", nil}, {"project", nil}, {"user", user}}
	for _, it := range proj {
		if it.Scope == memory.Session {
			tiers[0].items = append(tiers[0].items, it)
		} else {
			tiers[1].items = append(tiers[1].items, it)
		}
	}
	for _, t := range tiers {
		fmt.Fprintf(&b, "  %s %s\n", sAccent.Render(t.name), sDim.Render(fmt.Sprintf("(%d)", len(t.items))))
		for i, it := range t.items {
			if i == 10 {
				fmt.Fprintf(&b, "%s\n", sDim.Render(fmt.Sprintf("    … %d more (/memory search <query>)", len(t.items)-10)))
				break
			}
			fmt.Fprintf(&b, "    %s %s\n", sDim.Render(memory.Label(it, now)), it.Text)
		}
	}
	st := mem.Stats()
	vec := "off (no local embedding model)"
	if e := mem.Embedder(); e != nil {
		vec = e.Name()
	}
	fmt.Fprintf(&b, "%s", sDim.Render(fmt.Sprintf("  this process: %d recalls, %d notes injected (~%d tokens), %d written, %d deduplicated, %d refused · vectors: %s · budget %d tokens/turn\n  /memory search|forget|edit|add|promote — /memory help",
		st.Recalls, st.Injected, st.InjectedTokens, st.Written, st.Deduped, st.Refused, vec, mem.Budget)))
	return b.String()
}
