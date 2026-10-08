// Package editdiff is the line diff behind per-hunk review of edits
// (ADR 021 amendment 1): hunks with context, and the file with only the
// accepted hunks applied, byte-exact.
package editdiff

import (
	"strings"

	"github.com/rajasatyajit/ternly/internal/surface"
)

type opKind byte

const (
	opEq opKind = iota
	opDel
	opIns
)

type Op struct {
	kind opKind
	line string // with its trailing newline, if it had one
}

// maxDiffCells bounds the line-diff table; larger changes become one hunk.
const maxDiffCells = 4_000_000

// splitLines keeps each line's newline, so composing restores exact bytes.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")[:strings.Count(s, "\n")+b2i(!strings.HasSuffix(s, "\n"))]
}

// lineDiff is a longest-common-subsequence diff of two line lists.
func LineDiff(a, b []string) []Op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []Op
	for _, l := range a[:pre] {
		ops = append(ops, Op{opEq, l})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma)*len(mb) > maxDiffCells { // too big to align: delete then insert
		for _, l := range ma {
			ops = append(ops, Op{opDel, l})
		}
		for _, l := range mb {
			ops = append(ops, Op{opIns, l})
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
				ops = append(ops, Op{opEq, ma[i]})
				i, j = i+1, j+1
			case i < n && (j == m || dp[i+1][j] >= dp[i][j+1]): // deletions first, as in a unified diff
				ops = append(ops, Op{opDel, ma[i]})
				i++
			default:
				ops = append(ops, Op{opIns, mb[j]})
				j++
			}
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, Op{opEq, l})
	}
	return ops
}

// hunkCtx is the context lines around each change.
const hunkCtx = 3

// diffHunks groups the changes of ops into hunks; owner[i] is the hunk an
// op belongs to (-1: context or unchanged).
func Hunks(ops []Op) (hunks []surface.Hunk, owner []int) {
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
func Compose(ops []Op, owner []int, apply []bool) string {
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

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
