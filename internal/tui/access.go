package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Access settings (ADR 023): reduced motion (no shimmer, spinner or cursor
// blink, so nothing redraws on a timer) and a screen-reader mode (also
// words for states instead of glyphs). NO_COLOR is honoured by the colour
// profile; mouse capture is opt-in because it takes over text selection.
type Access struct {
	ReducedMotion bool // TERNLY_REDUCED_MOTION=1
	ScreenReader  bool // TERNLY_SCREEN_READER=1 (implies ReducedMotion)
	Mouse         bool // TERNLY_MOUSE=1: wheel scrolls the transcript
}

// AccessFromEnv reads the settings from the environment.
func AccessFromEnv() Access {
	on := func(k string) bool { v := os.Getenv(k); return v != "" && v != "0" && v != "false" }
	a := Access{ReducedMotion: on("TERNLY_REDUCED_MOTION"), ScreenReader: on("TERNLY_SCREEN_READER"), Mouse: on("TERNLY_MOUSE")}
	if a.ScreenReader {
		a.ReducedMotion = true
	}
	return a
}

// toolIcon is a tool block's state, as a glyph or (screen reader) a word.
func (m *Model) toolIcon(state int) string {
	if m.App.Access.ScreenReader {
		return map[int]string{0: "[running]", 1: sOK.Render("[done]"), 2: sErr.Render("[failed]"), 3: sWarn.Render("[unverified]")}[state]
	}
	switch state {
	case 1:
		return sOK.Render("●")
	case 2:
		return sErr.Render("●")
	case 3: // unverified: neither ✓ nor a failure
		return sWarn.Render("?")
	}
	if m.App.Access.ReducedMotion || m.perm != nil || m.review != nil { // still while the user decides
		return sDim.Render("◌")
	}
	return spinFrames[m.frame%len(spinFrames)]
}

// moving renders text that would shimmer; static under reduced motion.
func (m *Model) moving(text string) string {
	if m.App.Access.ReducedMotion {
		return sAccent.Render(text)
	}
	return shine(text, m.frame)
}

// linear: the accessible mode (ADR 023), inside the same renderer — no
// alternate screen; finished blocks are printed once into the terminal's
// scrollback (a screen reader reads them once, in order) and the live frame
// holds only what still changes plus a plain "> " input line; no box
// drawing, ASCII markdown, words for states, nothing animates.
func (m *Model) linear() bool { return m.App.Access.ScreenReader }

// mdStyle is the markdown style: ASCII in the accessible mode.
func (m *Model) mdStyle() string {
	if m.linear() {
		return "ascii"
	}
	return m.style
}

// final: the block won't change again (it can go to the scrollback).
func (m *Model) final(i int) bool {
	b := m.blocks[i]
	switch {
	case i == 0 && m.discovering: // the welcome block is rewritten once discovery ends
		return false
	case b.kind == bTool && b.state == 0:
		return false
	case b.kind == bAssistant && m.busy && i == len(m.blocks)-1: // still streaming
		return false
	}
	return true
}

// flushLinear prints the blocks that became final since the last call.
func (m *Model) flushLinear() tea.Cmd {
	if !m.linear() || !m.ready {
		return nil
	}
	m.printed = min(m.printed, len(m.blocks)) // the transcript was cleared or replaced
	k := m.printed
	for k < len(m.blocks) && m.final(k) {
		k++
	}
	if k == m.printed {
		return nil
	}
	var parts []string
	for _, b := range m.blocks[m.printed:k] {
		if b.rendered == "" {
			b.rendered = m.renderBlock(b)
		}
		parts = append(parts, b.rendered)
	}
	m.printed = k
	text := safeFrame(strings.Join(parts, "\n"))
	return tea.Println(text)
}

// renderLinear is the live frame in the accessible mode; the second value is
// the row of the input line.
func (m *Model) renderLinear() (string, int) {
	var sb strings.Builder
	for _, b := range m.blocks[min(m.printed, len(m.blocks)):] {
		if b.rendered == "" || b.kind == bTool && b.state == 0 {
			b.rendered = m.renderBlock(b)
		}
		sb.WriteString(b.rendered + "\n")
	}
	switch {
	case m.busy && (m.perm != nil || m.review != nil):
		sb.WriteString("Waiting for your answer.\n")
	case m.busy:
		sb.WriteString(m.activity + "…  (Esc interrupts)\n")
	}
	for _, v := range []string{m.permViewPlain(), m.reviewViewPlain()} {
		if v != "" {
			sb.WriteString(v + "\n")
		}
	}
	if m.picker != nil {
		sb.WriteString(m.pickerView() + "\n")
	}
	if m.comp != nil {
		sb.WriteString(m.compView() + "\n")
	}
	row := strings.Count(sb.String(), "\n")
	sb.WriteString("> " + m.ta.View() + "\n")
	sb.WriteString(m.statusBar())
	return sb.String(), row
}
