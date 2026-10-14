// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// TestComparePyCalls: a traced call is found when the graph has the edge
// from the declaration spanning its line, by the callee's name at or
// after its first line (a decorator's); a call of __init__ is found as
// the class's call, or a subclass's that inherits it (#249); a call into
// a lambda, or a file the layer did not read, is counted apart; a
// module's __getattr__ called from an import line is the import system's
// probe, counted apart too (#254), and from any other line missing; and the
// one the graph lacks is listed.
func TestComparePyCalls(t *testing.T) {
	root := t.TempDir()
	// a.py's lines 8 and 11, which the probes are told apart by.
	src := strings.Repeat("\n", 7) + "    from b import g\n\n\nx = b.LAZY\n"
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(root)
	add := func(id string, kind graph.Kind, name, file string, line int, end string) {
		attrs := map[string]string{}
		if end != "" {
			attrs["end_line"] = end
		}
		if err := b.AddNode(graph.Node{ID: graph.ID(id), Kind: kind, Name: name, Pos: graph.Position{File: file, Line: line}, Attrs: attrs}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(from, to string) {
		if err := b.AddEdge(graph.Edge{From: graph.ID(from), To: graph.ID(to), Kind: graph.KindCalls, Derivation: graph.Resolved}); err != nil {
			t.Fatal(err)
		}
	}
	add("py://a.py", graph.KindModule, "a.py", "a.py", 0, "")
	add("py://a.py#main", graph.KindFunc, "main", "a.py", 1, "9")
	add("py://a.py#main.inner", graph.KindFunc, "inner", "a.py", 3, "4")
	add("py://b.py", graph.KindModule, "b.py", "b.py", 0, "")
	add("py://b.py#S", graph.KindType, "S", "b.py", 1, "6")
	add("py://b.py#S.__init__", graph.KindMethod, "__init__", "b.py", 2, "3")
	add("py://b.py#f", graph.KindFunc, "f", "b.py", 9, "10") // decorated: first line 8
	add("py://b.py#g", graph.KindFunc, "g", "b.py", 12, "13")
	add("py://b.py#T", graph.KindType, "T", "b.py", 15, "16") // class T(S), no __init__
	add("py://b.py#__getattr__", graph.KindFunc, "__getattr__", "b.py", 20, "21")
	call("py://a.py#main", "py://b.py#S")
	call("py://a.py#main.inner", "py://b.py#f")
	call("py://a.py", "py://b.py#g")
	call("py://a.py", "py://b.py#T")
	if err := b.AddEdge(graph.Edge{From: "py://b.py#T", To: "py://b.py#S", Kind: graph.KindImplements, Derivation: graph.Resolved}); err != nil {
		t.Fatal(err)
	}
	traced := []PyTraceCall{
		{"a.py", 2, "b.py", "__init__", 2},      // S(): the class's call
		{"a.py", 4, "b.py", "f", 8},             // in inner, a decorated f
		{"a.py", 12, "b.py", "g", 12},           // at module level: the file's
		{"a.py", 13, "b.py", "__init__", 2},     // T(): S's, inherited
		{"a.py", 5, "b.py", "g", 12},            // in main: no such edge
		{"a.py", 6, "b.py", "<lambda>", 7},      // no declaration
		{"a.py", 6, "c.py", "h", 1},             // a file not read
		{"a.py", 7, "b.py", "gone", 1},          // no node of that name
		{"a.py", 8, "b.py", "__getattr__", 20},  // from b import g: a probe
		{"a.py", 11, "b.py", "__getattr__", 20}, // b.LAZY: missing
	}
	r := ComparePyCalls(b.Build(), root, traced)
	if r.Edges != [2]int{7, 4} || r.ImportProbes != 1 || r.NoNode != 1 || r.Unread != 1 || r.Traced != 10 {
		t.Errorf("result %+v", r)
	}
	if want := []string{"py://a.py -> py://b.py#__getattr__ (a.py:11 calls __getattr__ at b.py:20)",
		"py://a.py#main -> py://b.py#g (a.py:5 calls g at b.py:12)"}; !slices.Equal(r.Missing, want) {
		t.Errorf("missing %v, want %v", r.Missing, want)
	}
}
