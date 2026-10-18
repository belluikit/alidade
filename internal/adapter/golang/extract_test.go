// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belluikit/alidade/internal/graph"
)

const fixtureModule = "example.com/fixture"

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type extraction struct {
	g   *graph.Graph
	rep *Report
}

var (
	cacheMu sync.Mutex
	// cache is keyed by the options as printed, Only being a slice; a
	// pointer prints as its address, so it keys as it compared before.
	cache = map[string]extraction{}
)

// extract runs the adapter over the fixture once per distinct Options and
// hands every test the same immutable graph: a load type-checks the
// standard library each time, which is most of a second, and nothing here
// mutates what it gets.
func extract(t *testing.T, opts Options) (*graph.Graph, *Report) {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	key := fmt.Sprintf("%#v", opts)
	if e, ok := cache[key]; ok {
		return e.g, e.rep
	}
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	e := extraction{b.Build(), rep}
	cache[key] = e
	return e.g, e.rep
}

// id abbreviates a fixture ID: "shapes#Total" -> go://example.com/fixture/shapes#Total.
func id(s string) graph.ID { return graph.ID("go://" + fixtureModule + "/" + s) }

func hasEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func wantEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID, d graph.Derivation) {
	t.Helper()
	e, ok := hasEdge(g, from, kind, to)
	if !ok {
		t.Errorf("missing edge %s -%s-> %s", from, kind, to)
		return
	}
	if e.Derivation != d {
		t.Errorf("edge %s -%s-> %s: derivation %s, want %s", from, kind, to, e.Derivation, d)
	}
}

func wantNoEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) {
	t.Helper()
	if _, ok := hasEdge(g, from, kind, to); ok {
		t.Errorf("unexpected edge %s -%s-> %s", from, kind, to)
	}
}

func wantNode(t *testing.T, g *graph.Graph, id graph.ID, kind graph.Kind) graph.Node {
	t.Helper()
	n, ok := g.Node(id)
	if !ok {
		t.Errorf("missing node %s", id)
		return n
	}
	if n.Kind != kind {
		t.Errorf("node %s: kind %s, want %s", id, n.Kind, kind)
	}
	return n
}

func TestDeclarationsAndContains(t *testing.T) {
	g, rep := extract(t, DefaultOptions())

	wantNode(t, g, id("shapes"), graph.KindPackage)
	wantNode(t, g, id("app"), graph.KindPackage)
	for _, tt := range []struct {
		id   string
		kind graph.Kind
	}{
		{"shapes#Total", graph.KindFunc},
		{"shapes#Circle", graph.KindType},
		{"shapes#Circle.Area", graph.KindMethod},
		{"shapes#Shape.Area", graph.KindMethod}, // an interface's method is a declaration too
		{"shapes#Stack.Push", graph.KindMethod}, // keyed on the generic origin, no type args
		{"shapes#Circles", graph.KindType},      // an alias
		{"shapes#Unit", graph.KindVar},
		{"shapes#Sides", graph.KindConst},
		{"shapes#helper", graph.KindFunc},
	} {
		wantNode(t, g, id(tt.id), tt.kind)
	}

	wantEdge(t, g, id("shapes"), graph.KindContains, id("shapes#Total"), graph.Extracted)
	wantEdge(t, g, id("shapes#Circle"), graph.KindContains, id("shapes#Circle.Area"), graph.Extracted)
	wantEdge(t, g, id("shapes#Shape"), graph.KindContains, id("shapes#Shape.Area"), graph.Extracted)
	wantEdge(t, g, id("app"), graph.KindImports, id("shapes"), graph.Extracted)

	n := wantNode(t, g, id("shapes#Total"), graph.KindFunc)
	if n.Pos.File != "shapes/shapes.go" || n.Pos.Line == 0 || n.Pos.Col == 0 {
		t.Errorf("Total position = %+v, want shapes/shapes.go with a line and column", n.Pos)
	}
	if n.Attrs["exported"] != "" {
		t.Errorf("Total marked %q, want no exported attr on an exported decl", n.Attrs["exported"])
	}
	if h := wantNode(t, g, id("shapes#helper"), graph.KindFunc); h.Attrs["exported"] != "false" {
		t.Errorf("helper attrs = %v, want exported=false", h.Attrs)
	}
	if strings.Join(rep.Modules, ",") != fixtureModule || rep.Packages != 2 {
		t.Errorf("report = %+v, want module %s with 2 packages", rep, fixtureModule)
	}
}

// TestCallsAreTypeResolved is the point of the layer: a call through an
// interface resolves to every implementer, including a generated one.
func TestCallsAreTypeResolved(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	for _, impl := range []string{"shapes#Circle.Area", "shapes#Square.Area", "shapes#GenThing.Area"} {
		wantEdge(t, g, id("shapes#Total"), graph.KindCalls, id(impl), graph.Resolved)
	}
	wantEdge(t, g, id("app#Run"), graph.KindCalls, id("shapes#Total"), graph.Resolved)
	wantEdge(t, g, id("shapes#helper"), graph.KindCalls, id("shapes#helper"), graph.Resolved) // real recursion
}

// TestSyntheticFunctionsFoldIntoDeclarations: closures, instantiations and
// receiver wrappers are the SSA builder's, not the source's. They attribute
// to the declaration that owns them and never become nodes or self-calls.
func TestSyntheticFunctionsFoldIntoDeclarations(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	for n := range g.Nodes() {
		if strings.ContainsAny(string(n.ID), "$[") {
			t.Errorf("synthetic function became a node: %s", n.ID)
		}
	}
	wantEdge(t, g, id("shapes#Largest"), graph.KindCalls, id("shapes#Total"), graph.Resolved) // via closure
	wantEdge(t, g, id("shapes#Areas"), graph.KindCalls, id("shapes#Map"), graph.Resolved)     // via instantiation
	wantNoEdge(t, g, id("shapes#Circle.Area"), graph.KindCalls, id("shapes#Circle.Area"))     // (*Circle).Area wrapper
	wantNoEdge(t, g, id("shapes#Map"), graph.KindCalls, id("shapes#Map"))                     // instantiation -> origin
}

