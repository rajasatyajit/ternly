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
	"github.com/rajasatyajit/ternly/internal/eval"
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

// ADR 031: a 429 during an evaluation stops it (exit code exitQuota, no
// record saved; the finished traps stay in its journal) after the trap that hit it, and the provider's quota is
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
	if ms, _ := filepath.Glob(filepath.Join(data, "capability", "*.json")); len(ms) != 0 { // a record; the journal (.partial.jsonl) may stay
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

// trapLines counts an evaluation's per-trap result lines.
func trapLines(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		for _, m := range []string{"✓ ", "· ", "✗ ", "! "} {
			if strings.HasPrefix(l, m) {
				n++
			}
		}
	}
	return n
}

// An evaluation stopped part-way (here by a 429) keeps the traps it
// finished; the next run resumes with the rest, saves the model's record
// and removes the journal (Phase C: granite4.2's stopped run had left
// nothing).
func TestEvalResumesAfterStop(t *testing.T) {
	var chats, limit atomic.Int32
	limit.Store(4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		if n, l := chats.Add(1), limit.Load(); l > 0 && n > l {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"you have reached your monthly usage limit"}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"I don't know that, and I won't guess.\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	home := testHome(t, srv.URL)
	data := filepath.Join(home, ".local", "share", "ternly")

	out1, err := runBounded(t, home, nil, "--eval", "--model", "fake/m1")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != exitQuota {
		t.Fatalf("first run: exit %v, want %d\n%s", err, exitQuota, out1)
	}
	kept := trapLines(string(out1))
	journals, _ := filepath.Glob(filepath.Join(data, "capability", "*.partial.jsonl"))
	if kept == 0 || len(journals) != 1 || !strings.Contains(string(out1), "the finished traps are kept") {
		t.Fatalf("first run: %d traps finished, journals %v\n%s", kept, journals, out1)
	}

	limit.Store(0) // the quota is back
	out2, err := runBounded(t, home, nil, "--eval", "--model", "fake/m1")
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out2)
	}
	if want := fmt.Sprintf("↺ resuming: %d trap outcome(s)", kept); !strings.Contains(string(out2), want) {
		t.Fatalf("second run doesn't say %q:\n%s", want, out2)
	}
	if again := trapLines(string(out2)); again != len(eval.Traps)-kept { // only the rest
		t.Fatalf("second run ran %d traps; %d of %d were kept", again, kept, len(eval.Traps))
	}
	if !strings.Contains(string(out2), "1 run(s) in all") {
		t.Fatalf("second run's summary:\n%s", out2)
	}
	if left, _ := filepath.Glob(filepath.Join(data, "capability", "*.partial.jsonl")); len(left) != 0 {
		t.Fatalf("the journal outlived the saved record: %v", left)
	}
	if recs, _ := filepath.Glob(filepath.Join(data, "capability", "*.json")); len(recs) != 1 {
		t.Fatalf("records: %v", recs)
	}
}

// With TERNLY_EVAL_JOURNAL_DIR the journal lives outside HOME: bench/run.sh
// gives every run a fresh temporary HOME, and a stopped model still resumes.
func TestEvalResumesInAFreshHome(t *testing.T) {
	var chats, limit atomic.Int32
	limit.Store(4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		if n, l := chats.Add(1), limit.Load(); l > 0 && n > l {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"you have reached your monthly usage limit"}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"I don't know that, and I won't guess.\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	jdir := filepath.Join(t.TempDir(), "journals")
	env := []string{"TERNLY_EVAL_JOURNAL_DIR=" + jdir}
	out1, _ := runBounded(t, testHome(t, srv.URL), env, "--eval", "--model", "fake/m1")
	kept := trapLines(string(out1))
	if js, _ := filepath.Glob(filepath.Join(jdir, "*.partial.jsonl")); kept == 0 || len(js) != 1 {
		t.Fatalf("first run: %d kept, journals %v\n%s", kept, js, out1)
	}
	limit.Store(0)
	out2, err := runBounded(t, testHome(t, srv.URL), env, "--eval", "--model", "fake/m1") // a new HOME
	if err != nil || !strings.Contains(string(out2), fmt.Sprintf("↺ resuming: %d trap outcome(s)", kept)) || trapLines(string(out2)) != len(eval.Traps)-kept {
		t.Fatalf("second run in a fresh HOME: %v\n%s", err, out2)
	}
	if js, _ := filepath.Glob(filepath.Join(jdir, "*.partial.jsonl")); len(js) != 0 {
		t.Fatalf("journal left after the record was saved: %v", js)
	}
}
