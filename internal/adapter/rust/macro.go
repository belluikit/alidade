// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"maps"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// macroWritten adds a node for each method a derive or a macro writes that
// the workspace calls, under the type it is written for (ADR 002 rule 16,
// #72, the owner's decision): `types.rs#EnumDescriptorProto.name()`, which
// #[derive(prost::Message)] writes beside the field `name`, and
// `Square[Clone].clone()`. The index defines none of them, and lists none
// as external: a call's symbol, `impl#[EnumDescriptorProto]name().`, is
// all there is, and it names the module, the type, the trait if any and
// the method. So only a method something calls has a node. rust-analyzer's
// server is no help: it places such a method at what the macro read, the
// field. Each is registered as the symbol's definition, for the calls. A
// type of an included file is one too: the index lists the method as
// external, as everything in such a file is, and its type's node is the
// generated or included item (Shape[Clone].clone(), #84).
func (x *extractor) macroWritten() error {
	pkgs := x.packages()
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		dc := x.docs[rel]
		for _, o := range dc.d.GetOccurrences() {
			sym, _, _, ok := referenceAt(o)
			if !ok || x.bySym[sym] != nil {
				continue
			}
			d := x.writtenFor(sym, pkgs, dc)
			if d == nil {
				continue
			}
			if err := x.declareWritten(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// packages are the workspace's packages, by name: the ones that define
// something in the index.
func (x *extractor) packages() map[string]bool {
	out := map[string]bool{}
	for sym := range x.bySym {
		if s, err := scip.ParseSymbol(sym); err == nil && s.GetPackage() != nil {
			out[s.GetPackage().GetName()] = true
		}
	}
	return out
}

// writtenFor reads a symbol of a method an impl block holds,
// `[module/]impl#[Type][Trait]method().`, of a workspace package, and
// returns the def it would have under its type's node; nil for any other
// symbol, or one whose type has no node.
func (x *extractor) writtenFor(sym string, pkgs map[string]bool, from *doc) *def {
	s, err := scip.ParseSymbol(sym)
	if err != nil || s.GetPackage() == nil || !pkgs[s.GetPackage().GetName()] {
		return nil
	}
	ds := s.GetDescriptors()
	impl, ok := implMethod(ds)
	if !ok {
		return nil
	}
	at := strings.Index(sym, "impl#")
	ty := typeName(ds[impl+1].GetName())
	if ty == "" || at < 0 {
		return nil
	}
	owner, _ := x.resolve(sym[:at]+ty+"#", from)
	if owner == nil || owner.id == "" {
		return nil
	}
	path := slices.Clone(owner.path)
	if impl+3 == len(ds)-1 {
		path[len(path)-1] += "[" + typeName(ds[impl+2].GetName()) + "]"
	}
	path = append(path, unraw(ds[len(ds)-1].GetName())+"()")
	return &def{doc: owner.doc, sym: sym, kind: scip.SymbolInformation_Method, start: owner.start, path: path}
}

// implMethod finds, in a method's descriptors, the impl it is a method of:
// `impl` then its type, its trait if any, and the method last. ok is false
// for any other symbol.
func implMethod(ds []*scip.Descriptor) (impl int, ok bool) {
	n := len(ds)
	if n < 3 || ds[n-1].GetSuffix() != scip.Descriptor_Method {
		return 0, false
	}
	impl = slices.IndexFunc(ds, func(d *scip.Descriptor) bool {
		return d.GetName() == "impl" && d.GetSuffix() == scip.Descriptor_Type
	})
	return impl, impl >= 0 && (impl+2 == n-1 || impl+3 == n-1)
}

// declareWritten adds a macro-written method's node, contained by its
// type, and registers it as its symbol's definition. It is generated code
// (ADR 004), at its type's position.
func (x *extractor) declareWritten(d *def) error {
	id, err := x.m.RustID(d.doc.rel, d.path...)
	if err != nil {
		x.skip(d.doc.abs, int(d.start.line)+1, err)
		return nil
	}
	d.id = id
	x.bySym[d.sym] = append(x.bySym[d.sym], d)
	if x.emitted[id] {
		return nil
	}
	x.emitted[id] = true
	x.report.MacroWritten++
	owner := x.owner(d.doc.rel, d.path[:len(d.path)-1])
	// A derive on a test type writes test code (#106).
	err = x.b.AddNode(graph.Node{ID: id, Kind: graph.KindMethod, Name: strings.TrimSuffix(d.path[len(d.path)-1], "()"),
		Pos: graph.Position{File: d.doc.rel, Line: int(d.start.line) + 1, Col: int(d.start.col) + 1}, Generated: true,
		Attrs: map[string]string{"written_by": "a derive or a macro: its node is where the type is declared"},
		Test:  x.isTest(d) || x.testIDs[owner]})
	if err != nil {
		return err
	}
	return x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}