func TestImplementsIsStructural(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	wantEdge(t, g, id("shapes#Circle"), graph.KindImplements, id("shapes#Shape"), graph.Resolved)   // value receiver
	wantEdge(t, g, id("shapes#Square"), graph.KindImplements, id("shapes#Shape"), graph.Resolved)   // pointer receiver
	wantEdge(t, g, id("shapes#GenThing"), graph.KindImplements, id("shapes#Shape"), graph.Resolved) // generated
	wantEdge(t, g, id("shapes#Square"), graph.KindImplements, id("shapes#Sized"), graph.Resolved)   // embedded interface
	wantNoEdge(t, g, id("shapes#Circle"), graph.KindImplements, id("shapes#Sized"))
	wantNoEdge(t, g, id("shapes#Pair"), graph.KindImplements, id("shapes#Shape"))
	wantNoEdge(t, g, id("shapes#Sized"), graph.KindImplements, id("shapes#Shape")) // interfaces do not implement
	for _, e := range g.Out(id("shapes#Stack")) {
		if e.Kind == graph.KindImplements {
			t.Errorf("generic type emitted implements: %+v", e)
		}
	}
}

func TestReferences(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	wantEdge(t, g, id("shapes#Total"), graph.KindReferences, id("shapes#Shape"), graph.Resolved)     // parameter
	wantEdge(t, g, id("shapes#Pair"), graph.KindReferences, id("shapes#Circle"), graph.Resolved)     // field
	wantEdge(t, g, id("shapes#Pair"), graph.KindReferences, id("shapes#Square"), graph.Resolved)     // pointer field
	wantEdge(t, g, id("shapes#Registry"), graph.KindReferences, id("shapes#Circle"), graph.Resolved) // map and chan
	wantEdge(t, g, id("shapes#Registry"), graph.KindReferences, id("shapes#Named"), graph.Resolved)  // array
	wantEdge(t, g, id("shapes#Sized"), graph.KindReferences, id("shapes#Shape"), graph.Resolved)     // embedded
	wantEdge(t, g, id("shapes#Circles"), graph.KindReferences, id("shapes#Circle"), graph.Resolved)  // alias
	wantEdge(t, g, id("shapes#Unit"), graph.KindReferences, id("shapes#Circle"), graph.Resolved)     // var of named type
	wantNoEdge(t, g, id("shapes#Circle"), graph.KindReferences, id("shapes#Circle"))
	wantNoEdge(t, g, id("shapes#Circle.Area"), graph.KindReferences, id("shapes#Circle")) // receiver is contains
}

// TestFilteredToModule: nothing outside the module is a node except the
// packages boundary edges land on, and those are marked.
func TestFilteredToModule(t *testing.T) {
	g, rep := extract(t, DefaultOptions())
	var external []string
	for n := range g.Nodes() {
		if strings.HasPrefix(string(n.ID), "go://"+fixtureModule) {
			continue
		}
		if n.Kind != graph.KindPackage || n.Attrs["external"] != "true" {
			t.Errorf("node outside the module that is not an external package: %+v", n)
		}
		external = append(external, string(n.ID))
	}
	if want := []string{"go://fmt", "go://math"}; strings.Join(external, ",") != strings.Join(want, ",") {
		t.Errorf("external packages = %v, want %v", external, want)
	}
	e, ok := hasEdge(g, id("app#Run"), graph.KindCalls, "go://fmt")
	if !ok || e.Attrs["boundary"] != "package" {
		t.Errorf("boundary call from Run to fmt = %+v, %v; want a calls edge marked boundary=package", e, ok)
	}
	wantEdge(t, g, id("shapes"), graph.KindImports, "go://math", graph.Extracted)
	// The single module's edges out of the repository: app calls fmt,
	// app and shapes import fmt and math.
	if want := []ModuleEdge{{From: fixtureModule, Kind: graph.KindCalls, Count: 1}, {From: fixtureModule, Kind: graph.KindImports, Count: 2}}; !slices.Equal(rep.ModuleEdges, want) {
		t.Errorf("ModuleEdges = %+v, want %+v", rep.ModuleEdges, want)
	}
	if rep.Boundary != 1 {
		t.Errorf("report.Boundary = %d, want 1", rep.Boundary)
	}
}

func TestGeneratedIsMarkedByDefault(t *testing.T) {
	g, rep := extract(t, DefaultOptions())
	for _, s := range []string{"shapes#GenThing", "shapes#GenThing.Area", "shapes#GenThing.A", "shapes#GenHelper"} {
		n, ok := g.Node(id(s))
		if !ok || !n.Generated {
			t.Errorf("%s: generated=%v ok=%v, want a node marked generated", s, n.Generated, ok)
		}
	}
	if n, _ := g.Node(id("shapes#Total")); n.Generated {
		t.Error("Total marked generated")
	}
	if rep.Generated != 4 {
		t.Errorf("report.Generated = %d, want 4 (the type, its method and field, the function)", rep.Generated)
	}
	if got := g.Stats().GeneratedShare(); got <= 0 {
		t.Errorf("GeneratedShare = %v, want > 0", got)
	}
}

