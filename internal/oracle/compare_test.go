// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/build"
	"github.com/trendvidia/alidade/internal/graph"
)

// fixture is a module with one of each call the oracle resolves: a static
// call, a concrete method, a method of a generic type, a generic function,
// a call through a named interface and through error, a call leaving the
// module, a call through a function value, a closure, a range over a
// function (Go 1.23), a caller that does both, an init, and package-level
// initializers, one blank. A declaration a //line directive positions
// outside the tree is here too: the Go layer gives it no node (#109), so
// a call to it is a call to no node rather than a missing edge.
var fixture = map[string]string{
	"go.mod": "module example.com/m\n\ngo 1.26\n",
	"p/p.go": `package p

import "strings"

type T struct{}

func (T) M() int { return 1 }

type I interface{ M() int }

func Gen[X any](x X) X { return x }

func helper() int { return 2 }

func Static() int { return helper() + T{}.M() + Gen(3) }

func Through(i I) int { return i.M() }

func Err(err error) string { return err.Error() }

func Out(s string) string { return strings.ToUpper(s) }

func Value(f func() int) int { return f() }

func Closure() int { return func() int { return helper() }() }

type G[X any] struct{}

func (G[X]) Do() int { return helper() }

func Generic() int { return G[int]{}.Do() }

func iter(yield func(int) bool) { _ = yield(1) }

func Ranged() int {
	n := 0
	for v := range iter {
		n += v
	}
	return n
}

func Both(i I, f func() int) int { return i.M() + f() }

func init() { _ = helper() }

var V = helper()

var _ = Out("x")
`,
	"p/lined.go": `package p

func Caller() int { return Lined() }

//line /nowhere/gen.go:1
func Lined() int { return 4 }
`,
}

// broken is a module whose only package does not type-check: there is no
// oracle for it, and the oracle has to say so rather than report having
// read nothing.
var broken = map[string]string{
	"go.mod": "module example.com/broken\n\ngo 1.26\n",
	"q/q.go": "package q\n\nfunc F() int { return missing() }\n",
}

// consts is a module whose only declaration is a package-level const
// holding a call expression: `len` of a constant string. A constant
// expression can only call a builtin or a conversion, so there is
// nothing for either side to resolve.
var consts = map[string]string{
	"go.mod": "module example.com/consts\n\ngo 1.26\n",
	"c/c.go": "package c\n\nconst n = len(\"abc\")\n",
}

// write puts a fixture's files in a temporary directory.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func buildFixture(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir := write(t, fixture)
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return g, dir
}

const pkg = "go://example.com/m/p"

// TestOracleResolvesEachCall: the oracle's calls on the fixture, one per
// shape, and the dynamic and interface facts that explain the rest.
func TestOracleResolvesEachCall(t *testing.T) {
	_, dir := buildFixture(t)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[[2]graph.ID]CallKind{}
	for _, call := range c.Calls {
		got[[2]graph.ID{call.From, call.To}] = call.Kind
	}
	for _, want := range []struct {
		from, to string
		kind     CallKind
	}{
		{pkg + "#Static", pkg + "#helper", Static},
		{pkg + "#Static", pkg + "#T.M", Static},
		{pkg + "#Static", pkg + "#Gen", Static},
		{pkg + "#Through", pkg + "#I.M", Interface},
		{pkg + "#Out", "go://strings", External},
		{pkg + "#Closure", pkg + "#helper", Static},
		{pkg + "#Generic", pkg + "#G.Do", Static},
		{pkg + "#G.Do", pkg + "#helper", Static},
		{pkg + "#Ranged", pkg + "#iter", Static},
		{pkg + "#init", pkg + "#helper", Static},
		{pkg + "#Caller", pkg + "#Lined", Static},
		{pkg + "#V", pkg + "#helper", Static},
		{pkg, pkg + "#Out", Static},
	} {
		if k, ok := got[[2]graph.ID{graph.ID(want.from), graph.ID(want.to)}]; !ok || k != want.kind {
			t.Errorf("%s -> %s: %q, %v; want %q", want.from, want.to, k, ok, want.kind)
		}
	}
	if !c.Dynamic[pkg+"#Value"] || c.Dynamic[pkg+"#Static"] {
		t.Errorf("Dynamic = %v; want Value only", c.Dynamic)
	}
	if !c.Interfaces[pkg+"#Err"]["Error"] {
		t.Errorf("Err's call through error is not recorded: %v", c.Interfaces)
	}
	// Both calls through an interface and through a function value: the
	// two facts that classify weighs against each other.
	if !c.Dynamic[pkg+"#Both"] || !c.Interfaces[pkg+"#Both"]["M"] {
		t.Errorf("Both's two calls are not both recorded: dynamic %v, interfaces %v", c.Dynamic[pkg+"#Both"], c.Interfaces[pkg+"#Both"])
	}
}

