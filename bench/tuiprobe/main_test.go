package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A fake TUI for the end-to-end test: the test binary re-executed with
// TUIPROBE_FAKE=1. It enters the alternate screen, paints in a synchronized
// update, then echoes each key in colour (raw mode), like a minimal TUI.
func TestMain(m *testing.M) {
	if os.Getenv("TUIPROBE_FAKE") == "1" {
		fake()
		return
	}
	os.Exit(m.Run())
}

func fake() {
	if os.Getenv("TUIPROBE_DAEMON") == "1" { // leave a daemon outside the process group, like Codex's app-server
		c := exec.Command("sleep", "60")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		_ = c.Start()
	}
	fd := int(os.Stdin.Fd())
	if t, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		t.Lflag &^= unix.ECHO | unix.ICANON
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, t)
	}
	time.Sleep(50 * time.Millisecond)
	buf := make([]byte, 1)
	sync := ""
	if os.Getenv("TUIPROBE_ASK2026") == "1" { // ask before painting, like Bubble Tea, and show the answer
		fmt.Print("\x1b[?2026$p")
		var reply []byte
		for len(reply) < 32 {
			if n, err := os.Stdin.Read(buf); err != nil || n == 0 {
				break
			}
			if buf[0] == 0x1b {
				continue
			}
			reply = append(reply, buf[0])
			if buf[0] == 'y' {
				break
			}
		}
		sync = " sync=" + string(reply)
	}
	fmt.Print("\x1b[?1049h\x1b[?2026h\x1b[2J\x1b[Hhello probe" + sync + "\x1b[?2026l")
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return
		}
		if buf[0] == 'q' {
			return
		}
		if buf[0] == 'x' { // echo the prompt, think silently, then a long reply
			fmt.Print("x")
			time.Sleep(1500 * time.Millisecond)
			fmt.Print(strings.Repeat("a", 3000))
			continue
		}
		fmt.Printf("\x1b[38;2;10;20;30m%c\x1b[0m", buf[0])
	}
}

func TestProbeEndToEnd(t *testing.T) {
	exe, _ := os.Executable()
	raw := filepath.Join(t.TempDir(), "r.gz")
	res, err := probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1", false, "", "stable 300 10000; snap start; keys abc; idle 1; rss; resize 60 20", raw)
	if err != nil {
		t.Fatal(err)
	}
	st := res.Steps[0]
	if !st.OK || st.MS <= 0 || st.FirstByteMS <= 0 || st.FirstByteMS > st.MS {
		t.Fatalf("startup step: %+v", st)
	}
	if !strings.Contains(res.Snaps["start"], "hello probe") {
		t.Fatalf("snapshot: %q", res.Snaps["start"])
	}
	k := res.Steps[2]
	if len(k.EchoMS) != 3 || len(k.KeyBytes) != 3 {
		t.Fatalf("keys: %+v", k)
	}
	for i, e := range k.EchoMS {
		if e < 0 || e > 500 || k.KeyBytes[i] < 1 {
			t.Fatalf("key %d: echo %v ms, %d bytes", i, e, k.KeyBytes[i])
		}
	}
	if res.Steps[3].CPUPercent < 0 || res.Steps[4].RSSKiB <= 0 {
		t.Fatalf("idle/rss: %+v %+v", res.Steps[3], res.Steps[4])
	}
	if !res.Tech.AltScreen || res.Tech.SyncOutput != 1 || res.Tech.FullClears != 1 || res.Tech.TrueColor != 3 {
		t.Fatalf("technique: %+v", res.Tech)
	}
	b, cs, err := readRaw(raw)
	if err != nil || len(b) != res.Bytes || len(cs) == 0 {
		t.Fatalf("raw: %d bytes (%v), %d chunks; want %d", len(b), err, len(cs), res.Bytes)
	}
	sum := 0
	for _, c := range cs {
		sum += c.N
	}
	if sum != len(b) {
		t.Fatalf("chunk index sums to %d, bytes %d", sum, len(b))
	}
}