func TestTestsAreExplicit(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	if _, ok := g.Node(id("shapes#TestTotal")); ok {
		t.Error("TestTotal present with Tests=false")
	}

	opts := DefaultOptions()
	opts.Tests = true
	g, _ = extract(t, opts)
	wantNode(t, g, id("shapes#TestTotal"), graph.KindFunc)
	wantEdge(t, g, id("shapes#TestTotal"), graph.KindCalls, id("shapes#Total"), graph.Resolved)
	// Only the test files import testing, so the import is their node's,
	// and a test edge (#128).
	wantEdge(t, g, id("shapes#(test)"), graph.KindImports, "go://testing", graph.Extracted)
	wantNoEdge(t, g, id("shapes"), graph.KindImports, "go://testing")
	// The test variant of a package is the same package: one node, no
	// disagreement between the plain and test-augmented loads.
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts with Tests=true: %+v", c)
	}
	for n := range g.Nodes() {
		if strings.HasSuffix(string(n.ID), ".test") {
			t.Errorf("synthetic test main became a node: %s", n.ID)
		}
	}
}

// TestInitIsOneNode: init is the declaration Go keeps out of the package
// scope, and a package may declare several. They are one node, reached
// from the call graph, positioned at the first in source order and
// saying how many were folded into it.
func TestInitIsOneNode(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	n := wantNode(t, g, id("shapes#init"), graph.KindFunc)
	if n.Pos.File != "shapes/shapes.go" || n.Pos.Line != 121 {
		t.Errorf("init position = %+v, want shapes/shapes.go:121, the first in source order", n.Pos)
	}
	if n.Attrs["exported"] != "false" || n.Attrs["inits"] != "2" {
		t.Errorf("init attrs = %v, want exported=false and inits=2", n.Attrs)
	}
	wantEdge(t, g, id("shapes"), graph.KindContains, id("shapes#init"), graph.Extracted)
	wantEdge(t, g, id("shapes#init"), graph.KindCalls, id("shapes#registerAll"), graph.Resolved)
}

// TestInitWithAProductionAndATestDeclaration: a package whose init is
// declared in a production file and in a test one has a production init
// node, for the production declarations, positioned at the first of them;
// the test file's init folds into the package's test files' node, so the
// call only it makes is a test edge (#128). Before, it folded into the
// production init and that call was a production edge — the cost ADR 004
// rule 4 recorded.
func TestInitWithAProductionAndATestDeclaration(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, _ := extract(t, opts)
	n := wantNode(t, g, id("shapes#init"), graph.KindFunc)
	if n.Test || n.Pos.File != "shapes/shapes.go" || n.Pos.Line != 121 {
		t.Errorf("init = %+v; want a production node at shapes/shapes.go:121", n)
	}
	if n.Attrs["inits"] != "2" {
		t.Errorf("init attrs = %v, want inits=2 (the production ones)", n.Attrs)
	}
	if e, ok := hasEdge(g, id("shapes"), graph.KindContains, id("shapes#init")); !ok || e.Test {
		t.Errorf("contains edge to init: Test = %v, %v; want a production edge", e.Test, ok)
	}
	wantNoEdge(t, g, id("shapes#init"), graph.KindCalls, id("shapes#Circle.Area"))
	if e, ok := hasEdge(g, id("shapes#(test)"), graph.KindCalls, id("shapes#Circle.Area")); !ok || !e.Test {
		t.Errorf("test files -calls-> Circle.Area: Test = %v, %v; want a test edge", e.Test, ok)
	}
}

func TestNothingDroppedNothingConflicting(t *testing.T) {
	g, _ := extract(t, DefaultOptions())
	if d := g.Dropped(); len(d) != 0 {
		t.Errorf("dropped edges: %+v", d)
	}
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts: %+v", c)
	}
}

func TestDeterministic(t *testing.T) {
	dir := fixtureDir(t)
	var outs [2]bytes.Buffer
	for i := range outs {
		b := graph.NewBuilder(dir)
		if _, err := Extract(b, dir, DefaultOptions()); err != nil {
			t.Fatal(err)
		}
		if err := b.Build().WritePXF(&outs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outs[0].Bytes(), outs[1].Bytes()) {
		t.Error("two extractions of an unchanged tree differ")
	}
}

func TestExplicitModule(t *testing.T) {
	opts := DefaultOptions()
	opts.Module = fixtureModule
	if g, _ := extract(t, opts); g.NodeCount() == 0 {
		t.Error("explicit Module matching the load produced nothing")
	}
	opts.Module = "example.com/elsewhere"
	if g, rep := extract(t, opts); g.NodeCount() != 0 || rep.Packages != 0 {
		t.Errorf("Module naming a module that is not loaded produced %d nodes", g.NodeCount())
	}
}

func TestLoadErrorsAreReported(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":    "module example.com/broken\n\ngo 1.26\n",
		"broken.go": "package broken\n\nfunc F() int { return \"no\" }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// caliper refuses a load in which every package has errors and
	// carries the compiler's message. The root's module is then a failed
	// module like a nested one (#67): its package node carries the
	// message intact and the build goes on (#92).
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatalf("err = %v, want the failure recorded, not returned", err)
	}
	if strings.Join(rep.Failed, ",") != "example.com/broken" || len(rep.Modules) != 0 {
		t.Errorf("Failed = %v, Modules = %v; want the root module failed", rep.Failed, rep.Modules)
	}
	n, ok := b.Build().Node("go://example.com/broken")
	if !ok || !strings.Contains(n.Attrs["error"], "cannot use") {
		t.Errorf("package node = %+v, %v; want the compiler's type error on it", n, ok)
	}
}

