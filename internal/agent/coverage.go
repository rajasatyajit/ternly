package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/internal/verify"
)

// Verdicts of a turn's verification (ADR 015). Only VerdictVerified is ✓:
// the project's check passed and every changed source file was covered by
// a check that compiled it.
const (
	VerdictVerified   = "verified"
	VerdictFailed     = "failed"
	VerdictUnverified = "unverified"
)

// changedSources lists the files this turn created or modified (from the
// checkpoint taken before its first change, so shell edits count too).
// blind is set when changes can't be listed: no checkpoints, and a shell
// command ran.
func (a *Agent) changedSources(ctx context.Context, st *turnState) (files []string, blind bool) {
	if a.CP != nil && st.tree != "" {
		cs, err := a.CP.Pending(ctx, st.tree)
		if err == nil {
			for _, c := range cs {
				if c.Status != 'A' { // Pending describes restoring: 'A' recreates a file this turn deleted
					files = append(files, c.Path)
				}
			}
			return files, false
		}
	}
	for p := range st.paths {
		files = append(files, p)
	}
	return files, st.shellRan
}

// verifyTurn runs the project's check (if any) and the coverage checks on
// the turn's changed files, and returns the verdict with what the model and
// the user should see. ok is false when nothing was verified because
// verification is off or nothing changed.
func (a *Agent) verifyTurn(ctx context.Context, st *turnState) (verdict, text, label string, ran bool) {
	if st.verify == "off" {
		return "", "", "", false
	}
	changed, blind := a.changedSources(ctx, st)
	sources := 0
	for _, f := range changed {
		if verify.IsSource(f) {
			sources++
		}
	}
	if sources == 0 && !blind && (st.verify == "" || len(changed) == 0) {
		return "", "", "", false // nothing a check could cover
	}
	label = st.verify
	if label == "" {
		label = "coverage checks"
	}
	a.Emit(Event{Kind: EvVerify, Text: label})
	t0 := time.Now()
	end := func(v, text string) (string, string, string, bool) {
		a.Emit(Event{Kind: EvToolEnd, ToolID: "verify", Tool: "verify", OK: v == VerdictVerified, Verdict: v, Text: text, Elapsed: time.Since(t0)})
		return v, a.Reg.Redact.Apply(text), label, true
	}
	pol := a.Reg.Policy
	run := func(ctx context.Context, cmd string, repoCode bool) (string, int, error) {
		// Unsandboxed, a command that runs the repository's code is the user's call.
		if repoCode && pol.Unsandboxed && !pol.Confirm(ctx, "verify", cmd) {
			return "", 0, fmt.Errorf("%w: shell commands run unsandboxed here, and this check (it runs the repository's code) wasn't approved", verify.ErrNotRun)
		}
		return a.Reg.Sandbox.Run(ctx, a.Reg.Root, cmd, 600)
	}
	var declined []verify.Gap
	if st.verify != "" {
		out, code, err := run(ctx, st.verify, !safeCheck[st.verify])
		if errors.Is(err, verify.ErrNotRun) {
			declined = append(declined, verify.Gap{File: "(project check `" + st.verify + "`)", Why: strings.TrimPrefix(err.Error(), verify.ErrNotRun.Error()+": ")})
			err, code = nil, 0
		}
		if err != nil || code != 0 {
			if err != nil {
				out += "\n" + err.Error()
			}
			return end(VerdictFailed, "$ "+st.verify+"\n"+out)
		}
	}
	rep := verify.Cover(ctx, a.Reg.Root, changed, run)
	if rep.Failed != "" {
		return end(VerdictFailed, rep.Failed)
	}
	rep.Gaps = append(declined, rep.Gaps...)
	if blind {
		rep.Gaps = append(rep.Gaps, verify.Gap{File: "(shell changes)", Why: "files changed by shell commands can't be listed without checkpoints"})
	}
	var b strings.Builder
	if len(rep.Gaps) > 0 {
		fmt.Fprintf(&b, "unverified: no check covered %d changed source file(s):\n", len(rep.Gaps))
		for i, g := range rep.Gaps {
			if i == 8 {
				fmt.Fprintf(&b, "- … %d more\n", len(rep.Gaps)-8)
				break
			}
			fmt.Fprintf(&b, "- %s: %s\n", g.File, g.Why)
		}
		return end(VerdictUnverified, strings.TrimSpace(b.String()))
	}
	parts := rep.Summary
	if st.verify != "" {
		parts = append([]string{st.verify}, parts...)
	}
	fmt.Fprintf(&b, "%s · %d changed source file(s) covered", strings.Join(parts, " · "), len(rep.Covered))
	return end(VerdictVerified, b.String())
}

// safeCheck are the auto-detected checks that run none of the repository's
// own code (DetectVerify): they run unsandboxed without asking.
var safeCheck = map[string]bool{"go build ./... && go vet ./...": true, "python3 -m compileall -q .": true, "ruff check . && python3 -m compileall -q .": true}

// trackPath records the file an edit tool touched (for changedSources when
// checkpoints are off).
func (st *turnState) trackPath(rel func(string) string, args string) {
	var v struct{ Path string }
	if json.Unmarshal([]byte(args), &v) == nil && v.Path != "" {
		if st.paths == nil {
			st.paths = map[string]bool{}
		}
		p := v.Path
		if filepath.IsAbs(p) {
			p = rel(p)
		}
		st.paths[filepath.ToSlash(filepath.Clean(p))] = true
	}
}
