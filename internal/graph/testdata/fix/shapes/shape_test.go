package shapes

import "testing"

func TestTotal(t *testing.T) {
	if Total([]Shape{NewSquare(2)}) != 4 {
		t.Fatal("total")
	}
}
