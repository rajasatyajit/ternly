package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func paletteItems(m *Model) []string {
	var n []string
	for _, it := range m.comp.items {
		n = append(n, it.name)
	}
	return n
}

// Each row shows, at its right end, the keys that do the same; every row
// fits; where the description would lose its room, the keys go.
func TestPaletteKeysColumn(t *testing.T) {
	for _, s := range [][2]int{{120, 40}, {80, 24}} {
		m := sized(t, s[0], s[1])
		key(m, "ctrl+k")
		typeText(m, "panel")
		rows := fits(t, m.compView(), s[0])
		found := 0
		for _, r := range rows {
			if strings.Contains(r, "/panel ") && strings.HasSuffix(strings.TrimRight(r, " "), "Ctrl+X "+r[strings.LastIndex(r, " ")+1:]) {
				found++
				if w := lipgloss.Width(r); w != s[0]-1 {
					t.Errorf("%dx%d: a keyed row ends at column %d, not the right edge %d: %q", s[0], s[1], w, s[0]-1, r)
				}
			}
		}
		if found == 0 {
			t.Errorf("%dx%d: no row shows its keys:\n%s", s[0], s[1], strings.Join(rows, "\n"))
		}
	}
	m := sized(t, 40, 20) // too narrow: no keys, the description keeps its room
	key(m, "ctrl+k")
	typeText(m, "panel")
	if v := strings.Join(fits(t, m.compView(), 40), "\n"); strings.Contains(v, "Ctrl+X") {
		t.Errorf("40 columns: keys crowd the descriptions:\n%s", v)
	}
}

// Three letters or more also match descriptions, after the name matches.
func TestPaletteMatchesDescriptions(t *testing.T) {
	m := sized(t, 120, 40)
	key(m, "ctrl+k")
	typeText(m, "diff")
	names := paletteItems(m)
	if len(names) == 0 || names[0] != "diff" {
		t.Fatalf("a name match comes first: %v", names)
	}
	if !strings.Contains(strings.Join(names, ","), "panel changes") {
		t.Errorf("the changes panel (\"…the working tree's diff\") isn't listed: %v", names)
	}
	m.ta.Reset()
	m.comp = nil
	key(m, "ctrl+k")
	typeText(m, "zq")
	if m.comp != nil {
		t.Errorf("two letters matched descriptions: %v", paletteItems(m))
	}
}

// With nothing typed, the recently used come first, latest first.
func TestPaletteRecentFirst(t *testing.T) {
	m := sized(t, 120, 40)
	m.dispatch("/output")
	m.dispatch("/panel error")
	m.dispatch("/panel error") // once in the list
	m.dispatch("/sidebar")
	key(m, "ctrl+k")
	names := paletteItems(m)
	if len(names) < 3 || names[0] != "sidebar" || names[1] != "panel error" || names[2] != "output" {
		t.Fatalf("recent first: %v", names[:min(5, len(names))])
	}
	m.dispatch("/panel nosuch") // not a panel: not recorded
	if m.recent[0] != "sidebar" {
		t.Errorf("an unknown panel was recorded: %v", m.recent)
	}
}

// Two letters don't match descriptions: a pair found in a description but
// in no name stays out of the list.
func TestPaletteShortQueryNamesOnly(t *testing.T) {
	m := sized(t, 120, 40)
	names := []string{}
	descs := ""
	for _, c := range builtins {
		names = append(names, c.name)
		names = append(names, c.aliases...)
		descs += " " + strings.ToLower(c.desc)
	}
	for _, d := range panelDescs {
		descs += " " + strings.ToLower(d)
	}
	for k := range panelTitles {
		names = append(names, "panel "+string(k), string(k))
	}
	q := ""
	for a := 'a'; a <= 'z' && q == ""; a++ {
		for b := 'a'; b <= 'z'; b++ {
			p := string([]rune{a, b})
			if !strings.Contains(descs, p) {
				continue
			}
			hit := false
			for _, n := range names {
				if fuzzyScore(p, n) >= 0 {
					hit = true
					break
				}
			}
			if !hit {
				q = p
				break
			}
		}
	}
	if q == "" {
		t.Skip("no two letters in a description that no name has")
	}
	key(m, "ctrl+k")
	typeText(m, q)
	if m.comp != nil {
		t.Errorf("%q (two letters) matched descriptions: %v", q, paletteItems(m))
	}
}

// /output and /sidebar show the keys that do the same.
func TestPaletteCommandKeys(t *testing.T) {
	m := sized(t, 120, 40)
	key(m, "ctrl+k")
	keys := map[string]string{}
	for _, it := range m.comp.items {
		keys[it.name] = it.keys
	}
	if keys["output"] != "Ctrl+O · Ctrl+X o" || keys["sidebar"] != "Ctrl+B · Ctrl+X s" {
		t.Fatalf("keys: output %q, sidebar %q", keys["output"], keys["sidebar"])
	}
}

// The recent list holds each command once, five at most.
func TestRecentDistinct(t *testing.T) {
	m := sized(t, 120, 40)
	for _, c := range []string{"/output", "/why", "/output", "/panel error"} {
		m.dispatch(c)
	}
	seen := map[string]bool{}
	for _, r := range m.recent {
		if seen[r] {
			t.Fatalf("%q twice: %v", r, m.recent)
		}
		seen[r] = true
	}
	if strings.Join(m.recent, ",") != "panel error,output,why" {
		t.Fatalf("recent: %v", m.recent)
	}
	for _, c := range []string{"/cost", "/status", "/sidebar", "/panel plan"} {
		m.dispatch(c)
	}
	if len(m.recent) != 5 {
		t.Fatalf("more than five: %v", m.recent)
	}
}
