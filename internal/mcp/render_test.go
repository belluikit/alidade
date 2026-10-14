// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// renderedFacts names, for every field of the core's Node and Edge, the
// key it is rendered under on the agent-facing surface. A field added to
// either type without a line here fails the test below, and one that is
// not in the rendered JSON fails it too.
//
// This exists because the surface silently stopped rendering the test
// flag when it moved from an attribute to a field (#90): the fact stayed
// in the core and kept filtering correctly, so every behavioural test
// passed while a caller asking "is this a test declaration" was left with
// the filename to guess from. A fact the core holds is either legible on
// this surface or deliberately absent, never absent by omission.
var renderedFacts = struct {
	node, edge map[string]string
}{
	node: map[string]string{
		"ID": "id", "Kind": "k", "Name": "n", "Pos": "at", "Generated": "gen", "Test": "test", "Attrs": "attrs",
	},
	edge: map[string]string{
		"From": "f", "To": "t", "Kind": "k", "Derivation": "d", "Test": "test", "Attrs": "attrs",
	},
}

func TestRenderedFactsAreComplete(t *testing.T) {
	n := graph.Node{
		ID: "go://example.com/p#F", Kind: graph.KindFunc, Name: "F",
		Pos: graph.Position{File: "p/f_test.go", Line: 3, Col: 1}, Generated: true, Test: true,
		Attrs: map[string]string{"exported": "false"},
	}
	e := graph.Edge{
		From: "go://example.com/p", To: "go://example.com/p#F", Kind: graph.KindContains,
		Derivation: graph.Extracted, Test: true, Attrs: map[string]string{"boundary": "package"},
	}
	checkRendered(t, "Node", reflect.ValueOf(n), renderedFacts.node, renderNode(n))
	checkRendered(t, "Edge", reflect.ValueOf(e), renderedFacts.edge, renderEdge(e))
}

// checkRendered asserts that every field of a core type has a rendered
// key, that the fixture sets it (so an absent key means a dropped fact
// and not an empty one), and that the key is in the JSON.
func checkRendered(t *testing.T, what string, v reflect.Value, keys map[string]string, rendered any) {
	t.Helper()
	b, err := json.Marshal(rendered)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for i := range v.NumField() {
		f := v.Type().Field(i)
		key, ok := keys[f.Name]
		if !ok {
			t.Errorf("%s.%s has no rendered key: render it in %sOut and add it to renderedFacts, or record here why the surface omits it", what, f.Name, strings.ToLower(what))
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("the fixture leaves %s.%s zero, so this test could not tell a rendered fact from an omitted one", what, f.Name)
		}
		if !strings.Contains(out, `"`+key+`":`) {
			t.Errorf("%s.%s is not rendered: no %q in %s", what, f.Name, key, out)
		}
	}
}

// TestFactsAreLegibleOverTheProtocol is the same rule from the outside,
// through the real protocol rather than the renderer: a caller asking
// about a declaration can see that it is a test one, and that an edge
// touching it is a test edge, without inspecting a filename (#90). The
// generated flag is asserted alongside it, since it had the same hole —
// no test asserted it either.
func TestFactsAreLegibleOverTheProtocol(t *testing.T) {
	g, ids4 := testGraph(t)
	load, ready := ids4[1], ids4[3]
	h := &harness{t: t}
	h.c = connect(t, New(func() (*graph.Graph, BuildInfo, error) {
		return g, BuildInfo{Root: "/repo", Layers: []string{"go"}}, nil
	}))

	if r := h.callJSON("get_node", map[string]any{"id": ready}); r["test"] != true {
		t.Errorf("get_node on a test declaration = %v; want test:true", r)
	}
	if r := h.callJSON("get_node", map[string]any{"id": load}); r["test"] != nil {
		t.Errorf("get_node on a production declaration = %v; want no test flag", r)
	}
	r := h.callJSON("find_nodes", map[string]any{"query": "assertReady"})
	if ns := r["nodes"].([]any); len(ns) != 1 || ns[0].(map[string]any)["test"] != true {
		t.Errorf("find_nodes = %v; want the test declaration flagged", r["nodes"])
	}
	r = h.callJSON("neighbors", map[string]any{"id": load, "direction": "in", "edge_kinds": []string{"calls"}})
	seen := map[string]bool{}
	for _, e := range r["edges"].([]any) {
		m := e.(map[string]any)
		seen[end(r, m["f"])] = m["test"] == true
	}
	if !seen[ready] {
		t.Errorf("the edge from the test caller is not flagged: %v", r["edges"])
	}
	if seen[ids4[2]] {
		t.Errorf("the edge from the production caller is flagged: %v", r["edges"])
	}
}

