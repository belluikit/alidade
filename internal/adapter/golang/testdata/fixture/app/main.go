// Package app is the hand-written caller of shapes, and the one place the
// fixture crosses the module boundary (fmt, math via shapes).
package app

import (
	"fmt"

	"example.com/fixture/shapes"
)

// Run builds a few shapes and prints their total.
func Run() {
	total := shapes.Total([]shapes.Shape{shapes.Circle{Radius: 2}, &shapes.Square{Side: 3}})
	fmt.Println(total)
}
