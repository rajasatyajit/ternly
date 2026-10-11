package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rajasatyajit/ternly/internal/bgeval"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/idle"
)

// routingConfig is the "routing" config key (ADR 018). It takes a string
// ("v1" or "v2", the escape hatch) or an object:
//
//	"routing": {"version": "v2", "time_value_usd_per_hour": 20,
//	            "background_eval": {"enabled": true, "per_model_minutes": 20, "runs_per_week": 4}}
type routingConfig struct {
	Version        string   `json:"version"`
	TimeValue      *float64 `json:"time_value_usd_per_hour"`
	BackgroundEval evalCaps `json:"background_eval"`
}

// evalCaps bound background evaluations of new models (ADR 018 review,
// decision 3): Ollama Cloud quota only, per model and per week, with an off
// switch; paid APIs need an explicit budget (default $0).
type evalCaps struct {
	Enabled         *bool    `json:"enabled"`
	PerModelMinutes *float64 `json:"per_model_minutes"`
	RunsPerWeek     *int     `json:"runs_per_week"`
	PaidUSD         float64  `json:"paid_usd_per_week"`
}

func (r *routingConfig) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		r.Version = s
		return nil
	}
	type plain routingConfig
	return json.Unmarshal(b, (*plain)(r))
}

// modelsCtx is the context size /models ranks for: a typical agent context
// (ADR 018: 10–60 k tokens).
const modelsCtx = 20000

// defaultRouting is the router a config without "routing" gets: v2 from
// v0.1.1 (ADR 018: proven on the e2e suite, the baseline and the replay
// test); "v1" stays as an escape hatch for one release.
const defaultRouting = "v2"

// costModel builds routing v2's cost model, or nil for v1. flag overrides
// the config's version.
func (r routingConfig) costModel(flag, dataDir string, turn time.Duration) (*discover.CostModel, error) {
	v := r.Version
	if flag != "" {
		v = flag
	}
	if v == "" {
		v = defaultRouting
	}
	switch v {
	case "v1":
		return nil, nil
	case "v2":
	default:
		return nil, fmt.Errorf("routing: %q (want v1 or v2)", v)
	}
	lambda := discover.DefaultTimeValue
	if r.TimeValue != nil {
		if *r.TimeValue < 0 {
			return nil, fmt.Errorf("routing.time_value_usd_per_hour: %v (want ≥ 0; 0 routes on price alone)", *r.TimeValue)
		}
		lambda = *r.TimeValue
	}
	return &discover.CostModel{TimeValue: lambda, TurnLimit: turn, Speeds: discover.OpenSpeeds(filepath.Join(dataDir, "speed.json"))}, nil
}

// watchdogSetting is "reasoning_watchdog" (issue #2): {"tokens": N,
// "seconds": S}, either of which trips it (0: default, -1: off). The v0.1
// form, a bare number of stream chunks, is read as tokens for a release,
// with a note: Ollama sends about one token per chunk.
type watchdogSetting struct {
	Tokens  int     `json:"tokens"`
	Seconds float64 `json:"seconds"`
	legacy  bool
}

func (w *watchdogSetting) UnmarshalJSON(b []byte) error {
	var n int
	if json.Unmarshal(b, &n) == nil {
		*w = watchdogSetting{Tokens: n, legacy: true}
		return nil
	}
	type plain watchdogSetting
	return json.Unmarshal(b, (*plain)(w))
}

// backgroundEvals builds the scheduler of background evaluations (ADR 018
// review, decision 3). Each runs this binary's own --eval on one model: the
// bundled trap workspaces only, never the user's repository, in its own
// process group, logged to <data>/background-eval/<model>.log.
// It runs only when gate allows (idle, on AC power) and quota has room (ADR
// 031); leaving idle pauses the evaluation (runPausable), and a quota hit
// stops it (exit code exitQuota).
func backgroundEvals(c evalCaps, localOnly bool, dataDir string, router *discover.Router, gate *idle.Gate, quota func(*discover.Model) (bool, string), done func()) *bgeval.Scheduler {
	caps := bgeval.DefaultCaps
	if c.Enabled != nil {
		caps.Enabled = *c.Enabled
	}
	if c.PerModelMinutes != nil {
		caps.PerModel = time.Duration(*c.PerModelMinutes * float64(time.Minute))
	}
	if c.RunsPerWeek != nil {
		caps.PerWeek = *c.RunsPerWeek
	}
	caps.PaidUSD, caps.LocalOnly = c.PaidUSD, localOnly
	var allowed func() (bool, string) // nil: --models only reads the state
	if gate != nil {
		allowed = gate.Allowed
	}
	return &bgeval.Scheduler{
		Idle:   allowed,
		Caps:   caps,
		Ledger: bgeval.OpenLedger(filepath.Join(dataDir, "background-eval.json")),
		Models: router.Models,
		Quota:  quota,
		Done:   done,
		Run: func(ctx context.Context, m *discover.Model, budget float64, pause <-chan bool) error {
			logDir := filepath.Join(dataDir, "background-eval")
			if err := os.MkdirAll(logDir, 0o700); err != nil {
				return err
			}
			log, err := os.OpenFile(filepath.Join(logDir, strings.NewReplacer("/", "_", ":", "_").Replace(m.Key())+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			defer log.Close()
			cmd, err := evalCommand(ctx, m, budget, localOnly)
			if err != nil {
				return err
			}
			cmd.Stdout, cmd.Stderr = log, log
			return runPausable(cmd, pause)
		},
	}
}

// evalCommand is one background evaluation: this binary's --eval on the
// model, which builds its own throwaway trap workspaces (no -C: the user's
// repository is never involved), in its own process group.
func evalCommand(ctx context.Context, m *discover.Model, budget float64, localOnly bool) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"--eval", "--model", m.Key(), "--eval-runs", "1"}
	if budget > 0 {
		args = append(args, "--budget", fmt.Sprint(budget))
	}
	if localOnly {
		args = append(args, "--local-only")
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "TERNLY_BACKGROUND_EVAL=1") // the child never schedules evaluations itself
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}      // stopped whole: the eval's own ternly children too
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// exitQuota is --eval's exit code when a trap run hit the model's quota
// (EX_TEMPFAIL): the evaluation stopped and is retried after the reset.
const exitQuota = 75

// runPausable runs an evaluation's driver with a control pipe (its fd 3,
// TERNLY_EVAL_CONTROL=3): "p" pauses it — the trap in progress stops and
// the driver waits — and "r" resumes it, running that trap again and going
// on (ADR 031). A pipe, not signals: nothing is lost or fatal before the
// driver is ready, and its end (EOF) tells the driver ternly is gone.
func runPausable(cmd *exec.Cmd, pause <-chan bool) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer w.Close()
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, "TERNLY_EVAL_CONTROL=3")
	err = cmd.Start()
	r.Close()
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case p := <-pause:
				b := []byte("r")
				if p {
					b = []byte("p")
				}
				_, _ = w.Write(b)
			}
		}
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == exitQuota {
		return bgeval.ErrQuota
	}
	return err
}