// TestRealRepository is the measurement ADR 005 rests on, run against a real
// module named by ALIDADE_GO_REPO. It is skipped otherwise, since it takes
// seconds and depends on what is on the machine.
func TestRealRepository(t *testing.T) {
	dir := os.Getenv("ALIDADE_GO_REPO")
	if dir == "" {
		t.Skip("set ALIDADE_GO_REPO to a Go module to measure")
	}
	for _, tests := range []bool{false, true} {
		opts := DefaultOptions()
		opts.Tests = tests
		b := graph.NewBuilder(dir)
		start := time.Now()
		rep, err := Extract(b, dir, opts)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		g := b.Build()
		s := g.Stats()
		t.Logf("tests=%v: %s in %v (load %v, call graph %v, emit %v): %d nodes, %d edges, %d dropped, %d conflicts, generated share %.1f%%",
			tests, rep.Modules, time.Since(start).Round(time.Millisecond), rep.Load.Round(time.Millisecond),
			rep.CallGraph.Round(time.Millisecond), rep.Emit.Round(time.Millisecond), g.NodeCount(), g.EdgeCount(),
			len(g.Dropped()), len(g.Conflicts()), 100*s.GeneratedShare())
		t.Logf("  packages %d, decls %d, calls %d, boundary %d, generated %d",
			rep.Packages, rep.Decls, rep.Calls, rep.Boundary, rep.Generated)
		if len(g.Dropped()) != 0 {
			t.Errorf("dropped edges on a real module: first %+v", g.Dropped()[0])
		}
	}
}

// TestBuildConfigurationIsReported pins #43's first step: the report
// names the GOOS/GOARCH the load ran for, and a file whose constraint it
// does not satisfy is absent from the graph and named on its package
// node, so the absence is visible rather than silent.
func TestBuildConfigurationIsReported(t *testing.T) {
	g, rep := extract(t, DefaultOptions())
	if rep.Build == "" || !strings.Contains(rep.Build, "/") {
		t.Errorf("report.Build = %q, want GOOS/GOARCH", rep.Build)
	}
	want, files := 2, "other_arch.go plan9.go"
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		want, files = 1, "plan9.go"
	}
	if rep.Excluded != want {
		t.Errorf("report.Excluded = %d, want %d (%s)", rep.Excluded, want, files)
	}
	if !slices.Contains(rep.HostExcluded, "app/plan9.go") {
		t.Errorf("report.HostExcluded = %q, want app/plan9.go among them, relative to the root", rep.HostExcluded)
	}
	if n, ok := g.Node(graph.ID("go://" + fixtureModule + "/app")); !ok || n.Attrs["excluded"] != files {
		t.Errorf("app package excluded attr = %q, want %q", n.Attrs["excluded"], files)
	}
	if strings.Join(rep.Configs, ",")+rep.Build != rep.Build+rep.Build || rep.Recovered+rep.Added != 0 {
		t.Errorf("host-only report: Configs %v Build %s Recovered %d Added %d", rep.Configs, rep.Build, rep.Recovered, rep.Added)
	}
	if _, ok := g.Node(graph.ID("go://" + fixtureModule + "/app#Plan9Only")); ok {
		t.Error("Plan9Only is in the graph though its constraint is unsatisfied")
	}
}

// TestOtherConfigurationsLoad pins #43's second step: with Configs
// "auto", the file behind "linux && amd64" is loaded for that
// configuration, its declaration and call arrive tagged build=linux/amd64,
// and the package node no longer lists it as excluded — while plan9.go,
// which no candidate satisfies, stays excluded and listed. On a
// linux/amd64 host the file is in the host's load and nothing is added.
func TestOtherConfigurationsLoad(t *testing.T) {
	for _, mode := range []string{"auto", "linux/amd64"} {
		opts := DefaultOptions()
		opts.Configs = mode
		g, rep := extract(t, opts)
		fn := graph.ID("go://" + fixtureModule + "/app#LinuxAMD64Only")
		n, ok := g.Node(fn)
		if !ok {
			t.Fatalf("%s: %s missing", mode, fn)
		}
		call := callTo(t, g, fn, graph.ID("go://"+fixtureModule+"/shapes#Total"))
		if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
			if n.Attrs["build"] != "" || len(rep.Configs) != 1 || rep.Recovered != 0 {
				t.Errorf("%s on linux/amd64: build attr %q, Configs %v, Recovered %d", mode, n.Attrs["build"], rep.Configs, rep.Recovered)
			}
			continue
		}
		checkOverlay(t, mode, g, rep, n, call)
	}
}

func callTo(t *testing.T, g *graph.Graph, from, to graph.ID) graph.Edge {
	t.Helper()
	for _, e := range g.Out(from) {
		if e.Kind == graph.KindCalls && e.To == to {
			return e
		}
	}
	t.Fatalf("no calls edge %s -> %s", from, to)
	return graph.Edge{}
}

// checkOverlay is what a host other than linux/amd64 must see.
func checkOverlay(t *testing.T, mode string, g *graph.Graph, rep *Report, n graph.Node, call graph.Edge) {
	t.Helper()
	if n.Attrs["build"] != "linux/amd64" || call.Attrs["build"] != "linux/amd64" {
		t.Errorf("%s: build attrs: node %q edge %q, want linux/amd64", mode, n.Attrs["build"], call.Attrs["build"])
	}
	if strings.Join(rep.Configs, ",") != rep.Build+",linux/amd64" || rep.Recovered != 1 || rep.Excluded != 1 || rep.Added == 0 {
		t.Errorf("%s: Configs %v Recovered %d Excluded %d Added %d", mode, rep.Configs, rep.Recovered, rep.Excluded, rep.Added)
	}
	if pkg, _ := g.Node(graph.ID("go://" + fixtureModule + "/app")); pkg.Attrs["excluded"] != "plan9.go" {
		t.Errorf("%s: app excluded = %q, want plan9.go", mode, pkg.Attrs["excluded"])
	}
	// What both configurations declare is one plain node: the host
	// wrote it, the overlay skipped it.
	if run, _ := g.Node(graph.ID("go://" + fixtureModule + "/app#Run")); run.Attrs["build"] != "" {
		t.Errorf("%s: Run carries build=%q though the host loaded it", mode, run.Attrs["build"])
	}
}

