package shapes_test

import (
	"testing"

	"example.com/fix/shapes"
)

func TestX(t *testing.T) {
	if shapes.NewSquare(1).Area() != 1 {
		t.Fatal("area")
	}
}
