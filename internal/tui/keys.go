package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Keys (ADR 030, decision 2): a leader key, then a letter, opens a panel or
// runs an action, and while the leader waits a which-key popup lists the
// letters. The leader is configurable (config.json "keys": {"leader": …});
// the default is Ctrl+X. Never bound: Ctrl+D, R, S, Q and Z (EOF, reverse
// search, flow control, quit and suspend in shells and terminals), nor keys
// that type or edit. Where the terminal disambiguates keys (the kitty
// keyboard protocol), Alt+letter does the same directly; on other terminals
// Alt+x can't be told from Esc then x, so there it does nothing. Letters the
// input uses with Alt for word editing (b, c, d, f, l, u) have no direct key.
// The Ctrl+K palette lists every panel with its keys.

const DefaultLeader = "ctrl+x"

// reservedKeys can't be the leader: shell and terminal control keys, keys
// that type, edit or move, and ternly's own direct bindings.
var reservedKeys = map[string]string{
	"ctrl+d": "EOF in shells", "ctrl+r": "reverse search in shells", "ctrl+s": "flow control (XOFF)",
	"ctrl+q": "flow control (XON)", "ctrl+z": "suspend", "ctrl+c": "interrupt and exit", "ctrl+j": "a newline",
	"ctrl+m": "Enter", "ctrl+i": "Tab", "ctrl+h": "Backspace", "ctrl+[": "Esc",
	"ctrl+k": "the palette", "ctrl+o": "tool output", "ctrl+b": "sessions", "ctrl+l": "clear",
	"ctrl+p": "previous in lists", "ctrl+n": "next in lists", "ctrl+a": "start of line", "ctrl+e": "end of line",
	"ctrl+u": "clear the input", "ctrl+w": "delete a word", "ctrl+f": "forward a character",
	"ctrl+g": "select all", "ctrl+t": "transpose", "ctrl+v": "paste",
	"alt+b": "word back", "alt+f": "word forward", "alt+d": "delete a word", "alt+c": "capitalize a word",
	"alt+l": "lowercase a word", "alt+u": "uppercase a word",
	"enter": "send", "esc": "clear and interrupt", "tab": "complete", "space": "a space", "backspace": "delete",
}

// ParseLeader checks a configured leader: "" gives the default; a reserved
// or unusable key gives the default and a note saying why.
func ParseLeader(s string) (key, note string) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return DefaultLeader, ""
	}
	if why, ok := reservedKeys[s]; ok {
		return DefaultLeader, fmt.Sprintf("keys.leader %q is %s: using %s", s, why, DefaultLeader)
	}
	mod, k, ok := strings.Cut(s, "+")
	valid := ok && (mod == "ctrl" || mod == "alt") && len(k) == 1 && (k[0] >= 'a' && k[0] <= 'z' || k[0] >= '0' && k[0] <= '9') ||
		len(s) >= 2 && len(s) <= 3 && s[0] == 'f' && s[1] >= '1' && s[1] <= '9' && (len(s) == 2 || s[2] >= '0' && s[2] <= '2')
	if !valid {
		return DefaultLeader, fmt.Sprintf("keys.leader %q isn't a Ctrl+letter, Alt+letter or F-key: using %s", s, DefaultLeader)
	}
	return s, ""
}

// keyAction is what a letter after the leader does.
type keyAction struct {
	key, label string
	panel      panelKind // "" for an action that isn't a panel
	run        func(m *Model) tea.Cmd
}

// keyActions is filled in init: its actions refer to methods that read it.
var keyActions []keyAction

func init() {
	keyActions = []keyAction{
		{key: "a", label: "agents", panel: pAgents},
		{key: "c", label: "changes", panel: pChanges},
		{key: "e", label: "last error", panel: pError},
		{key: "p", label: "plan", panel: pPlan},
		{key: "r", label: "why (routing)", panel: pRouting},
		{key: "s", label: "sessions", run: func(m *Model) tea.Cmd { m.toggleSidebar(); return nil }},
		{key: "o", label: "tool output", run: func(m *Model) tea.Cmd { m.toggleExpanded(); return nil }},
		{key: "k", label: "commands", run: func(m *Model) tea.Cmd { m.openPalette(); return nil }},
	}
}

