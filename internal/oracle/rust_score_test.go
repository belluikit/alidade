// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// scoreGraph is rustGraph with positions and a written_by attribute: each
// node is at line of src/lib.rs, and the names in written are a derive's.
func scoreGraph(t *testing.T, nodes map[string][]string, lines map[string]int, written ...string) (*graph.Graph, map[string]graph.ID) {
	t.Helper()
	b := graph.NewBuilder("/repo")
	ids := map[string]graph.ID{}
	for name, path := range nodes {
		id, err := b.Minter().RustID("src/lib.rs", path...)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
		kind := graph.KindFunc
		if len(path) > 1 {
			kind = graph.KindMethod
		}
		n := graph.Node{ID: id, Kind: kind, Name: name, Pos: graph.Position{File: "src/lib.rs", Line: lines[name]}}
		if slices.Contains(written, name) {
			n.Attrs = map[string]string{"written_by": "a derive"}
		}
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build(), ids
}

// TestRustScore scores a tool's edges against rustc's: found, found as
// the trait's method (a generic bound), missed; and of its own edges
// right, bound, through dyn, wrong. An edge to a function with no node is
// in no score, and neither is a caller rustc did not compile. Written
// leaves out a derive's calls and an operator's.
func TestRustScore(t *testing.T) {
	g, ids := scoreGraph(t, map[string][]string{
		"write": {"write()"}, "helper": {"helper()"}, "other": {"other()"}, "idle": {"idle()"},
		"encode": {"Message", "encode()"}, "bigEncode": {"Big[Message]", "encode()"},
		"visit": {"Visitor", "visit()"}, "implVisit": {"Printer[Visitor]", "visit()"},
		"clone": {"Big[Clone]", "clone()"}, "eq": {"Big[PartialEq]", "eq()"},
		"case": {"tests", "case()"}, // an inline tests module: not in a library's build
	}, nil, "clone")
	rc := &RustCalls{Lib: RustLib{Package: "p", Crate: "p", Dir: "src"}, Calls: map[string]map[string]bool{
		"write":                 {"<Big as Message>::encode": true, "helper": true, "<Big as PartialEq>::eq": true, "nonode": true},
		"<Big as Clone>::clone": {"helper": true},
		"orphan":                {"helper": true, "<Ghost as PartialEq>::eq": true}, // a caller with no node; == on a type with none
	}, Funcs: map[string]bool{"write": true, "<Printer as Visitor>::visit": true},
		Locs: map[string][]string{"write": {"/repo/src/lib.rs:1"}, "nonode": {"/repo/src/lib.rs:20"}, "orphan": {"/repo/src/lib.rs:30"}, "helper": {"/repo/src/lib.rs:2"}}}
	ref := NewRustReference(g, rc, "/repo")
	if ref.Excluded != 0 || len(ref.uncovered) != 3 {
		t.Errorf("excluded %d, uncovered %v; want 0, and the edges to and from a function with no node", ref.Excluded, ref.uncovered)
	}
	tool := ref.Named(map[graph.ID]map[graph.ID]bool{
		ids["write"]: {ids["encode"]: true, ids["helper"]: true, ids["visit"]: true, ids["other"]: true},
		ids["idle"]:  {ids["helper"]: true}, // rustc compiled no idle: not scored
		ids["case"]:  {ids["helper"]: true}, // a tests module's: not named
	})
	if tool["tests::case"] != nil {
		t.Errorf("named %v; want no caller in a tests module", tool)
	}
	tool["write"]["gone"] = true // to a function with no node: not scored
	s := ref.Score("tool", tool)
	got := []int{s.Reference, s.Found, s.FoundBound, s.Edges, s.Right, s.Bound, s.Dyn, len(s.Wrong)}
	if want := []int{4, 1, 1, 4, 1, 1, 1, 1}; !slices.Equal(got, want) {
		t.Errorf("reference, found, found bound, edges, right, bound, dyn, wrong = %v; want %v", got, want)
	}
	if !slices.Equal(s.Wrong, []string{"write -> other"}) || !slices.Equal(s.Missed, []string{"<Big as Clone>::clone -> helper", "write -> <Big as PartialEq>::eq"}) {
		t.Errorf("wrong %v, missed %v", s.Wrong, s.Missed)
	}
	// The two edges with an end the layer has no node for are scored where
	// rustc declares their ends: the tool has write -> nonode there.
	ref.ScoreUncovered(s, "/repo", map[string]map[string]bool{"src/lib.rs:1": {"src/lib.rs:20": true}})
	if s.Uncovered != 3 || s.FoundUncovered != 1 {
		t.Errorf("uncovered %d, found %d; want 3, 1", s.Uncovered, s.FoundUncovered)
	}
	w := ref.Written()
	ws := w.Score("tool", tool)
	if ws.Reference != 2 || ws.Found+ws.FoundBound != 2 || w.Excluded != 3 || ref.Excluded != 0 || len(w.uncovered) != 2 {
		t.Errorf("written: reference %d, found %d, excluded %d (reference's %d), uncovered %v; want 2, 2, 3, 0 unchanged, and the == left out",
			ws.Reference, ws.Found+ws.FoundBound, w.Excluded, ref.Excluded, w.uncovered)
	}
}

// TestReadGraphifyRust places graphify's functions on the layer's nodes by
// file and line, and keeps its calls edges between two placed ones, with
// the INFERRED ones counted and left out of Extracted.
func TestReadGraphifyRust(t *testing.T) {
	g, ids := scoreGraph(t, map[string][]string{"f": {"f()"}, "m": {"T", "m()"}, "h": {"h()"}},
		map[string]int{"f": 1, "m": 3, "h": 5})
	path := filepath.Join(t.TempDir(), "graph.json")
	const gj = `{"nodes": [
	  {"id": "lib_f", "label": "f()", "source_file": "src/lib.rs", "source_location": "L1"},
	  {"id": "lib_t_m", "label": ".m()", "source_file": "src/lib.rs", "source_location": "L3"},
	  {"id": "lib_h", "label": "h()", "source_file": "src/lib.rs", "source_location": "L5"},
	  {"id": "lib_g", "label": "g()", "source_file": "src/lib.rs", "source_location": "L9"},
	  {"id": "lib_t", "label": "T", "source_file": "src/lib.rs", "source_location": "L2"},
	  {"id": "doc", "label": "f()", "source_file": "README.md", "source_location": "L1"}],
	 "links": [
	  {"source": "lib_f", "target": "lib_t_m", "relation": "calls", "confidence": "EXTRACTED"},
	  {"source": "lib_f", "target": "lib_h", "relation": "calls", "confidence": "INFERRED"},
	  {"source": "lib_f", "target": "lib_g", "relation": "calls", "confidence": "EXTRACTED"},
	  {"source": "lib_f", "target": "lib_t", "relation": "references", "confidence": "EXTRACTED"}]}`
	if err := os.WriteFile(path, []byte(gj), 0o644); err != nil {
		t.Fatal(err)
	}
	gf, err := ReadGraphifyRust(path, g)
	if err != nil {
		t.Fatal(err)
	}
	got := []int{gf.Functions, gf.Placed, gf.Ambiguous, gf.Edges, gf.Inferred, gf.Unplaced}
	if want := []int{4, 3, 0, 3, 1, 1}; !slices.Equal(got, want) {
		t.Errorf("functions, placed, ambiguous, edges, inferred, unplaced = %v; want %v", got, want)
	}
	f := ids["f"]
	if len(gf.Calls[f]) != 2 || !gf.Calls[f][ids["m"]] || !gf.Calls[f][ids["h"]] {
		t.Errorf("calls %v; want f -> m and f -> h", gf.Calls)
	}
	if len(gf.Extracted[f]) != 1 || !gf.Extracted[f][ids["m"]] {
		t.Errorf("extracted %v; want f -> m alone", gf.Extracted)
	}
	if !gf.At["src/lib.rs:1"]["src/lib.rs:9"] || gf.At["src/lib.rs:1"]["src/lib.rs:1"] {
		t.Errorf("at %v; want f -> g by line, placed or not", gf.At)
	}
}

// TestRustPlaceByLine names a function rustc calls differently from the
// layer by the layer's function declared on its line (a module `#[path]`
// renames), and leaves one with two candidates on its line as it is.
func TestRustPlaceByLine(t *testing.T) {
	g, ids := scoreGraph(t, map[string][]string{"run": {"run()"}, "stdio": {"stdio()"}, "a": {"a()"}, "b": {"b()"}, "spawn": {"spawn()"}},
		map[string]int{"run": 1, "stdio": 40, "a": 50, "b": 50, "spawn": 60})
	rc := &RustCalls{Lib: RustLib{Crate: "p", Dir: "src"}, Calls: map[string]map[string]bool{
		"run": {"imp::stdio": true, "imp::two": true},
	}, Funcs: map[string]bool{"run": true, "imp::spawn": true}, // imp::spawn compiled, calling nothing rustc names
		Locs: map[string][]string{"run": {"/repo/src/lib.rs:1"}, "imp::stdio": {"/repo/src/lib.rs:40"}, "imp::two": {"/repo/src/lib.rs:50"},
			"imp::spawn": {"/repo/src/lib.rs:60"}}}
	ref := NewRustReference(g, rc, "/repo")
	s := ref.Score("tool", ref.Named(map[graph.ID]map[graph.ID]bool{ids["run"]: {ids["stdio"]: true}, ids["spawn"]: {ids["run"]: true}}))
	if ref.Renamed != 2 || s.Reference != 1 || s.Found != 1 || len(ref.uncovered) != 1 {
		t.Errorf("renamed %d, reference %d, found %d, uncovered %v; want 2, 1, 1, and run -> imp::two", ref.Renamed, s.Reference, s.Found, ref.uncovered)
	}
	// spawn is compiled under its rustc name: its edge is scored, and wrong.
	if s.Edges != 2 || len(s.Wrong) != 1 {
		t.Errorf("edges %d, wrong %v; want 2, and spawn -> run", s.Edges, s.Wrong)
	}
}
