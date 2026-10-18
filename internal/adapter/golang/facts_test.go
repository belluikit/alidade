// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"errors"
	"github.com/trendvidia/caliper/pkg/analysis"
	"go/build"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// module writes a module of the given files under a temporary root and
// extracts it.
func module(t *testing.T, files map[string]string, opts Options) (*graph.Graph, *Report) {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/m\n\ngo 1.26\n"
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
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return b.Build(), rep
}

func wantM(t *testing.T, g *graph.Graph, from string, kind graph.EdgeKind, to string) {
	t.Helper()
	if _, ok := hasEdge(g, graph.ID("go://example.com/m/"+from), kind, graph.ID("go://example.com/m/"+to)); !ok {
		t.Errorf("no %s edge %s -> %s", kind, from, to)
	}
}

// TestVarInitializerCalls pins #95: a call made by a package-level
// variable's initializer, directly or in a closure assigned to it, is an
// edge from that variable, not lost with the package's synthetic init.
func TestVarInitializerCalls(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type Thing struct{}

func NewThing() *Thing { return &Thing{} }

func Helper() {}

func seeded(n int) int { return n }

var X = NewThing()

var Handler = func() { Helper() }

var a, b = seeded(1), seeded(2)
`}, DefaultOptions())
	wantM(t, g, "p#X", graph.KindCalls, "p#NewThing")
	wantM(t, g, "p#Handler", graph.KindCalls, "p#Helper")
	wantM(t, g, "p#a", graph.KindCalls, "p#seeded")
	wantM(t, g, "p#b", graph.KindCalls, "p#seeded")
}

// TestRecursionThroughAClosure pins #95: a function calling itself from a
// closure is recursion, and an edge.
func TestRecursionThroughAClosure(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

func Rec(n int) {
	func() {
		if n > 0 {
			Rec(n - 1)
		}
	}()
}

func Plain() { func() {}() }
`}, DefaultOptions())
	wantM(t, g, "p#Rec", graph.KindCalls, "p#Rec")
	if _, ok := hasEdge(g, "go://example.com/m/p#Plain", graph.KindCalls, "go://example.com/m/p#Plain"); ok {
		t.Error("a function calling its own closure is not recursion")
	}
}

// TestInterfaceMethodsHaveCallers pins #95: a call through an interface is
// an edge to the interface method the type checker resolved, as well as
// to the implementations the call graph finds — and the only edge when
// the program has none.
func TestInterfaceMethodsHaveCallers(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type Iface interface{ M() }

type Lonely interface{ N() }

type MyInt int

func (MyInt) M() {}

func ViaIface(i Iface) { i.M() }

func ViaLonely(l Lonely) { l.N() }

func ViaLocal(v any) {
	type equaler interface{ Equal(any) bool }
	if e, ok := v.(equaler); ok {
		e.Equal(v)
	}
}
`}, DefaultOptions())
	wantM(t, g, "p#ViaIface", graph.KindCalls, "p#Iface.M")
	wantM(t, g, "p#ViaIface", graph.KindCalls, "p#MyInt.M")
	wantM(t, g, "p#ViaLonely", graph.KindCalls, "p#Lonely.N")
	// A type declared inside a function has no node: a call through it
	// is no edge, rather than a dropped one.
	for _, d := range g.Dropped() {
		t.Errorf("dropped %+v", d)
	}
}

// TestAliasReceiverIsTheTypesMethod pins #95: a method declared through
// an alias receiver is the aliased type's method, one ID for the node and
// for its callers.
func TestAliasReceiverIsTheTypesMethod(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type T struct{}

type A = T

func (A) M() {}

func Use() { var a A; a.M() }
`}, DefaultOptions())
	wantM(t, g, "p#Use", graph.KindCalls, "p#T.M")
	if _, ok := g.Node("go://example.com/m/p#A.M"); ok {
		t.Error("the method has a second ID under the alias")
	}
	for _, d := range g.Dropped() {
		t.Errorf("dropped %+v", d)
	}
}

