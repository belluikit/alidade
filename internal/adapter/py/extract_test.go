// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
)

func extractFixture(t *testing.T) (*graph.Graph, *Report) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "proj"))
	if err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return b.Build(), rep
}

// edges lists a graph's edges of a kind as "from -> to", schemes dropped.
func edges(g *graph.Graph, kind graph.EdgeKind) []string {
	var out []string
	for e := range g.Edges() {
		if e.Kind == kind {
			out = append(out, strings.TrimPrefix(string(e.From), "py://")+" -> "+strings.TrimPrefix(string(e.To), "py://"))
		}
	}
	slices.Sort(out)
	return out
}

// TestExtractFiles: the files the layer reads — not a virtual environment
// (a pyvenv.cfg, whatever the directory's name), __pycache__ or testdata,
// which it counts — and, built without the python tag, each says its
// declarations and calls are not in the graph.
func TestExtractFiles(t *testing.T) {
	g, rep := extractFixture(t)
	var files []string
	for n := range g.Nodes() {
		if n.Kind == graph.KindModule {
			files = append(files, n.Pos.File)
			if got := n.Attrs["calls_unavailable"]; (got != "") == python.Available() {
				t.Errorf("%s calls_unavailable %q: it says so when, and only when, the build has no CPython", n.ID, got)
			}
		}
	}
	slices.Sort(files)
	want := []string{"app.py", "bad.py", "lib/helperlib.py", "pkg/__init__.py", "pkg/shapes.py", "scripts/tool.py", "uses_env.py"}
	if !slices.Equal(files, want) {
		t.Errorf("files %v, want %v", files, want)
	}
	if rep.Files != 7 || rep.Testdata != 1 {
		t.Errorf("Files %d, Testdata %d; want 7, 1", rep.Files, rep.Testdata)
	}
	if !python.Available() && (rep.Unanalyzed != 7 || rep.Declarations != 0 || len(edges(g, graph.KindCalls)) != 0) {
		t.Errorf("without CPython: unanalyzed %d, declarations %d, calls %v", rep.Unanalyzed, rep.Declarations, edges(g, graph.KindCalls))
	}
}

// TestExtractGraph pins the layer on the fixture, built with the python
// tag: declarations under their file or enclosing declaration, imports
// between files read, and calls from the declaration holding them (the
// file at module level), RESOLVED.
func TestExtractGraph(t *testing.T) {
	if !python.Available() {
		t.Skip("built without the python tag")
	}
	g, rep := extractFixture(t)
	calls := []string{
		"app.py#main -> app.py#total",
		"app.py#main -> pkg/shapes.py#Square",
		"app.py#main -> pkg/shapes.py#Square.area",
		"app.py#main -> pkg/shapes.py#helper",
		"app.py#main.inner -> app.py#main",
		"app.py#total -> pkg/shapes.py#Square.area",
		"pkg/shapes.py -> pkg/shapes.py#large",
		"pkg/shapes.py -> pkg/shapes.py#logged",
		"pkg/shapes.py -> pkg/shapes.py#small",
		"pkg/shapes.py#Square.area -> pkg/shapes.py#helper",
		"pkg/shapes.py#double -> pkg/shapes.py#helper",
		"scripts/tool.py -> lib/helperlib.py#go",
	}
	if got := edges(g, graph.KindCalls); !slices.Equal(got, calls) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(calls, "\n"))
	}
	// A function named as a value, not called, is referenced (#244).
	refs := []string{"app.py#main -> app.py#main.inner", "pkg/shapes.py -> pkg/shapes.py#large", "pkg/shapes.py -> pkg/shapes.py#small",
		"pkg/shapes.py#logged -> pkg/shapes.py#logged.wrapper"}
	if got := edges(g, graph.KindReferences); !slices.Equal(got, refs) || rep.References != len(refs) {
		t.Errorf("references %v (counted %d), want %v", got, rep.References, refs)
	}
	// A class implements the class it inherits from (#249). Mine's base is
	// in a virtual environment's package, inside the root but not read:
	// outside, with no edge.
	if got, want := edges(g, graph.KindImplements), []string{"pkg/shapes.py#Square -> pkg/shapes.py#Shape"}; !slices.Equal(got, want) ||
		[...]int{rep.Bases, rep.BasesOutside, rep.BasesUnplaced, rep.BasesUnresolved} != [...]int{1, 1, 0, 0} {
		t.Errorf("implements %v, bases %d outside %d unplaced %d unresolved %d; want %v, 1 1 0 0", got, rep.Bases, rep.BasesOutside, rep.BasesUnplaced, rep.BasesUnresolved, want)
	}
	imports := []string{"app.py -> pkg/__init__.py", "app.py -> pkg/shapes.py", "pkg/__init__.py -> pkg/shapes.py", "scripts/tool.py -> lib/helperlib.py"}
	if got := edges(g, graph.KindImports); !slices.Equal(got, imports) {
		t.Errorf("imports %v, want %v", got, imports)
	}
	checkResolved(t, g)
	checkDeclarations(t, g)
	// 19 declarations in the engine's fixture, and Mine in uses_env.py.
	// Imports outside: functools, os and sys in tool.py, sys in
	// uses_env.py, and vendored, inside the root in a virtual
	// environment. Call sites: 11 placed (@logged among them), and outside sum, print,
	// functools.wraps, four os.path calls, sys.path.insert in uses_env.py
	// and vendored.vend(), whose file the layer does not read; fn(*a) is
	// unplaced.
	got := [...]int{rep.Declarations, rep.Imports, rep.ImportsOutside, rep.ImportsUnresolved, rep.CallSites, rep.Calls, rep.CallsOutside, rep.CallsUnplaced, rep.CallsUnresolved, rep.Unparsed}
	if want := [...]int{20, 4, 5, 0, 21, 12, 9, 1, 0, 1}; got != want {
		t.Errorf("Declarations, Imports, ImportsOutside, ImportsUnresolved, CallSites, Calls, CallsOutside, CallsUnplaced, CallsUnresolved, Unparsed = %v, want %v", got, want)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].File != "bad.py" || !strings.Contains(rep.Skipped[0].Reason, "SyntaxError") {
		t.Errorf("skipped %+v, want bad.py's SyntaxError", rep.Skipped)
	}
}

