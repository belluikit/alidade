// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"strconv"
	"strings"

	"github.com/trendvidia/alidade/internal/graph"
	"github.com/trendvidia/alidade/internal/python"
)

// kinds maps the resolver's declaration kinds to the graph's.
var kinds = map[string]graph.Kind{
	"class": graph.KindType, "function": graph.KindFunc, "method": graph.KindMethod,
	"variable": graph.KindVar, "field": graph.KindField,
}

// declarations adds a node per declaration, contained by the declaration
// holding it or by its file. Two of one qualified name in one file — a
// function defined in both branches of an if — are one node, at the
// first (ADR 002 rule 15).
func (x *extractor) declarations(decls []python.Decl) error {
	x.decls = map[declKey]graph.ID{}
	for _, d := range decls {
		path := strings.Split(d.Path, ".")
		id, err := x.m.PyID(d.File, path...)
		if err != nil {
			x.skip(d.File, err)
			continue
		}
		x.decls[declKey{d.File, d.Line, path[len(path)-1]}] = id
		if x.emitted[id] {
			continue
		}
		x.emitted[id] = true
		x.report.Declarations++
		if err := x.b.AddNode(graph.Node{ID: id, Kind: kinds[d.Kind], Name: path[len(path)-1],
			Pos: graph.Position{File: d.File, Line: d.Line, Col: d.Col}, Attrs: map[string]string{"end_line": strconv.Itoa(d.EndLine)}}); err != nil {
			return err
		}
		if err := x.b.AddEdge(graph.Edge{From: x.owner(d.File, path[:len(path)-1]), To: id, Kind: graph.KindContains, Derivation: graph.Extracted}); err != nil {
			return err
		}
	}
	return nil
}

// owner is the node of the scope at path in a file: the innermost
// declaration of it that has a node, or the file.
func (x *extractor) owner(file string, path []string) graph.ID {
	for n := len(path); n > 0; n-- {
		if id, err := x.m.PyID(file, path[:n]...); err == nil && x.emitted[id] {
			return id
		}
	}
	id, _ := x.m.PyID(file)
	return id
}

// imports adds a file → file edge per import resolved to a file the layer
// reads, once per pair.
func (x *extractor) imports(r *python.Result) error {
	x.report.ImportsOutside, x.report.ImportsUnresolved = r.ImportsOutside, r.ImportsUnresolved
	seen := map[[2]graph.ID]bool{}
	for _, i := range r.Imports {
		if !x.read[i.Target] {
			x.report.ImportsOutside++ // inside the root, in a directory the layer skips
			continue
		}
		from, _ := x.m.PyID(i.File)
		to, _ := x.m.PyID(i.Target)
		if key := [2]graph.ID{from, to}; from != to && !seen[key] {
			seen[key] = true
			x.report.Imports++
			if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImports, Derivation: x.derivation(i.File)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// calls adds an edge from the declaration holding each call to the one
// jedi resolves it to (ADR 006, amended by #237: RESOLVED), once per pair,
// and counts every call site's outcome. A site whose targets all lie in
// files the layer does not read is outside; one with a target in a file
// it reads but no node there (a function inside a lambda, say) unplaced.
func (x *extractor) calls(r *python.Result) error {
	type site struct {
		file      string
		line, col int
	}
	type outcome struct{ placed, inRead bool }
	sites := map[site]*outcome{}
	seen := map[[2]graph.ID]bool{}
	for _, c := range r.Calls {
		s := site{c.File, c.Line, c.Col}
		o := sites[s]
		if o == nil {
			o = &outcome{}
			sites[s] = o
		}
		to, ok := x.decls[declKey{c.Target, c.TargetLine, c.TargetName}]
		if !ok {
			o.inRead = o.inRead || x.read[c.Target]
			continue
		}
		o.placed = true
		from := x.owner(c.File, splitPath(c.Caller))
		if key := [2]graph.ID{from, to}; !seen[key] {
			seen[key] = true
			x.report.Calls++
			if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindCalls, Derivation: x.derivation(c.File)}); err != nil {
				return err
			}
		}
	}
	x.report.CallSites = len(sites) + r.Outside + r.Unplaced + r.Unresolved
	x.report.CallsOutside, x.report.CallsUnplaced, x.report.CallsUnresolved = r.Outside, r.Unplaced, r.Unresolved
	for _, o := range sites {
		switch {
		case o.placed:
		case o.inRead:
			x.report.CallsUnplaced++
		default:
			x.report.CallsOutside++
		}
	}
	return nil
}

// derivation is an edge's from a file: INFERRED while the file's answers
// are the warm-up pass's, by name alone (#269), RESOLVED once jedi's.
func (x *extractor) derivation(file string) graph.Derivation {
	if x.inferred[file] {
		return graph.Inferred
	}
	return graph.Resolved
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// references adds an edge from the declaration holding a function's name,
// used as a value and not called, to the function (ADR 008, #244): a stub
// installed, a callback passed, a function returned. Once per pair.
func (x *extractor) references(r *python.Result) error {
	seen := map[[2]graph.ID]bool{}
	for _, c := range r.Refs {
		to, ok := x.decls[declKey{c.Target, c.TargetLine, c.TargetName}]
		if !ok {
			continue
		}
		from := x.owner(c.File, splitPath(c.Caller))
		if key := [2]graph.ID{from, to}; from != to && !seen[key] {
			seen[key] = true
			x.report.References++
			if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindReferences, Derivation: x.derivation(c.File)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// bases adds an implements edge from each class to each class it
// inherits from (#249, #240 decision 2), as jedi resolves the base: its
// direct bases only, a grandparent reached through the base's own edges.
// A base outside the files read has no node and is counted as outside.
func (x *extractor) bases(r *python.Result) error {
	x.report.BasesOutside, x.report.BasesUnplaced, x.report.BasesUnresolved = r.BasesOutside, r.BasesUnplaced, r.BasesUnresolved
	seen := map[[2]graph.ID]bool{}
	for _, c := range r.Bases {
		to, ok := x.decls[declKey{c.Target, c.TargetLine, c.TargetName}]
		if !ok {
			if x.read[c.Target] {
				x.report.BasesUnplaced++
			} else {
				x.report.BasesOutside++
			}
			continue
		}
		from := x.owner(c.File, splitPath(c.Caller))
		if key := [2]graph.ID{from, to}; from != to && !seen[key] {
			seen[key] = true
			x.report.Bases++
			if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImplements, Derivation: x.derivation(c.File)}); err != nil {
				return err
			}
		}
	}
	return nil
}