func actionFor(key string) *keyAction {
	for i := range keyActions {
		if keyActions[i].key == key {
			return &keyActions[i]
		}
	}
	return nil
}

func (m *Model) leader() string {
	if m.leaderKey == "" {
		return DefaultLeader
	}
	return m.leaderKey
}

// keyName is a key as the which-key popup and the palette show it: "ctrl+x"
// as "Ctrl+X".
func keyName(k string) string {
	parts := strings.Split(k, "+")
	for i, p := range parts {
		switch {
		case p == "ctrl" || p == "alt" || p == "shift":
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		default:
			parts[i] = strings.ToUpper(p)
		}
	}
	return strings.Join(parts, "+")
}

// actionKeys is how to reach an action: "Ctrl+X a", and "Alt+A" where the
// terminal tells it apart.
func (m *Model) actionKeys(a *keyAction) string {
	s := keyName(m.leader()) + " " + a.key
	if m.kbdEnhanced && directOK(a.key) {
		s += " · Alt+" + strings.ToUpper(a.key)
	}
	return s
}

// leaderKeyPress handles the leader and the key after it; handled is false
// for keys it leaves to the rest of onKey.
func (m *Model) leaderKeyPress(k tea.KeyPressMsg) (tea.Cmd, bool) {
	s := k.String()
	if m.leaderWait {
		m.leaderWait = false
		m.layout()
		if a := actionFor(s); a != nil {
			return m.runAction(a), true
		}
		if s != "esc" && s != m.leader() {
			m.addInfo(sDim.Render(fmt.Sprintf("  %s %s does nothing — the popup lists the keys", keyName(m.leader()), s)))
		}
		return nil, true
	}
	if s == m.leader() {
		if m.linear() { // no popup in the linear mode: the keys, as text
			var b []string
			for i := range keyActions {
				b = append(b, keyActions[i].key+" "+keyActions[i].label)
			}
			m.addInfo("  " + keyName(m.leader()) + ", then: " + strings.Join(b, ", "))
		}
		m.leaderWait = true
		m.layout()
		return nil, true
	}
	if m.kbdEnhanced {
		if l, ok := strings.CutPrefix(s, "alt+"); ok && directOK(l) {
			if a := actionFor(l); a != nil {
				return m.runAction(a), true
			}
		}
	}
	return nil, false
}

func (m *Model) runAction(a *keyAction) tea.Cmd {
	if a.panel != "" {
		return m.togglePanel(a.panel)
	}
	return a.run(m)
}

// whichKeyRows is the popup's height (0 while the leader isn't waiting).
func (m *Model) whichKeyRows() int {
	if !m.leaderWait || m.linear() {
		return 0
	}
	return (len(keyActions)+1)/2 + 3 // two columns, the title, two borders
}

// whichKeyView lists the letters after the leader, two columns.
func (m *Model) whichKeyView() string {
	half := (len(keyActions) + 1) / 2
	var rows []string
	rows = append(rows, sAccent.Render(keyName(m.leader()))+sDim.Render(" then a key · esc to cancel"))
	for i := range half {
		row := whichKeyCell(&keyActions[i], 24)
		if j := i + half; j < len(keyActions) {
			row += whichKeyCell(&keyActions[j], 24)
		}
		rows = append(rows, row)
	}
	border := lipgloss.RoundedBorder()
	if m.App.Access.ASCII {
		border = lipgloss.ASCIIBorder()
	}
	return lipgloss.NewStyle().Border(border).BorderForeground(cFaint).Padding(0, 1).MarginLeft(1).Render(strings.Join(rows, "\n"))
}

func whichKeyCell(a *keyAction, w int) string {
	cell := sAccent.Render(a.key) + " " + a.label
	return cell + strings.Repeat(" ", max(1, w-lipgloss.Width(cell)))
}

func (m *Model) toggleExpanded() {
	m.expanded = !m.expanded
	for _, b := range m.blocks {
		if b.kind == bTool {
			b.rendered = ""
		}
	}
	m.refresh(false)
}

func (m *Model) openPalette() {
	if m.ta.Value() == "" {
		m.ta.SetValue("/")
		m.ta.CursorEnd()
		m.updateCompletion()
	}
}

// directOK: Alt+letter can go straight to an action (the input doesn't use it
// for word editing).
func directOK(letter string) bool { return len(letter) == 1 && !strings.Contains("bcdflu", letter) }
