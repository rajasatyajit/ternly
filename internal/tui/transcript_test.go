package tui

import (
	"fmt"
	"strings"
	"testing"
)

// The virtualised transcript shows exactly what the full content would:
// the old refresh joined every block's rendering (each followed by a
// newline) plus the activity tail and showed a window of it.
func TestTranscriptMatchesFullContent(t *testing.T) {
	var blocks []*block
	for i := range 40 {
		r := fmt.Sprintf("\nblock %d", i)
		for j := range i % 4 {
			r += fmt.Sprintf("\n  line %d.%d", i, j)
		}
		blocks = append(blocks, &block{rendered: r})
	}
	for _, tail := range []string{"", "\n  Thinking…  esc to interrupt\n"} {
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.rendered + "\n")
		}
		all := strings.Split(sb.String()+tail, "\n")
		for _, h := range []int{3, 10, 37, 500} {
			tr := transcript{h: h, follow: true}
			tr.set(blocks, tail)
			window := func(top int) string {
				var out []string
				for i := range h {
					if top+i < len(all) {
						out = append(out, all[top+i])
					} else {
						out = append(out, "")
					}
				}
				return strings.Join(out, "\n")
			}
			if got, want := tr.view(), window(max(0, len(all)-h)); got != want {
				t.Fatalf("h=%d tail=%q bottom:\n%q\nwant\n%q", h, tail, got, want)
			}
			for tr.top > 0 { // scroll to the top, comparing every position
				tr.scroll(-1)
				if got, want := tr.view(), window(tr.top); got != want {
					t.Fatalf("h=%d top=%d:\n%q\nwant\n%q", h, tr.top, got, want)
				}
			}
			if tr.follow && len(all) > h {
				t.Fatal("scrolled up but still following")
			}
			tr.scroll(1 << 20)
			if !tr.follow || !tr.atBottom() {
				t.Fatal("scrolling past the end should follow again")
			}
		}
	}
}
