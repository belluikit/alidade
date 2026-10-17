// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// A DropReason says why an edge did not make it into the graph.
type DropReason string

// The reasons an edge is dropped. Both mean the same thing — an endpoint
// names a node nothing declared — and they are kept apart because which end
// failed says which adapter to look at.
const (
	DropUnknownSource DropReason = "unknown_source"
	DropUnknownTarget DropReason = "unknown_target"
)

// A DroppedEdge is an edge that could not be resolved, kept with its reason.
// Dropped edges are part of the graph's output rather than a log line: silent
// loss is the failure mode ADR 002 rule 3 exists to prevent, and a
// dropped-edge count that stops being small is the signal that the identity
// scheme has stopped matching reality.
type DroppedEdge struct {
	Edge   Edge
	Reason DropReason
}

// A Conflict records two adapters disagreeing about the same thing. The
// graph resolves every conflict deterministically — it never depends on
// which adapter ran first — and reports it rather than picking quietly,
// because two adapters minting one ID for two different things is exactly
// the silent identity failure this package is built to surface.
type Conflict struct {
	// Subject is the node ID, or "from -kind-> to" for an edge.
	Subject string
	// Field is the field they disagreed about.
	Field string
	// Kept and Discarded are the two values, rendered for display.
	Kept      string
	Discarded string
}

// A Builder collects what the layer adapters find and merges it into one
// [Graph].
//
// Adapters run independently and concurrently (ADR 001), so a Builder is
// safe for use from multiple goroutines, and its output does not depend on
// the order in which they report: contributions to one node are folded in a
// canonical order, not in arrival order. Two runs over an unchanged tree
// therefore produce byte-identical output however the adapters interleave.
//
// An adapter may add an edge before either endpoint exists. Endpoints are
// resolved in [Builder.Build], once everyone has reported.
type Builder struct {
	minter *Minter
	// layer tags every contribution made through this Builder, so a
	// layer's can be dropped and re-added between builds (see [Builder.Layer]).
	layer string
	*store
}

// A store is what every Builder over one graph shares: the primary
// root's Builder, each secondary root's (ADR 002 rule 12) and each
// layer's write to the same nodes and edges and differ only in the
// Minter and the layer tag.
type store struct {
	mu     sync.Mutex
	nodes  map[ID][]nodeContrib
	edges  []edgeContrib
	labels map[string]bool
}

type nodeContrib struct {
	layer string
	node  Node
}

type edgeContrib struct {
	layer string
	edge  Edge
}

// NewBuilder returns a Builder for a repository rooted at root. See
// [NewMinter] for what root does and when it may be empty.
func NewBuilder(root string) *Builder {
	return &Builder{
		minter: NewMinter(root),
		store:  &store{nodes: make(map[ID][]nodeContrib), labels: make(map[string]bool)},
	}
}

// Layer returns a Builder over the same graph whose contributions carry
// name, so [Builder.Drop] can remove them as one: a rebuild reruns a layer
// whose inputs changed and keeps every other layer's contributions
// (#56). A Builder with no layer tags nothing, and Drop("") drops that.
func (b *Builder) Layer(name string) *Builder {
	return &Builder{minter: b.minter, layer: name, store: b.store}
}

// WithMinter returns a Builder over the same graph, with the same layer,
// minting through m — a secondary root's Minter on a rebuild, whose
// label the first build already registered.
func (b *Builder) WithMinter(m *Minter) *Builder {
	return &Builder{minter: m, layer: b.layer, store: b.store}
}

// Drop removes every contribution tagged name, and returns how many nodes
// and edges went.
func (b *Builder) Drop(name string) (nodes, edges int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, cs := range b.nodes {
		kept := cs[:0:0]
		for _, c := range cs {
			if c.layer == name {
				nodes++
			} else {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			delete(b.nodes, id)
		} else {
			b.nodes[id] = kept
		}
	}
	kept := b.edges[:0:0]
	for _, c := range b.edges {
		if c.layer == name {
			edges++
		} else {
			kept = append(kept, c)
		}
	}
	b.edges = kept
	return nodes, edges
}

// Contributions returns the nodes and edges added through Builders
// tagged layer, as they were recorded: each position repo-relative, each
// edge unresolved. A worktree's graph takes a layer whose files equal the
// base's from the base this way rather than extracting it again (#25).
func (b *Builder) Contributions(layer string) ([]Node, []Edge) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var nodes []Node
	for _, cs := range b.nodes {
		for _, c := range cs {
			if c.layer == layer {
				nodes = append(nodes, c.node)
			}
		}
	}
	var edges []Edge
	for _, c := range b.edges {
		if c.layer == layer {
			edges = append(edges, c.edge)
		}
	}
	return nodes, edges
}