// TestClassifyNamesInterfaceDispatchBeforeFunctionValue: a caller that
// calls through a function value explains any edge out of itself, so the
// matched method name has to be tested first, or a caller that does both
// would have its interface dispatch counted as a function value.
func TestClassifyNamesInterfaceDispatchBeforeFunctionValue(t *testing.T) {
	g, dir := buildFixture(t)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	e := graph.Edge{From: pkg + "#Both", To: pkg + "#T.M", Kind: graph.KindCalls, Derivation: graph.Resolved}
	if got := classify(g, c, map[[2]graph.ID]bool{}, e); got != InterfaceDispatch {
		t.Errorf("classify(%s -> %s) = %q; want %q", e.From, e.To, got, InterfaceDispatch)
	}
}

// TestCompareCountsCallsToNoNode: the Go layer gives a declaration a
// //line directive positions outside the tree no node (#109), so the
// oracle's call to it is no edge the graph could have had — counted
// apart, not reported as a miss the layer could fix.
func TestCompareCountsCallsToNoNode(t *testing.T) {
	g, dir := buildFixture(t)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Node(pkg + "#Lined"); ok {
		t.Fatalf("%s#Lined has a node: the //line case this test rests on is gone", pkg)
	}
	r := Compare(g, c)
	if r.NoTarget != 1 {
		t.Errorf("NoTarget = %d; want 1 (the call to the //line declaration)", r.NoTarget)
	}
	for _, m := range r.Missing {
		if m.To == pkg+"#Lined" {
			t.Errorf("the call to a declaration with no node is reported as a miss: %v", m)
		}
	}
}

// TestCompareSeesMissesAndStrayEdges: on the fixture the graph and the
// oracle agree; with one real edge removed the comparison reports that
// miss, and with one bogus edge added it reports that edge unexplained —
// the proof that a clean result is not the harness failing to look.
func TestCompareSeesMissesAndStrayEdges(t *testing.T) {
	g, dir := buildFixture(t)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	clean := Compare(g, c)
	if len(clean.Missing) != 0 || len(clean.Unexplained) != 0 || clean.Found[Static] == 0 {
		t.Fatalf("the fixture's graph and oracle disagree:\n%s", clean)
	}
	removed := [2]graph.ID{pkg + "#Static", pkg + "#helper"}
	bogus := graph.Edge{From: pkg + "#Out", To: pkg + "#helper", Kind: graph.KindCalls, Derivation: graph.Resolved}
	r := Compare(rebuilt(t, g, dir, graph.KindCalls, removed, bogus), c)
	if len(r.Missing) != 1 || r.Missing[0].From != removed[0] || r.Missing[0].To != removed[1] {
		t.Errorf("Missing = %v; want only the removed edge", r.Missing)
	}
	if len(r.Unexplained) != 1 || r.Unexplained[0].From != bogus.From || r.Unexplained[0].To != bogus.To {
		t.Errorf("Unexplained = %v; want only the bogus edge", r.Unexplained)
	}
}

