// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// WritePXF writes the graph as a PXF document.
//
// PXF is the format alidade speaks; JSON belongs at an integration edge
// where a protocol demands it, not in the core. The graph is one of the
// things this tool is about — a .pxf layer is a first-class citizen of it
// (ADR 003) — so emitting anything else here would mean the tool could not
// read its own output.
//
// The encoding is deterministic: stats first, then nodes in ID order, edges
// in canonical order, and every map rendered with its keys sorted. Two
// builds over an unchanged tree produce byte-identical bytes, which is what
// makes the identity guarantee testable rather than asserted.
//
// There is no reader. Reading a graph back is the first half of a persisted
// graph, and ADR 005 does not have one: the graph is built from the working
// tree at query time, so it cannot go stale, and a loader would be the entry
// point for exactly the staleness bugs that decision avoids.
//
// extra blocks follow the stats block: a consumer's own report of the
// build that produced the graph, which the graph itself does not know.
func (g *Graph) WritePXF(w io.Writer, extra ...pxf.Entry) error {
	doc := g.document()
	doc.Entries = append(doc.Entries[:1], append(extra, doc.Entries[1:]...)...)
	if _, err := w.Write(pxf.FormatDocument(doc)); err != nil {
		return fmt.Errorf("graph: write pxf: %w", err)
	}
	return nil
}

// The wire shape lives here in full. It is a surface other things depend on
// — the MCP layer budgets tokens against it — so a block renamed on a whim
// breaks every consumer at once.
//
// Free-form vocabularies are strings and closed ones are identifiers: an
// adapter may emit a node kind this package has never heard of, but there
// are exactly three derivations and two drop reasons, and writing those as
// bare identifiers is what an enum looks like in PXF.
func (g *Graph) document() *pxf.Document {
	doc := &pxf.Document{
		LeadingComments: []pxf.Comment{{Text: "# alidade graph, built on demand from the working tree (ADR 005)."}},
		Entries:         []pxf.Entry{g.statsBlock()},
	}
	for _, id := range g.order {
		doc.Entries = append(doc.Entries, nodeBlock(g.nodes[id]))
	}
	for _, e := range g.edges {
		doc.Entries = append(doc.Entries, edgeBlock("edge", e, nil))
	}
	for _, d := range g.dropped {
		doc.Entries = append(doc.Entries, edgeBlock("dropped", d.Edge, &d.Reason))
	}
	for _, c := range g.conflicts {
		doc.Entries = append(doc.Entries, pxfBlock("conflict",
			pxfString("subject", c.Subject),
			pxfString("field", c.Field),
			pxfString("kept", c.Kept),
			pxfString("discarded", c.Discarded),
		))
	}
	return doc
}

func nodeBlock(n Node) pxf.Entry {
	return pxfBlock("node",
		pxfString("id", string(n.ID)),
		pxfString("kind", string(n.Kind)),
		pxfString("name", n.Name),
		pxfString("file", n.Pos.File),
		pxfInt("line", n.Pos.Line),
		pxfInt("col", n.Pos.Col),
		pxfBool("generated", n.Generated),
		pxfBool("test", n.Test),
		pxfStringMap("attrs", n.Attrs),
	)
}

// edgeBlock renders an edge. A dropped edge is the same shape plus the
// reason it did not resolve, so the two read alike and a consumer comparing
// them does not have to translate between two layouts.
func edgeBlock(name string, e Edge, reason *DropReason) pxf.Entry {
	entries := []pxf.Entry{
		pxfString("from", string(e.From)),
		pxfString("to", string(e.To)),
		pxfString("kind", string(e.Kind)),
		pxfIdent("derivation", e.Derivation.String()),
		pxfBool("test", e.Test),
	}
	if reason != nil {
		entries = append(entries, pxfIdent("reason", string(*reason)))
	}
	entries = append(entries, pxfStringMap("attrs", e.Attrs))
	return pxfBlock(name, entries...)
}

func (g *Graph) statsBlock() pxf.Entry {
	s := g.stats
	return pxfBlock("stats",
		pxfInt("nodes", s.Nodes),
		pxfInt("edges", s.Edges),
		pxfInt("generated", s.Generated),
		pxfInt("dropped", s.Dropped),
		pxfInt("conflicts", s.Conflicts),
		pxfCountMap("nodes_by_scheme", s.NodesByScheme, func(k Scheme) string { return string(k) }),
		// Derivations are ordered strongest first rather than
		// alphabetically: the constants carry that order, and reading
		// the counts in it is how a rising INFERRED share becomes
		// obvious rather than arithmetic.
		pxfCountMap("edges_by_derivation", s.EdgesByDerivation, Derivation.String),
		pxfCountMap("dropped_by_reason", s.DroppedByReason, func(k DropReason) string { return string(k) }),
	)
}

// The helpers below omit a field that carries nothing rather than writing an
// empty one. An absent field and a zero one mean the same thing in every
// case here — an unknown position, no attributes, not generated — and a
// document an agent reads is worth keeping short.

func pxfBlock(name string, entries ...pxf.Entry) pxf.Entry {
	return &pxf.Block{Name: name, Entries: slices.DeleteFunc(entries, func(e pxf.Entry) bool { return e == nil })}
}

func pxfString(key, v string) pxf.Entry {
	if v == "" {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.StringVal{Value: v}}
}

func pxfIdent(key, v string) pxf.Entry {
	return &pxf.Assignment{Key: key, Value: &pxf.IdentVal{Name: v}}
}

func pxfInt(key string, v int) pxf.Entry {
	if v == 0 {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.IntVal{Raw: strconv.Itoa(v)}}
}

func pxfBool(key string, v bool) pxf.Entry {
	if !v {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.BoolVal{Value: v}}
}

// pxfStringMap renders attributes as a map block, keys sorted.
func pxfStringMap(name string, m map[string]string) pxf.Entry {
	if len(m) == 0 {
		return nil
	}
	entries := make([]pxf.Entry, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		entries = append(entries, &pxf.MapEntry{Key: k, Value: &pxf.StringVal{Value: m[k]}})
	}
	return &pxf.Block{Name: name, Entries: entries}
}

// pxfCountMap renders a tally, ordered by the key type's own ordering so the
// counts read the way the vocabulary is declared.
func pxfCountMap[K cmpOrdered](name string, m map[K]int, key func(K) string) pxf.Entry {
	if len(m) == 0 {
		return nil
	}
	entries := make([]pxf.Entry, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		entries = append(entries, &pxf.MapEntry{Key: key(k), Value: &pxf.IntVal{Raw: strconv.Itoa(m[k])}})
	}
	return &pxf.Block{Name: name, Entries: entries}
}

// cmpOrdered is the subset of comparable key types the tallies use.
type cmpOrdered interface {
	~string | ~uint8
}