func TestAnalyse(t *testing.T) {
	b := []byte("\x1b[?1049h\x1b[?2026h\x1b[?1002;1006h\x1b[?2004h\x1b[2J\x1b[38;5;12mx\x1b[1;31;42my\x1b[48;2;1;2;3mz\x1b[>1u é")
	got := analyse(b)
	want := Technique{AltScreen: true, SyncOutput: 1, FullClears: 1, TrueColor: 1, Color256: 1, Color16: 2, NonASCII: 2, KittyKbd: true, MouseReport: true, BracketPaste: true}
	if got != want {
		t.Fatalf("analyse:\n got  %+v\n want %+v", got, want)
	}
	if n := analyse([]byte("plain text\x1b[1mbold\x1b[0m")); n.Color16 != 0 || n.TrueColor != 0 || n.Color256 != 0 {
		t.Fatalf("no colour expected: %+v", n)
	}
}

func TestFrames(t *testing.T) {
	ms := time.Millisecond
	cs := []chunk{{0, 10}, {1 * ms, 5}, {10 * ms, 7}, {11 * ms, 3}, {30 * ms, 1}}
	got := frames(cs, 4*ms)
	if fmt.Sprint(got) != "[15 10 1]" {
		t.Fatalf("frames: %v", got)
	}
}

func TestBuildEnv(t *testing.T) {
	t.Setenv("TUIPROBE_X", "1")
	env := strings.Join(buildEnv("TUIPROBE_X=,TUIPROBE_Y=2,NO_COLOR=1", false), "\n")
	if strings.Contains(env, "TUIPROBE_X=") || !strings.Contains(env, "TUIPROBE_Y=2") || !strings.Contains(env, "NO_COLOR=1") {
		t.Fatalf("env: %s", env)
	}
}

func TestUntil(t *testing.T) {
	exe, _ := os.Executable()
	res, err := probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1", false, "", "until 10000 hello\\sprobe; until 300 never-shown", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Steps[0].OK || res.Steps[0].MS <= 0 {
		t.Fatalf("until (shown): %+v", res.Steps[0])
	}
	if res.Steps[1].OK || res.Steps[1].Note == "" {
		t.Fatalf("until (never shown): %+v", res.Steps[1])
	}
}

// A program that asks for synchronized output support gets "recognized,
// reset" (2), as from a modern terminal; with -sync-capable=false, "not
// recognized" (0).
func TestSyncCapableAnswer(t *testing.T) {
	exe, _ := os.Executable()
	for _, c := range []struct {
		capable bool
		want    string
	}{{true, "sync=[?2026;2$y"}, {false, "sync=[?2026;0$y"}} {
		syncCapable = c.capable
		res, err := probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1,TUIPROBE_ASK2026=1", false, "", "until 5000 sync=; stable 200 3000; snap s", "")
		syncCapable = true
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Snaps["s"], c.want) {
			t.Errorf("capable=%v: screen %q, want %q", c.capable, strings.TrimSpace(res.Snaps["s"]), c.want)
		}
	}
}

// stream with a minimum size waits through silent thinking for the answer;
// without one it stops at the first quiet gap.
func TestStreamMinBytes(t *testing.T) {
	exe, _ := os.Executable()
	res, err := probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1", false, "", "stable 300 5000; type x; stream 500 10000 2000", "")
	if err != nil {
		t.Fatal(err)
	}
	st := res.Steps[len(res.Steps)-1]
	if !st.OK || st.Bytes < 3000 {
		t.Fatalf("with a minimum: ok=%v bytes=%d note=%q", st.OK, st.Bytes, st.Note)
	}
	res, _ = probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1", false, "", "stable 300 5000; type x; stream 500 10000", "")
	if st := res.Steps[len(res.Steps)-1]; st.Bytes >= 3000 {
		t.Fatalf("without a minimum the step should stop before the answer (bytes=%d)", st.Bytes)
	}
}