// rebuilt copies g without the edge of kind removed and with the edge
// added, and any nodes the added edge needs.
func rebuilt(t *testing.T, g *graph.Graph, dir string, kind graph.EdgeKind, removed [2]graph.ID, added graph.Edge, nodes ...graph.Node) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder(dir)
	for _, n := range nodes {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for n := range g.Nodes() {
		n.Pos.File = graph.FilePath(n.Pos.File)
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for e := range g.Edges() {
		if e.Kind == kind && e.From == removed[0] && e.To == removed[1] {
			continue
		}
		if err := b.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.AddEdge(added); err != nil {
		t.Fatal(err)
	}
	return b.Build()
}

// TestCompareImportsSeesDifferences: on the fixture the graph's imports
// are go list's; with one removed and one bogus added, the comparison
// reports exactly those.
func TestCompareImportsSeesDifferences(t *testing.T) {
	g, dir := buildFixture(t)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean := CompareImports(g, c); clean.Expected == 0 || len(clean.Missing) != 0 || len(clean.Extra) != 0 {
		t.Fatalf("the fixture's imports disagree:\n%s", clean)
	}
	removed := [2]graph.ID{pkg, "go://strings"}
	bogus := graph.Edge{From: pkg, To: "go://fmt", Kind: graph.KindImports, Derivation: graph.Extracted}
	fmtPkg := graph.Node{ID: "go://fmt", Kind: graph.KindPackage, Name: "fmt", Attrs: map[string]string{"external": "true"}}
	r := CompareImports(rebuilt(t, g, dir, graph.KindImports, removed, bogus, fmtPkg), c)
	if len(r.Missing) != 1 || r.Missing[0] != removed {
		t.Errorf("Missing = %v; want only the removed import", r.Missing)
	}
	if len(r.Extra) != 1 || r.Extra[0] != [2]graph.ID{bogus.From, bogus.To} {
		t.Errorf("Extra = %v; want only the bogus import", r.Extra)
	}
}

// TestOracleNamesPackagesThatDoNotTypeCheck: a package the type checker
// cannot read is no oracle, and its calls leave recall's denominator —
// so the oracle names it and the modules it walked, rather than
// reporting a count of zero with nothing to read it against.
func TestOracleNamesPackagesThatDoNotTypeCheck(t *testing.T) {
	dir := write(t, broken)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Packages != 0 {
		t.Fatalf("Packages = %d; want 0, the package does not type-check", c.Packages)
	}
	if len(c.Failed) != 1 || !strings.Contains(c.Failed[0], "example.com/broken/q") {
		t.Errorf("Failed = %v; want the one package that does not type-check", c.Failed)
	}
	if len(c.Modules) != 1 || c.Modules[0] != "example.com/broken" {
		t.Errorf("Modules = %v; want the one module walked", c.Modules)
	}
}

// TestRecallOfNothingIsNotOne: a kind the oracle named none of has no
// recall. Reported as 1 it reads as a pass, and would hide whatever
// emptied the denominator.
func TestRecallOfNothingIsNotOne(t *testing.T) {
	r := &Result{Expected: map[CallKind]int{}, Found: map[CallKind]int{}, Edges: map[Class]int{}}
	if v, ok := r.Recall(Interface); ok {
		t.Errorf("Recall(interface) = %v, %v; want no figure", v, ok)
	}
	if !strings.Contains(r.String(), "none of this kind") {
		t.Errorf("String() gives a figure for a kind with nothing expected:\n%s", r)
	}
}

// TestOracleReadsVariableInitializersOnly: the oracle attributes calls
// to a function, a method and a package-level variable, as the Go layer
// does — its initializerSpans takes `gd.Tok != token.VAR` out. A const's
// initializer is read by neither side, so its call expressions are no
// call sites: counted, they would inflate the positive marker that the
// oracle ran with sites no edge could ever come from.
func TestOracleReadsVariableInitializersOnly(t *testing.T) {
	dir := write(t, consts)
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Packages != 1 {
		t.Fatalf("Packages = %d; want 1, the package type-checks: %v %v", c.Packages, c.Failed, c.Unloaded)
	}
	if c.Sites != 0 {
		t.Errorf("Sites = %d; want 0: a const's initializer is not a declaration the oracle reads", c.Sites)
	}
}

// usesFixture has one use of a constant and of a variable per shape, and
// the near misses: a local and a field of the same name (#180).
var usesFixture = map[string]string{
	"go.mod": "module example.com/u\n\ngo 1.26\n",
	"u/u.go": `package u

const name = "n"

var limit = 1

type S struct{ limit int }

func Read() string { return name }

func (s S) Field() int { return s.limit }

func (S) Global() int { return limit }

func Local() string {
	name := "x"
	return name
}

func Closure() func() int { return func() int { return limit } }
`,
}

// TestUsesAgree: the oracle's uses and the graph's agree on the fixture,
// and a dropped and a bogus oracle use are each caught.
func TestUsesAgree(t *testing.T) {
	dir := write(t, usesFixture)
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	c, err := GoCalls(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Uses) != 3 {
		t.Fatalf("oracle uses %v, want Read, Global and Closure's", c.Uses)
	}
	if r := CompareUses(g, c); r.Uses[0] != r.Uses[1] || len(r.Extra) != 0 {
		t.Fatalf("not exact:\n%s", r)
	}
	c.Uses = append(c.Uses[1:], [2]graph.ID{"go://example.com/u/u#Local", "go://example.com/u/u#name"})
	if r := CompareUses(g, c); len(r.Missing) != 1 || len(r.Extra) != 1 {
		t.Errorf("missing %v, extra %v; want one of each", r.Missing, r.Extra)
	}
}
