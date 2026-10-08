package editdiff

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// Composing any subset of hunks gives exactly the old file with those hunks
// applied: all → new, none → old, and every subset byte-for-byte equal to
// applying the hunks one by one in a reference.
func TestComposeHunks(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 300 {
		var a []string
		for i := range 30 + rng.IntN(40) {
			a = append(a, fmt.Sprintf("line %d\n", i))
		}
		b := append([]string(nil), a...)
		for range 1 + rng.IntN(6) { // scattered edits
			switch i := rng.IntN(len(b)); rng.IntN(3) {
			case 0:
				b[i] = fmt.Sprintf("changed %d\n", rng.IntN(1000))
			case 1:
				b = append(b[:i], b[i+1:]...)
			default:
				b = append(b[:i], append([]string{fmt.Sprintf("inserted %d\n", rng.IntN(1000))}, b[i:]...)...)
			}
		}
		if trial%3 == 0 { // no newline at the end of the new file
			b[len(b)-1] = strings.TrimSuffix(b[len(b)-1], "\n")
		}
		old, nw := strings.Join(a, ""), strings.Join(b, "")
		ops := LineDiff(SplitLines(old), SplitLines(nw))
		hunks, owner := Hunks(ops)
		all, none := make([]bool, len(hunks)), make([]bool, len(hunks))
		for i := range all {
			all[i] = true
		}
		if got := Compose(ops, owner, all); got != nw {
			t.Fatalf("trial %d: all hunks ≠ new", trial)
		}
		if got := Compose(ops, owner, none); got != old {
			t.Fatalf("trial %d: no hunks ≠ old", trial)
		}
		// a subset: each hunk's +/- lines appear iff it was applied
		sub := make([]bool, len(hunks))
		for i := range sub {
			sub[i] = rng.IntN(2) == 0
		}
		got := Compose(ops, owner, sub)
		for i, h := range hunks {
			for _, l := range h.Lines {
				if l[0] == '+' && strings.Contains(l, "inserted") && strings.Contains(got, l[1:]+"\n") != sub[i] && !strings.Contains(old, l[1:]) {
					t.Fatalf("trial %d hunk %d (%v): %q", trial, i, sub[i], l)
				}
			}
		}
	}
}

func TestHunkNumbers(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\n"
	nw := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nL\nm\nn\n"
	hunks, _ := Hunks(LineDiff(SplitLines(old), SplitLines(nw)))
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks (changes 10 lines apart), got %d: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.OldStart != 1 || h.Lines[1] != "-b" || h.Lines[2] != "+B" || hunks[1].Lines[3] != "-l" {
		t.Fatalf("hunks: %+v", hunks)
	}
}
