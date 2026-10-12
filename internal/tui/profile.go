package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Frame-rate profiles (ADR 030, step 6). Bubble Tea writes a frame only on
// its ticker and can't change the rate once running, so ternly picks it at
// startup from the environment:
//
//   - full, 120 fps: on AC power, in a local terminal, on a capable CPU. The
//     fastest echo (ADR 032); its idle cost waits for upstream (bubbletea#1858).
//   - low-power, LowPowerFPS: on battery, or on a CPU with few cores or a low
//     clock. Its idle budget (≤ 0.5 %) isn't met on every CPU yet: it waits
//     for upstream (bubbletea#1858), as the full profile's does (ADR 030).
//   - remote, RemoteFPS: over SSH; fewer frames and bytes on the link, and no
//     spinner animation.
//
// ui.fps in config.json overrides the choice; /doctor shows it and why.
const (
	FullFPS     = FPS
	LowPowerFPS = 30
	RemoteFPS   = 30
)

// FrameProfile is the chosen frame rate and why.
type FrameProfile struct {
	Name string // "full", "low-power", "remote" or "configured"
	FPS  int
	Why  string
}

// Env is what the choice reads.
type Env struct {
	SSH        bool // SSH_CONNECTION or SSH_TTY
	OnBattery  bool // discharging, with no mains power online
	PowerKnown bool // the power source could be read
	CPUs       int  // logical CPUs
	MaxMHz     int  // the CPU's top clock (0: unknown)
}

// Minimums for a capable CPU.
const (
	capableCPUs = 4
	capableMHz  = 2000
)

// ChooseProfile picks the frame rate for e; fps > 0 is ui.fps.
func ChooseProfile(e Env, fps int) FrameProfile {
	if fps > 0 {
		return FrameProfile{"configured", min(max(fps, 1), FPS), fmt.Sprintf("ui.fps is %d in config.json", fps)}
	}
	switch {
	case e.SSH:
		return FrameProfile{"remote", RemoteFPS, "over SSH: fewer frames and bytes on the link"}
	case e.OnBattery:
		return FrameProfile{"low-power", LowPowerFPS, "on battery"}
	case e.CPUs > 0 && e.CPUs < capableCPUs:
		return FrameProfile{"low-power", LowPowerFPS, fmt.Sprintf("%d CPUs (fewer than %d)", e.CPUs, capableCPUs)}
	case e.MaxMHz > 0 && e.MaxMHz < capableMHz:
		return FrameProfile{"low-power", LowPowerFPS, fmt.Sprintf("a %d MHz CPU (under %d)", e.MaxMHz, capableMHz)}
	}
	power := "on AC power"
	if !e.PowerKnown {
		power = "power source unknown, taken as AC"
	}
	return FrameProfile{"full", FullFPS, fmt.Sprintf("%s, a local terminal, %d CPUs", power, e.CPUs)}
}

// DetectEnv reads the environment: SSH from the environment, the power
// source from /sys (Linux) or pmset (macOS, 300 ms at most), the CPU from
// the runtime and cpufreq.
func DetectEnv() Env {
	e := Env{SSH: os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "", CPUs: runtime.NumCPU()}
	switch runtime.GOOS {
	case "linux":
		e.OnBattery, e.PowerKnown = linuxPower("/sys/class/power_supply")
		if b, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); err == nil {
			if khz, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				e.MaxMHz = khz / 1000
			}
		}
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output(); err == nil {
			e.OnBattery, e.PowerKnown = strings.Contains(string(out), "'Battery Power'"), true
		}
	}
	return e
}

// linuxPower reads the power supplies under dir: on battery when a battery
// is discharging and no mains supply is online. A machine without supplies
// (a desktop, a VM) runs on mains.
func linuxPower(dir string) (onBattery, known bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	read := func(name, f string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name, f))
		return strings.TrimSpace(string(b))
	}
	mains, discharging := false, false
	for _, d := range ents {
		switch read(d.Name(), "type") {
		case "Mains", "USB":
			if read(d.Name(), "online") == "1" {
				mains = true
			}
		case "Battery":
			if read(d.Name(), "status") == "Discharging" {
				discharging = true
			}
		}
	}
	return discharging && !mains, true
}

// EchoWarning is what /doctor says when the frame rate makes typing slow to
// show: a key appears at the next frame, so below 30 fps it can wait up to a
// whole frame interval, half of one on average.
func (f FrameProfile) EchoWarning() string {
	if f.FPS <= 0 || f.FPS >= 30 {
		return ""
	}
	frame := 1000 / f.FPS
	return fmt.Sprintf("at %d fps a typed key shows within %d ms (about %d ms on average); 30 or more keeps it under %d ms", f.FPS, frame, frame/2, 1000/30)
}
