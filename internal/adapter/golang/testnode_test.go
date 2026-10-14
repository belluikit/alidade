// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// packageTestsFiles is #128's fixture: production files importing
// strings and net/http, and in-package test files that also import
// testing and crypto/tls, declare an init and a blank variable, and call
// production code. Production code calls a crypto/tls method through
// net/http's Response without importing crypto/tls.
var packageTestsFiles = map[string]string{
	"p/p.go": `package p

import (
	"net/http"
	"strings"
)

func Up(s string) string { return strings.ToUpper(s) }

func Export(r *http.Response) ([]byte, error) {
	return r.TLS.ExportKeyingMaterial("l", nil, 1)
}

func seed() int { return 1 }

func init() { _ = Up("x") }
`,
	"p/p_test.go": `package p

import (
	"crypto/tls"
	"strings"
	"testing"
)

var _ = seed()

func init() { _ = strings.TrimSpace(" ") }

func TestUp(t *testing.T) {
	_ = tls.VersionTLS13
	if Up("a") != "A" {
		t.Fatal("no")
	}
}
`,
}

// TestPackageTestsNode pins #128 (the owner's decision, ADR 004 rule 4,
// ADR 002 rule 5): a package's in-package test files have a node,
// go://<pkg>#(test), kind package_tests, a test node contained by the
// package; an import only they make, the init they declare and their blank
// variable's initializer are its, so the production view of a build with
// tests has the imports a build without tests has.
func TestPackageTestsNode(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, _ := module(t, packageTestsFiles, opts)
	n := wantNode(t, g, mID("p#(test)"), graph.KindPackageTests)
	if !n.Test || n.Pos.File != "p/p_test.go" {
		t.Errorf("test files' node = %+v; want a test node at p/p_test.go", n)
	}
	if e, ok := hasEdge(g, mID("p"), graph.KindContains, mID("p#(test)")); !ok || !e.Test {
		t.Errorf("p contains its test files: %v, %v; want a test edge", e, ok)
	}
	for _, imp := range []string{"testing", "crypto/tls"} {
		wantEdge(t, g, mID("p#(test)"), graph.KindImports, graph.ID("go://"+imp), graph.Extracted)
		wantNoEdge(t, g, mID("p"), graph.KindImports, graph.ID("go://"+imp))
	}
	// An import production files make too stays the package's.
	wantEdge(t, g, mID("p"), graph.KindImports, "go://strings", graph.Extracted)
	wantNoEdge(t, g, mID("p#(test)"), graph.KindImports, "go://strings")
	// The test init and the blank variable are the test files'.
	wantM(t, g, "p#(test)", graph.KindCalls, "p#seed")
	wantNoEdge(t, g, mID("p"), graph.KindCalls, mID("p#seed"))
	if init := wantNode(t, g, mID("p#init"), graph.KindFunc); init.Test || init.Attrs["inits"] != "" {
		t.Errorf("init = %+v; want the one production init", init)
	}
	wantM(t, g, "p#init", graph.KindCalls, "p#Up")
	wantNoDrops(t, g)
}

// TestExternalPackagesOnlyTestsUse pins the external half of #128: an
// external package is a test node when every edge into it is a test
// edge. testing is imported by test files only; crypto/tls is imported by
// test files only but called by production code (through net/http's
// Response), so it stays a production node.
func TestExternalPackagesOnlyTestsUse(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, _ := module(t, packageTestsFiles, opts)
	for ext, want := range map[string]bool{"testing": true, "crypto/tls": false, "strings": false} {
		if n, ok := g.Node(graph.ID("go://" + ext)); !ok || n.Test != want {
			t.Errorf("go://%s = %+v, %v; want present, test=%v", ext, n, ok, want)
		}
	}
}

// TestProductionImportsDoNotDependOnTests: the production imports of a
// build with tests are the imports of a build without them, and without
// tests there is no test files' node (#128).
func TestProductionImportsDoNotDependOnTests(t *testing.T) {
	imports := func(tests bool) []string {
		opts := DefaultOptions()
		opts.Tests = tests
		g, _ := module(t, packageTestsFiles, opts)
		if _, ok := g.Node(mID("p#(test)")); ok != tests {
			t.Errorf("tests=%v: test files' node present = %v", tests, ok)
		}
		var out []string
		for e := range g.Edges() {
			if e.Kind == graph.KindImports && !e.Test {
				out = append(out, string(e.From)+" "+string(e.To))
			}
		}
		slices.Sort(out)
		return out
	}
	if without, with := imports(false), imports(true); !slices.Equal(without, with) {
		t.Errorf("production imports differ:\n without tests: %v\n with tests:    %v", without, with)
	}
}

