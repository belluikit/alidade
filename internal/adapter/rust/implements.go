// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// implementations adds an implements edge from each type to each
// workspace trait it implements (ADR 009, ADR 006 rule 9), from two
// sources, since neither has all of them (measured on protowire-pb's
// Message, 55 implementations):
//   - rust-analyzer's server, asked for each workspace trait's
//     implementations: the exact trait, and the ones a macro_rules! macro
//     writes, which the index has no symbols for (15 of the 55). It misses
//     the ones written inside a function body (5).
//   - the index's symbols for the methods of those (impl#[Holder][Message]),
//     joined to the workspace trait of that name only when there is
//     exactly one. Only inside a function body: elsewhere the server has
//     the implementation, and a trait of another crate that shares a
//     workspace trait's name (prost's Message) would be joined to it.
func (x *extractor) implementations() error {
	seen := map[[2]graph.ID]bool{}
	add := func(from, to graph.ID) error {
		k := [2]graph.ID{from, to}
		if seen[k] {
			return nil
		}
		seen[k] = true
		x.report.Implements++
		return x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImplements, Derivation: graph.Resolved})
	}
	for _, ws := range x.indexed {
		if err := x.askServer(ws, add); err != nil {
			return err
		}
	}
	return x.inBodies(add)
}

// traits are the workspace's traits with a node, by workspace root, in
// the order of their files.
func (x *extractor) traits() map[string][]*def {
	out := map[string][]*def{}
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		for _, d := range x.defsOf[x.docs[rel]] {
			if d.kind == scip.SymbolInformation_Trait && d.id != "" {
				out[d.doc.ws] = append(out[d.doc.ws], d)
			}
		}
	}
	return out
}

// askServer asks rust-analyzer's server on one workspace where each of
// its traits is implemented, and adds an edge from each implementing type
// with a node. A server that cannot start, or fails, is reported and
// leaves those edges out: the index's still stand.
func (x *extractor) askServer(ws string, add func(from, to graph.ID) error) error {
	traits := x.traits()[ws]
	if len(traits) == 0 {
		return nil
	}
	l, err := startLSP(ws)
	if err != nil {
		x.skip(ws, 0, err)
		return nil
	}
	defer l.close()
	x.report.ServerColumns = "utf-16"
	if l.utf8 {
		x.report.ServerColumns = "utf-8"
	}
	for _, t := range traits {
		locs, err := l.implementations(t.doc, t.start, x.text)
		if err != nil {
			x.skip(t.doc.abs, int(t.start.line)+1, err)
			continue
		}
		for _, lo := range locs {
			ty := x.typeAt(lo)
			if ty == nil {
				x.report.ImplementsUnplaced++
				continue
			}
			if err := add(ty.id, t.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// typeAt is the type with a node a server's answer names: an
// implementation's answer is the implementing type's name in the impl's
// header, a reference to the type in the index, or, for a type a macro
// writes in the same call, its definition. nil for a type with no node:
// a type parameter (impl<F> Validator for F), one outside the workspace,
// a slice.
func (x *extractor) typeAt(lo location) *def {
	path, p := lo.at()
	rel, err := x.m.Rel(path)
	if err != nil {
		return nil
	}
	dc := x.docs[rel]
	if dc == nil {
		return nil
	}
	for _, o := range dc.d.GetOccurrences() {
		start, _, ok := occRange(o)
		if !ok || start != p || strings.HasPrefix(o.GetSymbol(), "local ") {
			continue
		}
		if d, _ := x.resolve(o.GetSymbol(), dc); d != nil && kinds[d.kind] == graph.KindType && d.kind != scip.SymbolInformation_Trait {
			return d
		}
	}
	return nil
}

// text is a file's text, by absolute path, for converting a server's
// columns.
func (x *extractor) text(path string) []byte {
	if rel, err := x.m.Rel(path); err == nil && x.docs[rel] != nil {
		return x.docs[rel].text
	}
	b, _ := os.ReadFile(path)
	return b
}

// inBodies adds the implementations written inside a function body, which
// the server does not find, from the index's names for their methods: the
// type's node is the one the method's path holds (local_impl.Local for
// local_impl.Local[Area].area), the trait the one workspace trait of that
// name that declares the method. An implementation whose trait's name two
// workspace traits share gets no edge, nor one of a trait elsewhere that
// only shares a workspace trait's name (fmt::Write and a workspace Write
// without write_str).
func (x *extractor) inBodies(add func(from, to graph.ID) error) error {
	byName := map[string][]*def{}
	for _, ts := range x.traits() {
		for _, t := range ts {
			byName[t.path[len(t.path)-1]] = append(byName[t.path[len(t.path)-1]], t)
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		for _, d := range x.defsOf[x.docs[rel]] {
			ty, trait, ok := x.bodyImpl(d, byName)
			if !ok {
				continue
			}
			if err := add(ty, trait); err != nil {
				return err
			}
		}
	}
	return nil
}

// bodyImpl reads one method of an implementation inside a function body:
// the node of its type and of the one workspace trait of the name its
// symbol gives that declares the method. ok is false for any other item,
// and for an implementation either node is missing for, which counts as
// unplaced when two workspace traits share the name.
func (x *extractor) bodyImpl(d *def, byName map[string][]*def) (ty, trait graph.ID, ok bool) {
	if !isFunc(d.kind) || d.holder == nil || len(d.path) < 2 {
		return "", "", false
	}
	typePath := typeOnly(d.path[:len(d.path)-1])
	if typePath == nil {
		return "", "", false // an inherent impl's method, or a nested function
	}
	_, name, _ := strings.Cut(strings.TrimSuffix(d.path[len(d.path)-2], "]"), "[")
	if len(byName[name]) > 1 {
		x.report.ImplementsUnplaced++
		return "", "", false
	}
	ty, err := x.m.RustID(d.doc.rel, typePath...)
	if err != nil || !x.emitted[ty] || len(byName[name]) == 0 {
		return "", "", false
	}
	t := byName[name][0]
	method, err := x.m.RustID(t.doc.rel, append(slices.Clone(t.path), d.path[len(d.path)-1])...)
	if err != nil || !x.emitted[method] {
		return "", "", false // a trait elsewhere of that name: fmt::Write, not a workspace Write
	}
	return ty, t.id, true
}
