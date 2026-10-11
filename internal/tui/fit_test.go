package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// sized is a model at w×h whose discovery found two providers' models.
func sized(t *testing.T, w, h int) *Model {
	t.Helper()
	m := benchModel(t, 0)
	local := &discover.Provider{ID: "ollama", Local: true}
	m.Update(discoveredMsg{models: []*discover.Model{
		{Provider: local, ProvID: "ollama", ID: "gemma4:latest", Tools: true},
		{Provider: &discover.Provider{ID: "openrouter"}, ProvID: "openrouter", ID: "some/model", Tools: true},
	}})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// lines returns the frame's lines without styling, each checked to fit w.
func fits(t *testing.T, frame string, w int) []string {
	t.Helper()
	ls := strings.Split(frame, "\n")
	for i, l := range ls {
		if lw := lipgloss.Width(l); lw > w {
			t.Errorf("line %d is %d columns, wider than %d: %q", i, lw, w, stripANSI(l))
		}
		ls[i] = stripANSI(l)
	}
	return ls
}

// F2 bug 1: the welcome hint was one 118-column line, cut off at 80 columns.
// It wraps at " · " and between words to the width it is shown at.
func TestWelcomeFitsWidth(t *testing.T) {
	for _, s := range [][2]int{{80, 24}, {120, 40}} {
		m := sized(t, s[0], s[1])
		ls := fits(t, m.View().Content, s[0])
		all := strings.Join(strings.Fields(strings.Join(ls, " ")), " ")
		for _, want := range []string{"/help or Ctrl+K for commands", "@ for files",
			"tasks are routed to the cheapest capable model and escalated only on failure"} {
			if !strings.Contains(all, want) {
				t.Errorf("%dx%d: the welcome lost %q:\n%s", s[0], s[1], want, strings.Join(ls, "\n"))
			}
		}
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 20}) // re-wraps on resize
		fits(t, m.View().Content, 60)
	}
}

// F2 bug 2: in the Ctrl+K palette a row with an argument hint padded its name
// by bytes, styling included, so its description started in another column.
// Every row's description starts in the same column, and every row fits.
func TestPaletteAligned(t *testing.T) {
	for _, s := range [][2]int{{80, 24}, {120, 40}} {
		m := sized(t, s[0], s[1])
		key(m, "ctrl+k")
		cols := map[int]string{}      // the column a description starts in: a command there
		for range len(m.comp.items) { // every row, as the list scrolls
			rows := fits(t, m.compView(), s[0])
			first := min(max(0, m.comp.sel-compRows/2), max(0, len(m.comp.items)-compRows))
			for i, r := range rows[:min(compRows, len(rows))] {
				it := m.comp.items[first+i]
				d := strings.TrimSuffix(strings.Fields(it.desc)[0], ",")
				c := lipgloss.Width(r[:strings.Index(r, " "+d)+1])
				cols[c] = it.name
			}
			key(m, "down")
		}
		if len(cols) != 1 {
			t.Errorf("%dx%d: descriptions start in %d columns: %v", s[0], s[1], len(cols), cols)
		}
		for c := range cols { // the name column is capped: descriptions keep their room
			if limit := max(12, s[0]*2/5) + 3; c > limit {
				t.Errorf("%dx%d: descriptions start at column %d, past %d", s[0], s[1], c, limit)
			}
		}
	}
}

// F2 bug 3: a long workspace path pushed the sandbox and permission mode off
// the header's right edge. They never truncate; the path elides in its middle.
func TestHeaderKeepsModes(t *testing.T) {
	long := "/home/someone/work/" + strings.Repeat("a-very-long-directory-name/", 6) + "project-x"
	for _, s := range [][2]int{{80, 24}, {120, 40}} {
		m := sized(t, s[0], s[1])
		m.App.Reg.Root = long
		h := fits(t, m.header(), s[0])[0]
		want := m.App.Reg.Sandbox.Mode() + " · " + m.App.Reg.Policy.Mode()
		if !strings.HasSuffix(h, want) {
			t.Errorf("%dx%d: the header lost %q: %q", s[0], s[1], want, h)
		}
		if !strings.Contains(h, "/home/") || !strings.Contains(h, "…") || !strings.Contains(h, "project-x") {
			t.Errorf("%dx%d: the path should keep its start and end around an ellipsis: %q", s[0], s[1], h)
		}
		m.App.Reg.Root = "/tmp/p"
		if h := fits(t, m.header(), s[0])[0]; strings.Contains(h, "…") || !strings.Contains(h, "/tmp/p") {
			t.Errorf("%dx%d: a short path is shown whole: %q", s[0], s[1], h)
		}
	}
}

