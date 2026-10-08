package tui

import (
	"sort"
	"strings"
)

// The transcript is virtualised (ADR 023): every block keeps its rendered
// lines, refresh only recounts them, and a frame joins just the visible
// window. With 10k lines a redraw no longer rebuilds and re-splits the
// whole transcript (33 ms before; the 16 ms frame budget).
type transcript struct {
	h      int      // visible lines
	top    int      // first visible line
	follow bool     // pinned to the bottom (new output scrolls into view)
	ends   []int    // ends[i]: lines in blocks[0..i]
	tail   []string // the activity line under the blocks
	total  int
	lines  [][]string // each block's lines as counted by set: a block may be re-rendered before the next frame
}

// lines are a block's rendered lines, cached until it is rendered again.
func (b *block) linesOf() []string {
	if b.lines == nil || b.linesFor != b.rendered {
		b.lines, b.linesFor = strings.Split(b.rendered, "\n"), b.rendered
	}
	return b.lines
}

// set recounts after blocks changed (O(blocks), no string building).
func (t *transcript) set(blocks []*block, tail string) {
	if cap(t.ends) < len(blocks) {
		t.ends = make([]int, len(blocks), 2*len(blocks))
		t.lines = make([][]string, len(blocks), 2*len(blocks))
	}
	t.ends, t.lines = t.ends[:len(blocks)], t.lines[:len(blocks)]
	n := 0
	for i, b := range blocks {
		ls := b.linesOf()
		t.lines[i] = ls
		n += len(ls)
		t.ends[i] = n
	}
	t.tail = strings.Split(tail, "\n")
	t.total = n + len(t.tail)
	t.clamp()
}

func (t *transcript) clamp() {
	maxTop := max(0, t.total-t.h)
	if t.follow || t.top > maxTop {
		t.top = maxTop
	}
	t.top = max(0, t.top)
}

func (t *transcript) atBottom() bool { return t.top >= max(0, t.total-t.h) }

func (t *transcript) scroll(n int) {
	t.top += n
	t.follow = false
	t.clamp()
	if t.atBottom() {
		t.follow = true
	}
}

// line returns line i of the whole transcript.
func (t *transcript) line(i int) string {
	n := len(t.ends)
	if n == 0 || i >= t.ends[n-1] {
		j := i
		if n > 0 {
			j -= t.ends[n-1]
		}
		return t.tail[j]
	}
	bi := sort.SearchInts(t.ends, i+1)
	start := 0
	if bi > 0 {
		start = t.ends[bi-1]
	}
	return t.lines[bi][i-start]
}

// view is the visible window, exactly h lines.
func (t *transcript) view() string {
	var sb strings.Builder
	for i := range t.h {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if j := t.top + i; j < t.total {
			sb.WriteString(t.line(j))
		}
	}
	return sb.String()
}
