// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// wide is a graph large enough that every budgeted tool truncates: a hub
// function calling 300 others, each called by a second hub through a
// chain, and a few hundred dropped edges in a dozen causes.
func wide() *graph.Graph {
	b := graph.NewBuilder("")
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	fn := func(i int) graph.ID { return graph.ID(fmt.Sprintf("go://example.com/wide/pkg#Function%03d", i)) }
	add := func(id graph.ID, name string) {
		must(b.AddNode(graph.Node{ID: id, Kind: graph.KindFunc, Name: name, Pos: graph.Position{File: "pkg/wide.go", Line: 10}}))
	}
	add("go://example.com/wide/pkg#Hub", "Hub")
	add("go://example.com/wide/pkg#Caller", "Caller")
	for i := range 300 {
		add(fn(i), fmt.Sprintf("Function%03d", i))
		must(b.AddEdge(graph.Edge{From: "go://example.com/wide/pkg#Hub", To: fn(i), Kind: graph.KindCalls, Derivation: graph.Resolved}))
		must(b.AddEdge(graph.Edge{From: "go://example.com/wide/pkg#Caller", To: fn(i), Kind: graph.KindCalls, Derivation: graph.Resolved}))
		if i > 0 {
			must(b.AddEdge(graph.Edge{From: fn(i - 1), To: fn(i), Kind: graph.KindReferences, Derivation: graph.Resolved}))
		}
	}
	schemes := []string{"go", "proto", "sql", "pxf", "md", "ts"}
	kinds := []graph.EdgeKind{graph.KindCalls, graph.KindReferences}
	for i := range 240 {
		to := graph.ID(fmt.Sprintf("%s://missing/target%03d", schemes[i%len(schemes)], i))
		must(b.AddEdge(graph.Edge{From: fn(i), To: to, Kind: kinds[i%len(kinds)], Derivation: graph.Extracted}))
	}
	return b.Build()
}

func wideHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t}
	h.c = connect(t, New(func() (*graph.Graph, BuildInfo, error) { return wide(), BuildInfo{Root: "/repo"}, nil }))
	return h
}

// TestEveryBudgetedToolKeepsItsBudget pins #100: under every max_tokens,
// what a tool returns is within it — by the size of the bytes sent, not
// only by the count the result reports — extras (seeds, matched, the
// dropped-edge groups) included.
func TestEveryBudgetedToolKeepsItsBudget(t *testing.T) {
	h := wideHarness(t)
	tools := map[string]map[string]any{
		"find_nodes":            {"query": "Function", "limit": 200},
		"neighbors":             {"id": "go://example.com/wide/pkg#Hub", "depth": 2},
		"shortest_path":         {"from": "go://example.com/wide/pkg#Function000", "to": "go://example.com/wide/pkg#Function299", "max_depth": 12},
		"subgraph_for_question": {"question": "Function000 Function001 Function002 Function003 Function004 Function005 Function006 Function007", "seeds": 50},
		"graph_issues":          {},
	}
	for name, args := range tools {
		for _, budget := range []int{minBudget, 300, 500, 1000, 2000} {
			a := map[string]any{"max_tokens": budget}
			for k, v := range args {
				a[k] = v
			}
			text, isErr := h.call(name, a)
			if isErr {
				t.Fatalf("%s: %s", name, text)
			}
			if est := (len(text) + 3) / 4; est > budget {
				t.Errorf("%s at max_tokens %d: %d bytes, ~%d tokens", name, budget, len(text), est)
			}
		}
	}
}

