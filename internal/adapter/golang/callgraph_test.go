// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// TestFunctionValueCallsAreTheValuesThatReachThem pins #94 (the owner's
// decision, ADR 006): a call through a function value is an edge to the
// functions that can reach the site, not to every function of the program
// with a matching signature, which CHA alone gave — 7 edges for this
// fixture's Run. The edge is RESOLVED: VTA's answer is the type flow the
// SSA program establishes.
func TestFunctionValueCallsAreTheValuesThatReachThem(t *testing.T) {
	g, _ := module(t, map[string]string{
		"p/p.go": `package p

func Run(f func()) { f() }

func A() {}
func B() {}
func C() {}

func Use() { Run(A) }

var hook = B

func Fire() { hook() }
`,
	}, DefaultOptions())
	wantEdge(t, g, mID("p#Run"), graph.KindCalls, mID("p#A"), graph.Resolved)
	wantEdge(t, g, mID("p#Fire"), graph.KindCalls, mID("p#B"), graph.Resolved)
	for _, from := range []string{"p#Run", "p#Fire"} {
		var to []graph.ID
		for _, e := range g.Out(mID(from)) {
			if e.Kind == graph.KindCalls {
				to = append(to, e.To)
			}
		}
		if len(to) != 1 {
			t.Errorf("%s calls %v; want only the value that reaches it", from, to)
		}
	}
	wantNoDrops(t, g)
}

// TestInterfaceCallsKeepEveryImplementation pins the other half of #94:
// a call through an interface is CHA's, every implementation of the
// method, because VTA alone loses an implementation whose value is made
// where it cannot see — here, an error from the standard library, whose
// Error a module type implements. The day interface calls go through VTA,
// F stops calling E.Error and this fails.
func TestInterfaceCallsKeepEveryImplementation(t *testing.T) {
	g, _ := module(t, map[string]string{
		"p/p.go": `package p

import "strconv"

type E struct{}

func (E) Error() string { return "e" }

func F() string {
	_, err := strconv.Atoi("x")
	return err.Error()
}
`,
	}, DefaultOptions())
	wantEdge(t, g, mID("p#F"), graph.KindCalls, mID("p#E.Error"), graph.Resolved)
}

// TestInitializerFunctionValueCalls pins that a package-level variable's
// initializer takes the same answer as a function body: an initializer
// calling through a function value calls what reaches it.
func TestInitializerFunctionValueCalls(t *testing.T) {
	g, _ := module(t, map[string]string{
		"p/p.go": `package p

func A() int { return 1 }
func B() int { return 2 }

var pick = A

var V = pick()
`,
	}, DefaultOptions())
	wantEdge(t, g, mID("p#V"), graph.KindCalls, mID("p#A"), graph.Resolved)
	wantNoEdge(t, g, mID("p#V"), graph.KindCalls, mID("p#B"))
}

// TestAnonymousInterfaceMethodValues pins #129: a method of an anonymous
// interface has no declaration, so a call reaching it through a method
// value is no edge — not a drop of an edge to its bare name, and not an
// edge to a package function that shares the name. A named interface's
// method value is still an edge to the interface method.
func TestAnonymousInterfaceMethodValues(t *testing.T) {
	const src = `package p

type x struct{}

func (x) Close() {}

type Closer interface{ Close() }

func run(f func()) { f() }

func T() {
	var c interface{ Close() } = x{}
	run(c.Close)
}

func U() {
	var c Closer = x{}
	run(c.Close)
}
`
	for _, decoy := range []string{"", "\nfunc Close() {}\n"} {
		g, _ := module(t, map[string]string{"p/p.go": src + decoy}, DefaultOptions())
		wantNoEdge(t, g, mID("p#run"), graph.KindCalls, mID("p#Close"))
		wantEdge(t, g, mID("p#run"), graph.KindCalls, mID("p#Closer.Close"), graph.Resolved)
		wantNoDrops(t, g)
	}
}
