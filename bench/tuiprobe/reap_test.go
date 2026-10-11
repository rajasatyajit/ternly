package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// spawnDaemon starts `sleep 60` in its own session (it leaves our process
// group, like Codex's app-server) with the given HOME.
func spawnDaemon(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "60")
	c.Env = append(os.Environ(), "HOME="+home)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Wait() }()
	return c
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestReapByHome(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	d := spawnDaemon(t, home)
	keep := spawnDaemon(t, other)
	t.Cleanup(func() { _ = keep.Process.Kill() })
	sig, surv := reap(home, 2*time.Second)
	if len(sig) != 1 || sig[0] != d.Process.Pid || len(surv) != 0 {
		t.Fatalf("reaped %v, survivors %v; want [%d], none", sig, surv, d.Process.Pid)
	}
	time.Sleep(100 * time.Millisecond)
	if alive(d.Process.Pid) {
		t.Fatal("the daemon with the run's HOME is still alive")
	}
	if !alive(keep.Process.Pid) {
		t.Fatal("a process with another HOME was stopped")
	}
	if s, _ := reap(home, time.Second); len(s) != 0 {
		t.Fatalf("second reap found %v", s)
	}
}

// The end-to-end run reaps a daemon the program leaves behind.
func TestProbeReapsLeftovers(t *testing.T) {
	home := t.TempDir()
	exe, _ := os.Executable()
	res, err := probeRun([]string{exe}, 80, 24, "TUIPROBE_FAKE=1,TUIPROBE_DAEMON=1,HOME="+home, false, "", "stable 300 10000", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reaped) == 0 || len(res.Survivors) != 0 {
		t.Fatalf("reaped %v, survivors %v", res.Reaped, res.Survivors)
	}
	if left := withHome(home); len(left) != 0 {
		t.Fatalf("processes with the run's HOME remain: %v", left)
	}
}

func TestHomeOf(t *testing.T) {
	if h := homeOf("A=1,HOME=/x/y,B=2"); h != "/x/y" {
		t.Fatalf("homeOf = %q", h)
	}
	if homeOf("A=1") != "" {
		t.Fatal("no HOME expected")
	}
}

// A daemon that ignores SIGTERM is killed after the grace period.
func TestReapKillsTermIgnorer(t *testing.T) {
	home := t.TempDir()
	c := exec.Command("sh", "-c", `trap "" TERM; sleep 60 & wait`)
	c.Env = append(os.Environ(), "HOME="+home)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Wait() }()
	time.Sleep(200 * time.Millisecond)
	if _, surv := reap(home, 500*time.Millisecond); len(surv) != 0 {
		t.Fatalf("survivors after SIGKILL: %v", surv)
	}
	time.Sleep(100 * time.Millisecond)
	if alive(c.Process.Pid) {
		t.Fatal("the TERM-ignoring daemon is still alive")
	}
}