// TestTypeParameterConstraintsAreReferences pins #95: a constraint is a
// type the declaration names.
func TestTypeParameterConstraintsAreReferences(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type Iface interface{ M() }

type Number interface{ ~int | ~float64 }

func G[T Iface](x T) {}

type Box[N Number] struct{ v N }
`}, DefaultOptions())
	wantM(t, g, "p#G", graph.KindReferences, "p#Iface")
	wantM(t, g, "p#Box", graph.KindReferences, "p#Number")
}

// TestTestDirectoryNameIsNotATest pins #95: a production package in a
// directory named like an external test package is not a test.
func TestTestDirectoryNameIsNotATest(t *testing.T) {
	g, rep := module(t, map[string]string{"e2e_test/e.go": "package e2e\n\nfunc Run() {}\n"}, DefaultOptions())
	if n, ok := g.Node("go://example.com/m/e2e_test"); !ok || n.Test {
		t.Errorf("package node = %+v, %v; want present and not a test", n, ok)
	}
	if rep.Tests != 0 {
		t.Errorf("Tests = %d without -tests", rep.Tests)
	}
	// A real external test package still is one.
	opts := DefaultOptions()
	opts.Tests = true
	g, _ = module(t, map[string]string{
		"e2e_test/e.go": "package e2e\n\nfunc Run() {}\n",
		"q/q.go":        "package q\n\nfunc Q() {}\n",
		"q/q_test.go":   "package q_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/q\"\n)\n\nfunc TestQ(t *testing.T) { q.Q() }\n",
	}, opts)
	if n, ok := g.Node("go://example.com/m/q_test"); !ok || !n.Test {
		t.Errorf("external test package = %+v, %v; want marked a test", n, ok)
	}
	if n, ok := g.Node("go://example.com/m/e2e_test"); !ok || n.Test {
		t.Errorf("with tests, package e2e = %+v, %v; want not a test", n, ok)
	}
}

// TestRootExternalTestPackage pins #45: the external test package of a
// module's root package, m_test, is the module's, and with tests its
// declarations are in the graph as those of a subpackage's are. A module
// whose path is m_test itself is still its own.
func TestRootExternalTestPackage(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, rep := module(t, map[string]string{
		"m.go":      "package m\n\nfunc M() {}\n",
		"m_test.go": "package m_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m\"\n)\n\nfunc BenchmarkM(b *testing.B) { m.M() }\n",
	}, opts)
	if n, ok := g.Node("go://example.com/m_test#BenchmarkM"); !ok || !n.Test {
		t.Errorf("m_test#BenchmarkM = %+v, %v; want a test declaration", n, ok)
	}
	if !slices.ContainsFunc(g.Out("go://example.com/m_test#BenchmarkM"), func(e graph.Edge) bool { return e.To == "go://example.com/m#M" }) {
		t.Error("m_test#BenchmarkM has no edge to m#M")
	}
	if rep.Tests == 0 {
		t.Error("Tests = 0")
	}
	for _, tt := range []struct{ pkg, want string }{
		{"example.com/m_test", "example.com/m"},
		{"example.com/m_testing", ""},
		{"example.com/m/q_test", "example.com/m"},
	} {
		if got := Owner(tt.pkg, []string{"example.com/m"}); got != tt.want {
			t.Errorf("Owner(%s) = %q, want %q", tt.pkg, got, tt.want)
		}
	}
	if got := Owner("example.com/m_test", []string{"example.com/m", "example.com/m_test"}); got != "example.com/m_test" {
		t.Errorf("Owner(example.com/m_test) beside a module of that path = %q, want that module", got)
	}
}

// TestPackagesWithNoProductionFilesAreTests pins #113 (the owner's
// decision, ADR 004 rule 4): a package whose files on disk are all
// _test.go files is a test package, with tests loaded or not — a build
// without tests has no such package — whatever its name. Files are
// counted on disk, not in the build: a package whose only production file
// is for another platform stays a production package on this host, and a
// package with production and test files is production. A directory of
// external test files only (gameflow's paperstate) is one too.
func TestPackagesWithNoProductionFilesAreTests(t *testing.T) {
	files := map[string]string{
		"p/p.go":             "package p\n\nfunc P() {}\n",
		"p/p_test.go":        "package p\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) { P() }\n",
		"acc/acc_test.go":    "package acc\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/p\"\n)\n\nfunc TestA(t *testing.T) { p.P() }\n",
		"plat/plat_other.go": "//go:build " + otherGOOS() + "\n\npackage plat\n\nfunc Plat() {}\n",
		"plat/plat_test.go":  "package plat\n\nimport \"testing\"\n\nfunc TestPlat(t *testing.T) {}\n",
		// Only external test files: the loader still reports a package
		// of the directory's own name, which declares nothing.
		"ext/ext_test.go": "package ext_test\n\nimport \"testing\"\n\nfunc TestExt(t *testing.T) {}\n",
	}
	for _, tests := range []bool{false, true} {
		opts := DefaultOptions()
		opts.Tests = tests
		g, _ := module(t, files, opts)
		for pkg, want := range map[string]bool{"acc": true, "ext": true, "p": false, "plat": false} {
			if n, ok := g.Node(mID(pkg)); !ok || n.Test != want {
				t.Errorf("tests=%v: package %s = %+v, %v; want present, test=%v", tests, pkg, n, ok, want)
			}
		}
	}
}

// otherGOOS is a GOOS that is not the host's, for a file no load of the
// host's configuration includes.
func otherGOOS() string {
	if runtime.GOOS == "plan9" {
		return "linux"
	}
	return "plan9"
}

// TestHandWrittenMethodsOfAGeneratedType: a generated type that carries a
// hand-written method is in the graph with both its methods, the
// hand-written one with its calls, under the same settings whether tests
// are loaded or not — generated code is always in (#126, which removed the
// option that left it out and made #111's question moot).
func TestHandWrittenMethodsOfAGeneratedType(t *testing.T) {
	files := map[string]string{
		"p/msg.pb.go": "// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage p\n\ntype Msg struct{}\n\nfunc (Msg) Reset() {}\n",
		"p/v.go":      "package p\n\ntype Validator interface{ Validate() }\n\nfunc helper() {}\n\nfunc (Msg) Validate() { helper() }\n\nfunc Use(m *Msg) { m.Validate() }\n",
	}
	for _, tests := range []bool{false, true} {
		opts := DefaultOptions()
		opts.Tests = tests
		g, _ := module(t, files, opts)
		if n := wantNode(t, g, mID("p#Msg"), graph.KindType); !n.Generated {
			t.Errorf("tests=%v: Msg is not marked generated", tests)
		}
		if n := wantNode(t, g, mID("p#Msg.Validate"), graph.KindMethod); n.Generated {
			t.Errorf("tests=%v: the hand-written Validate is marked generated", tests)
		}
		wantM(t, g, "p#Msg.Validate", graph.KindCalls, "p#helper")
		wantM(t, g, "p#Msg", graph.KindImplements, "p#Validator")
		wantM(t, g, "p#Use", graph.KindReferences, "p#Msg")
		wantNoDrops(t, g)
	}
}

// TestTestsKeepProductionImplements pins #95: loading tests makes a
// package's test variant the one emitted, and its types a universe apart
// from the plain package the rest of the module imports; satisfaction is
// decided in the plain one, so -tests loses no production implements edge.
func TestTestsKeepProductionImplements(t *testing.T) {
	files := map[string]string{
		"a/a.go":      "package a\n\ntype Arg struct{}\n\ntype Doer interface{ Do(*Arg) }\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\ntype fake struct{}\n\nfunc (fake) Do(*Arg) {}\n\nfunc TestX(t *testing.T) {}\n",
		"b/b.go":      "package b\n\nimport \"example.com/m/a\"\n\ntype Impl struct{}\n\nfunc (Impl) Do(*a.Arg) {}\n",
	}
	for _, tests := range []bool{false, true} {
		opts := DefaultOptions()
		opts.Tests = tests
		g, _ := module(t, files, opts)
		if _, ok := hasEdge(g, "go://example.com/m/b#Impl", graph.KindImplements, "go://example.com/m/a#Doer"); !ok {
			t.Errorf("tests=%v: b.Impl does not implement a.Doer", tests)
		}
		// A test type satisfies its package's interface in the test
		// variant's universe, the only one it is in.
		if _, ok := hasEdge(g, "go://example.com/m/a#fake", graph.KindImplements, "go://example.com/m/a#Doer"); tests && !ok {
			t.Error("tests=true: the test type a.fake does not implement a.Doer")
		}
	}
}

// TestGoVersionTagsAreTheToolchains pins #112: a "//go:build go1.N" file
// is in the host configuration exactly when the toolchain that loads its
// module has reached go1.N — the go command run in the module, not the
// toolchain alidade was built with. A go1.27 binary on a go1.26 host must
// read a go1.27 file as excluded, and a tag above build.Default's newest
// is true when the loading toolchain is that new.
func TestGoVersionTagsAreTheToolchains(t *testing.T) {
	newest := 0
	for _, tag := range build.Default.ReleaseTags {
		if n, err := strconv.Atoi(strings.TrimPrefix(tag, "go1.")); err == nil && n > newest {
			newest = n
		}
	}
	above := "go1." + strconv.Itoa(newest+1)
	for _, tt := range []struct {
		loader, expr string
		want         bool
	}{
		{"go1.26.3", "go1.27", false},
		{"go1.27.0", "go1.27", true},
		{"go1.27rc1", "linux && go1.21", true},
		{"devel go1.28-abcdef Tue", "go1.28", true},
		{above + ".0", above, true},
		{"go1.20", above, false},
	} {
		got := withToolchain(t, tt.loader, func(dir string) bool {
			fc, err := readConstraint(writeGoFile(t, dir, "//go:build "+tt.expr))
			if err != nil {
				t.Fatal(err)
			}
			return fc.satisfied(config{goos: "linux", goarch: "amd64"})
		})
		if got != tt.want {
			t.Errorf("loader %s, //go:build %s: satisfied = %v, want %v", tt.loader, tt.expr, got, tt.want)
		}
	}
}

// withToolchain runs f over a fresh module whose go command reports
// version, and restores the real toolchain after.
func withToolchain(t *testing.T, version string, f func(dir string) bool) bool {
	t.Helper()
	real := goVersion
	t.Cleanup(func() { goVersion = real })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked string
	goVersion = func(d string) (string, error) { asked = d; return version, nil }
	got := f(dir)
	if asked != dir {
		t.Errorf("the go command was asked in %q, not the module root %q", asked, dir)
	}
	return got
}

func writeGoFile(t *testing.T, dir, header string) string {
	t.Helper()
	p := filepath.Join(dir, "sub", "f.go")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(header+"\n\npackage sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// mID is a node of the fixture module of module().
func mID(s string) graph.ID { return graph.ID("go://example.com/m/" + s) }

func wantNoDrops(t *testing.T, g *graph.Graph) {
	t.Helper()
	for _, d := range g.Dropped() {
		t.Errorf("dropped %+v", d)
	}
}

// TestInterfaceMethodsAreNotCallers pins the other half of #95's
// interface-call item: an interface method has no body, so it is never
// the from of a calls edge. Every SSA function that gives one as its
// declaration is the builder's — a method value's bound closure, a
// method expression's thunk, the promotion wrapper of a type embedding
// the interface — and attributing its one dispatch to the method made
// the method call itself, which
// TestSyntheticFunctionsFoldIntoDeclarations forbids, and call an
// implementation it cannot name.
func TestInterfaceMethodsAreNotCallers(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type Iface interface{ M() }

type T struct{}

func (T) M() {}

type Embeds struct{ Iface }

func Value(i Iface) func() { return i.M }

func Expr() func(Iface) { return Iface.M }

func Convert(e Embeds) Iface { return e }

func Make() Iface { return T{} }
`}, DefaultOptions())
	wantNoEdge(t, g, mID("p#Iface.M"), graph.KindCalls, mID("p#Iface.M"))
	wantNoEdge(t, g, mID("p#Iface.M"), graph.KindCalls, mID("p#T.M"))
	if out := g.Out(mID("p#Iface.M")); len(out) != 0 {
		t.Errorf("the interface method calls %d things", len(out))
	}
	wantNoDrops(t, g)
}

