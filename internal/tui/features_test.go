package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
)

func key(m *Model, s string) tea.Cmd {
	var k tea.KeyPressMsg
	switch s {
	case "ctrl+k":
		k = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	case "ctrl+o":
		k = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	case "tab":
		k = tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		r := []rune(s)[0]
		k = tea.KeyPressMsg{Code: r, Text: s}
	}
	_, c := m.Update(k)
	return c
}

func typeText(m *Model, s string) {
	for _, r := range s {
		key(m, string(r))
	}
}

// Ctrl+K opens every command, scrollable; typing filters it.
func TestPalette(t *testing.T) {
	m := testModel(t)
	key(m, "ctrl+k")
	if m.comp == nil || len(m.comp.items) != len(builtins) {
		t.Fatalf("palette: %v items, want all %d commands", m.comp, len(builtins))
	}
	for range 10 {
		key(m, "down")
	}
	if v := m.View().Content; !strings.Contains(v, "11/") || !strings.Contains(v, "/"+m.comp.items[10].name) {
		t.Fatalf("the palette should scroll to the selection:\n%s", v)
	}
	typeText(m, "them")
	if m.comp == nil || m.comp.items[0].name != "theme" {
		t.Fatalf("typing should filter: %+v", m.comp)
	}
}

// @ lists workspace files through the confined glob tool; Tab inserts one.
func TestFilePicker(t *testing.T) {
	m := testModel(t)
	root := m.App.Reg.Root
	for _, f := range []string{"internal/router/route.go", "internal/router/route_test.go", "cmd/main.go", "README.md"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		_ = os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644)
	}
	typeText(m, "fix @rou")
	c := key(m, "t") // "@rout"
	msg := runAll(c)
	if msg == nil {
		t.Fatal("no file listing requested")
	}
	m.Update(msg)
	if m.comp == nil || !m.comp.file || !strings.HasPrefix(m.comp.items[0].name, "internal/router/route") {
		t.Fatalf("picker: %+v", m.comp)
	}
	key(m, "tab")
	if v := m.ta.Value(); !strings.HasPrefix(v, "fix @internal/router/route") || !strings.HasSuffix(v, " ") {
		t.Fatalf("inserted %q", v)
	}
}

// runAll runs a (batched) command until it yields a filesMsg.
func runAll(c tea.Cmd) tea.Msg {
	if c == nil {
		return nil
	}
	switch msg := c().(type) {
	case tea.BatchMsg:
		for _, cc := range msg {
			if r := runAll(cc); r != nil {
				return r
			}
		}
	case filesMsg:
		return msg
	}
	return nil
}

// Ctrl+O shows tool output in full; collapsed, the transcript says how much is hidden.
func TestCollapsibleToolOutput(t *testing.T) {
	m := testModel(t)
	out := strings.Repeat("line\n", 40) + "LAST"
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "bash", ToolID: "1", Text: "go test"}))
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolEnd, Tool: "bash", ToolID: "1", OK: true, Text: out}))
	if v := m.View().Content; strings.Contains(v, "LAST") || !strings.Contains(v, "+39 lines · ctrl+o expands") {
		t.Fatalf("collapsed:\n%s", v)
	}
	key(m, "ctrl+o")
	if v := m.View().Content; !strings.Contains(v, "LAST") {
		t.Fatalf("expanded:\n%s", v)
	}
}

// The terminal's own cursor sits where the next character goes (no blink
// redraws); under reduced motion it doesn't blink.
func TestRealCursor(t *testing.T) {
	m := testModel(t)
	typeText(m, "hello")
	v := m.View()
	if v.Cursor == nil {
		t.Fatal("no terminal cursor")
	}
	row := strings.Split(v.Content, "\n")[v.Cursor.Y]
	if !strings.Contains(row, "hello") || !v.Cursor.Blink {
		t.Fatalf("cursor at %+v on %q", v.Cursor.Position, row)
	}
	plain := []rune(stripANSI(row))
	if v.Cursor.X != len([]rune(strings.SplitN(string(plain), "hello", 2)[0]))+5 {
		t.Fatalf("cursor x %d on %q", v.Cursor.X, string(plain))
	}
	m.App.Access.ReducedMotion = true
	if m.View().Cursor.Blink {
		t.Fatal("reduced motion: the cursor still blinks")
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i++; i < len(s) && (s[i] < 0x40 || s[i] > 0x7e || s[i] == '['); i++ {
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Reduced motion: a running turn doesn't start the ticker, and nothing shimmers.
func TestReducedMotion(t *testing.T) {
	m := testModel(t)
	m.App.Access = Access{ReducedMotion: true, ScreenReader: true}
	m.discovering, m.ticking = false, false
	m.busy, m.activity = true, "Thinking"
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "bash", ToolID: "1", Text: "ls"}))
	if m.ticking {
		t.Fatal("reduced motion: the ticker started")
	}
	v := m.View().Content
	if !strings.Contains(v, "[running]") || strings.ContainsAny(v, strings.Join(spinFrames, "")) {
		t.Fatalf("screen reader: want words, not spinner glyphs:\n%s", v)
	}
	m.Update(tickMsg(time.Now()))
	if m.ticking {
		t.Fatal("reduced motion: a tick rescheduled")
	}
}

// Regaining focus re-reads the background (GNOME light/dark), unless the
// user fixed the theme; the wheel scrolls when mouse support is on.
func TestFocusAndWheel(t *testing.T) {
	m := testModel(t)
	m.themeSet = false // following the terminal
	if _, c := m.Update(tea.FocusMsg{}); c == nil {
		t.Fatal("focus: no background request")
	}
	m.themeSet = true
	if _, c := m.Update(tea.FocusMsg{}); c != nil && runAll(c) != nil {
		t.Fatal("a fixed theme shouldn't be re-detected")
	}
	for range 200 {
		m.addInfo("x")
	}
	top := m.vp.top
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.vp.top != top-3 || m.vp.follow {
		t.Fatalf("wheel up: top %d → %d", top, m.vp.top)
	}
	m.App.Access.Mouse = true
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse support on: no mouse mode")
	}
}

// The @ picker's listing arrives asynchronously: on a loaded machine it can
// open between typing a complete path and pressing Enter. Enter then must
// send the prompt, not swallow the keystroke by inserting the same path
// again (main's CI: TestTUIArchitectTestMention timed out under load).
func TestEnterSubmitsWhenPickerOpensLate(t *testing.T) {
	m := testModel(t)
	root := m.App.Reg.Root
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	m.discovering = false
	typeText(m, "what is in @a.go")
	m.Update(filesMsg{q: "a.go", hits: []string{"a.go"}}) // the listing lands now
	if m.comp == nil || !m.comp.file {
		t.Fatal("test premise: the picker should be open")
	}
	_, c := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.ta.Value() != "" || c == nil {
		t.Fatalf("Enter didn't send the prompt; input %q", m.ta.Value())
	}
	if m.comp != nil {
		t.Fatal("the picker stayed open after sending")
	}
	// a partial token still picks the file (Enter inserts, as before)
	typeText(m, "and @a")
	m.Update(filesMsg{q: "a", hits: []string{"a.go", "b/a.txt"}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v := m.ta.Value(); v != "and @a.go " {
		t.Fatalf("a partial token: %q", v)
	}
}
