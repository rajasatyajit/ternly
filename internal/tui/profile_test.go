package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rajasatyajit/ternly/internal/agent"
)

func TestChooseProfile(t *testing.T) {
	desk := Env{PowerKnown: true, CPUs: 16, MaxMHz: 5000}
	for _, c := range []struct {
		name string
		env  Env
		fps  int
		want FrameProfile
	}{
		{"desktop", desk, 0, FrameProfile{Name: "full", FPS: 120}},
		{"power unknown", Env{CPUs: 8}, 0, FrameProfile{Name: "full", FPS: 120}},
		{"battery", Env{PowerKnown: true, OnBattery: true, CPUs: 16}, 0, FrameProfile{Name: "low-power", FPS: LowPowerFPS}},
		{"few cores", Env{PowerKnown: true, CPUs: 2}, 0, FrameProfile{Name: "low-power", FPS: LowPowerFPS}},
		{"slow clock", Env{PowerKnown: true, CPUs: 8, MaxMHz: 1500}, 0, FrameProfile{Name: "low-power", FPS: LowPowerFPS}},
		{"ssh", Env{SSH: true, CPUs: 16}, 0, FrameProfile{Name: "remote", FPS: RemoteFPS}},
		{"ssh on battery", Env{SSH: true, OnBattery: true, CPUs: 16}, 0, FrameProfile{Name: "remote", FPS: RemoteFPS}},
		{"configured", Env{SSH: true}, 90, FrameProfile{Name: "configured", FPS: 90}},
		{"configured too high", desk, 500, FrameProfile{Name: "configured", FPS: 120}},
	} {
		got := ChooseProfile(c.env, c.fps)
		if got.Name != c.want.Name || got.FPS != c.want.FPS || got.Why == "" {
			t.Errorf("%s: %+v, want %s at %d fps with a reason", c.name, got, c.want.Name, c.want.FPS)
		}
	}
	if got := ChooseProfile(Env{CPUs: 8}, 0); !strings.Contains(got.Why, "unknown") {
		t.Errorf("an unread power source isn't said: %q", got.Why)
	}
}