// Adopt adds nodes and edges as [Builder.Contributions] returned them
// from another Builder of a tree with the same layout: positions are
// already repo-relative and are not relativized again. A node or edge is
// shared, not copied, since Build never mutates what it was given.
func (b *Builder) Adopt(nodes []Node, edges []Edge) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, n := range nodes {
		b.nodes[n.ID] = append(b.nodes[n.ID], nodeContrib{b.layer, n})
	}
	for _, e := range edges {
		b.edges = append(b.edges, edgeContrib{b.layer, e})
	}
}

// Minter returns the Minter adapters should mint their IDs through. It is
// the Builder's own, so every ID in a graph is relativized against one root.
func (b *Builder) Minter() *Minter { return b.minter }

// Secondary returns a Builder over the same graph for a second repository
// at root, whose file-anchored IDs and positions carry "@label/" (ADR 002
// rule 12). A label used twice in one graph is an error: two roots under
// one label would share IDs, which is the collision the label prevents.
func (b *Builder) Secondary(label, root string) (*Builder, error) {
	m, err := b.minter.Secondary(label, root)
	if err != nil {
		return nil, fmt.Errorf("graph: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.labels[label] {
		return nil, fmt.Errorf("graph: secondary root label %q used twice", label)
	}
	b.labels[label] = true
	return &Builder{minter: m, layer: b.layer, store: b.store}, nil
}

// AddNode records a node. Adding the same ID twice is not an error and not
// an overwrite: the contributions are merged in [Builder.Build], which is
// how two extractors that both know about one entity converge on one node.
//
// The node's position is normalized to a repo-relative path, so an adapter
// may pass whatever absolute path its upstream library reports.
func (b *Builder) AddNode(n Node) error {
	if !n.ID.Valid() {
		return fmt.Errorf("graph: node %q has no known scheme; mint IDs with a Minter", n.ID)
	}
	if n.Kind == "" {
		return fmt.Errorf("graph: node %s has no kind", n.ID)
	}
	if n.Pos.File != "" {
		rel, err := b.minter.PosPath(n.Pos.File)
		if err != nil {
			return fmt.Errorf("graph: node %s position: %w", n.ID, err)
		}
		n.Pos.File = string(rel)
	}
	n.Attrs = maps.Clone(n.Attrs)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.nodes[n.ID] = append(b.nodes[n.ID], nodeContrib{b.layer, n})
	return nil
}

// AddEdge records an edge. Its endpoints need not exist yet; they are
// resolved in [Builder.Build] and the edge is dropped there if they never
// do.
//
// Everything checkable without the rest of the graph is checked here, so an
// adapter learns about its own bug at the call site rather than as a missing
// edge later. A derivation is one of those things (ADR 006).
func (b *Builder) AddEdge(e Edge) error {
	if !e.From.Valid() {
		return fmt.Errorf("graph: edge source %q has no known scheme; mint IDs with a Minter", e.From)
	}
	if !e.To.Valid() {
		return fmt.Errorf("graph: edge target %q has no known scheme; mint IDs with a Minter", e.To)
	}
	if e.Kind == "" {
		return fmt.Errorf("graph: edge %s -> %s has no kind", e.From, e.To)
	}
	if !e.Derivation.valid() {
		return fmt.Errorf("graph: edge %s -%s-> %s has no derivation; every edge states how it was derived (ADR 006)", e.From, e.Kind, e.To)
	}
	e.Attrs = maps.Clone(e.Attrs)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.edges = append(b.edges, edgeContrib{b.layer, e})
	return nil
}

// Build merges everything reported so far into a graph: it folds each node's
// contributions, collapses duplicate edges, resolves endpoints, and drops
// what does not resolve.
//
// Build does not consume the Builder — adapters may keep reporting and
// Build again — and it never mutates what it was given.
func (b *Builder) Build() *Graph {
	b.mu.Lock()
	ids := slices.Sorted(maps.Keys(b.nodes))
	contribs := make(map[ID][]Node, len(b.nodes))
	for id, cs := range b.nodes {
		ns := make([]Node, 0, len(cs))
		for _, c := range cs {
			ns = append(ns, c.node)
		}
		contribs[id] = ns
	}
	edges := make([]Edge, 0, len(b.edges))
	for _, c := range b.edges {
		edges = append(edges, c.edge)
	}
	b.mu.Unlock()

	g := &Graph{
		nodes: make(map[ID]Node, len(ids)),
		order: ids,
		out:   make(map[ID][]Edge),
		in:    make(map[ID][]Edge),
	}
	for _, id := range ids {
		n, conflicts := foldNode(id, contribs[id])
		g.nodes[id] = n
		g.conflicts = append(g.conflicts, conflicts...)
	}

	merged, edgeConflicts := foldEdges(edges)
	g.conflicts = append(g.conflicts, edgeConflicts...)
	for _, e := range merged {
		// An edge's test-ness is derived here rather than reported by
		// the adapter that found the edge: it is a function of the
		// endpoints, and this is the one place every node contribution
		// has been merged, so a layer emitting an edge into another
		// layer's test declaration cannot get it wrong or leave it out.
		e.Test = g.nodes[e.From].Test || g.nodes[e.To].Test
		switch {
		case !g.has(e.From):
			g.dropped = append(g.dropped, DroppedEdge{Edge: e, Reason: DropUnknownSource})
		case !g.has(e.To):
			g.dropped = append(g.dropped, DroppedEdge{Edge: e, Reason: DropUnknownTarget})
		default:
			g.edges = append(g.edges, e)
			g.out[e.From] = append(g.out[e.From], e)
			g.in[e.To] = append(g.in[e.To], e)
		}
	}

	g.externalTests()
	g.stats = g.computeStats()
	return g
}

// onlyTestCode reports whether a node has an edge from its own layer and
// every such edge is a test edge.
func (g *Graph) onlyTestCode(id ID) bool {
	layer, _ := id.Scheme()
	code := 0
	for _, e := range g.in[id] {
		if s, _ := e.From.Scheme(); s != layer {
			continue
		}
		if !e.Test {
			return false
		}
		code++
	}
	return code > 0
}

// externalTests marks an external node — a package the graph names and
// did not read, attribute external=true — a test node when every edge
// into it from its own layer is a test edge: only test code imports it or
// calls into it (#128). No adapter can observe that, since none read its
// source; it is a fact about the edges into it, which only the merged
// graph has. An edge from another layer — a document mentioning the
// package — is not code using it and does not count toward the rule, but
// it does touch a test node once the node is marked, so it is derived
// again, as every edge's test-ness is from its endpoints: left as it was,
// it was a production edge into a test node (#133).
func (g *Graph) externalTests() {
	marked := map[ID]bool{}
	for id, n := range g.nodes {
		if n.Attrs["external"] == "true" && !n.Test && g.onlyTestCode(id) {
			n.Test = true
			g.nodes[id] = n
			marked[id] = true
		}
	}
	if len(marked) == 0 {
		return
	}
	mark := func(es []Edge) {
		for i := range es {
			if marked[es[i].To] {
				es[i].Test = true
			}
		}
	}
	mark(g.edges)
	for _, es := range g.out {
		mark(es)
	}
	for id := range marked {
		mark(g.in[id])
	}
	// A dropped edge carries the same flag and is written out with it, and
	// the exact-tests build reads it to decide which dropped edges are a
	// test's (#133), so it is derived again too.
	for i := range g.dropped {
		if marked[g.dropped[i].Edge.To] {
			g.dropped[i].Edge.Test = true
		}
	}
}

// foldNode reduces one node's contributions to a single node, in an order
// that does not depend on which adapter reported first: contributions are
// sorted canonically, then folded field by field with the first non-zero
// value winning. Every discarded non-zero value is reported as a conflict.
func foldNode(id ID, contribs []Node) (Node, []Conflict) {
	if len(contribs) == 1 {
		n := contribs[0]
		n.ID = id
		return n, nil
	}
	slices.SortStableFunc(contribs, compareNodes)

	var conflicts []Conflict
	out := contribs[0]
	out.ID = id
	out.Attrs = maps.Clone(out.Attrs)
	subject := string(id)
	for _, c := range contribs[1:] {
		conflicts = mergeNode(&out, c, subject, conflicts)
	}
	return out, conflicts
}

// mergeNode folds one further contribution into out. Each field takes the
// first non-zero value in canonical order, and every value that loses is
// reported: an adapter that knew something different about this node is
// either wrong or looking at a different thing, and both are worth seeing.
func mergeNode(out *Node, c Node, subject string, conflicts []Conflict) []Conflict {
	if c.Kind != out.Kind {
		conflicts = append(conflicts, Conflict{subject, "kind", string(out.Kind), string(c.Kind)})
	}
	switch {
	case out.Name == "":
		out.Name = c.Name
	case c.Name != "" && c.Name != out.Name:
		conflicts = append(conflicts, Conflict{subject, "name", out.Name, c.Name})
	}
	switch {
	case out.Pos.zero():
		out.Pos = c.Pos
	case !c.Pos.zero() && c.Pos != out.Pos:
		conflicts = append(conflicts, Conflict{subject, "position", showPos(out.Pos), showPos(c.Pos)})
	}
	// Generated is a union, not a choice: a declaration one adapter saw
	// in generated source is generated, whatever another saw (ADR 004).
	if c.Generated != out.Generated {
		conflicts = append(conflicts, Conflict{subject, "generated", "true", "false"})
		out.Generated = true
	}
	// Test is a union for the same reason (ADR 004 rule 4): a declaration
	// one adapter read from a test file is a test declaration.
	if c.Test != out.Test {
		conflicts = append(conflicts, Conflict{subject, "test", "true", "false"})
		out.Test = true
	}
	out.Attrs, conflicts = foldAttrs(out.Attrs, c.Attrs, subject, conflicts)
	return conflicts
}

// foldEdges collapses edges that assert the same relationship into one and
// returns them in canonical order. Edges are grouped in one pass and each
// group folded on its own, so the cost is linear in the number of edges and
// the output does not depend on the order they arrived in.
func foldEdges(edges []Edge) ([]Edge, []Conflict) {
	groups := make(map[edgeKey][]Edge)
	for _, e := range edges {
		k := e.key()
		groups[k] = append(groups[k], e)
	}
	keys := slices.SortedFunc(maps.Keys(groups), compareEdgeKeys)

	var conflicts []Conflict
	out := make([]Edge, 0, len(keys))
	for _, k := range keys {
		e, c := foldEdge(groups[k])
		out = append(out, e)
		conflicts = append(conflicts, c...)
	}
	return out, conflicts
}

// foldEdge reduces one relationship's contributions to a single edge. A
// disagreement about derivation is not a conflict — it is the expected case
// when a heuristic adapter and a type-resolving one both find the same
// relationship — and the stronger claim simply wins.
func foldEdge(contribs []Edge) (Edge, []Conflict) {
	if len(contribs) == 1 {
		return contribs[0], nil
	}
	slices.SortStableFunc(contribs, compareEdges)

	var conflicts []Conflict
	out := contribs[0]
	out.Attrs = maps.Clone(out.Attrs)
	subject := fmt.Sprintf("%s -%s-> %s", out.From, out.Kind, out.To)
	for _, c := range contribs[1:] {
		out.Derivation = stronger(out.Derivation, c.Derivation)
		out.Attrs, conflicts = foldAttrs(out.Attrs, c.Attrs, subject, conflicts)
	}
	return out, conflicts
}

// foldAttrs merges src into dst without letting map iteration order reach
// the output: keys are visited sorted, and a key already present with a
// different value is a conflict rather than an overwrite.
func foldAttrs(dst, src map[string]string, subject string, conflicts []Conflict) (map[string]string, []Conflict) {
	if len(src) == 0 {
		return dst, conflicts
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for _, k := range slices.Sorted(maps.Keys(src)) {
		v := src[k]
		switch have, ok := dst[k]; {
		case !ok:
			dst[k] = v
		case have != v:
			kept, discarded := minMax(have, v)
			dst[k] = kept
			conflicts = append(conflicts, Conflict{subject, "attr " + k, kept, discarded})
		}
	}
	return dst, conflicts
}

// compareNodes orders a node's contributions. Any total order would make
// the fold deterministic; this one puts the most informative contribution
// first, so a node that knows its position beats one that does not. Every
// field is compared, so contributions that differ in anything sort the
// same way whichever arrived first.
func compareNodes(a, b Node) int {
	if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
		return c
	}
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if az, bz := a.Pos.zero(), b.Pos.zero(); az != bz {
		if bz {
			return -1
		}
		return 1
	}
	if c := strings.Compare(a.Pos.File, b.Pos.File); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Pos.Line, b.Pos.Line); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Pos.Col, b.Pos.Col); c != 0 {
		return c
	}
	if a.Generated != b.Generated {
		if a.Generated {
			return -1
		}
		return 1
	}
	if a.Test != b.Test {
		if a.Test {
			return -1
		}
		return 1
	}
	return strings.Compare(attrsKey(a.Attrs), attrsKey(b.Attrs))
}

// compareEdges orders one relationship's contributions, strongest evidence
// first, so the fold starts from the most trustworthy claim.
func compareEdges(a, b Edge) int {
	if c := cmp.Compare(a.Derivation, b.Derivation); c != 0 {
		return c
	}
	return strings.Compare(attrsKey(a.Attrs), attrsKey(b.Attrs))
}

// compareEdgeKeys puts edges in the canonical order the graph reports them
// in: by source, then relationship, then target.
func compareEdgeKeys(a, b edgeKey) int {
	if c := strings.Compare(string(a.from), string(b.from)); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.kind), string(b.kind)); c != 0 {
		return c
	}
	return strings.Compare(string(a.to), string(b.to))
}

// attrsKey renders attributes as one comparable string, for ordering only.
func attrsKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(0)
	}
	return b.String()
}

func showPos(p Position) string { return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col) }

// minMax orders two disagreeing values so the winner does not depend on
// which adapter reported first.
func minMax(a, b string) (kept, discarded string) {
	if a <= b {
		return a, b
	}
	return b, a
}
