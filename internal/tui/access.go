package tui

import "os"

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