// exactFiles: production code calls io.Writer.Write; only a test brings
// net/http/httptest, whose ResponseRecorder is an io.Writer, into the
// program.
var exactFiles = map[string]string{
	"p/p.go":      "package p\n\nimport \"io\"\n\nfunc W(w io.Writer) { _, _ = w.Write(nil) }\n",
	"p/p_test.go": "package p\n\nimport (\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc TestW(t *testing.T) { W(httptest.NewRecorder()) }\n",
}

// productionView is a graph's production nodes and edges: what
// exclude_tests leaves.
func productionView(g *graph.Graph) (nodes, edges []string) {
	for n := range g.Nodes() {
		if !n.Test {
			nodes = append(nodes, string(n.ID))
		}
	}
	for e := range g.Edges() {
		if !e.Test {
			edges = append(edges, string(e.From)+" -"+string(e.Kind)+"-> "+string(e.To))
		}
	}
	slices.Sort(nodes)
	slices.Sort(edges)
	return nodes, edges
}

// TestExactTestsIsTheBuildWithoutTests pins #133 (the owner's decision):
// with ExactTests, the production view of a graph built with tests is the
// graph built without them, node for node and edge for edge. Without it, a
// production interface call gains a package-level edge into a package only
// a test brings in — the case the option exists for — which this asserts
// first, so the test cannot pass for want of the case.
func TestExactTestsIsTheBuildWithoutTests(t *testing.T) {
	opts := DefaultOptions()
	without, _ := module(t, exactFiles, opts)
	opts.Tests = true
	plain, _ := module(t, exactFiles, opts)
	if e, ok := hasEdge(plain, mID("p#W"), graph.KindCalls, "go://net/http/httptest"); !ok || e.Test {
		t.Fatalf("with tests, W has no production edge into httptest (%v, %v): the fixture does not show the case", e, ok)
	}
	opts.ExactTests = true
	exact, _ := module(t, exactFiles, opts)
	wn, we := productionView(without)
	xn, xe := productionView(exact)
	if !slices.Equal(wn, xn) || !slices.Equal(we, xe) {
		t.Errorf("the production view is not the build without tests:\n nodes %v\n    vs %v\n edges %v\n    vs %v", wn, xn, we, xe)
	}
	if n, ok := exact.Node("go://net/http/httptest"); !ok || !n.Test {
		t.Errorf("httptest = %+v, %v; want a test node", n, ok)
	}
	wantM(t, exact, "p#TestW", graph.KindCalls, "p#W")
	wantNoDrops(t, exact)
}

// TestExactTestsWithoutTestsIsTheOrdinaryBuild pins the state the CLI makes
// unreachable (-tests-exact implies -tests) but the option does not: with
// ExactTests and no Tests there is nothing to exclude, so the extraction is
// the ordinary one and the report does not claim exactness it did not
// compute, which is what makes graph_stats' go_tests: "exact" mean the
// second load really ran.
func TestExactTestsWithoutTestsIsTheOrdinaryBuild(t *testing.T) {
	opts := DefaultOptions()
	without, plainRep := module(t, exactFiles, opts)
	opts.ExactTests = true
	alone, rep := module(t, exactFiles, opts)
	if rep.ExactTests || plainRep.ExactTests {
		t.Errorf("ExactTests reported without Tests: %v, %v", rep.ExactTests, plainRep.ExactTests)
	}
	wn, we := productionView(without)
	an, ae := productionView(alone)
	if !slices.Equal(wn, an) || !slices.Equal(we, ae) {
		t.Errorf("the flag alone changed the graph:\n nodes %v\n    vs %v\n edges %v\n    vs %v", wn, an, we, ae)
	}
}

