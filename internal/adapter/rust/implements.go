// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
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
	l := x.server(ws)
	if l == nil {
		return nil
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
// implementation's answer is where the implementing type starts in the
// impl's header, and the type is the first index reference there or after
// it on that line (the server starts `&Gauge` at its `&`). For a type a
// macro writes in the same call, the reference is its definition. nil for a
// type with no node: a type parameter (impl<F> Validator for F), one outside
// the workspace (Vec, whose argument is not the type), a slice.
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
	first := firstFrom(dc, p)
	if first == nil {
		return nil
	}
	if d, _ := x.resolve(first.GetSymbol(), dc); d != nil && kinds[d.kind] == graph.KindType && d.kind != scip.SymbolInformation_Trait {
		return d
	}
	return nil
}

// firstFrom is the first occurrence on p's line at or after p that names
// an item, not a local.
func firstFrom(dc *doc, p pos) *scip.Occurrence {
	var first *scip.Occurrence
	var at pos
	for _, o := range dc.d.GetOccurrences() {
		start, _, ok := occRange(o)
		if !ok || start.line != p.line || start.before(p) || strings.HasPrefix(o.GetSymbol(), "local ") {
			continue
		}
		if first == nil || start.before(at) {
			first, at = o, start
		}
	}
	return first
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
// the server does not find (ADR 009, ADR 006 rule 9). The type's node is
// the one the method's path holds (local_impl.Local for
// local_impl.Local[Area].area); the trait is the one the impl's header
// names, read from the index's reference to it there: the exact trait, so
// fmt::Display inside a body is not a workspace trait named Display that
// declares fmt too. One edge per implementation, from its methods.
func (x *extractor) inBodies(add func(from, to graph.ID) error) error {
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		for _, d := range x.defsOf[x.docs[rel]] {
			ty, trait, ok := x.bodyImpl(d)
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
// the node of its type and of the workspace trait its impl's header names.
// ok is false for any other item, and for an implementation of a trait
// outside the workspace (fmt::Display) or whose header the index does not
// show (one a macro writes in a body).
func (x *extractor) bodyImpl(d *def) (ty, trait graph.ID, ok bool) {
	if !isFunc(d.kind) || d.holder == nil || len(d.path) < 2 {
		return "", "", false
	}
	typePath := typeOnly(d.path[:len(d.path)-1])
	if typePath == nil {
		return "", "", false // an inherent impl's method, or a nested function
	}
	ty, err := x.m.RustID(d.doc.rel, typePath...)
	if err != nil || !x.emitted[ty] {
		return "", "", false
	}
	_, name, _ := strings.Cut(strings.TrimSuffix(d.path[len(d.path)-2], "]"), "[")
	sym := headerTrait(d, name)
	// A trait of the workspace has a def; one outside it (fmt::Display)
	// has none and gets no edge.
	t, _ := x.resolve(sym, d.doc)
	if sym == "" || t == nil {
		return "", "", false
	}
	return ty, t.id, true
}

// headerTrait finds the trait's symbol on the header of the impl holding
// the method d, whose trait the method's symbol names: the nearest header
// above d, `impl … Trait … for Type … {`, with an index reference to a
// type of that name, the first such, which is the trait's: it comes
// before `for`. Reading the header is the one place the layer reads syntax
// beyond the token after a reference (ADR 006 rule 9, #72). A header
// nested in an earlier method of the same impl names another trait and is
// passed over; one naming the same trait gives the same edge.
func headerTrait(d *def, trait string) string {
	lines := bytes.Split(d.doc.text, []byte("\n"))
	for l := d.start.line; l >= 0 && int(l) < len(lines); l-- {
		t := bytes.TrimSpace(lines[l])
		if !bytes.HasPrefix(t, []byte("impl")) && !bytes.HasPrefix(t, []byte("unsafe impl")) {
			continue
		}
		if sym := headerRef(d.doc, lines, l, trait); sym != "" {
			return sym
		}
	}
	return ""
}

// headerRef reads the header starting on line first, up to the line
// holding its "{", and returns the first index reference on it to a type
// named trait.
func headerRef(dc *doc, lines [][]byte, first int32, trait string) string {
	last := first
	for int(last) < len(lines)-1 && !bytes.Contains(lines[last], []byte("{")) {
		last++
	}
	var found string
	var at pos
	for _, o := range dc.d.GetOccurrences() {
		sym, start, _, ok := referenceAt(o)
		if !ok || start.line < first || start.line > last || descriptorName(sym) != trait {
			continue
		}
		if found == "" || start.before(at) {
			found, at = sym, start
		}
	}
	return found
}

// descriptorName is the name a symbol's last descriptor gives, without
// generic arguments: Visitor for `Visitor<'de>`.
func descriptorName(sym string) string {
	s, err := scip.ParseSymbol(sym)
	if err != nil || len(s.GetDescriptors()) == 0 {
		return ""
	}
	ds := s.GetDescriptors()
	return typeName(ds[len(ds)-1].GetName())
}
