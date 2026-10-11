package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/discover"
)

// quotaServer answers every chat with a monthly-limit 429.
func quotaServer(t *testing.T, chats *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		chats.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"you have reached your monthly usage limit"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// realBinary builds ternly (the build cache makes the second build a link):
// an evaluation's trap runs re-execute the binary, which the test binary
// (TERNLY_TEST_ARGS) can't stand in for.
func realBinary(t *testing.T) string {
	bin := filepath.Join(t.TempDir(), "ternly")
	if out, err := exec.Command("go", "build", "-trimpath", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// runBounded runs the binary in its own process group, killed whole after 3 minutes.
func runBounded(t *testing.T, home string, extraEnv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, realBinary(t), args...)
	cmd.Env = append(childEnv(home), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd.CombinedOutput()
}

// ADR 031: a 429 during an evaluation stops it (exit code exitQuota,
// nothing saved) after the trap that hit it, and the provider's quota is
// recorded for every ternly process: the scheduler then leaves it alone.
func TestEvalStopsOnQuota(t *testing.T) {
	var chats atomic.Int32
	home := testHome(t, quotaServer(t, &chats).URL)
	out, err := runBounded(t, home, nil, "--eval", "--model", "fake/m1")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != exitQuota {
		t.Fatalf("exit %v, want %d\n%s", err, exitQuota, out)
	}
	if !strings.Contains(string(out), "hit its quota") || strings.Count(string(out), "\n✓ ")+strings.Count(string(out), "\n✗ ") > 0 {
		t.Fatalf("output:\n%s", out)
	}
	if n := chats.Load(); n == 0 || n > 3 { // the first trap's request(s), then nothing
		t.Fatalf("%d chat requests: the evaluation went on after the quota hit", n)
	}
	data := filepath.Join(home, ".local", "share", "ternly")
	log := discover.OpenQuotaLog(filepath.Join(data, "quota.json"))
	if ok, q := log.Exhausted("fake", time.Now()); !ok || q.Model != "fake/m1" {
		t.Fatalf("quota not recorded: %v %+v", ok, q)
	}
	if ms, _ := filepath.Glob(filepath.Join(data, "capability", "*")); len(ms) != 0 {
		t.Fatalf("a stopped evaluation saved a measurement: %v", ms)
	}
}

// A trap run (TERNLY_EVAL=1) never starts an evaluation of its own.
func TestEvalRefusedInTrapRun(t *testing.T) {
	var chats atomic.Int32
	home := testHome(t, quotaServer(t, &chats).URL)
	out, err := runBounded(t, home, []string{"TERNLY_EVAL=1"}, "--eval", "--model", "fake/m1")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || chats.Load() != 0 {
		t.Fatalf("exit %v, %d chats\n%s", err, chats.Load(), out)
	}
}