// TestExactTestsWithRecoveredConfigs pins the pair no fixture combined:
// -go-configs auto with -tests-exact. The constrained file is a test file,
// so the production load never reads the configuration that brings it in —
// the state where the two loads disagree about which platforms they read,
// and the widening is a whole platform rather than a package. Without
// exactness it leaks: the production interface call gains calls edges into
// packages only the recovered configuration's test brings in, which this
// asserts first so the comparison cannot pass for want of the case.
func TestExactTestsWithRecoveredConfigs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the constrained test file is the host's, so no configuration is recovered")
	}
	files := func() map[string]string {
		return map[string]string{
			"p/p.go":              "package p\n\nimport \"io\"\n\nfunc W(w io.Writer) { _, _ = w.Write(nil) }\n",
			"p/w_windows_test.go": "//go:build windows\n\npackage p\n\nimport (\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc TestWin(t *testing.T) { W(httptest.NewRecorder()) }\n",
		}
	}
	opts := DefaultOptions()
	opts.Configs = "auto"
	without, _ := module(t, files(), opts)
	opts.Tests = true
	plain, _ := module(t, files(), opts)
	if e, ok := hasEdge(plain, mID("p#W"), graph.KindCalls, "go://net/http/httptest"); !ok || e.Test {
		t.Fatalf("the recovered configuration left no production edge into httptest (%v, %v): the fixture does not show the case", e, ok)
	}
	opts.ExactTests = true
	exact, rep := module(t, files(), opts)
	// The recovered configuration must have run under exactness too, or
	// the comparison below holds for want of the second load.
	if n, ok := exact.Node(mID("p#TestWin")); !ok || !n.Test {
		t.Fatalf("p#TestWin = %+v, %v; want the test the recovered configuration brings in, configs %v", n, ok, rep.Configs)
	}
	wn, we := productionView(without)
	xn, xe := productionView(exact)
	if !slices.Equal(wn, xn) || !slices.Equal(we, xe) {
		t.Errorf("a recovered configuration changed the production view:\n nodes %v\n    vs %v\n edges %v\n    vs %v", wn, xn, we, xe)
	}
	// The report is the production graph's: the windows configuration
	// recovered only a test file, so it is not among the configurations
	// reported, which are the build without tests' (#142).
	_, prodRep := module(t, files(), func() Options { o := DefaultOptions(); o.Configs = "auto"; return o }())
	if !slices.Equal(rep.Configs, prodRep.Configs) {
		t.Errorf("Configs = %v; want the production graph's %v", rep.Configs, prodRep.Configs)
	}
	wantNoDrops(t, exact)
}

