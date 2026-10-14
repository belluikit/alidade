// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/trendvidia/alidade/internal/graph"
)

// nodeOut is a node as the surface renders it: short keys, position as
// one file:line string, attributes only when present.
//
// Every fact the core holds about a node is rendered here, and
// TestRenderedFactsAreComplete holds this struct to that: a fact the
// graph carries and the surface drops is one a caller has to infer from
// a filename, which is what the fact exists to replace (#90).
type nodeOut struct {
	ID    string            `json:"id"`
	Kind  string            `json:"k"`
	Name  string            `json:"n,omitempty"`
	At    string            `json:"at,omitempty"`
	Gen   bool              `json:"gen,omitempty"`
	Test  bool              `json:"test,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// edgeOut is an edge as the surface renders it. d is the derivation, on
// every edge, always (ADR 006 rule 3). In a traversal's answer f and t
// are indexes into its nodes, which hold both ends of every edge (#157);
// where an answer has no node list — graph_issues' dropped edges — they
// are IDs.
type edgeOut struct {
	From  any               `json:"f"`
	To    any               `json:"t"`
	Kind  string            `json:"k"`
	D     string            `json:"d"`
	Test  bool              `json:"test,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

func renderNode(n graph.Node) nodeOut {
	o := nodeOut{ID: string(n.ID), Kind: string(n.Kind), Name: n.Name, Gen: n.Generated, Test: n.Test, Attrs: n.Attrs}
	if n.Pos.File != "" {
		o.At = n.Pos.File
		if n.Pos.Line > 0 {
			o.At = fmt.Sprintf("%s:%d", n.Pos.File, n.Pos.Line)
		}
	}
	return o
}

func renderEdge(e graph.Edge) edgeOut {
	return edgeOut{From: string(e.From), To: string(e.To), Kind: string(e.Kind), D: e.Derivation.String(), Test: e.Test, Attrs: e.Attrs}
}

// compact drops from a node of a traversal's answer what its ID already
// says (#157): its name, when the ID ends with it, and in its position
// the file, when the ID names that file, leaving ":line". Nothing is
// lost — both read back from the ID — and an answer of many nodes is a
// third shorter or more. get_node, one node, is rendered whole.
func compact(o nodeOut) nodeOut {
	if o.Name != "" && endsWithName(o.ID, o.Name) {
		o.Name = ""
	}
	if file := idFile(o.ID); file != "" && o.At != "" {
		if rest, ok := strings.CutPrefix(o.At, file); ok && (rest == "" || rest[0] == ':') {
			o.At = rest
		}
	}
	return o
}

// endsWithName reports whether an ID ends with a node's name as its last
// segment, after a "#", "/" or ".".
func endsWithName(id, name string) bool {
	if !strings.HasSuffix(id, name) || len(id) == len(name) {
		return false
	}
	return strings.ContainsRune("#/.", rune(id[len(id)-len(name)-1]))
}

// idFile is the repo-relative file an ID is keyed by, for the layers that
// key by path — md, ts, pxf, goasm, and a .proto file — or "".
func idFile(id string) string {
	scheme, body, ok := strings.Cut(id, "://")
	if !ok {
		return ""
	}
	switch scheme {
	case "md", "ts", "py", "pxf", "goasm":
		file, _, _ := strings.Cut(body, "#")
		return graph.FilePath(file)
	case "proto":
		if file, ok := strings.CutPrefix(body, "file/"); ok {
			return graph.FilePath(file)
		}
	}
	return ""
}

// estimate is the token estimate of a rendered item: four bytes a token.
func estimate(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return (len(b) + 3) / 4
}

// A page is a budgeted result under construction. Items are added in the
// order a traversal finds them and refused once the budget is spent; what
// was refused is collected, each node and edge once however many times a
// traversal reached it, so the result says exactly what it left out
// (#100).
type page struct {
	budget, used int
	nodes        []nodeOut
	edges        []edgeOut
	seen         map[graph.ID]bool
	// index is each node's position in nodes, which edges refer by.
	index     map[graph.ID]int
	seenEdges map[edgeKey]bool
	truncated bool
	omitNodes map[graph.ID]bool
	omitEdges map[edgeKey]bool
	// stubGenerated renders a generated node a traversal reaches as its
	// ID and kind only (exclude_generated, #126): the path through it —
	// hand-written code to the generated type to the schema it came from
	// — stays, every edge on it real and with its derivation, and the
	// node's name, position and attributes are left out. A node asked
	// for by name, a start or a seed, is rendered whole.
	stubGenerated bool
}

// An edgeKey identifies an edge on a page. A traversal in both directions
// meets each edge twice, once from each end, and it must appear once.
type edgeKey struct {
	from graph.ID
	kind graph.EdgeKind
	to   graph.ID
}

func keyOf(e graph.Edge) edgeKey { return edgeKey{e.From, e.Kind, e.To} }

func newPage(budget int) *page {
	return &page{budget: budget, used: envelope, seen: make(map[graph.ID]bool), index: make(map[graph.ID]int), seenEdges: make(map[edgeKey]bool),
		omitNodes: make(map[graph.ID]bool), omitEdges: make(map[edgeKey]bool)}
}

// reserve charges what a result carries beside its nodes and edges — a
// count, a list of seed IDs — so the page's tokens are what the caller
// receives (#100). It reports whether it fit.
func (p *page) reserve(v any) bool {
	return p.fit(estimate(v) + 1)
}

// addNode adds a node unless it is already on the page or does not fit.
// It reports whether the node is on the page afterwards.
func (p *page) addNode(n graph.Node) bool {
	if p.seen[n.ID] {
		return true
	}
	o := compact(renderNode(n))
	if !p.fit(estimate(o)) {
		p.omitNodes[n.ID] = true
		return false
	}
	p.seen[n.ID] = true
	p.index[n.ID] = len(p.nodes)
	p.nodes = append(p.nodes, o)
	return true
}

// addStep adds an edge together with the node it led to, as one unit: an
// edge to a node that is not on the page would be a dangling reference.
func (p *page) addStep(e graph.Edge, to graph.Node) bool {
	if p.seenEdges[keyOf(e)] {
		return p.seen[to.ID]
	}
	if p.truncated {
		p.omit(e, to)
		return false
	}
	newNode := !p.seen[to.ID]
	eo := renderEdge(e)
	eo.From, eo.To = p.ref(e.From, to.ID), p.ref(e.To, to.ID)
	cost := estimate(eo)
	var no nodeOut
	if newNode {
		no = compact(renderNode(to))
		if p.stubGenerated && to.Generated {
			no = nodeOut{ID: no.ID, Kind: no.Kind, Gen: true}
		}
		cost += estimate(no)
	}
	if !p.fit(cost) {
		p.omit(e, to)
		return false
	}
	if newNode {
		p.seen[to.ID] = true
		p.index[to.ID] = len(p.nodes)
		p.nodes = append(p.nodes, no)
	}
	p.seenEdges[keyOf(e)] = true
	p.edges = append(p.edges, eo)
	return true
}

// ref is how an edge on the page names one of its ends: the end's index
// in the nodes, or, for the node the edge brings with it — its target
// going out, its source coming in — the index that node is about to
// take. A node not on the page — none, as an edge is added with its far
// node and from a node already there — would be named by its ID.
func (p *page) ref(id, bringing graph.ID) any {
	if i, ok := p.index[id]; ok {
		return i
	}
	if id == bringing {
		return len(p.nodes)
	}
	return string(id)
}

// omit records a step left off the page: the edge, and its far node
// unless that node is on the page by another edge.
func (p *page) omit(e graph.Edge, to graph.Node) {
	p.omitEdges[keyOf(e)] = true
	if !p.seen[to.ID] {
		p.omitNodes[to.ID] = true
	}
}

// fit reserves cost tokens if they are available. Once the page is
// truncated nothing more fits, so a traversal's later, smaller items do
// not slip in ahead of the larger one that was refused.
func (p *page) fit(cost int) bool {
	if p.truncated || p.used+cost > p.budget {
		p.truncated = true
		return false
	}
	p.used += cost
	return true
}

// result is the shape every traversal tool returns.
type result struct {
	Nodes     []nodeOut      `json:"nodes"`
	Edges     []edgeOut      `json:"edges"`
	Truncated bool           `json:"truncated,omitempty"`
	Omitted   *omitted       `json:"omitted,omitempty"`
	Tokens    int            `json:"tokens"`
	Extra     map[string]any `json:"-"`
}

type omitted struct {
	Nodes int `json:"nodes"`
	Edges int `json:"edges"`
}

func (p *page) result() result {
	r := result{Nodes: p.nodes, Edges: p.edges, Truncated: p.truncated, Tokens: p.used}
	if r.Nodes == nil {
		r.Nodes = []nodeOut{}
	}
	if r.Edges == nil {
		r.Edges = []edgeOut{}
	}
	if p.truncated {
		r.Omitted = &omitted{Nodes: len(p.omitNodes), Edges: len(p.omitEdges)}
	}
	return r
}

// jsonResult renders v as the tool's text content. Extra fields on a
// result are merged in at the top level, sorted so output is stable.
func jsonResult(v any) *mcpsdk.CallToolResult {
	if r, ok := v.(result); ok && len(r.Extra) > 0 {
		v = withExtra(r)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return mcpsdk.NewToolResultErrorf("render: %v", err)
	}
	return mcpsdk.NewToolResultText(string(b))
}

// withExtra flattens a result and its extras into one ordered object.
func withExtra(r result) json.RawMessage {
	base, _ := json.Marshal(r)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(base, &m)
	for k, v := range r.Extra {
		b, err := json.Marshal(v)
		if err == nil {
			m[k] = b
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		kb, _ := json.Marshal(k)
		out = append(out, kb...)
		out = append(out, ':')
		out = append(out, m[k]...)
	}
	return append(out, '}')
}

func errorResult(err error) *mcpsdk.CallToolResult {
	return mcpsdk.NewToolResultError(err.Error())
}
