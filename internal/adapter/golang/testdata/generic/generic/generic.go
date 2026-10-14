// Package generic holds the shapes of the calls the oracle found missing
// out of generic code (#150), one per mechanism.
package generic

import (
	"fmt"
	"sync"
)

// Queue is a generic type no code in the module instantiates, as
// protocompile's slicesx.Queue: its methods' calls must still be edges.
type Queue[E any] struct{ items []E }

// Take is a generic function called only from Queue's methods.
func Take[E any](s []E, n int) []E { return s[:n] }

// PopBack calls a generic function of its own package.
func (q *Queue[E]) PopBack() []E { return Take(q.items, 1) }

// Format calls out of the module.
func (q *Queue[E]) Format() string { return fmt.Sprint(len(q.items)) }

// Res is chameleon's resolver.Resolution: its method calls out of the
// module from a closure, and only another module instantiates it.
type Res[T any] struct {
	once sync.Once
	f    func()
}

// Destroy calls sync.Once.Do with a closure.
func (r *Res[T]) Destroy() {
	r.once.Do(func() {
		if r.f != nil {
			r.f()
		}
	})
}

// Walk is protocompile's linker.resolveInFile: a generic function that
// calls itself, instantiated by UseWalk.
func Walk[T any](xs []T, n int) int {
	if n == 0 {
		return 0
	}
	return Walk(xs, n-1)
}

// UseWalk instantiates Walk.
func UseWalk() int { return Walk([]int{1}, 3) }

// Box is a generic type; IntBox is a defined type over one of its
// instantiations, as protocompile's ast.ExprAny over id.DynNode.
type Box[T any] struct{ v T }

// IntBox has methods of its own.
type IntBox Box[int]

// Span is a method of IntBox.
func (b IntBox) Span() int { return b.v }

// wrapper is unexported and never converted to an interface, as
// protocompile's parser.exprComma.
type wrapper struct{ b IntBox }

// Span calls IntBox.Span.
func (w wrapper) Span() int { return w.b.Span() }