// extractDir runs the adapter over one of the other fixtures, uncached.
func extractDir(t *testing.T, fixture string, opts Options) (*graph.Graph, *Report) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return b.Build(), rep
}

// TestMultiModule is #67's done-when: a repository of two modules, the
// nested one reaching the root's by a replace directive and no go.work,
// yields one graph holding both modules' Go, with the edges from the
// nested module into the root's resolved at declaration granularity —
// calls, imports, references and implements — and nothing marked external
// but the standard library.
func TestMultiModule(t *testing.T) {
	g, rep := extractDir(t, "multi", DefaultOptions())
	const root, sub = "go://example.com/multi/", "go://example.com/multi/sub/"
	if want := "example.com/multi,example.com/multi/sub"; strings.Join(rep.Modules, ",") != want || len(rep.Failed) != 0 {
		t.Errorf("modules = %v, failed = %v; want %s and none failed", rep.Modules, rep.Failed, want)
	}
	wantNode(t, g, root+"core#Lookup", graph.KindFunc)
	wantNode(t, g, sub+"mem#Find", graph.KindFunc)
	wantEdge(t, g, sub+"mem#Find", graph.KindCalls, root+"core#Lookup", graph.Resolved)
	if e, _ := hasEdge(g, sub+"mem#Find", graph.KindCalls, root+"core#Lookup"); e.Attrs["boundary"] != "" {
		t.Errorf("cross-module call is marked boundary=%q; want declaration granularity", e.Attrs["boundary"])
	}
	wantEdge(t, g, sub+"mem", graph.KindImports, root+"core", graph.Extracted)
	wantEdge(t, g, sub+"mem#Find", graph.KindReferences, root+"core#Key", graph.Resolved)
	wantEdge(t, g, sub+"mem#Mem", graph.KindImplements, root+"core#Store", graph.Resolved)
	wantNoEdge(t, g, root+"core#Lookup", graph.KindCalls, sub+"mem#Find")
	for n := range g.Nodes() {
		if n.Attrs["external"] == "true" && strings.HasPrefix(string(n.ID), root) {
			t.Errorf("%s is marked external: the root module's node was stood in for", n.ID)
		}
		if n.Attrs["error"] != "" {
			t.Errorf("%s carries an error: %s", n.ID, n.Attrs["error"])
		}
	}
	if rep.Packages != 2 || rep.Boundary != 0 {
		t.Errorf("report = %+v, want 2 packages and no boundary calls", rep)
	}
	want := []ModuleEdge{
		{From: "example.com/multi/sub", To: "example.com/multi", Kind: graph.KindCalls, Count: 1},
		{From: "example.com/multi/sub", To: "example.com/multi", Kind: graph.KindImplements, Count: 1},
		{From: "example.com/multi/sub", To: "example.com/multi", Kind: graph.KindImports, Count: 1},
		{From: "example.com/multi/sub", To: "example.com/multi", Kind: graph.KindReferences, Count: 1},
	}
	if !slices.Equal(rep.ModuleEdges, want) {
		t.Errorf("ModuleEdges = %+v, want %+v", rep.ModuleEdges, want)
	}
}

// seamEdges counts the graph's edges from a package of module from into a
// package of module to, by kind and by whether the core marked them test
// ones. A nested module's path extends its parent's, so a target under
// from is not across the seam. The graph is the oracle the report is read
// against: the core derives an edge's Test from its endpoints.
func seamEdges(g *graph.Graph, from, to string) (prod, test map[graph.EdgeKind]int) {
	prod, test = map[graph.EdgeKind]int{}, map[graph.EdgeKind]int{}
	in := func(id graph.ID, mod string) bool { return strings.HasPrefix(string(id), "go://"+mod+"/") }
	for e := range g.Edges() {
		if !in(e.From, from) || !in(e.To, to) || in(e.To, from) {
			continue
		}
		if e.Test {
			test[e.Kind]++
		} else {
			prod[e.Kind]++
		}
	}
	return prod, test
}

// TestMultiModuleTestEdges pins the test-ness of the module edges #142
// counts apart, for the two kinds a single module cannot reach: a
// references or implements edge stays inside one module there, and
// moduleEdge drops a self-edge, so only calls and imports are visible.
// Across the seam a test file of the nested module references, implements
// and calls the root module, and the report's two sides must still add up
// to the graph's — which is the only oracle, since the core derives an
// edge's Test from its endpoints.
func TestMultiModuleTestEdges(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, rep := extractDir(t, "multi", opts)

	const root, sub = "example.com/multi", "example.com/multi/sub"
	seam := func(es []ModuleEdge) map[graph.EdgeKind]int {
		out := map[graph.EdgeKind]int{}
		for _, e := range es {
			if e.From == sub && e.To == root {
				out[e.Kind] += e.Count
			}
		}
		return out
	}
	prod, test := seamEdges(g, sub, root)
	for _, kind := range []graph.EdgeKind{graph.KindCalls, graph.KindReferences, graph.KindImplements} {
		if test[kind] == 0 {
			t.Fatalf("no test %s edge crosses the module seam: the graph has test %v and production %v, so the fixture stopped exercising the flag", kind, test, prod)
		}
	}
	if got := seam(rep.ModuleEdges); !maps.Equal(got, prod) {
		t.Errorf("ModuleEdges across the seam = %v, the graph's production edges %v", got, prod)
	}
	if got := seam(rep.TestModuleEdges); !maps.Equal(got, test) {
		t.Errorf("TestModuleEdges across the seam = %v, the graph's test edges %v", got, test)
	}
}