// TestOmittedIsExact pins #100: a cut neighbourhood says exactly what it
// left out — the uncut one's nodes and edges, less the ones shown — each
// counted once however many edges reached it.
func TestOmittedIsExact(t *testing.T) {
	h := wideHarness(t)
	args := map[string]any{"id": "go://example.com/wide/pkg#Hub", "depth": 3, "direction": "both"}
	full := h.callJSON("neighbors", argsWithBudget(args, maxBudget))
	if full["truncated"] != nil {
		t.Fatal("the uncut neighbourhood was cut")
	}
	allNodes, allEdges := len(full["nodes"].([]any)), len(full["edges"].([]any))
	for _, budget := range []int{minBudget, 700, 3000} {
		r := h.callJSON("neighbors", argsWithBudget(args, budget))
		om, ok := r["omitted"].(map[string]any)
		if !ok {
			t.Fatalf("budget %d: not cut", budget)
		}
		nodes := len(r["nodes"].([]any)) + int(om["nodes"].(float64))
		edges := len(r["edges"].([]any)) + int(om["edges"].(float64))
		if nodes != allNodes || edges != allEdges {
			t.Errorf("budget %d: shown+omitted = %d nodes, %d edges; uncut has %d, %d", budget, nodes, edges, allNodes, allEdges)
		}
	}
}

func argsWithBudget(args map[string]any, budget int) map[string]any {
	out := map[string]any{"max_tokens": budget}
	for k, v := range args {
		out[k] = v
	}
	return out
}

// TestFiltersNameWhatTheGraphHolds pins #100: a filter value the graph
// has no instance of, or edge_kinds in the wrong shape, is an error that
// lists what there is — never an empty answer read as "no relationship".
func TestFiltersNameWhatTheGraphHolds(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"neighbors", map[string]any{"id": idLoad, "edge_kinds": []any{"call"}}, `"call" is no edge kind of this graph; it has calls,`},
		{"neighbors", map[string]any{"id": idLoad, "edge_kinds": "calls"}, "edge_kinds: want an array of strings"},
		{"shortest_path", map[string]any{"from": idLoad, "to": idMsg, "edge_kinds": []any{"Calls"}}, `"Calls" is no edge kind`},
		{"subgraph_for_question", map[string]any{"question": "LoadConfig", "edge_kinds": []any{1}}, "an element of type float64"},
		{"find_nodes", map[string]any{"query": "Load", "kind": "function"}, `"function" is no node kind of this graph; it has `},
		{"find_nodes", map[string]any{"query": "Load", "scheme": "golang"}, `"golang" is no scheme of this graph; it has go,`},
		{"graph_issues", map[string]any{"kind": "conflict"}, `"conflict" is not dropped, conflicts or both`},
	}
	for _, c := range cases {
		text, isErr := h.call(c.tool, c.args)
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s %v: error=%v %q, want an error containing %q", c.tool, c.args, isErr, text, c.want)
		}
	}
	// A kind the graph holds still filters.
	r := h.callJSON("neighbors", map[string]any{"id": idLoad, "edge_kinds": []any{"calls"}})
	for _, e := range r["edges"].([]any) {
		if k := e.(map[string]any)["k"]; k != "calls" {
			t.Errorf("edge_kinds [calls] let a %v edge through", k)
		}
	}
}

// TestFindNodesTrimsTheQuery pins #100: surrounding whitespace is not part
// of a name.
func TestFindNodesTrimsTheQuery(t *testing.T) {
	h := newHarness(t)
	want := h.callJSON("find_nodes", map[string]any{"query": "LoadConfig"})["matched"]
	for _, q := range []string{" LoadConfig", "LoadConfig ", "\tloadconfig\n"} {
		if got := h.callJSON("find_nodes", map[string]any{"query": q})["matched"]; got != want || want == float64(0) {
			t.Errorf("find_nodes %q matched %v, want %v", q, got, want)
		}
	}
}

// TestIssuesGroupsAreAList pins #100: kind=conflicts gives an empty list of
// groups, not null.
func TestIssuesGroupsAreAList(t *testing.T) {
	h := newHarness(t)
	text, _ := h.call("graph_issues", map[string]any{"kind": "conflicts"})
	if !strings.Contains(text, `"dropped_by_missing":[]`) {
		t.Errorf("kind=conflicts: %s", text)
	}
}
