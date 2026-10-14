// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"slices"
	"testing"
)

// TestStatsReportWhatTheADRsRequire covers the three numbers a consumer has
// to be able to see: the generated share (ADR 004 rule 3), the split of
// edges by derivation (ADR 006 rule 4), and the dropped-edge count
// (ADR 002 rule 3). Each is a number that means an assumption has stopped
// holding when it moves.
func TestStatsReportWhatTheADRsRequire(t *testing.T) {
	g := buildFixture(t)
	s := g.Stats()

	if s.Nodes != 10 || s.Edges != 10 {
		t.Fatalf("Nodes, Edges = %d, %d; want 10, 10", s.Nodes, s.Edges)
	}
	wantScheme := map[Scheme]int{SchemeProto: 3, SchemeGo: 4, SchemeSQL: 2, SchemeMarkdown: 1}
	for scheme, want := range wantScheme {
		if s.NodesByScheme[scheme] != want {
			t.Errorf("NodesByScheme[%s] = %d, want %d", scheme, s.NodesByScheme[scheme], want)
		}
	}
	if s.Generated != 2 {
		t.Errorf("Generated = %d, want 2", s.Generated)
	}
	if got := s.GeneratedShare(); got != 0.2 {
		t.Errorf("GeneratedShare() = %v, want 0.2", got)
	}
	wantDerivation := map[Derivation]int{Extracted: 7, Resolved: 2, Inferred: 1}
	for d, want := range wantDerivation {
		if s.EdgesByDerivation[d] != want {
			t.Errorf("EdgesByDerivation[%s] = %d, want %d", d, s.EdgesByDerivation[d], want)
		}
	}
	if s.Dropped != 0 || s.Conflicts != 0 {
		t.Errorf("Dropped, Conflicts = %d, %d; want 0, 0", s.Dropped, s.Conflicts)
	}
}

// TestNodesAreOrderedByID pins the traversal order the whole output rests
// on. It is what makes two runs diffable, and what lets the MCP surface cut
// a result off at a token budget and get the same prefix every time.
func TestNodesAreOrderedByID(t *testing.T) {
	g := buildFixture(t)

	var ids []ID
	for n := range g.Nodes() {
		ids = append(ids, n.ID)
	}
	if len(ids) != g.NodeCount() {
		t.Fatalf("Nodes() yielded %d, want %d", len(ids), g.NodeCount())
	}
	if !slices.IsSorted(ids) {
		t.Errorf("Nodes() out of order: %v", ids)
	}
}

func TestEdgesAreInCanonicalOrder(t *testing.T) {
	g := buildFixture(t)

	var prev Edge
	first := true
	count := 0
	for e := range g.Edges() {
		count++
		if !first && compareEdgeKeys(prev.key(), e.key()) >= 0 {
			t.Errorf("Edges() out of order at %s -%s-> %s", e.From, e.Kind, e.To)
		}
		prev, first = e, false
	}
	if count != g.EdgeCount() {
		t.Fatalf("Edges() yielded %d, want %d", count, g.EdgeCount())
	}
}

// TestIterationStopsEarly covers the break path of both iterators, which a
// token-budgeted consumer will take on every large graph.
func TestIterationStopsEarly(t *testing.T) {
	g := buildFixture(t)

	seen := 0
	for range g.Nodes() {
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("Nodes() yielded %d after a break, want 1", seen)
	}

	seen = 0
	for range g.Edges() {
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("Edges() yielded %d after a break, want 1", seen)
	}
}

// TestTraversalCrossesLayers is the point of the whole package: one
// traversal answers "which Go code reaches this table", without the caller
// joining anything (ADR 001).
func TestTraversalCrossesLayers(t *testing.T) {
	m := NewMinter(testRoot)
	caller := mustID(m.GoID("github.com/x/app/store", "Store.Load"))
	genFunc := mustID(m.GoID("github.com/x/app/gen", "GetUser"))
	query := mustID(m.SQLQueryID("example.com/m/db", "GetUser"))
	table := mustID(m.SQLID(SQLTable, "users"))
	g := buildFixture(t)

	// Go caller -> generated Go -> the sqlc query that generated it ->
	// the table it reads. Four nodes, three layers, no join in the
	// caller.
	if !hasEdge(g.Out(caller), genFunc, KindCalls) {
		t.Fatalf("Out(%s) = %v, want a call to the generated function", caller, g.Out(caller))
	}
	if !hasEdgeFrom(g.In(genFunc), query, KindGenerates) {
		t.Fatalf("In(%s) = %v, want the query that generated it", genFunc, g.In(genFunc))
	}
	if !hasEdge(g.Out(query), table, KindQueries) {
		t.Fatalf("Out(%s) = %v, want the table it reads", query, g.Out(query))
	}

	// The .proto half of the same graph, and the ADR 002 rule 6 shape:
	// the message and the generated type are two nodes joined by an
	// edge, not one node.
	message := mustID(m.ProtoID("app.v1.User"))
	genType := mustID(m.GoID("github.com/x/app/gen", "User"))
	if message == genType {
		t.Fatal("schema and generated artifact share an ID")
	}
	if !hasEdge(g.Out(message), genType, KindGenerates) {
		t.Errorf("Out(%s) = %v, want a generates edge", message, g.Out(message))
	}
	if n, _ := g.Node(genType); !n.Generated {
		t.Error("generated Go type is not marked generated")
	}
	if n, _ := g.Node(message); n.Generated {
		t.Error("the .proto message is marked generated")
	}
}

// TestInferredEdgesAreIdentifiable is ADR 006 rule 4 from the consumer's
// side: filtering to the high-confidence graph is one predicate on data the
// edge already carries, so the core does not have to decide for anyone.
func TestInferredEdgesAreIdentifiable(t *testing.T) {
	g := buildFixture(t)

	var confident, heuristic int
	for e := range g.Edges() {
		if e.Derivation == Inferred {
			heuristic++
			continue
		}
		confident++
	}
	if heuristic != 1 {
		t.Errorf("found %d INFERRED edges, want 1", heuristic)
	}
	if confident != g.EdgeCount()-heuristic {
		t.Errorf("confident + heuristic = %d, want %d", confident+heuristic, g.EdgeCount())
	}
}

func TestNodeLookupMisses(t *testing.T) {
	g := buildFixture(t)
	if _, ok := g.Node("sql://table/nothing"); ok {
		t.Error("Node() found a node that was never added")
	}
	if got := g.Out("sql://table/nothing"); got != nil {
		t.Errorf("Out() = %v for an unknown node, want nil", got)
	}
	if got := g.In("sql://table/nothing"); got != nil {
		t.Errorf("In() = %v for an unknown node, want nil", got)
	}
}

// hasEdge looks through outgoing edges; hasEdgeFrom through incoming ones.
func hasEdge(edges []Edge, to ID, kind EdgeKind) bool {
	return slices.ContainsFunc(edges, func(e Edge) bool { return e.To == to && e.Kind == kind })
}

func hasEdgeFrom(edges []Edge, from ID, kind EdgeKind) bool {
	return slices.ContainsFunc(edges, func(e Edge) bool { return e.From == from && e.Kind == kind })
}
