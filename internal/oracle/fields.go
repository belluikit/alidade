// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/belluikit/alidade/internal/graph"
)

// The fields oracle (#193): every field of a named struct type the type
// checker declares, and the named types of the graph each field's type
// expression names as written in the source. The layer walks the field's
// types.Type instead; reading the syntax is the independent half.

// fields records one package's struct fields and their references.
func (w *walker) fields(pkg *packages.Package) {
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok || ts.Assign.IsValid() {
					continue // an alias declares no fields
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					w.structFields(pkg, ts.Name.Name, st)
				}
			}
		}
	}
}

func (w *walker) structFields(pkg *packages.Package, typeName string, st *ast.StructType) {
	for _, fl := range st.Fields.List {
		names := fl.Names
		if len(names) == 0 {
			names = []*ast.Ident{embeddedName(fl.Type)}
		}
		for _, n := range names {
			if n == nil || n.Name == "_" {
				continue
			}
			id := w.id(pkg.PkgPath, typeName+"."+n.Name)
			if id == "" {
				continue
			}
			w.out.Fields = append(w.out.Fields, id)
			for _, to := range w.namedIn(pkg, fl.Type) {
				w.out.FieldRefs = append(w.out.FieldRefs, [2]graph.ID{id, to})
			}
		}
	}
}

// embeddedName is the field name an embedded field's type gives it.
func embeddedName(e ast.Expr) *ast.Ident {
	switch t := e.(type) {
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	case *ast.Ident:
		return t
	}
	return nil
}

// namedIn lists, once each, the package-level type names of the graph an
// expression writes — an alias as itself, and the types inside a nested
// anonymous struct as the field's, which is how the layer reads them.
func (w *walker) namedIn(pkg *packages.Package, e ast.Expr) []graph.ID {
	seen := map[graph.ID]bool{}
	var out []graph.ID
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		tn, ok := pkg.TypesInfo.Uses[id].(*types.TypeName)
		if !ok || tn.Pkg() == nil {
			return true
		}
		if tn.Parent() != tn.Pkg().Scope() || !w.inGraph(tn.Pkg().Path()) {
			return true
		}
		if to := w.id(tn.Pkg().Path(), tn.Name()); to != "" && !seen[to] {
			seen[to] = true
			out = append(out, to)
		}
		return true
	})
	return out
}

// A FieldsResult compares the graph's Go field nodes and their
// references with the oracle's. Counts are [oracle, graph has].
type FieldsResult struct {
	Fields, Refs               [2]int
	MissingFields, ExtraFields []string
	MissingRefs, ExtraRefs     []string
}

// CompareFields measures g's Go field nodes and field references. An
// extra is a field node, or a reference from one, of a package the oracle
// loaded that it does not report.
func CompareFields(g *graph.Graph, c *Calls) *FieldsResult {
	r := &FieldsResult{}
	want := map[graph.ID]bool{}
	for _, f := range c.Fields {
		want[f] = true
		r.Fields[0]++
		if nodeIn(g, f) {
			r.Fields[1]++
		} else {
			r.MissingFields = append(r.MissingFields, string(f))
		}
	}
	for n := range g.Nodes() {
		if n.Kind == graph.KindField && strings.HasPrefix(string(n.ID), "go://") && c.LoadedPackages[packageOf(n.ID)] && !want[n.ID] {
			r.ExtraFields = append(r.ExtraFields, string(n.ID))
		}
	}
	r.Refs, r.MissingRefs = edgeRecall(g, c.FieldRefs, graph.KindReferences)
	wantRef := map[[2]graph.ID]bool{}
	for _, e := range c.FieldRefs {
		wantRef[e] = true
	}
	for e := range g.Edges() {
		if e.Kind == graph.KindReferences && want[e.From] && !wantRef[[2]graph.ID{e.From, e.To}] {
			r.ExtraRefs = append(r.ExtraRefs, string(e.From)+" -> "+string(e.To))
		}
	}
	sortAll(&r.MissingFields, &r.ExtraFields, &r.ExtraRefs)
	return r
}

func (r *FieldsResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "struct fields %d / %d, %d extra; field references %d / %d, %d extra\n",
		r.Fields[1], r.Fields[0], len(r.ExtraFields), r.Refs[1], r.Refs[0], len(r.ExtraRefs))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing field", r.MissingFields}, {"extra field", r.ExtraFields}, {"missing field ref", r.MissingRefs}, {"extra field ref", r.ExtraRefs}} {
		for i, s := range l.items {
			if i == 5 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-5)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
