package main

import (
	"strings"
	"testing"
)

// Bytes are counted on the row the cursor is on: moved there by CUP, VPA,
// CUU/CUD or a newline.
func TestRowBytes(t *testing.T) {
	in := "\x1b[2;1Habc" + "\x1b[5;1Hxy" + "\x1b[2Aq" + "\nzz"
	var b strings.Builder
	dumpRow = 3
	defer func() { dumpRow = 0 }()
	rowBytes([]byte(in), 40, &b)
	got := b.String()
	for _, want := range []string{"\n2\t9\t", "\n5\t8\t", "\n3\t5\t", "\n4\t3\t", `"q"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
