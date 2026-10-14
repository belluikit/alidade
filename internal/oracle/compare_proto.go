// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// ProtoResult compares the graph's proto layer with protoc and the
// generated .pb.go files. Each count is [oracle, graph has]; the Missing
// lists name what protoc has and the graph lacks, the Extra lists what
// the graph has in a file protoc compiled and protoc does not. A file
// protoc refused is in neither: its graph nodes are not compared.
type ProtoResult struct {
	Files, Failed, MapEntries                       int
	Decls, Imports, Refs, Generates                 [2]int
	MissingDecls, MissingImports, MissingRefs       []string
	MissingGenerates, WrongGenerates                []string
	ExtraDecls, ExtraImports, ExtraRefs, ExtraKinds []string
}

// CompareProto compares a graph with the proto oracle.
func CompareProto(g *graph.Graph, p *Proto) *ProtoResult {
	r := &ProtoResult{Files: len(p.Files), Failed: len(p.Failed), MapEntries: p.MapEntries}
	r.decls(g, p)
	r.Imports, r.MissingImports = edgeRecall(g, p.Imports, graph.KindImports)
	r.Refs, r.MissingRefs = edgeRecall(g, p.Refs, graph.KindReferences)
	r.ExtraImports = extraEdges(g, p, p.Imports, graph.KindImports, graph.KindProtoFile)
	r.ExtraRefs = extraEdges(g, p, p.Refs, graph.KindReferences, "")
	r.generates(g, p)
	return r
}

// compared reports whether a graph node comes from a file protoc compiled.
func compared(g *graph.Graph, p *Proto, id graph.ID) bool {
	n, ok := g.Node(id)
	return ok && p.Files[n.Pos.File]
}

func (r *ProtoResult) decls(g *graph.Graph, p *Proto) {
	for id, kind := range p.Decls {
		r.Decls[0]++
		n, ok := g.Node(id)
		switch {
		case !ok:
			r.MissingDecls = append(r.MissingDecls, fmt.Sprintf("%s %s", kind, id))
		case n.Kind != kind:
			r.ExtraKinds = append(r.ExtraKinds, fmt.Sprintf("%s is %s, protoc says %s", id, n.Kind, kind))
		default:
			r.Decls[1]++
		}
	}
	for n := range g.Nodes() {
		if !strings.HasPrefix(string(n.ID), "proto://") || n.Kind == graph.KindProtoFile || !p.Files[n.Pos.File] {
			continue
		}
		if _, ok := p.Decls[n.ID]; !ok {
			r.ExtraDecls = append(r.ExtraDecls, fmt.Sprintf("%s %s", n.Kind, n.ID))
		}
	}
	sortAll(&r.MissingDecls, &r.ExtraDecls, &r.ExtraKinds)
}

// edgeRecall counts how many of the oracle's edges the graph has.
func edgeRecall(g *graph.Graph, want [][2]graph.ID, kind graph.EdgeKind) ([2]int, []string) {
	var n [2]int
	var missing []string
	for _, e := range want {
		n[0]++
		if hasEdge(g, e[0], kind, e[1]) {
			n[1]++
		} else {
			missing = append(missing, string(e[0])+" -> "+string(e[1]))
		}
	}
	slices.Sort(missing)
	return n, missing
}

// extraEdges lists the graph's edges of a kind, from a proto node of a
// compiled file (of fromKind when given), that the oracle lacks.
func extraEdges(g *graph.Graph, p *Proto, want [][2]graph.ID, kind graph.EdgeKind, fromKind graph.Kind) []string {
	set := map[[2]graph.ID]bool{}
	for _, e := range want {
		set[e] = true
	}
	var out []string
	for e := range g.Edges() {
		if e.Kind != kind || !strings.HasPrefix(string(e.From), "proto://") || set[[2]graph.ID{e.From, e.To}] {
			continue
		}
		if n, ok := g.Node(e.From); !ok || fromKind != "" && n.Kind != fromKind || !compared(g, p, e.From) {
			continue
		}
		out = append(out, string(e.From)+" -> "+string(e.To))
	}
	slices.Sort(out)
	return out
}

// generates compares the layer's generates edges with protoc-gen-go's
// tables, for the messages and enums protoc compiled. An edge whose Go
// end the Go layer did not see is dropped, not absent: it counts.
func (r *ProtoResult) generates(g *graph.Graph, p *Proto) {
	have := map[graph.ID][]graph.ID{}
	add := func(e graph.Edge) {
		if e.Kind == graph.KindGenerates && strings.HasPrefix(string(e.From), "proto://") {
			have[e.From] = append(have[e.From], e.To)
		}
	}
	for e := range g.Edges() {
		add(e)
	}
	for _, d := range g.Dropped() {
		add(d.Edge)
	}
	for _, e := range p.Generates {
		if _, ok := p.Decls[e[0]]; !ok {
			continue // a type generated from a .proto protoc did not compile here
		}
		r.Generates[0]++
		switch got := have[e[0]]; {
		case slices.Contains(got, e[1]):
			r.Generates[1]++
		case len(got) == 0:
			r.MissingGenerates = append(r.MissingGenerates, string(e[0])+" -> "+string(e[1]))
		default:
			r.WrongGenerates = append(r.WrongGenerates, fmt.Sprintf("%s -> %s, graph says %v", e[0], e[1], got))
		}
	}
	sortAll(&r.MissingGenerates, &r.WrongGenerates)
}

func sortAll(lists ...*[]string) {
	for _, l := range lists {
		slices.Sort(*l)
	}
}

func (r *ProtoResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "proto files compiled %d, refused %d, map entries not compared %d\n", r.Files, r.Failed, r.MapEntries)
	fmt.Fprintf(&b, "proto decls %d / %d, imports %d / %d, refs %d / %d, generates %d / %d\n",
		r.Decls[1], r.Decls[0], r.Imports[1], r.Imports[0], r.Refs[1], r.Refs[0], r.Generates[1], r.Generates[0])
	fmt.Fprintf(&b, "proto extra decls %d, extra imports %d, extra refs %d, kind mismatches %d, wrong generates %d\n",
		len(r.ExtraDecls), len(r.ExtraImports), len(r.ExtraRefs), len(r.ExtraKinds), len(r.WrongGenerates))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing decl", r.MissingDecls}, {"missing import", r.MissingImports}, {"missing ref", r.MissingRefs},
		{"missing generates", r.MissingGenerates}, {"wrong generates", r.WrongGenerates}, {"kind", r.ExtraKinds},
		{"extra decl", r.ExtraDecls}, {"extra import", r.ExtraImports}, {"extra ref", r.ExtraRefs}} {
		for i, s := range l.items {
			if i == 10 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-10)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}

func hasEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) bool {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return true
		}
	}
	return false
}
