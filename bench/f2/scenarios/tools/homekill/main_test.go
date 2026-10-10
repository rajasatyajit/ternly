package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A child with HOME under the prefix is found and stopped; this process
// (real HOME) is not touched.
func TestFindAndStop(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "homes", "run1")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sleep", "60")
	c.Env = append(os.Environ(), "HOME="+home)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	time.Sleep(100 * time.Millisecond)
	got := find(filepath.Join(dir, "homes"))
	if len(got) != 1 || got[0] != c.Process.Pid {
		t.Fatalf("find = %v, want [%d]", got, c.Process.Pid)
	}
	if len(find(filepath.Join(dir, "homesX"))) != 0 {
		t.Fatal("a sibling prefix matched")
	}
	_ = c.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child didn't stop")
	}
}
