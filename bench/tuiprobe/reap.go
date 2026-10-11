package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ancestors returns pid and every process above it.
func ancestors(pid int) map[int]bool {
	out := map[int]bool{}
	for pid > 1 && !out[pid] {
		out[pid] = true
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		s := string(b)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) < 2 {
			break
		}
		pid, _ = strconv.Atoi(f[1])
	}
	return out
}

// withHome lists the processes whose environment has exactly HOME=home,
// other than this process and its ancestors. Daemons a harness starts keep
// the run's unique HOME even when they leave its process group, so this
// finds them without matching command lines.
func withHome(home string) []int {
	if home == "" {
		return nil
	}
	skip := ancestors(os.Getpid())
	want := []byte("HOME=" + home)
	ents, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || skip[pid] {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(b, []byte{0}) {
			if bytes.Equal(kv, want) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

// reap stops every process with HOME=home: SIGTERM, then SIGKILL after
// grace; it returns the pids signalled and those still alive afterwards.
func reap(home string, grace time.Duration) (signalled, survivors []int) {
	signalled = withHome(home)
	if len(signalled) == 0 {
		return nil, nil
	}
	for _, p := range signalled {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && len(withHome(home)) > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	for _, p := range withHome(home) { // still alive after the grace: SIGKILL
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	time.Sleep(200 * time.Millisecond)
	return signalled, withHome(home)
}

// homeOf returns the HOME an env spec sets ("" if none).
func homeOf(spec string) string {
	for _, kv := range strings.Split(spec, ",") {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			return v
		}
	}
	return ""
}