// F2 bug 4: Esc and Ctrl+U didn't clear typed input (Esc only interrupted,
// Ctrl+U paged up). Esc clears the input, and interrupts a running turn only
// when there is nothing typed; Ctrl+U deletes to the start of the line, as in
// a shell. Ctrl+D is bound to nothing (owner's rule: no Ctrl+D/R/S/Q/Z);
// PgUp/PgDn scroll.
func TestEscAndCtrlUClearInput(t *testing.T) {
	m := sized(t, 120, 40)
	press := func(k tea.KeyPressMsg) { m.Update(k) }
	esc := tea.KeyPressMsg{Code: tea.KeyEscape}
	typeText(m, "hello world")
	press(esc)
	if v := m.ta.Value(); v != "" {
		t.Fatalf("Esc left %q in the input", v)
	}
	typeText(m, "abc def")
	press(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if v := m.ta.Value(); v != "" {
		t.Fatalf("Ctrl+U left %q in the input", v)
	}

	for range 200 { // a transcript taller than the screen
		m.addInfo("line")
	}
	m.refresh(true)
	top := m.vp.top
	press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.vp.top >= top {
		t.Fatalf("PgUp didn't scroll (%d → %d)", top, m.vp.top)
	}
	top = m.vp.top // scrolled up: either key would move the view if it scrolled
	press(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	press(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.vp.top != top {
		t.Fatalf("Ctrl+U/Ctrl+D scrolled the transcript (%d → %d)", top, m.vp.top)
	}
	typeText(m, "xy")
	press(tea.KeyPressMsg{Code: tea.KeyLeft})
	press(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if v := m.ta.Value(); v != "xy" {
		t.Fatalf("Ctrl+D edited the input: %q", v)
	}

	// a running turn: Esc with text clears it first, then interrupts
	cancelled := false
	m.busy, m.cancel = true, func() { cancelled = true }
	typeText(m, "queued later")
	press(esc)
	if cancelled || m.ta.Value() != "" {
		t.Fatalf("Esc with text: cancelled %v, input %q (want the input cleared, the turn running)", cancelled, m.ta.Value())
	}
	press(esc)
	if !cancelled {
		t.Fatal("Esc with nothing typed should interrupt the running turn")
	}
}

// Widths are display columns, not runes: a workspace path or session title in
// CJK, or wide file names in the @ picker, take two columns a character.
func TestFitWideCharacters(t *testing.T) {
	for _, w := range []int{24, 80, 120} {
		m := sized(t, w, 24)
		m.App.Reg.Root = "/home/someone/作業/" + strings.Repeat("とても長いディレクトリの名前/", 5) + "プロジェクト"
		h := fits(t, m.header(), w)[0]
		if want := m.App.Reg.Sandbox.Mode() + " · " + m.App.Reg.Policy.Mode(); !strings.HasSuffix(h, want) {
			t.Errorf("%d columns: the header lost %q: %q", w, want, h)
		}
	}
	m := sized(t, 80, 24)
	m.comp = &completion{file: true, items: []compItem{
		{name: "docs/設計書/アーキテクチャ概要.md", desc: "12 KB"},
		{name: "src/main.go", desc: "3 KB"},
		{name: "画像/スクリーンショット🎉.png", desc: "88 KB"},
	}}
	cols := map[int]bool{}
	for _, r := range fits(t, m.compView(), 80) {
		if i := strings.Index(r, " KB"); i > 0 {
			d := strings.LastIndex(r[:i], " ")
			cols[lipgloss.Width(r[:d+1])] = true
		}
	}
	if len(cols) != 1 {
		t.Errorf("@ picker: sizes start in %d columns: %v\n%s", len(cols), cols, stripANSI(m.compView()))
	}
	// short wide names fit the column whole: it is sized in columns, not runes
	m.comp = &completion{file: true, items: []compItem{{name: "設計書.md", desc: "1 KB"}, {name: "a.go", desc: "1 KB"}}}
	if v := stripANSI(m.compView()); !strings.Contains(v, "@設計書.md ") {
		t.Errorf("@ picker cut a name that fits:\n%s", v)
	}
}
