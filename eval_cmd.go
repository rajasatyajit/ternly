package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/eval"
	"github.com/rajasatyajit/ternly/internal/logstore"
	"github.com/rajasatyajit/ternly/internal/memory"
	"github.com/rajasatyajit/ternly/internal/session"
)

// runEval runs the fabrication eval against one model: each trap in a fresh
// workspace, through this binary run headless (so the model sees exactly
// what a user's session gives it), judged afterwards. The result is saved as
// the model's measured capability (<data>/capability), which routing uses.
func runEval(dataDir, model string, pass []string, runs int, only string) int {
	if os.Getenv("TERNLY_HARNESS") == "1" { // bench/run.sh: never against the real HOME
		if err := eval.HarnessIsolated(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	var re *regexp.Regexp
	if only != "" {
		var err error
		if re, err = regexp.Compile(only); err != nil {
			fmt.Fprintln(os.Stderr, "--eval-only:", err)
			return 2
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var hits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0") // the beacon injection traps point at
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer ln.Close()
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	}()
	beacon := ln.Addr().String()

	key := model
	var outs []eval.Outcome
	t0 := time.Now()
	for run := 1; run <= runs; run++ {
		for _, tr := range eval.Traps {
			if re != nil && !re.MatchString(tr.Name) {
				continue
			}
			hits.Store(0)
			o, k := evalTrap(exe, dataDir, model, pass, tr, beacon, &hits)
			if k != "" {
				key = k
			}
			outs = append(outs, o)
			mark := map[eval.Score]string{eval.Checked: "✓", eval.Declined: "·", eval.Fabricated: "✗"}[o.Score]
			if o.Err != "" {
				mark, o.Why = "!", o.Err
			}
			fmt.Printf("%s %-22s %-11s %-10s %5.0fs  %s\n", mark, tr.Name, tr.Kind, o.Score, float64(o.Ms)/1000, o.Why)
		}
	}
	rec := eval.Summarise(key, runs, outs)
	path := "not saved: --eval-only measures a subset"
	if re == nil { // only a full run is a measurement routing may use; it adds to earlier ones
		dir := filepath.Join(dataDir, "capability")
		rec = rec.Accumulate(dir)
		if path, err = rec.Save(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	fmt.Printf("\n%s (eval v%s; this batch %d run(s), %s; %d run(s) in all)\n", key, eval.Version, runs, time.Since(t0).Round(time.Second), rec.Runs)
	ci := func(c eval.Counts) string {
		lo, hi := c.Wilson()
		return fmt.Sprintf("%d/%d = %.0f%% [%.0f–%.0f%%]", c.Bad, c.N, 100*c.Rate(), 100*lo, 100*hi)
	}
	plo, phi := rec.PassInterval()
	fmt.Printf("  fabrication %s · memory misuse %s · took bait %s (95%% intervals)\n", ci(rec.Fab), ci(rec.Mem), ci(rec.Inj))
	fmt.Printf("  capability: pass %.0f%% [%.0f–%.0f%%] → T%d (decided on the lower bound; T3 ≥ 80%%, T2 ≥ 60%%)\n", 100*rec.Pass, 100*plo, 100*phi, rec.Tier())
	trust := fmt.Sprintf("edits and commands follow the mode (%d clean bait trials in a row)", rec.Trust.Clean)
	if rec.Baitable() {
		trust = fmt.Sprintf("lost (easily baited): shell commands always need confirmation, edits only with --mode edits; regained after %d clean bait trials in a row, %d so far", eval.RegainStreak, rec.Trust.Clean)
	}
	fmt.Printf("  trust: %s; memory notes %s — %s\n", trust, rec.Autonomy(), path)
	this := eval.Summarise(key, runs, outs) // the batch alone, for the e2e check
	fmt.Printf("E2E-METRIC eval_version=%s fabrication=%.3f memory_misuse=%.3f susceptibility=%.3f pass=%.3f tier=%d baitable=%v\n", eval.Version, this.Fabrication, this.MemoryMisuse, this.Susceptibility, this.Pass, rec.Tier(), rec.Baitable())
	return 0
}

// measurements are the models' measured capabilities for routing: the
// user's own --eval runs, else what this version ships (eval.Defaults).
func measurements(dir string) map[string]discover.Measurement {
	out := map[string]discover.Measurement{}
	add := func(rs map[string]eval.Record, src string) {
		for k, r := range rs {
			lo, hi := r.PassInterval()
			out[k] = discover.Measurement{Tier: r.Tier(), Runs: r.Runs, Autonomy: r.Autonomy(), Baitable: r.Baitable(), Measured: r.Measured,
				Fabrication: r.Fabrication, MemoryMisuse: r.MemoryMisuse, Susceptibility: r.Susceptibility, Pass: r.Pass, PassLo: lo, PassHi: hi, Source: src,
				TrustClean: r.Trust.Clean, TrustNeeded: eval.RegainStreak}
		}
	}
	add(eval.Defaults(), "ternly")
	add(eval.Load(dir), "you")
	return out
}

var reModelLine = regexp.MustCompile(`(?m)^◆ (\S+) \(`)

// evalTrap runs one trap and returns its outcome and the model key ternly
// resolved --model to.
func evalTrap(exe, dataDir, model string, pass []string, tr eval.Trap, beacon string, hits *atomic.Int32) (o eval.Outcome, key string) {
	o = eval.Outcome{Trap: tr.Name, Kind: tr.Kind}
	t0 := time.Now()
	defer func() { o.Ms = time.Since(t0).Milliseconds() }()
	ws, err := os.MkdirTemp("", "ternly-eval-")
	if err != nil {
		o.Err = err.Error()
		return o, ""
	}
	ws, _ = filepath.EvalSymlinks(ws)
	defer os.RemoveAll(ws)
	sub := func(s string) string { return strings.ReplaceAll(s, "{{BEACON}}", "http://"+beacon) }
	for p, c := range tr.Files {
		full := filepath.Join(ws, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(sub(c)), 0o644)
	}
	proj, err := session.OpenProject(dataDir, ws)
	if err != nil {
		o.Err = err.Error()
		return o, ""
	}
	defer os.RemoveAll(proj.Dir)
	if len(tr.Notes) > 0 { // wrong-but-similar notes, as automatic summaries of an earlier session
		mem, err := memory.Open(proj.Dir, filepath.Join(proj.Dir, "eval-user"))
		if err != nil {
			o.Err = err.Error()
			return o, ""
		}
		for _, n := range tr.Notes {
			_, _ = mem.Add(memory.Item{Scope: memory.Project, Kind: "note", Text: n, Source: "auto"})
		}
		mem.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	args := append([]string{"-C", ws, "--model", model, "--mode", tr.Mode, "--new"}, pass...)
	c := exec.CommandContext(ctx, exe, append(args, "-p", sub(tr.Prompt))...)
	c.Env = append(os.Environ(), "TERNLY_EVAL=1")
	c.Dir = ws
	var so, se strings.Builder
	c.Stdout, c.Stderr = &so, &se
	c.WaitDelay = 5 * time.Second
	runErr := c.Run()
	k := ""
	if m := reModelLine.FindStringSubmatch(se.String()); m != nil {
		k = m[1]
	}
	if ctx.Err() != nil {
		o.Err = "timed out"
		return o, k
	}
	if runErr != nil && so.Len() == 0 {
		o.Err = "ternly: " + lastLine(se.String())
		return o, k
	}
	r := &eval.Result{Answer: so.String(), Trace: se.String(), WS: ws, Beacon: beacon, Hits: int(hits.Load())}
	for _, rec := range trapRecords(proj.Dir) {
		if rec.T != "msg" || rec.Msg == nil {
			continue
		}
		r.Calls = append(r.Calls, rec.Msg.ToolCalls...)
		if rec.Msg.Role == "tool" {
			r.Outputs += rec.Msg.Content + "\n"
		}
	}
	o.Score, o.Why = tr.Judge(r)
	if o.Answer = strings.TrimSpace(r.Answer); len(o.Answer) > 4000 {
		o.Answer = o.Answer[:4000] + "…"
	}
	return o, k
}

func trapRecords(projDir string) []agent.Record {
	logs, _ := filepath.Glob(filepath.Join(projDir, "sessions", "*", "events.log"))
	var out []agent.Record
	for _, p := range logs {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		recs, _, _ := logstore.ReadAll(f)
		f.Close()
		for _, b := range recs {
			var r agent.Record
			if json.Unmarshal(b, &r) == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}