// graphModuleEdges counts, from the built graph, the edges of the fixture
// module into packages outside it, by kind and test-ness: the second
// derivation the report's ModuleEdges and TestModuleEdges are held to.
func graphModuleEdges(g *graph.Graph) (prod, test map[graph.EdgeKind]int) {
	prod, test = map[graph.EdgeKind]int{}, map[graph.EdgeKind]int{}
	for e := range g.Edges() {
		to, ok := g.Node(e.To)
		if !ok || to.Attrs["external"] != "true" || !strings.HasPrefix(string(e.From), "go://example.com/m") {
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

func reportEdges(es []ModuleEdge) map[graph.EdgeKind]int {
	out := map[graph.EdgeKind]int{}
	for _, e := range es {
		if e.From == "example.com/m" && e.To == "" {
			out[e.Kind] += e.Count
		}
	}
	return out
}

// TestModuleEdgesSplitByTest pins #142: with tests loaded, ModuleEdges
// counts the production edges and TestModuleEdges the test ones, each
// equal to the graph's own count, so the two sum to the graph's.
func TestModuleEdgesSplitByTest(t *testing.T) {
	opts := DefaultOptions()
	opts.Tests = true
	g, rep := module(t, exactFiles, opts)
	prod, test := graphModuleEdges(g)
	if len(test) == 0 || len(prod) == 0 {
		t.Fatalf("the fixture has production %v and test %v edges out of the module; want both", prod, test)
	}
	if got := reportEdges(rep.ModuleEdges); !maps.Equal(got, prod) {
		t.Errorf("ModuleEdges = %v, the graph's production edges %v", got, prod)
	}
	if got := reportEdges(rep.TestModuleEdges); !maps.Equal(got, test) {
		t.Errorf("TestModuleEdges = %v, the graph's test edges %v", got, test)
	}
}

// TestExactReportIsTheProductionGraphs pins #142 (the owner's decision,
// E+): with ExactTests the report describes the production graph — its
// module edges are the build without tests' and the exact graph's
// production ones, not the load with tests' (3 boundary calls where the
// graph had none), and its node and call counts are that build's too —
// and from the load with tests it keeps only the test node count and the
// test edges' module counts.
func TestExactReportIsTheProductionGraphs(t *testing.T) {
	opts := DefaultOptions()
	_, without := module(t, exactFiles, opts)
	opts.Tests, opts.ExactTests = true, true
	g, rep := module(t, exactFiles, opts)
	prod, test := graphModuleEdges(g)
	if got := reportEdges(rep.ModuleEdges); !maps.Equal(got, prod) || !maps.Equal(got, reportEdges(without.ModuleEdges)) {
		t.Errorf("ModuleEdges = %v; want the exact graph's production edges %v, as the build without tests reports %v", got, prod, reportEdges(without.ModuleEdges))
	}
	if got := reportEdges(rep.TestModuleEdges); !maps.Equal(got, test) {
		t.Errorf("TestModuleEdges = %v, the exact graph's test edges %v", got, test)
	}
	tests := 0
	for n := range g.Nodes() {
		if s, _ := n.ID.Scheme(); s == graph.SchemeGo && n.Test && n.Attrs["external"] != "true" {
			tests++
		}
	}
	if rep.Tests != tests || !rep.ExactTests {
		t.Errorf("Tests = %d, ExactTests = %v; want the graph's %d test nodes, exact", rep.Tests, rep.ExactTests, tests)
	}
	if got, want := productionCounts(rep), productionCounts(without); got != want {
		t.Errorf("Packages/Decls/Calls/Boundary = %v; want the build without tests' %v, the production view the report describes", got, want)
	}
	// Those are only pinned if the load with tests counts otherwise: the
	// exact report's point is that it does not report that load.
	opts.ExactTests = false
	_, with := module(t, exactFiles, opts)
	if productionCounts(with) == productionCounts(without) {
		t.Fatalf("the load with tests counts the same %v as the one without, so the fixture stopped telling the two apart", productionCounts(with))
	}
}

// productionCounts is the counters an exact report takes from the load
// without tests: Packages, Decls, Calls and Boundary (#133, #142).
func productionCounts(r *Report) [4]int {
	return [4]int{r.Packages, r.Decls, r.Calls, r.Boundary}
}

// TestExactReportSkipsOnce pins that the exact report lists a skipped
// go.mod once. The module walk is of the tree, not of a load, so both of
// exact mode's loads report the same unreadable go.mod (#92); a caller
// reconciling Skipped with the files it has would otherwise find the
// file twice and no second file.
func TestExactReportSkipsOnce(t *testing.T) {
	files := maps.Clone(exactFiles)
	files["plug/go.mod"] = "go 1.26\n"
	opts := DefaultOptions()
	opts.Tests, opts.ExactTests = true, true
	_, rep := module(t, files, opts)
	var got []string
	for _, s := range rep.Skipped {
		got = append(got, s.File)
	}
	if !slices.Equal(got, []string{"plug/go.mod"}) {
		t.Errorf("Skipped = %v; want plug/go.mod exactly once, though both of exact mode's loads walk it", got)
	}
}

// initCallers is #142's hardest fixture: a package of n production inits,
// each calling a different package outside the module, and an in-package
// test file with one more. A package's inits share one ID (ADR 002 rule 1
// forbids a positional one), so marking the test file's as a test
// declaration marks every init of the package; the call graph walks them
// sorted by name, and with ten of them "init#10" — the test file's —
// sorts between "init#1" and "init#2", so the eight production inits
// after it had their module edges booked as test ones. Distinct callee
// packages are what makes that visible: an edge is counted once, and
// every function of one package outside the module lands on that
// package's single node, so n calls to one package are one edge.
func initCallers(n int) map[string]string {
	calls := []struct{ pkg, expr string }{
		{"strings", `_ = strings.ToUpper("a")`},
		{"strconv", `_ = strconv.Itoa(1)`},
		{"path", `_ = path.Base("/a")`},
		{"sort", `sort.Ints(nil)`},
		{"math", `_ = math.Abs(1)`},
		{"errors", `_ = errors.New("a")`},
		{"unicode", `_ = unicode.IsDigit('1')`},
		{"bytes", `_ = bytes.Contains(nil, nil)`},
		{"hash/crc32", `_ = crc32.ChecksumIEEE(nil)`},
	}
	var imports, body strings.Builder
	for _, c := range calls[:n] {
		imports.WriteString("\t\"" + c.pkg + "\"\n")
		body.WriteString("func init() { " + c.expr + " }\n")
	}
	return map[string]string{
		"q/q.go":      "package q\n\nimport (\n" + imports.String() + ")\n\n" + body.String(),
		"q/q_test.go": "package q\n\nimport \"text/tabwriter\"\n\nfunc init() { _ = tabwriter.NewWriter(nil, 0, 0, 0, ' ', 0) }\n",
	}
}

// TestInitEdgesOfATestFile pins #142 where the counts are easiest to get
// wrong: an init in an in-package test file makes its own module edges
// test ones and leaves the package's production inits' alone, however
// many there are. Both sides are read from the graph, which the core
// marks from the endpoints, so the report cannot drift from it. One init
// passes whatever the code does — the test file's sorts last — which is
// why nine is here too.
func TestInitEdgesOfATestFile(t *testing.T) {
	for _, n := range []int{1, 9} {
		opts := DefaultOptions()
		opts.Tests = true
		g, rep := module(t, initCallers(n), opts)
		prod, test := graphModuleEdges(g)
		if prod[graph.KindCalls] != n || test[graph.KindCalls] != 1 {
			t.Fatalf("%d production inits: the graph has production %v and test %v edges out of the module; want %d calls and 1", n, prod, test, n)
		}
		if got := reportEdges(rep.ModuleEdges); !maps.Equal(got, prod) {
			t.Errorf("%d production inits: ModuleEdges = %v, the graph's production edges %v", n, got, prod)
		}
		if got := reportEdges(rep.TestModuleEdges); !maps.Equal(got, test) {
			t.Errorf("%d production inits: TestModuleEdges = %v, the graph's test edges %v", n, got, test)
		}
	}
}

// TestInitEdgesOfAFoldedTestFile pins the other half of the same rule.
// An init the fold gives the test files' node as its From — an external
// test package's, or one in a directory with no production file — has no
// production init sharing its ID to mark, and its module edges are test
// ones all the same. Leaving the mark out altogether, rather than only
// where the fold applies, books both of these as production.
func TestInitEdgesOfAFoldedTestFile(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"external test package": {
			"q/q.go":      "package q\n\nimport \"strings\"\n\nfunc init() { _ = strings.ToUpper(\"a\") }\n\nfunc F() {}\n",
			"q/x_test.go": "package q_test\n\nimport (\n\t\"text/tabwriter\"\n\n\t\"example.com/m/q\"\n)\n\nfunc init() { _ = tabwriter.NewWriter(nil, 0, 0, 0, ' ', 0) }\n\nfunc use() { q.F() }\n",
		},
		"test-only directory": {
			"q/q.go":      "package q\n\nimport \"strings\"\n\nfunc init() { _ = strings.ToUpper(\"a\") }\n",
			"h/h_test.go": "package h\n\nimport \"text/tabwriter\"\n\nfunc init() { _ = tabwriter.NewWriter(nil, 0, 0, 0, ' ', 0) }\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOptions()
			opts.Tests = true
			g, rep := module(t, files, opts)
			prod, test := graphModuleEdges(g)
			if prod[graph.KindCalls] != 1 || test[graph.KindCalls] != 1 {
				t.Fatalf("the graph has production %v and test %v edges out of the module; want one call each", prod, test)
			}
			if got := reportEdges(rep.ModuleEdges); !maps.Equal(got, prod) {
				t.Errorf("ModuleEdges = %v, the graph's production edges %v", got, prod)
			}
			if got := reportEdges(rep.TestModuleEdges); !maps.Equal(got, test) {
				t.Errorf("TestModuleEdges = %v, the graph's test edges %v", got, test)
			}
		})
	}
}