// TestGeneratedIsLegible pins the same for generated code, which the
// surface has always rendered and no test asserted.
func TestGeneratedIsLegible(t *testing.T) {
	h := newHarness(t)
	if r := h.callJSON("get_node", map[string]any{"id": idCfgType}); r["gen"] != true {
		t.Errorf("get_node on a generated declaration = %v; want gen:true", r)
	}
	if r := h.callJSON("get_node", map[string]any{"id": idLoad}); r["gen"] != nil {
		t.Errorf("get_node on a hand-written declaration = %v; want no gen flag", r)
	}
}

// TestStatsCapsSkipped: graph_stats takes no max_tokens, so every
// unbounded list it carries is capped and says how many it left out — a
// repository whose names mostly hold spaces has one skip per file, and
// one broken package puts every transitive importer of it in
// go_packages_without_calls.
func TestStatsCapsSkipped(t *testing.T) {
	info := BuildInfo{}
	for i := range maxSkipped + 7 {
		info.Skipped = append(info.Skipped, graph.Skip{Layer: "md", File: "docs/My Notes " + strconv.Itoa(i) + ".md", Reason: "segment contains whitespace"})
		info.GoPackagesWithoutCalls = append(info.GoPackagesWithoutCalls, "example.com/m/p"+strconv.Itoa(i))
		info.GoPackagesFailed = append(info.GoPackagesFailed, "example.com/m/b"+strconv.Itoa(i))
	}
	o := renderStats(graph.NewBuilder("").Build(), info)
	if len(o.Skipped) != maxSkipped {
		t.Errorf("itemized %d skips, want %d", len(o.Skipped), maxSkipped)
	}
	if o.SkippedOmitted != 7 {
		t.Errorf("skipped_omitted = %d, want 7", o.SkippedOmitted)
	}
	if o.Skipped[0].File != "docs/My Notes 0.md" {
		t.Errorf("first skip is %q, want the first given", o.Skipped[0].File)
	}
	if len(o.GoPkgsFailed) != maxSkipped || o.GoPkgsFailedOm != 7 {
		t.Errorf("go_packages_failed: %d itemized, %d omitted", len(o.GoPkgsFailed), o.GoPkgsFailedOm)
	}
	if len(o.GoPkgsNoCalls) != maxSkipped || o.GoPkgsNoCallOm != 7 {
		t.Errorf("go_packages_without_calls: %d itemized, %d omitted", len(o.GoPkgsNoCalls), o.GoPkgsNoCallOm)
	}
}

// compact drops only what the ID says, and it reads back (#157): a name
// the ID ends with, and the file part of a position the ID is keyed by.
func TestCompactIsLossless(t *testing.T) {
	for _, c := range []struct {
		in, want nodeOut
	}{
		{nodeOut{ID: "go://example.com/p#Type.Method", Name: "Method", At: "p/x.go:12"}, nodeOut{ID: "go://example.com/p#Type.Method", At: "p/x.go:12"}},
		{nodeOut{ID: "proto://pkg.v1.Msg.field", Name: "field", At: "proto/x.proto:4"}, nodeOut{ID: "proto://pkg.v1.Msg.field", At: "proto/x.proto:4"}},
		{nodeOut{ID: "ts://app/src/A.svelte", Name: "A.svelte", At: "app/src/A.svelte"}, nodeOut{ID: "ts://app/src/A.svelte"}},
		{nodeOut{ID: "md://docs/a.md#getting-started", Name: "Getting Started", At: "docs/a.md:3"}, nodeOut{ID: "md://docs/a.md#getting-started", Name: "Getting Started", At: ":3"}},
		{nodeOut{ID: "proto://file/proto/a.proto", Name: "a.proto", At: "proto/a.proto"}, nodeOut{ID: "proto://file/proto/a.proto"}},
		// a name the ID does not end with as a segment stays: Unit is not
		// the segment "MyUnit"
		{nodeOut{ID: "go://example.com/p#MyUnit", Name: "Unit"}, nodeOut{ID: "go://example.com/p#MyUnit", Name: "Unit"}},
	} {
		if got := compact(c.in); got.Name != c.want.Name || got.At != c.want.At {
			t.Errorf("compact(%+v) = %+v, want name %q at %q", c.in, got, c.want.Name, c.want.At)
		}
	}
}
