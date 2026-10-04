package main

import (
	"fmt"

	"example.com/fix/shapes"
)

func main() {
	sq := shapes.NewSquare(3)
	fmt.Println(sq.ID, shapes.Total([]shapes.Shape{sq}))
}
