// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import "github.com/trendvidia/alidade/internal/graph"

// A sink is where an extractor's nodes and edges go. The host
// configuration's pass writes to the builder and records what it wrote;
// each further configuration's pass writes only what the host's did not,
// marked with the configuration it came from, so a declaration shared by
// every platform is one plain node and a platform-specific one says
// which platform (#43).
type sink interface {
	AddNode(graph.Node) error
	AddEdge(graph.Edge) error
}

type edgeKey struct {
	from graph.ID
	kind graph.EdgeKind
	to   graph.ID
}

// A recorder passes everything to the builder and remembers it.
type recorder struct {
	b     *graph.Builder
	nodes map[graph.ID]bool
	edges map[edgeKey]bool
}

func newRecorder(b *graph.Builder) *recorder {
	return &recorder{b: b, nodes: map[graph.ID]bool{}, edges: map[edgeKey]bool{}}
}

func (r *recorder) AddNode(n graph.Node) error {
	r.nodes[n.ID] = true
	return r.b.AddNode(n)
}

func (r *recorder) AddEdge(e graph.Edge) error {
	r.edges[edgeKey{e.From, e.Kind, e.To}] = true
	return r.b.AddEdge(e)
}

// An overlay adds what the recorder has not seen, tagged with its
// configuration, and counts what it added. A node this overlay itself
// added it will add again: a pass learns some of a package node's
// attributes only after the call graph is built — the error of a package
// that did not compile, the import that cost it its calls — and those
// belong on the node whichever pass found the package. What the host's
// pass already has is still left alone, which is the rule (#43).
type overlay struct {
	r      *recorder
	config string
	nodes  int
	edges  int
	// added are the node IDs this pass contributed, which is what
	// decides whether a fact of this pass is the graph's: see
	// [overlay.onlyAdded].
	added map[graph.ID]bool
}

func newOverlay(r *recorder, config string) *overlay {
	return &overlay{r: r, config: config, added: map[graph.ID]bool{}}
}

func (o *overlay) AddNode(n graph.Node) error {
	if o.r.nodes[n.ID] && !o.added[n.ID] {
		return nil
	}
	n.Attrs = withAttr(n.Attrs, "build", o.config)
	if !o.added[n.ID] {
		o.nodes++
		o.added[n.ID] = true
	}
	return o.r.AddNode(n)
}

// onlyAdded keeps the package paths whose package node this pass added.
// A package the host's configuration loaded is in the graph whole, and
// calling it failed because another platform cannot build it would say
// the graph lacks what it has; a package the host never saw is the one
// these lists exist for, and nothing else names it (#95). A path no ID
// can be minted for was never emitted either, so it is not added.
func (o *overlay) onlyAdded(m *graph.Minter, paths []string) []string {
	var out []string
	for _, p := range paths {
		if id, err := m.GoID(p, ""); err == nil && o.added[id] {
			out = append(out, p)
		}
	}
	return out
}

func (o *overlay) AddEdge(e graph.Edge) error {
	if o.r.edges[edgeKey{e.From, e.Kind, e.To}] {
		return nil
	}
	e.Attrs = withAttr(e.Attrs, "build", o.config)
	o.edges++
	return o.r.AddEdge(e)
}

func withAttr(attrs map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(attrs)+1)
	for kk, vv := range attrs {
		out[kk] = vv
	}
	out[k] = v
	return out
}
