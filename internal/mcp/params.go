// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/trendvidia/alidade/internal/graph"
)

type contextT = context.Context

// Budget bounds, in estimated tokens.
const (
	defaultBudget = 4000
	minBudget     = 200
	maxBudget     = 100000
	// envelope is what the result's own framing costs, reserved before
	// any item is added.
	envelope = 48
)

// Traversal bounds.
const (
	defaultDepth = 1
	// questionDepth is subgraph_for_question's default: a chain across
	// layers (table -> struct -> query method -> caller) is three hops.
	questionDepth = 3
	maxDepth      = 4
	defaultLimit  = 20
	maxLimit      = 200
	maxPathDepth  = 12
)

// budgetParam reads max_tokens, clamped to the bounds.
func budgetParam(req mcpsdk.CallToolRequest) int {
	return clamp(req.GetInt("max_tokens", defaultBudget), minBudget, maxBudget)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// idParam reads a node ID parameter and checks it names a node.
func idParam(req mcpsdk.CallToolRequest, g *graph.Graph, key string) (graph.ID, error) {
	s, err := req.RequireString(key)
	if err != nil {
		return "", err
	}
	id := graph.ID(s)
	if _, ok := g.Node(id); !ok {
		return "", fmt.Errorf("%s: no node %q; find_nodes gives IDs", key, s)
	}
	return id, nil
}

// A direction says which edges a traversal follows.
type direction string

const (
	dirOut  direction = "out"
	dirIn   direction = "in"
	dirBoth direction = "both"
)

func directionParam(req mcpsdk.CallToolRequest, def direction) (direction, error) {
	switch d := direction(req.GetString("direction", string(def))); d {
	case dirOut, dirIn, dirBoth:
		return d, nil
	default:
		return "", fmt.Errorf("direction: %q is not out, in or both", d)
	}
}

// An edgeFilter says which edges a traversal may follow.
type edgeFilter struct {
	kinds           map[graph.EdgeKind]bool
	excludeInferred bool
	// excludeTests leaves out every node from a _test.go file and every
	// edge touching one, which is the graph a build without -tests gives
	// (#81): "does this have a production caller" and "is it called from
	// a test at all" are one graph and a filter. The node carries the
	// fact and the core derives the edge's, so this is a field read and
	// never a lookup back through the graph.
	excludeTests bool
	// excludeGenerated keeps generated code out of the answer and in the
	// walk (#126): generated code is the bridge between hand-written code
	// and its schema, so a traversal still crosses it, and the page
	// renders what it crosses as a stub (see page.stubGenerated).
	excludeGenerated bool
	// relationships leaves out what a question is not asking about:
	// contains edges followed downward (a container's children — the
	// fan-out) and calls that leave the module at package granularity
	// (boundary=package). A question answered by breadth-first expansion
	// under a budget would otherwise spend it on a table's columns and a
	// function's calls into fmt before it reached the caller three hops
	// away. Containment upward — a column to its table, a method to its
	// type — is one node and stays. An explicit edge_kinds naming
	// contains turns the downward walk back on. Markdown nodes are
	// reached but not expanded through; see leaf.
	relationships bool
}

// filterParam reads the edge filter. An edge kind the graph does not
// hold is an error naming the ones it does, and so is edge_kinds given
// as anything but an array of strings: either would otherwise answer
// "no relationship", which a caller cannot tell from a real one (#100).
func filterParam(req mcpsdk.CallToolRequest, g *graph.Graph) (edgeFilter, error) {
	f := edgeFilter{excludeInferred: req.GetBool("exclude_inferred", false), excludeTests: req.GetBool("exclude_tests", false),
		excludeGenerated: req.GetBool("exclude_generated", false)}
	kinds, err := stringsParam(req, "edge_kinds")
	if err != nil || len(kinds) == 0 {
		return f, err
	}
	have := edgeKinds(g)
	f.kinds = make(map[graph.EdgeKind]bool, len(kinds))
	for _, k := range kinds {
		if !have[k] {
			return f, fmt.Errorf("edge_kinds: %q is no edge kind of this graph; it has %s", k, strings.Join(slices.Sorted(maps.Keys(have)), ", "))
		}
		f.kinds[graph.EdgeKind(k)] = true
	}
	return f, nil
}

// stringsParam reads an optional array-of-strings parameter, refusing any
// other shape rather than reading it as absent.
func stringsParam(req mcpsdk.CallToolRequest, key string) ([]string, error) {
	v, ok := req.GetArguments()[key]
	if !ok || v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: want an array of strings, got %T", key, v)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("%s: want an array of strings, got an element of type %T", key, e)
		}
		out = append(out, s)
	}
	return out, nil
}

// edgeKinds is the set of edge kinds the graph holds. Kinds are open in
// the core — a layer names its own — so the graph, not a list, is what a
// filter is checked against.
func edgeKinds(g *graph.Graph) map[string]bool {
	out := map[string]bool{}
	for e := range g.Edges() {
		out[string(e.Kind)] = true
	}
	return out
}

// nodeFilterParams reads find_nodes' kind and scheme, each checked
// against what the graph holds.
func nodeFilterParams(req mcpsdk.CallToolRequest, g *graph.Graph) (graph.Kind, graph.Scheme, error) {
	kind, scheme := req.GetString("kind", ""), req.GetString("scheme", "")
	if kind == "" && scheme == "" {
		return "", "", nil
	}
	kinds, schemes := map[string]bool{}, map[string]bool{}
	for n := range g.Nodes() {
		kinds[string(n.Kind)] = true
		s, _ := n.ID.Scheme()
		schemes[string(s)] = true
	}
	if kind != "" && !kinds[kind] {
		return "", "", fmt.Errorf("kind: %q is no node kind of this graph; it has %s", kind, strings.Join(slices.Sorted(maps.Keys(kinds)), ", "))
	}
	if scheme != "" && !schemes[scheme] {
		return "", "", fmt.Errorf("scheme: %q is no scheme of this graph; it has %s", scheme, strings.Join(slices.Sorted(maps.Keys(schemes)), ", "))
	}
	return graph.Kind(kind), graph.Scheme(scheme), nil
}

// leaf reports whether a traversal in relationship mode stops at a node
// it reached: a Markdown section is shown but not expanded, since a
// document that mentions two declarations is not a relationship between
// them, and expanding through it would fan a question out to everything
// the document happens to name.
func (f edgeFilter) leaf(id graph.ID) bool {
	if !f.relationships {
		return false
	}
	s, _ := id.Scheme()
	return s == graph.SchemeMarkdown
}

func (f edgeFilter) allows(e graph.Edge) bool {
	if f.excludeInferred && e.Derivation == graph.Inferred {
		return false
	}
	if f.excludeTests && e.Test {
		return false
	}
	if f.relationships && f.kinds == nil && e.Attrs["boundary"] != "" {
		return false
	}
	return f.kinds == nil || f.kinds[e.Kind]
}

// downward reports whether a traversal from a node along one of its
// outgoing edges is the walk relationship mode leaves out: into a
// container's children.
func (f edgeFilter) downward(e graph.Edge) bool {
	return f.relationships && f.kinds == nil && e.Kind == graph.KindContains
}
