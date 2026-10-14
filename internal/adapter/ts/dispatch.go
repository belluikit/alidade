// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"slices"

	tsgo "github.com/microsoft/typescript-go/shim"

	"github.com/trendvidia/alidade/internal/graph"
)

// Dispatch through interfaces (#229, the owner's decision; ADR 007
// amended): a call the checker resolves to an interface's member —
// services.workspace.open() to WorkspaceService.open — reaches every
// class whose implements clause names that interface. Declared, not
// structural: a class is an interface's implementation when it says so,
// as TypeScript itself checks it, and an object literal that merely has
// the same members is not one (pxfed has 146 interfaces, most of them
// data shapes).
//
//   - implements (RESOLVED): class → each interface its implements clause
//     names, and each interface those extend, as the checker resolves the
//     names.
//   - calls (RESOLVED, via=interface): from the declaration holding a call
//     to an interface member, to each implementing class's member of that
//     name — its own, or one it inherits — beside the edge to the
//     interface member itself, which stays. The interface's members
//     include those it inherits, so a call to a base interface's member
//     reaches a class that implements an interface extending it.
//
// Classes and calls are met in any order across the programs, so the
// pass records both and emits the dispatch edges once every program is
// walked.
type dispatch struct {
	// impls maps an interface member's node to its implementations'.
	impls map[graph.ID][]graph.ID
	// calls are the calls resolved to an interface member, by caller.
	calls []ifaceCall
}

type ifaceCall struct{ from, member graph.ID }

// implements records a class's implementations and emits its implements
// edges: to the class its extends clause names (#249, #240 decision 2),
// and to the interfaces of its implements clause.
func (w *fileWalk) implements(class *tsgo.Node, classID graph.ID) error {
	if err := w.extends(class, classID); err != nil {
		return err
	}
	clause := tsgo.ImplementsTypes(class)
	if len(clause) == 0 {
		return nil
	}
	sym := w.c.GetSymbolAtLocation(class.Name())
	if sym == nil {
		return nil
	}
	instance := w.c.GetDeclaredTypeOfSymbol(sym)
	seen := map[*tsgo.Symbol]bool{}
	for _, t := range clause {
		iface := w.resolve(t.Expression())
		if iface == nil {
			continue
		}
		if err := w.implementsInterface(classID, instance, w.c.GetDeclaredTypeOfSymbol(iface), seen); err != nil {
			return err
		}
	}
	return nil
}

// extends emits the implements edge from a class to the class it
// extends, as the checker resolves the name: its direct base only, whose
// own edge reaches the next. A base outside the tree's sources (Error),
// or a value the checker names no class by (a mixin's call), has none.
func (w *fileWalk) extends(class *tsgo.Node, classID graph.ID) error {
	base := tsgo.ExtendsType(class)
	if base == nil {
		return nil
	}
	sym := w.resolve(base.Expression())
	if sym == nil || sym.Flags&tsgo.SymbolFlagsClass == 0 {
		return nil
	}
	to, where := w.p.targetOf(sym.Declarations)
	if where != targetNode || to == classID {
		return nil
	}
	w.p.x.report.Extends++
	return w.p.x.b.AddEdge(graph.Edge{From: classID, To: to, Kind: graph.KindImplements, Derivation: graph.Resolved})
}

// implementsInterface emits the implements edge to one interface and its
// bases, and maps each member to the class's.
func (w *fileWalk) implementsInterface(classID graph.ID, instance, iface *tsgo.Type, seen map[*tsgo.Symbol]bool) error {
	isym := iface.Symbol()
	if isym == nil || seen[isym] {
		return nil
	}
	seen[isym] = true
	if to, where := w.p.targetOf(isym.Declarations); where == targetNode {
		w.p.x.report.Implements++
		if err := w.p.x.b.AddEdge(graph.Edge{From: classID, To: to, Kind: graph.KindImplements, Derivation: graph.Resolved}); err != nil {
			return err
		}
	}
	for _, m := range w.c.GetPropertiesOfType(iface) {
		w.mapMember(m, instance)
	}
	for _, base := range w.c.GetBaseTypes(iface) {
		if err := w.implementsInterface(classID, instance, base, seen); err != nil {
			return err
		}
	}
	return nil
}

// mapMember maps one interface member to the class's member of its name,
// its own or one it inherits.
func (w *fileWalk) mapMember(m *tsgo.Symbol, instance *tsgo.Type) {
	member, where := w.p.targetOf(m.Declarations)
	if where != targetNode {
		return
	}
	own := w.c.GetPropertyOfType(instance, m.Name)
	if own == nil {
		return
	}
	impl, where := w.p.targetOf(own.Declarations)
	if where != targetNode || impl == member || slices.Contains(w.p.dispatch.impls[member], impl) {
		return
	}
	w.p.dispatch.impls[member] = append(w.p.dispatch.impls[member], impl)
}

// interfaceMember reports whether a declaration is a member of an
// interface, or of a type literal a type alias names.
func interfaceMember(d *tsgo.Node) bool {
	holder := d.Parent
	return holder != nil && (holder.Kind == tsgo.KindInterfaceDeclaration || holder.Kind == tsgo.KindTypeLiteral)
}

// emitDispatch adds, for every call to an interface member, the edges to
// its implementations.
func (p *checkPass) emitDispatch() error {
	for _, c := range p.dispatch.calls {
		for _, impl := range p.dispatch.impls[c.member] {
			key := [2]graph.ID{c.from, impl}
			if p.called[key] {
				continue // the caller calls the implementation itself too
			}
			p.called[key] = true
			p.x.report.Dispatch++
			if err := p.x.b.AddEdge(graph.Edge{From: c.from, To: impl, Kind: graph.KindCalls, Derivation: graph.Resolved,
				Attrs: map[string]string{"via": "interface"}}); err != nil {
				return err
			}
		}
	}
	return nil
}