func TestLinuxPower(t *testing.T) {
	supply := func(dir, name string, files map[string]string) {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range files {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	laptop := t.TempDir()
	supply(laptop, "AC", map[string]string{"type": "Mains", "online": "0"})
	supply(laptop, "BAT0", map[string]string{"type": "Battery", "status": "Discharging"})
	if bat, known := linuxPower(laptop); !bat || !known {
		t.Errorf("unplugged laptop: battery %v known %v", bat, known)
	}
	supply(laptop, "AC", map[string]string{"type": "Mains", "online": "1"})
	if bat, _ := linuxPower(laptop); bat {
		t.Error("plugged in: on battery")
	}
	desktop := t.TempDir()
	if bat, known := linuxPower(desktop); bat || !known {
		t.Errorf("no supplies (a desktop): battery %v known %v", bat, known)
	}
	if _, known := linuxPower(filepath.Join(desktop, "missing")); known {
		t.Error("an unreadable /sys counted as known")
	}
}

// Over SSH the spinner stops (no animation on the link).
func TestRemoteProfileStopsAnimation(t *testing.T) {
	m := benchModel(t, 0)
	if m.App.Access.ReducedMotion {
		t.Fatal("reduced motion without a remote profile")
	}
	app := *m.App
	app.Frame = ChooseProfile(Env{SSH: true}, 0)
	if m2 := New(&app, true); !m2.App.Access.ReducedMotion {
		t.Fatal("remote profile: the spinner still animates")
	}
}

func TestProgramOptionsFPS(t *testing.T) {
	if n := len(ProgramOptions(0)); n != 1 {
		t.Fatalf("%d options", n)
	}
}

// /doctor names the frame rate, its profile and why.
func TestDoctorFrameRate(t *testing.T) {
	m := sized(t, 120, 40)
	m.App.Frame = ChooseProfile(Env{PowerKnown: true, OnBattery: true, CPUs: 8}, 0)
	msg := m.cmdDoctor("")()
	if s := stripANSI(string(msg.(infoMsg))); !strings.Contains(s, "30 fps, the low-power profile: on battery") {
		t.Fatalf("/doctor:\n%s", s)
	}
}

func TestASCIIFrame(t *testing.T) {
	for in, want := range map[string]string{
		"plain text, \x1b[1mbold\x1b[0m": "plain text, \x1b[1mbold\x1b[0m",
		"◆ m1 ▸ cheapest · ✓ · ⚠ 1":      "* m1 > cheapest . v . ! 1",
		"╭──╮\n│ab│\n╰──╯":               "+--+\n|ab|\n+--+",
		"日本 ok":                          "???? ok",
		"↑12 ↓3 … ↻ ⎿":                   "^12 v3 . ~ `",
	} {
		got := asciiFrame(in)
		if got != want {
			t.Errorf("asciiFrame(%q) = %q, want %q", in, got, want)
		}
		if ansi.StringWidth(got) != ansi.StringWidth(in) {
			t.Errorf("width changed: %q (%d) → %q (%d)", in, ansi.StringWidth(in), got, ansi.StringWidth(got))
		}
	}
}

// In the ASCII mode the whole frame is ASCII: the input box's corners, the
// popup, a panel, the status line, and text in other scripts.
func TestASCIIModeFrameIsASCII(t *testing.T) {
	for _, s := range [][2]int{{80, 24}, {200, 50}} {
		m := sized(t, s[0], s[1])
		m.App.Access.ASCII = true
		m.addInfo("  日本語 — “quoted” → done ✓")
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: lane(1, "lexer", "running", 3)})
		keyp(m, "ctrl+x")
		frames := []string{m.View().Content}
		keyp(m, "e")
		frames = append(frames, m.View().Content)
		for i, f := range frames {
			for j := 0; j < len(f); j++ {
				if f[j] >= 0x80 {
					t.Fatalf("%dx%d frame %d: a non-ASCII byte at %d: %q", s[0], s[1], i, j, stripANSI(f[max(0, j-40):min(len(f), j+20)]))
				}
			}
		}
		if v := stripANSI(frames[0]); !strings.Contains(v, "+--") || !strings.Contains(v, "??????") {
			t.Errorf("%dx%d: no ASCII box or stand-ins:\n%s", s[0], s[1], v)
		}
	}
}

// The linear mode announces each subagent's changes of state, not each step.
func TestLinearAnnouncesLanes(t *testing.T) {
	m := sized(t, 120, 40)
	m.App.Access.ScreenReader = true
	n0 := len(m.blocks)
	for _, s := range []*agent.SubState{lane(1, "lexer", "queued", 0), lane(1, "lexer", "running", 1), lane(1, "lexer", "running", 2), lane(1, "lexer", "done", 5)} {
		m.onAgent(agent.Event{Kind: agent.EvSubagent, Sub: s})
	}
	var got []string
	for _, b := range m.blocks[n0:] {
		got = append(got, stripANSI(b.text))
	}
	all := strings.Join(got, "\n")
	if len(got) != 3 || !strings.Contains(all, "[agent lexer] queued") || !strings.Contains(all, "[agent lexer] running: ollama/gemma4:latest, step 1, grep Resize") ||
		!strings.Contains(all, "[agent lexer] done: ollama/gemma4:latest, step 5, 1.2k tokens in, 12s") {
		t.Fatalf("announcements:\n%s", all)
	}
}

// Below 30 fps /doctor says how long a typed key can wait for its frame.
func TestDoctorEchoWarning(t *testing.T) {
	m := sized(t, 120, 40)
	m.App.Frame = ChooseProfile(Env{}, 10)
	s := stripANSI(string(m.cmdDoctor("")().(infoMsg)))
	if !strings.Contains(s, "at 10 fps a typed key shows within 100 ms (about 50 ms on average)") {
		t.Fatalf("/doctor at 10 fps:\n%s", s)
	}
	for _, fps := range []int{30, 120} {
		if w := ChooseProfile(Env{}, fps).EchoWarning(); w != "" {
			t.Errorf("%d fps warned: %q", fps, w)
		}
	}
}
