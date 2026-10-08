package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The @ file picker (ADR 023): typing @ and part of a path lists matching
// workspace files, fuzzy-ranked; Tab or Enter inserts @path. Files come
// from the confined glob tool (os.Root), off the UI goroutine.

type filesMsg struct {
	q    string
	hits []string
}

// atToken is the @mention being typed at the end of v, if any.
func atToken(v string) (string, bool) {
	i := strings.LastIndexAny(v, " \n\t")
	tok := v[i+1:]
	if !strings.HasPrefix(tok, "@") || strings.HasPrefix(v, "/") || strings.HasPrefix(v, "!") {
		return "", false
	}
	return tok[1:], true
}

// globPattern matches paths whose base name contains q's characters in order.
func globPattern(q string) string {
	if q == "" {
		return "**"
	}
	var b strings.Builder
	b.WriteString("**/*")
	for _, r := range q {
		if strings.ContainsRune("*?[]{}\\/", r) {
			continue
		}
		b.WriteRune(r)
		b.WriteByte('*')
	}
	return b.String()
}

func (m *Model) globFiles(q string) tea.Cmd {
	t := m.App.Reg.Get("glob")
	if t == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		args, _ := json.Marshal(map[string]string{"pattern": globPattern(q)})
		out, err := t.Run(ctx, args)
		if err != nil || out == "no matches" {
			return filesMsg{q: q}
		}
		return filesMsg{q: q, hits: strings.Split(out, "\n")}
	}
}

// onFiles shows the files for the @token still being typed.
func (m *Model) onFiles(f filesMsg) {
	q, ok := atToken(m.ta.Value())
	if !ok || q != f.q {
		return // typed on since: a newer request is on its way
	}
	type scored struct {
		p string
		s int
	}
	var all []scored
	lq := strings.ToLower(q)
	for _, p := range f.hits {
		lp := strings.ToLower(p)
		s := fuzzyScore(lq, lp[strings.LastIndex(lp, "/")+1:]) + 50 // the base name first
		if s < 50 {
			s = fuzzyScore(lq, lp)
		}
		if s >= 0 {
			all = append(all, scored{p, s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s > all[j].s })
	if len(all) == 0 {
		m.comp = nil
		m.layout()
		return
	}
	m.comp = &completion{file: true}
	for _, a := range all {
		m.comp.items = append(m.comp.items, compItem{name: untrusted(a.p)})
	}
	m.layout()
}