// checkDeclarations: declarations under their file or enclosing
// declaration, with their kind and lines.
func checkDeclarations(t *testing.T, g *graph.Graph) {
	t.Helper()
	contains := edges(g, graph.KindContains)
	for _, w := range []string{"app.py -> app.py#main", "app.py#main -> app.py#main.inner", "pkg/shapes.py#Square -> pkg/shapes.py#Square.area",
		"pkg/shapes.py#Square -> pkg/shapes.py#Square.sides", "pkg/shapes.py -> pkg/shapes.py#UNIT"} {
		if !slices.Contains(contains, w) {
			t.Errorf("no contains edge %s", w)
		}
	}
	for id, want := range map[string]struct {
		kind graph.Kind
		line int
		end  string
	}{
		"py://pkg/shapes.py#Square": {graph.KindType, 13, "20"}, "py://pkg/shapes.py#Square.area": {graph.KindMethod, 19, "20"},
		"py://pkg/shapes.py#helper": {graph.KindFunc, 23, "24"}, "py://pkg/shapes.py#UNIT": {graph.KindVar, 3, "3"},
		"py://pkg/shapes.py#Shape.sides": {graph.KindField, 7, "7"}, "py://app.py#main.inner": {graph.KindFunc, 14, "15"},
	} {
		n, ok := g.Node(graph.ID(id))
		if !ok || n.Kind != want.kind || n.Pos.Line != want.line || n.Attrs["end_line"] != want.end {
			t.Errorf("%s = %+v, want %s at %d to %s", id, n, want.kind, want.line, want.end)
		}
	}
}

// checkResolved: calls, imports and references are RESOLVED.
func checkResolved(t *testing.T, g *graph.Graph) {
	t.Helper()
	for e := range g.Edges() {
		if (e.Kind == graph.KindCalls || e.Kind == graph.KindImports || e.Kind == graph.KindReferences) && e.Derivation != graph.Resolved {
			t.Errorf("%s %s -> %s is %s, want RESOLVED", e.Kind, e.From, e.To, e.Derivation)
		}
	}
}

// TestWorkerFailureIsSkipped: a worker that fails changes nothing in the
// graph, only the build's time, so it is said where skipped entries are.
func TestWorkerFailureIsSkipped(t *testing.T) {
	if !python.Available() {
		t.Skip("needs the python build tag")
	}
	defer func(f func(string, []python.File, int) (*python.Result, error)) { analyzeFiles = f }(analyzeFiles)
	analyzeFiles = func(root string, files []python.File, workers int) (*python.Result, error) {
		r, err := python.Analyze(root, files, 1)
		if err == nil {
			r.Workers, r.WorkerErrors = 3, []string{"python worker: exit status 2: boom", "python worker: exit status 2: boom"}
		}
		return r, err
	}
	_, rep := extractFixture(t)
	var said []string
	for _, s := range rep.Skipped {
		if strings.Contains(s.Reason, "Python workers failed") {
			said = append(said, s.Reason)
		}
	}
	want := "2 of 3 Python workers failed and their files were read in this process instead, more slowly: python worker: exit status 2: boom"
	if len(said) != 1 || said[0] != want {
		t.Errorf("worker failures said as %q, want once, %q", said, want)
	}
}
