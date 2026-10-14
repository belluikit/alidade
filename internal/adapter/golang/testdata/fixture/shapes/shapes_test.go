package shapes

import "testing"

func TestTotal(t *testing.T) {
	if Total([]Shape{Circle{Radius: 0}}) != 0 {
		t.Fatal("expected zero")
	}
}

// unit is set by a test-only init, so the package declares init in both a
// production file and a test one: one node, production, positioned at the
// first production init.
var unit float64

func init() { unit = Circle{Radius: 1}.Area() }
