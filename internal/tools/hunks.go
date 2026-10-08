package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/rajasatyajit/ternly/internal/surface"
)

// Per-hunk review of edits (ADR 021 amendment 1): an edit that would ask a
// person is shown as hunks; only the accepted ones are written.

type opKind byte

const (
	opEq opKind = iota
	opDel
	opIns
)

type diffOp struct {
	kind opKind
	line string // with its trailing newline, if it had one
}

// maxDiffCells bounds the line-diff table; larger changes become one hunk.
const maxDiffCells = 4_000_000

// splitLines keeps each line's newline, so composing restores exact bytes.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")[:strings.Count(s, "\n")+boolInt(!strings.HasSuffix(s, "\n"))]
}

// lineDiff is a longest-common-subsequence diff of two line lists.
func lineDiff(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{opEq, l})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma)*len(mb) > maxDiffCells { // too big to align: delete then insert
		for _, l := range ma {
			ops = append(ops, diffOp{opDel, l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{opIns, l})
		}
	} else {
		n, m := len(ma), len(mb)
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				ops = append(ops, diffOp{opEq, ma[i]})
				i, j = i+1, j+1
			case i < n && (j == m || dp[i+1][j] >= dp[i][j+1]): // deletions first, as in a unified diff
				ops = append(ops, diffOp{opDel, ma[i]})
				i++
			default:
				ops = append(ops, diffOp{opIns, mb[j]})
				j++
			}
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{opEq, l})
	}
	return ops
}

// hunkCtx is the context lines around each change.
const hunkCtx = 3

// diffHunks groups the changes of ops into hunks; owner[i] is the hunk an
// op belongs to (-1: context or unchanged).
func diffHunks(ops []diffOp) (hunks []surface.Hunk, owner []int) {
	owner = make([]int, len(ops))
	for i := range owner {
		owner[i] = -1
	}
	type span struct{ s, e int } // ops[s:e] is one hunk's changes (and the equal lines between them)
	var spans []span
	for i := 0; i < len(ops); {
		if ops[i].kind == opEq {
			i++
			continue
		}
		s, e := i, i
		for e < len(ops) {
			if ops[e].kind != opEq {
				e++
				continue
			}
			run := e
			for run < len(ops) && ops[run].kind == opEq {
				run++
			}
			if run < len(ops) && run-e <= 2*hunkCtx { // a short equal run joins two changes
				e = run
				continue
			}
			break
		}
		spans = append(spans, span{s, e})
		i = e
	}
	// line numbers before each op
	oldAt, newAt := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, o := range ops {
		oldAt[i+1], newAt[i+1] = oldAt[i], newAt[i]
		if o.kind != opIns {
			oldAt[i+1]++
		}
		if o.kind != opDel {
			newAt[i+1]++
		}
	}
	for hi, sp := range spans {
		from, to := max(0, sp.s-hunkCtx), min(len(ops), sp.e+hunkCtx)
		h := surface.Hunk{OldStart: oldAt[from] + 1, NewStart: newAt[from] + 1}
		for i := from; i < to; i++ {
			o := ops[i]
			prefix := " "
			switch o.kind {
			case opDel:
				prefix = "-"
				h.OldLines++
			case opIns:
				prefix = "+"
				h.NewLines++
			default:
				h.OldLines++
				h.NewLines++
			}
			h.Lines = append(h.Lines, prefix+strings.TrimSuffix(o.line, "\n"))
			if i >= sp.s && i < sp.e && o.kind != opEq {
				owner[i] = hi
			}
		}
		hunks = append(hunks, h)
	}
	return hunks, owner
}

// composeHunks is old with only the accepted hunks applied.
func composeHunks(ops []diffOp, owner []int, apply []bool) string {
	var b strings.Builder
	for i, o := range ops {
		accepted := owner[i] >= 0 && owner[i] < len(apply) && apply[owner[i]]
		switch o.kind {
		case opEq:
			b.WriteString(o.line)
		case opDel:
			if !accepted {
				b.WriteString(o.line)
			}
		case opIns:
			if accepted {
				b.WriteString(o.line)
			}
		}
	}
	return b.String()
}

// editReview is one edit's proposal and the person's answer, carried from
// Registry.Call through the policy (which asks) back to Call (which writes).
type editReview struct {
	proposal surface.EditProposal
	ops      []diffOp
	owner    []int
	path     string // resolved
	old      string // the file when the edit was proposed
	decision *surface.EditDecision
}

type reviewKey struct{}

func withReview(ctx context.Context, r *editReview) context.Context {
	return context.WithValue(ctx, reviewKey{}, r)
}

func reviewFrom(ctx context.Context) *editReview {
	r, _ := ctx.Value(reviewKey{}).(*editReview)
	return r
}

// review asks the person through the reviewer. Allowed when any hunk is
// accepted; "always" is honoured only where allowAlways (not for a model
// whose trust is lost, ADR 020).
func (p *Policy) review(ctx context.Context, r *editReview, why string, allowAlways bool) (bool, string) {
	pr := r.proposal
	pr.Why = why
	d := p.Review(ctx, pr)
	if len(d.Apply) != len(pr.Hunks) { // a malformed answer is a no
		d.Apply = make([]bool, len(pr.Hunks))
	}
	r.decision = &d
	any := false
	for _, a := range d.Apply {
		any = any || a
	}
	if !any {
		return false, ""
	}
	if d.Always && allowAlways {
		p.mu.Lock()
		p.always["edit"] = true
		p.mu.Unlock()
	}
	return true, ""
}

func allTrue(bs []bool) bool {
	for _, b := range bs {
		if !b {
			return false
		}
	}
	return true
}

// writePartial writes the file with only the accepted hunks and tells the
// model which ones were declined, so it doesn't silently re-apply them.
func (r *Registry) writePartial(rv *editReview) (string, error) {
	// the review may have waited a while: hunks are composed from the file
	// as proposed, so a change since (the user's, another tool's) must not
	// be overwritten
	now, err := r.readRegular(r.inRoot(rv.path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if string(now) != rv.old {
		return "", fmt.Errorf("%s changed while the edit was being reviewed; nothing was written — read it again before editing", rv.proposal.Path)
	}
	content := composeHunks(rv.ops, rv.owner, rv.decision.Apply)
	if _, err := r.write(rv.path, content); err != nil {
		return "", err
	}
	var applied, declined []string
	for i, h := range rv.proposal.Hunks {
		label := fmt.Sprintf("%d (lines %d–%d)", i+1, h.OldStart, h.OldStart+max(h.OldLines, 1)-1)
		if rv.decision.Apply[i] {
			applied = append(applied, label)
		} else {
			declined = append(declined, label)
		}
	}
	return fmt.Sprintf("edited %s partially: the user applied hunk(s) %s and declined hunk(s) %s of %d. The declined changes are not in the file; don't re-apply them unless the user asks.",
		rv.proposal.Path, strings.Join(applied, ", "), strings.Join(declined, ", "), len(rv.proposal.Hunks)), nil
}
