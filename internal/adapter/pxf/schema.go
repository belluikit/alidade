// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"strings"

	"github.com/trendvidia/alidade/internal/graph"
)

// LinkSchema joins each .pxf field and block to the .proto field it sets
// (#190, the owner's decision): a references edge, resolved by walking
// the entry's block path through the message its document declares.
// It runs over the merged graph, after every layer has reported, as the
// Markdown linker does (ADR 001's link step), because the schema is the
// proto layer's.
//
// The message an entry is decoded against is its document's @type, or
// the type of the directive it sits in (@header chameleon.v1.LayerHeader
// { … }). A chameleon layer with no @type takes the one type the other
// layers of its chain declare: the resolver merges every layer of a
// chain into one message, so they are all of that type. Such an edge is
// DECLARED, by the chain, with via=chain; one from a declared type is
// RESOLVED. A chain whose layers declare no type, or more than one,
// gives its untyped layers none.
//
// A path segment after a repeated or map field is the element's key —
// domains.core-interpreter is an element of domains — and an element
// sets the same field as its collection. An entry whose path the schema
// does not have is left unlinked and counted.
func LinkSchema(b *graph.Builder, g *graph.Graph, rep *Report) {
	l := &schemaLinker{b: b, g: g, rep: rep, types: map[string]graph.ID{}, inferred: map[string]bool{}}
	l.declaredTypes()
	l.chainTypes()
	for n := range g.Nodes() {
		if n.Kind == graph.KindField || n.Kind == graph.KindBlock {
			l.entry(n.ID)
		}
	}
}

type schemaLinker struct {
	b   *graph.Builder
	g   *graph.Graph
	rep *Report
	// types maps a root — a file's path, or file#@directive — to the
	// message it is decoded against; inferred marks the files whose
	// type came from their chain.
	types    map[string]graph.ID
	inferred map[string]bool
}

// declaredTypes reads each document's and directive's type from its
// references edge to a message of the graph.
func (l *schemaLinker) declaredTypes() {
	for n := range l.g.Nodes() {
		body, ok := strings.CutPrefix(string(n.ID), "pxf://")
		if !ok {
			continue
		}
		file, path, hasPath := strings.Cut(body, "#")
		if hasPath && !strings.HasPrefix(path, "@") {
			continue
		}
		if msg, ok := l.messageOf(n.ID); ok {
			if hasPath {
				l.types[file+"#"+path] = msg
			} else {
				l.types[file] = msg
			}
		}
	}
}

// messageOf is the message a node's references edge names, when the
// graph has that message.
func (l *schemaLinker) messageOf(id graph.ID) (graph.ID, bool) {
	for _, e := range l.g.Out(id) {
		if e.Kind != graph.KindReferences || !strings.HasPrefix(string(e.To), "proto://") {
			continue
		}
		if n, ok := l.g.Node(e.To); ok && n.Kind == graph.KindMessage {
			return e.To, true
		}
	}
	return "", false
}

// chainTypes gives each untyped layer of a chain the one type the
// chain's typed layers declare.
func (l *schemaLinker) chainTypes() {
	implied := map[string]map[graph.ID]bool{} // untyped layer -> types its chains imply
	for n := range l.g.Nodes() {
		if n.Kind == graph.KindPXFFile {
			l.imply(l.layersOf(n.ID), implied)
		}
	}
	for f, ts := range implied {
		if len(ts) != 1 {
			continue // two chains disagree about the file
		}
		for t := range ts {
			l.types[f] = t
			l.inferred[f] = true
			l.rep.SchemaInferred++
		}
	}
}

// imply records, for one chain's untyped layers, the type its typed
// layers declare, when they declare exactly one.
func (l *schemaLinker) imply(layers []string, implied map[string]map[graph.ID]bool) {
	chainType := map[graph.ID]bool{}
	for _, f := range layers {
		if t, ok := l.types[f]; ok {
			chainType[t] = true
		}
	}
	if len(chainType) != 1 {
		return
	}
	for _, f := range layers {
		if _, typed := l.types[f]; typed {
			continue
		}
		if implied[f] == nil {
			implied[f] = map[graph.ID]bool{}
		}
		for t := range chainType {
			implied[f][t] = true
		}
	}
}

// layersOf lists the layer files a chain specification loads: its
// references edges to .pxf files that carry the layer they are.
func (l *schemaLinker) layersOf(chain graph.ID) []string {
	var out []string
	for _, e := range l.g.Out(chain) {
		if e.Kind != graph.KindReferences || e.Attrs["layer"] == "" {
			continue
		}
		if f, ok := strings.CutPrefix(string(e.To), "pxf://"); ok && !strings.Contains(f, "#") {
			out = append(out, f)
		}
	}
	return out
}

// entry links one field or block to the proto field it sets.
func (l *schemaLinker) entry(id graph.ID) {
	body, ok := strings.CutPrefix(string(id), "pxf://")
	if !ok {
		return
	}
	file, path, ok := strings.Cut(body, "#")
	if !ok {
		return
	}
	root, rest := file, path
	if strings.HasPrefix(path, "@") {
		dir, sub, nested := strings.Cut(path, ".")
		if !nested {
			return // the directive itself references its type already
		}
		root, rest = file+"#"+dir, sub
	}
	msg, typed := l.types[root]
	if !typed {
		return
	}
	field, ok := l.resolve(msg, strings.Split(rest, "."))
	if !ok {
		l.rep.SchemaUnresolved++
		return
	}
	l.rep.SchemaLinked++
	e := graph.Edge{From: id, To: field, Kind: graph.KindReferences, Derivation: graph.Resolved}
	if l.inferred[root] {
		e.Derivation, e.Attrs = graph.Declared, map[string]string{"via": "chain"}
	}
	_ = l.b.AddEdge(e)
}

// resolve walks a block path from a message, returning the proto field
// its last named segment sets.
func (l *schemaLinker) resolve(msg graph.ID, segs []string) (graph.ID, bool) {
	var field graph.ID
	for i := 0; i < len(segs); i++ {
		field = graph.ID(string(msg) + "." + segs[i])
		n, ok := l.g.Node(field)
		if !ok || n.Kind != graph.KindField {
			return "", false
		}
		if next, ok := l.messageOf(field); ok {
			msg = next
		}
		if n.Attrs["repeated"] == "true" || n.Attrs["map"] == "true" {
			i++ // the element's key: an element sets its collection's field
		}
	}
	return field, field != ""
}
