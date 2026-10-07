package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/session"
)

// End-to-end tests run the real ternly (run()) in a child process: the test
// binary re-executes itself with TERNLY_TEST_ARGS set.
func TestMain(m *testing.M) {
	if a := os.Getenv("TERNLY_TEST_ARGS"); a != "" {
		var args []string
		_ = json.Unmarshal([]byte(a), &args)
		if ph := os.Getenv("TERNLY_TEST_CRASH_PHASE"); ph != "" { // die like a crash between switch phases
			session.CrashHook = func(p string) {
				if p == ph {
					_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				}
			}
		}
		os.Args = append([]string{"ternly"}, args...)
		os.Exit(run())
	}
	sweepHomes()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { // a killed run (Ctrl+C, timeout) removes its homes first
		<-sig
		stopping.Store(true) // no new homes from tests still running
		stopChildren()
		removeOurHomes()
		os.Exit(2)
	}()
	os.Exit(m.Run())
}

const homePrefix = "ternly-e2e-home-"

var stopping atomic.Bool // set by the signal handler

// stopChildren ends this process's children (the ternly runs): SIGTERM first
// (ternly then cancels its background git and exits), SIGKILL after 5 s.
// They're found with pgrep -P, not from the tests' exec.Cmds (which other
// goroutines are still starting).
func stopChildren() {
	kids := func() []int {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
		var pids []int
		for _, f := range strings.Fields(string(out)) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
		return pids
	}
	for _, pid := range kids() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(kids()) > 0; {
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range kids() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// removeOurHomes removes every home named for this process, including any
// created while the handler ran.
func removeOurHomes() {
	ents, _ := os.ReadDir(homeRoot())
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), fmt.Sprintf("%s%d-", homePrefix, os.Getpid())) {
			_ = os.RemoveAll(filepath.Join(homeRoot(), e.Name()))
		}
	}
}

// homeRoot is where test homes go: $TERNLY_TEST_TMP, else /var/tmp (the OS
// temp dir the sandbox doesn't replace), else os.TempDir().
func homeRoot() string {
	if d := os.Getenv("TERNLY_TEST_TMP"); d != "" {
		return d
	}
	if fi, err := os.Stat("/var/tmp"); err == nil && fi.IsDir() {
		if f, err := os.CreateTemp("/var/tmp", ".ternly-probe-"); err == nil {
			f.Close()
			os.Remove(f.Name())
			return "/var/tmp"
		}
	}
	return os.TempDir()
}

// sweepHomes removes test homes whose test process is gone (killed with
// SIGKILL, which no handler sees).
func sweepHomes() {
	ents, _ := os.ReadDir(homeRoot())
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), homePrefix)
		if !ok {
			continue
		}
		pidStr, _, _ := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid == os.Getpid() || syscall.Kill(pid, 0) == nil {
			continue // alive (or ours)
		}
		_ = os.RemoveAll(filepath.Join(homeRoot(), e.Name()))
	}
}
