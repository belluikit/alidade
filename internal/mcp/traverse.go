// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"slices"

	"github.com/belluikit/alidade/internal/graph"
)

// A step is one edge a traversal followed and the node at its far end.
type step struct {
	edge graph.Edge
	to   graph.Node
}

// edgesFrom lists the edges a traversal may follow out of a node in the
// given direction, in canonical order, out before in.
func edgesFrom(g *graph.Graph, id graph.ID, dir direction, f edgeFilter) []step {
	var steps []step
	if dir != dirIn {
		for _, e := range g.Out(id) {
			if n, ok := g.Node(e.To); ok && f.allows(e) && !f.downward(e) {
				steps = append(steps, step{e, n})
			}
		}
	}
	if dir != dirOut {
		for _, e := range g.In(id) {
			if n, ok := g.Node(e.From); ok && f.allows(e) {
				steps = append(steps, step{e, n})
			}
		}
	}
	return steps
}

// neighbors walks breadth-first from start to the given depth, adding
// every step to the page until it is full. Breadth-first is what makes
// the budget meaningful: the nearest relationships land before the
// farther ones, so a cut result is the close neighbourhood, not a random
// sample of the far one. Once the page is full the walk goes on to the
// same depth only to count what it leaves out, so omitted is exact (#100).
func neighbors(g *graph.Graph, p *page, start graph.Node, dir direction, depth int, f edgeFilter) {
	p.addNode(start)
	frontier := []graph.ID{start.ID}
	visited := map[graph.ID]bool{start.ID: true}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []graph.ID
		for _, id := range frontier {
			for _, s := range edgesFrom(g, id, dir, f) {
				p.addStep(s.edge, s.to)
				if !visited[s.to.ID] {
					visited[s.to.ID] = true
					if !f.leaf(s.to.ID) {
						next = append(next, s.to.ID)
					}
				}
			}
		}
		frontier = next
	}
}

// shortestPath finds the fewest edges from one node to another, following
// the direction and filter, within maxDepth hops. It returns the steps in
// order from the start, or nil if there is no such path.
func shortestPath(g *graph.Graph, from, to graph.ID, dir direction, maxDepth int, f edgeFilter) []step {
	if from == to {
		return []step{}
	}
	parents := map[graph.ID]parent{from: {}}
	frontier := []graph.ID{from}
	for d := 0; d < maxDepth && len(frontier) > 0; d++ {
		var next []graph.ID
		for _, id := range frontier {
			for _, s := range edgesFrom(g, id, dir, f) {
				if _, seen := parents[s.to.ID]; seen {
					continue
				}
				parents[s.to.ID] = parent{id, s.edge}
				if s.to.ID == to {
					return unwind(g, parents, from, to)
				}
				next = append(next, s.to.ID)
			}
		}
		frontier = next
	}
	return nil
}

// A parent is how a breadth-first search reached a node.
type parent struct {
	prev graph.ID
	via  graph.Edge
}

// unwind rebuilds the path from the parent pointers, start first.
func unwind(g *graph.Graph, parents map[graph.ID]parent, from, to graph.ID) []step {
	var path []step
	for id := to; id != from; id = parents[id].prev {
		n, _ := g.Node(id)
		path = append(path, step{parents[id].via, n})
	}
	slices.Reverse(path)
	return path
}