// TestAnonymousInterfaceMethodsAreNotCallers pins the sharpest case of
// the same rule: an anonymous interface's method has no receiver name,
// so its ID is the bare method name — the ID of a package-level function
// of that name. Attributing the wrapper's dispatch to it put a call on a
// declaration that never made it, which is the wrong edge ADR 002 rule 3
// exists to refuse.
func TestAnonymousInterfaceMethodsAreNotCallers(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type T struct{}

func (T) M() {}

func M() { helper() }

func helper() {}

func Value(i interface{ M() }) func() { return i.M }

func Feed() { Value(T{}) }
`}, DefaultOptions())
	wantM(t, g, "p#M", graph.KindCalls, "p#helper")
	wantNoEdge(t, g, mID("p#M"), graph.KindCalls, mID("p#T.M"))
	wantNoDrops(t, g)
}

// TestInterfaceCallsFromAVariableInitializer pins #95's interface-call
// item where its var-initializer item meets it: an initializer naming an
// interface method reaches it the same way a function body does. The
// call graph has no implementation to offer here, so this edge is the
// only one there is.
func TestInterfaceCallsFromAVariableInitializer(t *testing.T) {
	g, _ := module(t, map[string]string{"p/p.go": `package p

type Iface interface{ M() int }

var src Iface

var V = src.M()

var Lazy = func() int { return src.M() }
`}, DefaultOptions())
	wantM(t, g, "p#V", graph.KindCalls, "p#Iface.M")
	wantM(t, g, "p#Lazy", graph.KindCalls, "p#Iface.M")
	wantNoDrops(t, g)
}

// TestGeneratedVariableInitializers: a generated variable's initializer
// calls are edges from it, and a generated blank variable's from the
// package, as a hand-written one's are (#95; generated code is always in
// since #126).
func TestGeneratedVariableInitializers(t *testing.T) {
	g, _ := module(t, map[string]string{
		"p/zz.pb.go": "// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage p\n\nvar Registry = newRegistry()\n\nvar _ = newRegistry()\n",
		"p/v.go":     "package p\n\ntype R struct{}\n\nfunc newRegistry() *R { return &R{} }\n\nvar Kept = newRegistry()\n",
	}, DefaultOptions())
	if n := wantNode(t, g, mID("p#Registry"), graph.KindVar); !n.Generated {
		t.Error("Registry is not marked generated")
	}
	wantM(t, g, "p#Registry", graph.KindCalls, "p#newRegistry")
	wantM(t, g, "p#Kept", graph.KindCalls, "p#newRegistry")
	wantEdge(t, g, mID("p"), graph.KindCalls, mID("p#newRegistry"), graph.Resolved)
	wantNoDrops(t, g)
}

// TestOneCallInitializingSeveralVariables exercises the branch of
// valueSpans that the fixtures of #95 do not: one call initializing
// several names, where the span belongs to all of them at once — each is
// an edge, generated or not.
func TestOneCallInitializingSeveralVariables(t *testing.T) {
	g, _ := module(t, map[string]string{
		"p/zz.pb.go": "// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage p\n\nvar G1, G2 = pair()\n",
		"p/v.go":     "package p\n\nfunc pair() (int, int) { return 1, 2 }\n\nvar K1, K2 = pair()\n\nvar u1, u2 int\n",
	}, DefaultOptions())
	for _, n := range []string{"p#G1", "p#G2", "p#K1", "p#K2"} {
		wantM(t, g, n, graph.KindCalls, "p#pair")
	}
	wantNoEdge(t, g, mID("p"), graph.KindCalls, mID("p#pair"))
	wantNoDrops(t, g)
}

// TestBrokenPackagesAreThereAndSaySo pins #95 on caliper#38: a package
// mid-edit is in the graph with its error and the declarations the type
// checker resolved, so calls into it land; and a clean package cut off
// from the call graph by a broken import says so.
func TestBrokenPackagesAreThereAndSaySo(t *testing.T) {
	g, rep := module(t, map[string]string{
		// An error inside a function body.
		"b/b.go": "package b\n\nfunc B() int { return \"wip\" }\n\nfunc Other() {}\n",
		"a/a.go": "package a\n\nimport \"example.com/m/b\"\n\nfunc A() { b.B() }\n",
		// An error outside one: a declaration, which the call graph
		// cannot build around.
		"d/d.go": "package d\n\nvar Broken int = \"x\"\n\nfunc D() {}\n",
		"e/e.go": "package e\n\nimport \"example.com/m/d\"\n\nfunc E() { d.D() }\n",
		"c/c.go": "package c\n\nfunc C() {}\n",
	}, DefaultOptions())

	for id, want := range map[string][2]string{
		// The broken packages carry the compiler's own message, at its
		// position: no "# package" header of the go tool's, and nothing
		// of go/packages' "-: " for a message that has no position.
		"b":   {"error", "b/b.go:3:23: cannot use \"wip\" (untyped string constant) as int value in return statement"},
		"d":   {"error", "d/d.go:3:18: cannot use \"x\" (untyped string constant) as int value in variable declaration"},
		"b#B": {"partial", "true"}, "d#D": {"partial", "true"}, // what resolved in them
		"c#C": {"partial", ""}, // a clean declaration is not partial
		"e":   {"calls_unavailable", "example.com/m/d"},
	} {
		n, ok := g.Node(graph.ID("go://example.com/m/" + id))
		if got := n.Attrs[want[0]]; !ok || got != want[1] {
			t.Errorf("%s = %+v, %v; want %s %q", id, n, ok, want[0], want[1])
		}
	}
	wantM(t, g, "a#A", graph.KindCalls, "b#B")
	if got := strings.Join(rep.FailedPackages, " "); got != "example.com/m/b example.com/m/d" {
		t.Errorf("FailedPackages = %q", got)
	}
	if got := strings.Join(rep.WithoutCalls, " "); got != "example.com/m/e" {
		t.Errorf("WithoutCalls = %q", got)
	}
	for _, d := range g.Dropped() {
		t.Errorf("dropped %+v", d)
	}
}

// platformOnlyFiles are a module with a package only windows builds,
// one only the plugin tag includes, and one only plan9 builds.
func platformOnlyFiles() map[string]string {
	return map[string]string{
		"win/w_windows.go": "package win\n\nfunc W() {}\n",
		"plug/main.go":     "//go:build plugin\n\npackage main\n\nfunc Plug() {}\n",
		"nine/n.go":        "//go:build plan9\n\npackage nine\n",
		"p/p.go":           "package p\n\nfunc P() {}\n",
	}
}

// TestPlatformOnlyPackages pins #95: a package whose every file is for
// another platform is never matched by ./..., so no load names it. It is
// recorded as skipped on the host, saying whether -go-configs auto would
// bring it in: since #48 also one only a build tag includes; not one no
// platform auto considers builds, with its tags or without.
func TestPlatformOnlyPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform-only package is the host's")
	}
	g, rep := module(t, platformOnlyFiles(), DefaultOptions())
	if _, ok := g.Node("go://example.com/m/win"); ok {
		t.Fatal("the windows-only package loaded on the host")
	}
	reasons := map[string]string{}
	for _, s := range rep.Skipped {
		reasons[s.File] = s.Reason
	}
	for dir, want := range map[string]string{
		"win":  "-go-configs auto would load another platform's",
		"plug": "-go-configs auto would load them with their tags",
		"nine": "for every platform -go-configs auto considers",
	} {
		if !strings.Contains(reasons[dir], want) {
			t.Errorf("%s: reason %q, want %q", dir, reasons[dir], want)
		}
	}
	// A reason reaches the build's output, where something
	// machine-specific makes two builds of one tree disagree
	// (graph.SkipReason), and go_build already says which platform this
	// build read.
	for f, r := range reasons {
		if strings.Contains(r, runtime.GOOS) || strings.Contains(r, runtime.GOARCH) {
			t.Errorf("%s: reason %q names the host platform", f, r)
		}
	}
}

// TestAutoBringsInPlatformOnlyPackages: -go-configs auto reads the
// package only windows builds, and the one only the plugin tag includes
// with the tag set, its build attribute saying so (#95, #48); the one no
// platform auto considers builds stays skipped.
func TestAutoBringsInPlatformOnlyPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform-only package is the host's")
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	g, rep := module(t, platformOnlyFiles(), opts)
	if _, ok := g.Node("go://example.com/m/win#W"); !ok {
		t.Errorf("-go-configs auto did not bring in win#W; configs %v", rep.Configs)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].File != "nine" {
		t.Errorf("Skipped = %+v after auto; want only nine, which no platform auto considers builds", rep.Skipped)
	}
	if n, ok := g.Node("go://example.com/m/plug#Plug"); !ok || !strings.HasSuffix(n.Attrs["build"], "+plugin") {
		t.Errorf("plug#Plug = %+v, %v; want it read with the plugin tag set, and its build attribute saying so", n, ok)
	}
}

// TestOnlyTheTestVariantFailed pins #95: when the plain package loads and
// only its test variant does not, go_packages_failed names the package
// path — never the go/packages ID, which reads "p [p.test]" and holds a
// space the space-joined field cannot carry — and the error still reaches
// that path's node, so every path named there answers get_node with it.
func TestOnlyTheTestVariantFailed(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, rep := module(t, map[string]string{
		"p/p.go":      "package p\n\nfunc P() {}\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) { var _ int = \"bad\" }\n",
	}, opts)
	if got := rep.FailedPackages; len(got) != 1 || got[0] != "example.com/m/p" {
		t.Fatalf("FailedPackages = %q", got)
	}
	for _, p := range rep.FailedPackages {
		if strings.ContainsAny(p, " \t") {
			t.Errorf("FailedPackages holds %q, which go_packages_failed joins with spaces", p)
		}
		n, ok := g.Node(graph.ID("go://" + p))
		if !ok {
			t.Fatalf("no node for %q", p)
		}
		if want := "p/p_test.go:5:40: cannot use \"bad\" (untyped string constant) as int value in variable declaration"; n.Attrs["error"] != want {
			t.Errorf("%s error = %q, want %q", p, n.Attrs["error"], want)
		}
	}
	// The production declaration is still whole: only the test variant
	// failed, so nothing of it is partial.
	if n, ok := g.Node("go://example.com/m/p#P"); !ok || n.Attrs["partial"] != "" {
		t.Errorf("p#P = %+v, %v; want a node that is not partial", n, ok)
	}
}

// TestRecoveredDirectoryIsNotSkipped pins #95: a directory one chosen
// configuration brings in is in the graph, so it is not reported skipped
// for the files that stay excluded — those are named in the package
// node's "excluded" attribute, and a skip would say the package is absent
// when it is not.
func TestRecoveredDirectoryIsNotSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform-only package is the host's")
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	g, rep := module(t, map[string]string{
		"win/w_windows.go": "package win\n\nfunc W() {}\n",
		"win/tagged.go":    "//go:build plan9\n\npackage win\n\nfunc T() {}\n",
		"p/p.go":           "package p\n\nfunc P() {}\n",
	}, opts)
	if _, ok := g.Node("go://example.com/m/win#W"); !ok {
		t.Fatalf("-go-configs auto did not bring in win#W; configs %v", rep.Configs)
	}
	for _, s := range rep.Skipped {
		t.Errorf("skipped %+v, but win is in the graph", s)
	}
	if n, _ := g.Node("go://example.com/m/win"); n.Attrs["excluded"] != "tagged.go" {
		t.Errorf("win = %+v; want the one file no configuration builds under excluded", n)
	}
}

// TestUnreadableGoFileCostsOneFact pins #92 on #95's walk: the candidate
// files now come from a directory walk rather than from the load, so one
// of them can be unreadable. That must cost the one file, reported
// skipped, and never the Go layer — a failed layer is never kept (#93),
// so an error here is the whole repository's Go graph.
func TestUnreadableGoFileCostsOneFact(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"p/p.go": "package p\n\nfunc P() {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A dangling symlink named *.go, in a directory no package came
	// from: ./... never matches it, so only the walk sees it.
	if err := os.MkdirAll(filepath.Join(dir, "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone.go"), filepath.Join(dir, "q", "broken.go")); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatalf("Extract = %v; one unreadable file must cost one fact, not the layer", err)
	}
	if _, ok := b.Build().Node("go://example.com/m/p#P"); !ok {
		t.Error("the readable package is missing")
	}
	var got []string
	for _, s := range rep.Skipped {
		got = append(got, s.File+": "+s.Reason)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "q/broken.go: open: ") {
		t.Errorf("Skipped = %q; want the one unreadable file, with why", got)
	}
}

// TestFailedPackageOfAnotherConfiguration pins #95: a package that fails
// to type-check only for a further configuration is named in
// go_packages_failed too, and its node carries the error — the host's
// pass never saw the package, and a further pass adds only what the
// host's did not, so neither the list nor the attribute may be the host
// pass's alone.
func TestFailedPackageOfAnotherConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform-only package is the host's")
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	g, rep := module(t, map[string]string{
		"win/w_windows.go": "package win\n\nfunc W() int { return \"nope\" }\n",
		"p/p.go":           "package p\n\nfunc P() {}\n",
	}, opts)
	if got := rep.FailedPackages; len(got) != 1 || got[0] != "example.com/m/win" {
		t.Errorf("FailedPackages = %q; want the package broken under windows", got)
	}
	n, ok := g.Node("go://example.com/m/win")
	want := "win/w_windows.go:3:23: cannot use \"nope\" (untyped string constant) as int value in return statement"
	if !ok || n.Attrs["error"] != want {
		t.Errorf("win = %+v, %v; want error %q", n, ok, want)
	}
	if d, ok := g.Node("go://example.com/m/win#W"); !ok || d.Attrs["partial"] != "true" {
		t.Errorf("win#W = %+v, %v; want a partial declaration", d, ok)
	}
	if got := rep.WithoutCalls; len(got) != 0 {
		t.Errorf("WithoutCalls = %q; want none", got)
	}
}

// TestCallsUnavailableUnderAnotherConfiguration pins #95: the attribute
// saying why a package has no calls is learnt after the call graph is
// built, so a further configuration's pass has already emitted that
// package node by the time it knows — the attribute must still reach the
// node, as it does for the host's pass. Measured on fyne under
// -go-configs auto, where hack/winhoverprobe is the case.
func TestCallsUnavailableUnderAnotherConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the platform-only package is the host's")
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	g, rep := module(t, map[string]string{
		// An error outside a function body: the call graph cannot be
		// built around d, so win, which imports it, loses its calls.
		"p/p.go":           "package p\n\nfunc P() {}\n",
		"d/d.go":           "package d\n\nvar Broken int = \"x\"\n\nfunc D() {}\n",
		"win/w_windows.go": "package win\n\nimport \"example.com/m/d\"\n\nfunc W() { d.D() }\n",
	}, opts)
	if got := rep.WithoutCalls; len(got) != 1 || got[0] != "example.com/m/win" {
		t.Fatalf("WithoutCalls = %q", got)
	}
	for _, p := range rep.WithoutCalls {
		n, ok := g.Node(graph.ID("go://" + p))
		if !ok || n.Attrs["calls_unavailable"] != "example.com/m/d" {
			t.Errorf("%s = %+v, %v; want calls_unavailable naming d", p, n, ok)
		}
		if n.Attrs["build"] == "" {
			t.Errorf("%s = %+v; want the configuration it came from", p, n)
		}
	}
}

// TestHostPackageBrokenElsewhereIsNotNamedFailed pins #95's other half: a
// package the host's configuration loads whole is in the graph whole, so
// a further configuration finding it broken adds nothing — neither a node
// (a further pass adds only what the host's did not, #43) nor a name in
// go_packages_failed, which would say the graph lacks what it has.
func TestHostPackageBrokenElsewhereIsNotNamedFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the host is the configuration that breaks")
	}
	opts := DefaultOptions()
	opts.Configs = "windows/amd64"
	g, rep := module(t, map[string]string{
		// Whole on the host; under windows the _windows.go file joins it
		// and redeclares P, so the package does not type-check there.
		"p/p.go":         "package p\n\nfunc P() {}\n",
		"p/p_windows.go": "package p\n\nfunc P() {}\n",
		// q keeps the windows load from being a module that loads no
		// package at all, which is TestConfigurationThatDoesNotLoad.
		"q/q.go": "package q\n\nfunc Q() {}\n",
	}, opts)
	if got := rep.FailedPackages; len(got) != 0 {
		t.Errorf("FailedPackages = %q; want none: the host loaded p whole", got)
	}
	n, _ := g.Node("go://example.com/m/p")
	if n.Attrs["error"] != "" {
		t.Errorf("p error = %q; want none", n.Attrs["error"])
	}
	if d, ok := g.Node("go://example.com/m/p#P"); !ok || d.Attrs["partial"] != "" {
		t.Errorf("p#P = %+v, %v; want the host's whole declaration", d, ok)
	}
}

// TestConfigurationThatDoesNotLoad pins #92 on #95's extra passes: a
// configuration the module does not load for at all is reported skipped
// and costs that configuration, never the host's graph — the Go layer
// returning an error drops every Go fact of the repository (#93).
func TestConfigurationThatDoesNotLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the host is the configuration that does not load")
	}
	opts := DefaultOptions()
	opts.Configs = "windows/amd64"
	g, rep := module(t, map[string]string{
		// The module's one package redeclares P under windows, so no
		// package of it loads for that configuration.
		"p/p.go":         "package p\n\nfunc P() {}\n",
		"p/p_windows.go": "package p\n\nfunc P() {}\n",
	}, opts)
	if _, ok := g.Node("go://example.com/m/p#P"); !ok {
		t.Error("the host's declaration is missing: the extra configuration cost the layer")
	}
	var got []string
	for _, s := range rep.Skipped {
		got = append(got, s.File+": "+s.Reason)
	}
	if len(got) != 1 || !strings.Contains(got[0], "does not load for windows/amd64") {
		t.Errorf("Skipped = %q; want the one configuration, with why", got)
	}
}

// TestGenericMethodsDoNotStopTheCallGraph pins #216: a method with type
// parameters of its own, which Go 1.27 admits, reached by the call
// graph's walk of the program's runtime types. x/tools v0.44.0 panicked
// there ("panic: Int", the type parameter of math/rand/v2's Rand.N) and
// took the whole build with it. Both ways in: the standard library's,
// through a *rand.Rand converted to an interface, and the module's own.
func TestGenericMethodsDoNotStopTheCallGraph(t *testing.T) {
	if !strings.Contains(runtime.Version(), "go1.27") && !strings.Contains(runtime.Version(), "go1.28") {
		t.Skip("methods with type parameters are Go 1.27's")
	}
	g, rep := module(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.27\n",
		"p/p.go": `package p

import "math/rand/v2"

var Source any = rand.New(rand.NewPCG(1, 2))

type Box struct{ n int }

func (b *Box) Scale[T ~int | ~int64](by T) T { return T(b.n) * by }

var Boxed any = &Box{}

func Helper() int { return 1 }

func Use(b *Box) int64 { return b.Scale(int64(Helper())) }
`}, DefaultOptions())
	wantM(t, g, "p#Use", graph.KindCalls, "p#Helper")
	wantM(t, g, "p#Use", graph.KindCalls, "p#Box.Scale")
	if len(rep.WithoutCalls) != 0 {
		t.Errorf("WithoutCalls = %v; the call graph built", rep.WithoutCalls)
	}
}

// TestCallGraphPanicCostsTheModulesCalls pins #216's other half: a panic
// inside the call graph's build, whatever construct causes the next one,
// leaves the module's declarations in the graph and its packages named as
// without calls, the panic as the cause, instead of failing the build.
func TestCallGraphPanicCostsTheModulesCalls(t *testing.T) {
	defer func(f func(*analysis.Engine) (*analysis.InterProceduralContext, error)) { callGraphOf = f }(callGraphOf)
	callGraphOf = func(*analysis.Engine) (*analysis.InterProceduralContext, error) { panic("Int") }
	g, rep := module(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/m/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	}, DefaultOptions())
	for _, id := range []string{"a#A", "b#B"} {
		if _, ok := g.Node(graph.ID("go://example.com/m/" + id)); !ok {
			t.Errorf("declaration %s lost with the call graph", id)
		}
	}
	for _, id := range []string{"a", "b"} {
		n, _ := g.Node(graph.ID("go://example.com/m/" + id))
		if got := n.Attrs["calls_unavailable"]; got != "the call graph could not be built: Int" {
			t.Errorf("%s calls_unavailable = %q", id, got)
		}
	}
	if got := strings.Join(rep.WithoutCalls, " "); got != "example.com/m/a example.com/m/b" {
		t.Errorf("WithoutCalls = %q", got)
	}
	if _, ok := hasEdge(g, "go://example.com/m/a", graph.KindImports, "go://example.com/m/b"); !ok {
		t.Error("imports lost with the call graph")
	}
}

// goRepositoryTree writes a checkout of the Go repository in miniature:
// std and cmd modules, and misc, an ordinary one.
func goRepositoryTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"src/go.mod":         "module std\n\ngo 1.26\n",
		"src/bytes/b.go":     "package bytes\n\nfunc B() {}\n",
		"src/cmd/go.mod":     "module cmd\n\ngo 1.26\n",
		"src/cmd/go/main.go": "package main\n\nfunc main() {}\n",
		"misc/go.mod":        "module misc\n\ngo 1.26\n",
		"misc/m.go":          "package misc\n\nfunc M() {}\n",
	} {
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

// TestUnbuiltGoRepositoryIsReported pins #219 (the owner's decision): a
// checkout of the Go repository with no compiled toolchain is not loaded
// against the installed one, which would show that toolchain's cmd and
// tests under the checkout's paths. Its std and cmd modules are skipped,
// each saying to run make.bash; misc, an ordinary module, is read.
func TestUnbuiltGoRepositoryIsReported(t *testing.T) {
	dir := goRepositoryTree(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, s := range rep.Skipped {
		if strings.Contains(s.Reason, "make.bash") {
			named = append(named, s.File)
		}
	}
	slices.Sort(named)
	if got := strings.Join(named, " "); got != "src/cmd/go.mod src/go.mod" {
		t.Errorf("skipped for make.bash: %q, want src/cmd/go.mod src/go.mod", got)
	}
	if !slices.Equal(rep.Modules, []string{"misc"}) {
		t.Errorf("modules %v, want misc alone", rep.Modules)
	}
	if _, ok := b.Build().Node("go://misc#M"); !ok {
		t.Error("misc was not read")
	}
}

// TestBuiltGoRepositoryIsKept: with its compiled tools, the checkout's
// std and cmd are modules like any other.
func TestBuiltGoRepositoryIsKept(t *testing.T) {
	dir := goRepositoryTree(t)
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "tool", "darwin_arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	mods, _, err := Modules(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kept, skipped := unbuiltGoRepository(graph.NewMinter(dir), mods, nil); len(kept) != 3 || len(skipped) != 0 {
		t.Errorf("built: kept %d modules and skipped %v, want all three and none", len(kept), skipped)
	}
}

// TestTaggedVariantIsRead pins #48 end to end: under auto, the variant
// of a declaration that only a build tag includes is read with the tag
// set. Its declaration keeps the untagged variant's node — one name, one
// ID — and what only it has, a call and a declaration of its own, is in
// the graph, marked with the configuration that brought it in, every tag
// named.
func TestTaggedVariantIsRead(t *testing.T) {
	opts := DefaultOptions()
	opts.Configs = "auto"
	g, rep := module(t, map[string]string{
		"p/draw_gl.go":   "//go:build gl\n\npackage p\n\nfunc Draw() int { return glOnly() }\n\nfunc glOnly() int { return 1 }\n",
		"p/draw_nogl.go": "//go:build !gl\n\npackage p\n\nfunc Draw() int { return 0 }\n",
		"p/mobile.go":    "//go:build mobile\n\npackage p\n\nfunc Touch() {}\n",
	}, opts)
	if n, ok := g.Node("go://example.com/m/p#Draw"); !ok || n.Attrs["build"] != "" || n.Pos.File != "p/draw_nogl.go" {
		t.Errorf("p#Draw = %+v, %v; want the host's, untagged variant", n, ok)
	}
	n, ok := g.Node("go://example.com/m/p#glOnly")
	if !ok || !strings.HasSuffix(n.Attrs["build"], "+gl+mobile") {
		t.Fatalf("p#glOnly = %+v, %v; want it read with gl and mobile set; configs %v", n, ok, rep.Configs)
	}
	if !slices.ContainsFunc(g.Out("go://example.com/m/p#Draw"), func(e graph.Edge) bool {
		return e.To == "go://example.com/m/p#glOnly" && e.Kind == graph.KindCalls && e.Attrs["build"] == n.Attrs["build"]
	}) {
		t.Errorf("no calls edge p#Draw -> p#glOnly marked %s: %+v", n.Attrs["build"], g.Out("go://example.com/m/p#Draw"))
	}
	if _, ok := g.Node("go://example.com/m/p#Touch"); !ok {
		t.Error("p#Touch, behind mobile, is not in the graph")
	}
	explicit := DefaultOptions()
	explicit.Configs = "linux/amd64+gl"
	g, _ = module(t, map[string]string{
		"p/p.go":       "package p\n\nfunc P() {}\n",
		"p/draw_gl.go": "//go:build gl\n\npackage p\n\nfunc GL() {}\n",
	}, explicit)
	if n, ok := g.Node("go://example.com/m/p#GL"); !ok || n.Attrs["build"] != "linux/amd64+gl" {
		t.Errorf("explicit linux/amd64+gl: p#GL = %+v, %v", n, ok)
	}
}

// TestProceedStopsBeforeEmit pins #43's hook: Options.Proceed is asked
// after the host's load with the files it leaves out and whether it has
// tests, and false ends the extraction with ErrStopped before anything
// is emitted.
func TestProceedStopsBeforeEmit(t *testing.T) {
	files := map[string]string{
		"p/p.go":       "package p\n\nfunc P() {}\n",
		"p/p_other.go": "//go:build " + otherGOOS() + "\n\npackage p\n\nfunc Q() {}\n",
	}
	for _, proceed := range []bool{false, true} {
		var asked [][]string
		var askedTests []bool
		opts := DefaultOptions()
		opts.Tests = true
		opts.Proceed = func(excluded []string, tests bool) bool {
			asked = append(asked, slices.Clone(excluded))
			askedTests = append(askedTests, tests)
			return proceed
		}
		dir := moduleDir(t, files)
		b := graph.NewBuilder(dir)
		_, err := Extract(b, dir, opts)
		if len(asked) != 1 || !slices.Equal(asked[0], []string{"p/p_other.go"}) || !askedTests[0] {
			t.Errorf("proceed=%v: asked %q tests %v, want once, p/p_other.go, with tests", proceed, asked, askedTests)
		}
		nodes, _ := b.Contributions("")
		switch {
		case !proceed && (!errors.Is(err, ErrStopped) || len(nodes) != 0):
			t.Errorf("proceed=false: err %v, %d nodes; want ErrStopped and none", err, len(nodes))
		case proceed && (err != nil || len(nodes) == 0):
			t.Errorf("proceed=true: err %v, %d nodes; want the graph", err, len(nodes))
		}
	}
}

// moduleDir writes files, and a go.mod of example.com/m unless they hold
// one, into a new directory.
func moduleDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files = maps.Clone(files)
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/m\n\ngo 1.26\n"
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
