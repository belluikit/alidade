// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"maps"
	"slices"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// includedCalls adds the calls of each function of a hand-written file
// that an include! reads (#88, the owner's decision): rust-analyzer's
// index has no document of such a file, and its server's call hierarchy
// answers for it. The call hierarchy reports calls alone, so a function
// the file names as a value has no references edge; nor does it report a
// call of a method of an impl block the file itself writes (measured,
// rust-analyzer 1.100).
func (x *extractor) includedCalls() error {
	seen := map[edgeKey]bool{}
	for _, rel := range slices.Sorted(maps.Keys(x.included)) {
		items := x.included[rel]
		for _, id := range slices.Sorted(maps.Keys(items)) {
			d := items[id]
			if !isFunc(d.kind) || d.doc == nil || d.doc.rel != rel {
				continue
			}
			if err := x.callsOf(d, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// callsOf adds the edges of one function of an included file.
func (x *extractor) callsOf(d *def, seen map[edgeKey]bool) error {
	l := x.server(d.doc.ws)
	if l == nil {
		return nil
	}
	calls, err := l.outgoing(d.doc, d.start, x.text)
	if err != nil {
		x.skip(d.doc.abs, int(d.start.line)+1, err)
		return nil
	}
	for _, c := range calls {
		for _, r := range c.from {
			path, target := x.definedAt(l, d.doc, lastChar(r), c)
			if err := x.includedCall(d, target, path, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// lastChar is where the last character of a range is: the function's own
// name in a path that names it (`crate::helper`), whose first is the crate.
func lastChar(r [2]pos) pos {
	if r[1].line == r[0].line && r[1].col > r[0].col {
		return pos{r[1].line, r[1].col - 1}
	}
	return r[0]
}

// definedAt is what a call site of an included file calls, as the index
// would name it: the server's definition of the name there, the impl's
// method for a trait's method called on a known type, where the call
// hierarchy names the trait's (Area.area() for h.area()). The call
// hierarchy's target stands when the definition gives none.
func (x *extractor) definedAt(l *lsp, dc *doc, at pos, c outgoingCall) (string, *def) {
	if locs, err := l.definition(dc, at, x.text); err == nil && len(locs) == 1 {
		path, p := locs[0].at()
		if d := x.funcAt(path, p); d != nil {
			return path, d
		}
	}
	return c.path, x.funcAt(c.path, c.to)
}

// includedCall adds one call from a function of an included file, once
// per pair, and counts the call site's outcome as edges does: one to a
// function outside the repository is CallsOutside, one to a function of
// it with no node (Cargo's output) CallsUnplaced.
func (x *extractor) includedCall(from, target *def, path string, seen map[edgeKey]bool) error {
	x.report.CallSites++
	if target == nil {
		if _, err := x.m.Rel(path); err != nil {
			x.report.CallsOutside++
		} else {
			x.report.CallsUnplaced++
		}
		return nil
	}
	k := edgeKey{from.id, target.id, graph.KindCalls}
	if seen[k] {
		return nil
	}
	seen[k] = true
	x.report.Calls++
	return x.b.AddEdge(graph.Edge{From: k.from, To: k.to, Kind: k.kind, Derivation: graph.Resolved})
}

// funcAt is the function with a node whose name starts at p in the file at
// path: one the index defines, or one of an included file; nil for any
// other place (the standard library, a dependency, Cargo's output).
func (x *extractor) funcAt(path string, p pos) *def {
	rel, err := x.m.Rel(path)
	if err != nil {
		return nil
	}
	var defs []*def
	if dc := x.docs[rel]; dc != nil {
		defs = x.funcs[dc]
	} else if items := x.included[rel]; items != nil {
		for _, d := range items {
			defs = append(defs, d)
		}
	}
	for _, d := range defs {
		if isFunc(d.kind) && d.start == p && d.id != "" {
			return d
		}
	}
	return nil
}

// An outlineImpl is an impl block of an included file's outline, `impl
// crate::Area for Hand`: the node of its type and its trait's name.
type outlineImpl struct {
	ty    graph.ID
	trait string
}

// fromOutlines adds an implements edge for each impl block of an included
// file, from its type to the workspace trait of its name when exactly one
// workspace trait has that name (#88, the owner's decision): the server
// answers for such an implementation at the include!, which names no type.
func (x *extractor) fromOutlines(add func(from, to graph.ID) error) error {
	byName := map[string][]graph.ID{}
	for _, ts := range x.traits() {
		for _, t := range ts {
			name := t.path[len(t.path)-1]
			byName[name] = append(byName[name], t.id)
		}
	}
	for _, items := range x.included {
		for _, d := range items {
			if d.kind == scip.SymbolInformation_Trait {
				byName[d.path[len(d.path)-1]] = append(byName[d.path[len(d.path)-1]], d.id)
			}
		}
	}
	for _, oi := range x.outlineImpls {
		ids := slices.Compact(slices.Sorted(slices.Values(byName[oi.trait])))
		if len(ids) != 1 {
			x.report.ImplementsUnplaced++
			continue
		}
		if err := add(oi.ty, ids[0]); err != nil {
			return err
		}
	}
	return nil
}

// atHandInclude says whether a server's answer points at the include! of
// a hand-written file or into the file: where it places what the file
// implements (at the include! when two modules include it, else in its
// impl header), which fromOutlines reads instead.
func (x *extractor) atHandInclude(lo location) bool {
	path, p := lo.at()
	rel, err := x.m.Rel(path)
	if err != nil {
		return false
	}
	return slices.Contains(x.handSites[rel], p) || x.included[rel] != nil
}
