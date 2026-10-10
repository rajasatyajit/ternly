// homekill stops every process whose environment has HOME inside a given
// directory: the isolated homes of one F2 survey run. It identifies processes
// by /proc/<pid>/environ, never by command line, so it can't hit the
// caller's own shells (they keep the real HOME). SIGTERM first, then SIGKILL
// after a grace period. Daemons a harness leaves behind in their own session
// (Codex's app-server, for one) are found the same way.
//
//	homekill [-grace 3s] [-n] <home-prefix>
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	grace := flag.Duration("grace", 3*time.Second, "time between SIGTERM and SIGKILL")
	dry := flag.Bool("n", false, "list only")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: homekill [-grace d] [-n] <home-prefix>")
		os.Exit(2)
	}
	prefix, err := filepath.Abs(flag.Arg(0))
	if err != nil || len(prefix) < 8 { // never a short path like / or /home
		fmt.Fprintln(os.Stderr, "homekill: refusing prefix", flag.Arg(0))
		os.Exit(2)
	}
	pids := find(prefix)
	for _, p := range pids {
		fmt.Printf("%d %s\n", p, cmdline(p))
	}
	if *dry || len(pids) == 0 {
		return
	}
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	deadline := time.Now().Add(*grace)
	for time.Now().Before(deadline) && len(alive(pids)) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	for _, p := range alive(pids) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	time.Sleep(200 * time.Millisecond)
	if left := find(prefix); len(left) > 0 {
		fmt.Fprintf(os.Stderr, "homekill: still running: %v\n", left)
		os.Exit(1)
	}
}

// find lists processes (not this one) with HOME under prefix.
func find(prefix string) []int {
	self := os.Getpid()
	var out []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if h, ok := strings.CutPrefix(string(kv), "HOME="); ok && (h == prefix || strings.HasPrefix(h, prefix+"/")) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

func alive(pids []int) []int {
	var out []int
	for _, p := range pids {
		if syscall.Kill(p, 0) == nil {
			if st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p)); err == nil && !bytes.Contains(st, []byte(") Z ")) {
				out = append(out, p)
			}
		}
	}
	return out
}

func cmdline(p int) string {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p))
	s := strings.ReplaceAll(string(bytes.TrimRight(b, "\x00")), "\x00", " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
