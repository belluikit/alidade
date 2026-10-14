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

// The uses oracle (#180): the type checker's Uses of every identifier in a
// function's or a method's body that names a package-level constant or
// variable of the graph, closures folded into their declaration. It is the
// same type checker the Go layer reads, over a load of its own, so it
// checks the layer's walk and identities rather than the type checker.

// uses records the package-level constants and variables a body names.
func (w *walker) uses(pkg *packages.Package, from graph.ID, body *ast.BlockStmt) {
	if from == "" {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		o := pkg.TypesInfo.Uses[id]
		if !packageValue(o) || !w.inGraph(o.Pkg().Path()) {
			return true
		}
		k := [2]graph.ID{from, w.id(o.Pkg().Path(), o.Name())}
		if k[1] != "" && !w.usesSeen[k] {
			w.usesSeen[k] = true
			w.out.Uses = append(w.out.Uses, k)
		}
		return true
	})
}

// packageValue reports whether o is a package-level constant or variable.
func packageValue(o types.Object) bool {
	switch o := o.(type) {
	case *types.Const:
	case *types.Var:
		if o.IsField() {
			return false
		}
	default:
		return false
	}
	return o.Pkg() != nil && o.Parent() == o.Pkg().Scope()
}

// A UsesResult compares the graph's references edges to constants and
// variables with the type checker's uses. Counts are [oracle, graph has].
type UsesResult struct {
	Uses           [2]int
	Missing, Extra []string
}

// CompareUses measures g's references edges from Go functions and methods
// to Go constants and variables against the oracle's uses. An extra is
// such an edge from a package the oracle loaded that it does not report.
func CompareUses(g *graph.Graph, c *Calls) *UsesResult {
	r := &UsesResult{}
	r.Uses, r.Missing = edgeRecall(g, c.Uses, graph.KindReferences)
	want := map[[2]graph.ID]bool{}
	for _, u := range c.Uses {
		want[u] = true
	}
	for e := range g.Edges() {
		if e.Kind != graph.KindReferences || want[[2]graph.ID{e.From, e.To}] || !c.LoadedPackages[packageOf(e.From)] || !valueNode(g, e.To) || !funcNode(g, e.From) {
			continue
		}
		r.Extra = append(r.Extra, string(e.From)+" -> "+string(e.To))
	}
	sortAll(&r.Extra)
	return r
}

// packageOf is the package ID of a Go declaration's ID.
func packageOf(id graph.ID) graph.ID {
	p, _, _ := strings.Cut(string(id), "#")
	return graph.ID(p)
}

func valueNode(g *graph.Graph, id graph.ID) bool {
	n, ok := g.Node(id)
	return ok && (n.Kind == graph.KindConst || n.Kind == graph.KindVar)
}

func funcNode(g *graph.Graph, id graph.ID) bool {
	n, ok := g.Node(id)
	return ok && (n.Kind == graph.KindFunc || n.Kind == graph.KindMethod)
}

func (r *UsesResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "uses of constants and variables %d / %d, %d extra\n", r.Uses[1], r.Uses[0], len(r.Extra))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing use", r.Missing}, {"extra use", r.Extra}} {
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
