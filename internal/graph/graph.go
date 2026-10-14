// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import "iter"

// A Graph is the merged output of every layer adapter: one node set, one
// edge set, and the accounting of what did not make it in.
//
// It is immutable once [Builder.Build] returns, and it is built on demand
// and held for the session rather than persisted (ADR 005), so it cannot
// disagree with the working tree it was built from. Nodes and edges handed
// out by a Graph share their attribute maps with it and must not be
// modified.
type Graph struct {
	nodes     map[ID]Node
	order     []ID
	edges     []Edge
	out       map[ID][]Edge
	in        map[ID][]Edge
	dropped   []DroppedEdge
	conflicts []Conflict
	stats     Stats
}

// has reports whether the ID names a node in the graph.
func (g *Graph) has(id ID) bool {
	_, ok := g.nodes[id]
	return ok
}

// Node returns the node with the given ID.
func (g *Graph) Node(id ID) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// NodeCount returns the number of nodes.
func (g *Graph) NodeCount() int { return len(g.nodes) }

// EdgeCount returns the number of resolved edges, which excludes those
// dropped for an unresolvable endpoint. See [Graph.Dropped].
func (g *Graph) EdgeCount() int { return len(g.edges) }

// Nodes iterates every node, ordered by ID. The order is stable across
// builds of an unchanged tree, so output taken from it is diffable.
func (g *Graph) Nodes() iter.Seq[Node] {
	return func(yield func(Node) bool) {
		for _, id := range g.order {
			if !yield(g.nodes[id]) {
				return
			}
		}
	}
}

// Edges iterates every resolved edge in canonical order: by source, then
// relationship, then target.
func (g *Graph) Edges() iter.Seq[Edge] {
	return func(yield func(Edge) bool) {
		for _, e := range g.edges {
			if !yield(e) {
				return
			}
		}
	}
}

// Out returns the edges leaving a node, in canonical order. Every edge
// carries its derivation, so a consumer that wants the high-confidence
// graph alone filters on it (ADR 006 rule 4); the core does not decide that
// for anyone.
func (g *Graph) Out(id ID) []Edge { return g.out[id] }

// In returns the edges arriving at a node, in canonical order.
func (g *Graph) In(id ID) []Edge { return g.in[id] }

// Dropped returns every edge that could not be resolved, with its reason,
// in canonical order. This is output rather than diagnostics: an edge
// dropped is a relationship the graph does not know about, and a consumer
// reading absence as "there is no such relationship" should be able to see
// whether that absence was measured or lost.
func (g *Graph) Dropped() []DroppedEdge { return g.dropped }

// Conflicts returns every disagreement between adapters that the merge
// resolved. A non-empty result is worth reading: two adapters minting one
// ID for two different things is the silent identity failure ADR 002 is
// written against.
func (g *Graph) Conflicts() []Conflict { return g.conflicts }

// Stats returns the graph's own accounting of itself.
func (g *Graph) Stats() Stats { return g.stats }

// Stats is what a graph reports about its own shape. Three of its fields
// exist because an ADR requires them: the generated share (ADR 004 rule 3),
// the count of edges by derivation (ADR 006 rule 4), and the dropped-edge
// count (ADR 002 rule 3). Each is a number that, when it moves, means an
// assumption underneath the graph has stopped holding.
type Stats struct {
	// Nodes and Edges are the resolved totals.
	Nodes int
	Edges int
	// NodesByScheme counts nodes per layer.
	NodesByScheme map[Scheme]int
	// Generated counts nodes that came from generated source. A caller
	// that does not know 45% of a repo's functions are generated will
	// misread any centrality or hotspot measure taken over it.
	Generated int
	// EdgesByDerivation counts edges by how they were derived. A rising
	// INFERRED share means a heuristic adapter is dominating, and the
	// answer is tightening the adapter rather than relabelling.
	EdgesByDerivation map[Derivation]int
	// Dropped counts unresolvable edges, and DroppedByReason splits them
	// by which endpoint failed. If this stops being small, the identity
	// scheme has stopped matching reality — which is a question for
	// ADR 002, not for an adapter.
	Dropped         int
	DroppedByReason map[DropReason]int
	// Conflicts counts adapter disagreements resolved by the merge.
	Conflicts int
}

// GeneratedShare returns the fraction of nodes that came from generated
// source, or 0 for an empty graph.
func (s Stats) GeneratedShare() float64 {
	if s.Nodes == 0 {
		return 0
	}
	return float64(s.Generated) / float64(s.Nodes)
}

// computeStats tallies the graph. It walks the canonical orders rather than
// the maps, so the counts are built the same way every time.
func (g *Graph) computeStats() Stats {
	s := Stats{
		Nodes:             len(g.order),
		Edges:             len(g.edges),
		NodesByScheme:     make(map[Scheme]int),
		EdgesByDerivation: make(map[Derivation]int),
		Dropped:           len(g.dropped),
		DroppedByReason:   make(map[DropReason]int),
		Conflicts:         len(g.conflicts),
	}
	for _, id := range g.order {
		n := g.nodes[id]
		s.NodesByScheme[n.scheme()]++
		if n.Generated {
			s.Generated++
		}
	}
	for _, e := range g.edges {
		s.EdgesByDerivation[e.Derivation]++
	}
	for _, d := range g.dropped {
		s.DroppedByReason[d.Reason]++
	}
	return s
}
