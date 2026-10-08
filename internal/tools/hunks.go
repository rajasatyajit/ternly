package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/rajasatyajit/ternly/internal/editdiff"
	"github.com/rajasatyajit/ternly/internal/surface"
)

// Per-hunk review of edits (ADR 021 amendment 1): an edit that would ask a
// person is shown as hunks; only the accepted ones are written.

// editReview is one edit's proposal and the person's answer, carried from
// Registry.Call through the policy (which asks) back to Call (which writes).
type editReview struct {
	tool     string
	load     func() (path, old, new string, err error) // the edit's proposal, computed when the policy asks
	rel      func(string) string                       // workspace-relative path for the person
	proposal surface.EditProposal
	ops      []editdiff.Op
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
// whose trust is lost, ADR 020). handled is false when there's nothing to
// show as hunks (an edit that will fail in Run, or no change): the caller
// asks yes or no as before.
func (p *Policy) review(ctx context.Context, r *editReview, why string, allowAlways bool) (ok bool, reason string, handled bool) {
	path, old, nw, err := r.load()
	if err != nil || old == nw {
		return false, "", false
	}
	r.ops = editdiff.LineDiff(editdiff.SplitLines(old), editdiff.SplitLines(nw))
	hunks, owner := editdiff.Hunks(r.ops)
	r.owner, r.path, r.old = owner, path, old
	r.proposal = surface.EditProposal{Tool: r.tool, Path: r.rel(path), NewFile: old == "", Hunks: hunks}
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
		return false, "", true
	}
	if d.Always && allowAlways {
		p.mu.Lock()
		p.always["edit"] = true
		p.mu.Unlock()
	}
	return true, "", true
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
	content := editdiff.Compose(rv.ops, rv.owner, rv.decision.Apply)
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
