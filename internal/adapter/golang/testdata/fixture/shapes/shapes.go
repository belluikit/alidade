// Package shapes is the fixture for the Go adapter: an interface, two
// structural implementers (one by pointer receiver), a closure, a generic
// function, and a generated file. Every declaration here is referenced by
// name in extract_test.go, so a change to this file is a change to the
// expected graph.
package shapes

import "math"

// Shape is satisfied structurally by Circle, *Square and GenThing.
type Shape interface {
	Area() float64
}

// Circle implements Shape by value receiver.
type Circle struct {
	Radius float64
}

// Area returns the circle's area.
func (c Circle) Area() float64 { return math.Pi * c.Radius * c.Radius }

// Square implements Shape by pointer receiver only.
type Square struct {
	Side float64
}

// Area returns the square's area.
func (s *Square) Area() float64 { return s.Side * s.Side }

// Scale is a method that does not take part in Shape.
func (s *Square) Scale(f float64) { s.Side *= f }

// Named is a non-struct type with no methods; it references nothing.
type Named int

// Pair references Circle and Square from its fields.
type Pair struct {
	C Circle
	S *Square
}

// Total sums the areas of its shapes through the interface, so the call
// graph resolves Shape.Area to every implementer.
func Total(shapes []Shape) float64 {
	var t float64
	for _, s := range shapes {
		t += s.Area()
	}
	return t
}

// Largest calls Total through a closure, which must fold into Largest.
func Largest(groups [][]Shape) float64 {
	best := 0.0
	each := func(g []Shape) {
		if a := Total(g); a > best {
			best = a
		}
	}
	for _, g := range groups {
		each(g)
	}
	return best
}

// Map is generic; instantiations must fold into this one declaration.
func Map[T any, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

// Areas instantiates Map and calls Area through a method value.
func Areas(cs []Circle) []float64 {
	return Map(cs, Circle.Area)
}

// Unit is a package-level variable referencing Circle.
var Unit = Circle{Radius: 1}

// Sides is a package-level constant.
const Sides = 4

// Stack is a generic type; its method must key on the origin, and it takes
// no part in implements.
type Stack[T any] struct{ items []T }

// Push appends to the stack.
func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

// Circles is an alias; its references are those of the aliased type.
type Circles = []Circle

// Sized embeds Shape, so its method set is Shape's plus Edge.
type Sized interface {
	Shape
	Edge() float64
}

// Edge satisfies Sized together with Area.
func (s *Square) Edge() float64 { return s.Side }

// Registry references Circle through a map and a channel.
type Registry struct {
	ByName map[string]*Circle
	Events chan Circle
	Arr    [2]Named
}

// helper is unexported and recursive.
func helper(n int) int {
	if n == 0 {
		return 0
	}
	return helper(n - 1)
}

func init() { Unit.Radius = 1 }

func init() { registerAll() }

// registerAll is reached only from init.
func registerAll() {}