// TestNestedModuleThatFailsToLoad: the root module is read, the nested
// module that does not type-check keeps a package node carrying the
// error, and the report names it.
func TestNestedModuleThatFailsToLoad(t *testing.T) {
	g, rep := extractDir(t, "brokensub", DefaultOptions())
	if want := "example.com/brokensub"; strings.Join(rep.Modules, ",") != want || strings.Join(rep.Failed, ",") != want+"/tools" {
		t.Errorf("modules = %v, failed = %v; want %s loaded and %s/tools failed", rep.Modules, rep.Failed, want, want)
	}
	wantNode(t, g, "go://example.com/brokensub#OK", graph.KindFunc)
	n := wantNode(t, g, "go://example.com/brokensub/tools", graph.KindPackage)
	if !strings.Contains(n.Attrs["error"], "cannot use") || n.Pos.File != "tools/go.mod" || n.Name != "tools" {
		t.Errorf("failed module node = %+v; want the compiler's error, positioned at its go.mod", n)
	}
}

func TestModules(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "svc/go.mod", "vendor/x/go.mod", "testdata/y/go.mod", ".hidden/go.mod", "_gen/go.mod", "node_modules/z/go.mod", "svc/inner/go.mod"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		name := "example.com/" + strings.ReplaceAll(filepath.Dir(f), string(filepath.Separator), "/")
		if err := os.WriteFile(filepath.Join(dir, f), []byte("module "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mods, _, err := Modules(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mods {
		rel, _ := filepath.Rel(dir, m.Dir)
		got = append(got, rel+"="+m.Path)
	}
	if want := ".=example.com/.,svc=example.com/svc,svc/inner=example.com/svc/inner"; strings.Join(got, ",") != want {
		t.Errorf("Modules = %v, want %s", got, want)
	}
	if o := owner("example.com/svc/inner/pkg", []string{"example.com/svc", "example.com/svc/inner", "example.com/."}); o != "example.com/svc/inner" {
		t.Errorf("owner = %q, want the longest module path", o)
	}
	if o := owner("example.com/other", []string{"example.com/svc"}); o != "" {
		t.Errorf("owner of a path outside every module = %q", o)
	}
	if n := moduleName("github.com/x/lib/v2"); n != "lib" {
		t.Errorf("moduleName = %q, want lib", n)
	}
}

// TestStandardLibraryIsTheGoRepositorysOwn pins #217: in a checkout of
// the Go repository, std is a module of the tree whose packages are not
// under its path — bytes, not std/bytes — and they are its, not
// external; cmd's stay cmd's, a dotted path stays outside, and C is
// nobody's. In any other repository std is not a module of the graph and
// a standard-library path is external, as before. Such a checkout's
// dependencies are read from source.
func TestStandardLibraryIsTheGoRepositorysOwn(t *testing.T) {
	goRepo := []string{"misc", "std", "cmd"}
	for path, want := range map[string]string{
		"bytes": "std", "math/rand/v2": "std", "internal/godebug": "std",
		"cmd/asm": "cmd", "cmd": "cmd", "misc/ios": "misc",
		"golang.org/x/tools/go/ssa": "", "C": "",
	} {
		if got := Owner(path, goRepo); got != want {
			t.Errorf("in the Go repository, Owner(%q) = %q, want %q", path, got, want)
		}
	}
	if got := Owner("bytes", []string{"example.com/m"}); got != "" {
		t.Errorf("elsewhere, Owner(bytes) = %q, want external", got)
	}
	if !goRepository([]Module{{Path: "std"}, {Path: "cmd"}}) || goRepository([]Module{{Path: "example.com/m"}}) {
		t.Error("goRepository: the Go repository is the tree with a std module")
	}
	if forTree([]Module{{Path: "std"}}, DefaultOptions()).DepsFromExportData {
		t.Error("the Go repository's dependencies are its own compiler's export data, which the go command refuses")
	}
	if !forTree([]Module{{Path: "example.com/m"}}, DefaultOptions()).DepsFromExportData {
		t.Error("another repository lost export data")
	}
}

// TestNothingIsMarkedWithoutTests: node test-ness is only populated
// under Options.Tests, and an edge's is derived from it, so with tests
// off the graph must carry neither. This is the half that says the
// derivation cannot invent a test edge out of a production graph.
func TestNothingIsMarkedWithoutTests(t *testing.T) {
	g, rep := extract(t, DefaultOptions())
	for n := range g.Nodes() {
		if n.Test {
			t.Errorf("%s is a test node in a graph built without tests", n.ID)
		}
	}
	for e := range g.Edges() {
		if e.Test {
			t.Errorf("edge %s -%s-> %s is a test edge in a graph built without tests", e.From, e.Kind, e.To)
		}
	}
	if rep.Tests != 0 {
		t.Errorf("report.Tests = %d without tests", rep.Tests)
	}
}

// TestEveryEdgeTouchingATestDeclarationIsMarked is the rule itself, over
// the whole fixture rather than over chosen examples, and in both
// directions: an edge from a test declaration into production and an
// edge from production into a test declaration. The one-sided lookup —
// checking only the target — is the mistake that put 68 unmarked edges
// in the Markdown layer, so the test fails if the fixture stops
// exercising either direction.
func TestEveryEdgeTouchingATestDeclarationIsMarked(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, _ := extract(t, opts)
	test := map[graph.ID]bool{}
	for n := range g.Nodes() {
		test[n.ID] = n.Test
	}
	var fromTest, toTest int
	for e := range g.Edges() {
		want := test[e.From] || test[e.To]
		if e.Test != want {
			t.Errorf("edge %s -%s-> %s: Test = %v, want %v (from=%v to=%v)", e.From, e.Kind, e.To, e.Test, want, test[e.From], test[e.To])
		}
		switch {
		case test[e.From] && !test[e.To]:
			fromTest++
		case !test[e.From] && test[e.To]:
			toTest++
		}
	}
	if fromTest == 0 || toTest == 0 {
		t.Fatalf("the fixture exercises %d edges out of a test declaration and %d into one; both directions are the point", fromTest, toTest)
	}
}

// TestTestsAreMarked is #81's pin: under Tests, a declaration from a
// _test.go file carries test=true, and so does every edge touching it —
// its contains edge, its calls into production — while production
// declarations and the edges between them carry nothing, so leaving out
// what is marked gives the graph a build without tests would.
func TestTestsAreMarked(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, rep := extract(t, opts)
	if tt := wantNode(t, g, id("shapes#TestTotal"), graph.KindFunc); !tt.Test {
		t.Errorf("TestTotal.Test = false, want a test declaration")
	}
	if total := wantNode(t, g, id("shapes#Total"), graph.KindFunc); total.Test {
		t.Errorf("Total.Test = true, want a production declaration")
	}
	for _, e := range []struct {
		from graph.ID
		kind graph.EdgeKind
		to   graph.ID
		test bool
	}{
		{id("shapes"), graph.KindContains, id("shapes#TestTotal"), true},
		{id("shapes#TestTotal"), graph.KindCalls, id("shapes#Total"), true},
		{id("shapes"), graph.KindContains, id("shapes#Total"), false},
		{id("app#Run"), graph.KindCalls, id("shapes#Total"), false},
	} {
		got, ok := hasEdge(g, e.from, e.kind, e.to)
		if !ok || got.Test != e.test {
			t.Errorf("edge %s -%s-> %s: Test = %v, %v; want %v", e.from, e.kind, e.to, got.Test, ok, e.test)
		}
	}
	if rep.Tests == 0 {
		t.Errorf("report.Tests = 0 with tests loaded")
	}
	if _, plain := extract(t, DefaultOptions()); plain.Tests != 0 {
		t.Errorf("report.Tests = %d without tests", plain.Tests)
	}
}

// Calls out of generic code are edges whatever instantiates it (#150):
// a method of a generic type nothing in the module instantiates, a
// generic function calling itself, and a call to a method of a type
// defined over a generic instantiation. The shapes are the oracle's ten
// misses on protocompile and chameleon, in a module of their own.
func TestGenericCalls(t *testing.T) {
	g, _ := extractDir(t, "generic", DefaultOptions())
	gid := func(s string) graph.ID { return graph.ID("go://example.com/generic/generic#" + s) }
	for _, c := range []struct{ from, to graph.ID }{
		{gid("Queue.PopBack"), gid("Take")},
		{gid("Queue.Format"), "go://fmt"},
		{gid("Res.Destroy"), "go://sync"},
		{gid("Walk"), gid("Walk")},
		{gid("wrapper.Span"), gid("IntBox.Span")},
	} {
		if _, ok := hasEdge(g, c.from, graph.KindCalls, c.to); !ok {
			t.Errorf("missing %s -calls-> %s", c.from, c.to)
		}
	}
}

// TestUsesOfConstantsAndVariables is #180's done-when for the edge: a
// function or method that names a package-level constant or variable of
// the graph references it, RESOLVED; a local, a field, a dependency's
// variable or an initializer is no such edge.
func TestUsesOfConstantsAndVariables(t *testing.T) {
	g, _ := extractDir(t, "uses", DefaultOptions())
	app := func(s string) graph.ID { return graph.ID("go://example.com/uses/app#" + s) }
	cfgID := func(s string) graph.ID { return graph.ID("go://example.com/uses/cfg#" + s) }
	for _, c := range []struct{ from, to graph.ID }{
		{app("Analyze"), app("envName")},
		{app("Server.Default"), app("limit")},
		{app("Later"), app("limit")},
		{app("Remote"), cfgID("Timeout")},
		{app("Remote"), cfgID("Mode")},
		{app("Count"), app("counter")},
		{app("Box.Name"), app("envName")},
		{app("init"), app("limit")},
		{app("init"), app("counter")},
	} {
		wantEdge(t, g, c.from, graph.KindReferences, c.to, graph.Resolved)
	}
	for _, c := range []struct{ from, to graph.ID }{
		{app("Shadow"), app("envName")},
		{app("Server.Limit"), app("limit")},
		{app("Args"), "go://os"},
		{app("derived"), app("envName")},
	} {
		wantNoEdge(t, g, c.from, graph.KindReferences, c.to)
	}
	if n := len(g.Dropped()); n != 0 {
		t.Errorf("%d edges dropped: %v", n, g.Dropped())
	}
}

// With tests in, a test function's use is an edge too, and a test one.
func TestUsesFromTests(t *testing.T) {
	for _, opts := range []Options{{Tests: true}, {Tests: true, ExactTests: true}} {
		g, _ := extractDir(t, "uses", opts)
		from, to := graph.ID("go://example.com/uses/app#TestAnalyze"), graph.ID("go://example.com/uses/app#envName")
		e, ok := hasEdge(g, from, graph.KindReferences, to)
		if !ok || !e.Test {
			t.Errorf("%+v: edge %s -> %s present %v, test %v; want a test edge", opts, from, to, ok, e.Test)
		}
		wantEdge(t, g, graph.ID("go://example.com/uses/app#Analyze"), graph.KindReferences, to, graph.Resolved)
	}
}

// TestStructFields is #193's edge of the Go layer: a named struct type's
// fields are nodes go://pkg#Type.field, contained by the type, each
// referencing the named types of the graph its type mentions.
func TestStructFields(t *testing.T) {
	g, rep := extractDir(t, "fields", DefaultOptions())
	rid := func(s string) graph.ID { return graph.ID("go://example.com/fields/rec#" + s) }
	for _, f := range []string{"Node.Base", "Node.Next", "Node.Created", "Node.Meta", "Node.limit", "Base.ID", "Box.Item", "Holder.V"} {
		wantNode(t, g, rid(f), graph.KindField)
		typ, _, _ := strings.Cut(f, ".")
		wantEdge(t, g, rid(typ), graph.KindContains, rid(f), graph.Extracted)
	}
	wantEdge(t, g, rid("Node.Base"), graph.KindReferences, rid("Base"), graph.Resolved) // embedded
	wantEdge(t, g, rid("Node.Next"), graph.KindReferences, rid("Node"), graph.Resolved) // self
	wantNoEdge(t, g, rid("Holder.V"), graph.KindReferences, rid("Keyed"))               // a constraint the field does not write
	wantEdge(t, g, rid("Holder"), graph.KindReferences, rid("Keyed"), graph.Resolved)   // the type's declaration does
	for _, absent := range []string{"Node._", "Node.Meta.Owner", "Alias.Next", "Alias.Base", "fake.calls", "Copy.ID"} {
		if _, ok := g.Node(rid(absent)); ok {
			t.Errorf("%s is a node", absent)
		}
	}
	if n, _ := g.Node(rid("Node.limit")); n.Attrs["exported"] != "false" {
		t.Errorf("Node.limit: exported attr %q, want false", n.Attrs["exported"])
	}
	if rep.Fields != 8 {
		t.Errorf("report.Fields = %d, want 8", rep.Fields)
	}
	if n := len(g.Dropped()); n != 0 {
		t.Errorf("%d edges dropped: %v", n, g.Dropped())
	}
	gt, _ := extractDir(t, "fields", Options{Tests: true})
	if n, ok := gt.Node(rid("fake.calls")); !ok || !n.Test {
		t.Errorf("fake.calls with tests: present %v, test %v", ok, n.Test)
	}
}

// TestOnlyLoadsTheNamedPackages pins Options.Only (#25): a load of one
// package emits its declarations alone, and every edge leaving it that a
// full build has — an edge into a package it did not load named by ID, to
// resolve against a base graph, not a boundary edge to the package.
func TestOnlyLoadsTheNamedPackages(t *testing.T) {
	full, _ := extract(t, DefaultOptions())
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	opts := DefaultOptions()
	opts.Only = []string{fixtureModule + "/app"}
	if _, err := Extract(b, dir, opts); err != nil {
		t.Fatal(err)
	}
	sub := b.Build()
	for n := range sub.Nodes() {
		if strings.HasPrefix(string(n.ID), string(id("shapes"))) {
			t.Errorf("a package not asked for is in the subset: %s", n.ID)
		}
	}
	got := map[string]bool{}
	for e := range sub.Edges() {
		got[edgeLine(e)] = true
	}
	for _, d := range sub.Dropped() {
		got[edgeLine(d.Edge)] = true // its end is the base's
	}
	want := edgesFrom(full, id("app"))
	if len(want) == 0 {
		t.Fatal("the full build has no edge leaving app")
	}
	for k := range want {
		if !got[k] {
			t.Errorf("the subset lacks %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("the subset has %s, a full build does not", k)
		}
	}
	if !got[edgeLine(graph.Edge{From: id("app#Run"), To: id("shapes#Total"), Kind: graph.KindCalls})] {
		t.Error("app#Run's call into shapes is not to shapes#Total")
	}
}

func edgeLine(e graph.Edge) string {
	return string(e.From) + " -" + string(e.Kind) + "-> " + string(e.To)
}

// edgesFrom lists the edges leaving a package or a declaration of it.
func edgesFrom(g *graph.Graph, pkg graph.ID) map[string]bool {
	out := map[string]bool{}
	for e := range g.Edges() {
		if e.From == pkg || strings.HasPrefix(string(e.From), string(pkg)+"#") {
			out[edgeLine(e)] = true
		}
	}
	return out
}

// TestOnlyAPackageTheHostDoesNotBuild is #35: a load naming a package
// the host's configuration builds no file of fails its whole module, so
// the host's load leaves it out, and with no other package asked for
// the module is not loaded for the host at all. Without further
// configurations the package is skipped, as a load of every package
// skips it.
func TestOnlyAPackageTheHostDoesNotBuild(t *testing.T) {
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	dir := t.TempDir()
	for rel, src := range map[string]string{
		"go.mod":                     "module example.com/w\n\ngo 1.26\n",
		"a/a.go":                     "package a\n\nfunc A() int { return 1 }\n",
		"plat/plat_" + other + ".go": "package plat\n\nfunc P() int { return 1 }\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultOptions()
	opts.Only = []string{"example.com/w/plat"}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 0 || rep.Packages != 0 || rep.Decls != 0 {
		t.Errorf("failed %v, %d packages, %d declarations: want the module loaded for nothing", rep.Failed, rep.Packages, rep.Decls)
	}
	if !slices.ContainsFunc(rep.Skipped, func(s graph.Skip) bool { return s.File == "plat" }) {
		t.Errorf("plat is not skipped: %+v", rep.Skipped)
	}
}
