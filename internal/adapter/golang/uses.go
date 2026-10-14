// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/packages"

	"github.com/belluikit/alidade/internal/graph"
)

// uses emits a references edge (RESOLVED) from each function and method of
// one package to every package-level constant and variable of the graph it
// names: "what reads caliperBinaryEnv" (#180, the owner's decision, option
// B). The type checker's Uses decides what an identifier is, so a local
// of the same name, a field, or a name in a comment is not a use. A
// closure's uses are its enclosing declaration's, as its calls are. Uses
// of a dependency's constants and variables are left out: the
// declaration has no node, and an edge to its package would only repeat
// that the function imports it. Uses in a package-level initializer
// (var x = y) are left out too: the decision covers functions.
func (x *extractor) uses(pkg *packages.Package) error {
	if pkg.TypesInfo == nil {
		return nil
	}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || x.outside(fd.Pos()) {
				continue
			}
			fn, ok := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			if err := x.usesFrom(pkg.TypesInfo, fn, fd.Body); err != nil {
				return err
			}
		}
	}
	return nil
}

// usesFrom emits the references edges of one function's body.
func (x *extractor) usesFrom(info *types.Info, fn *types.Func, body *ast.BlockStmt) error {
	from, err := x.funcID(fn)
	if err != nil {
		return err
	}
	if id, ok := x.testInit(fn); ok {
		// An in-package test file's init is the test files' node (#128).
		from = id
	}
	seen := map[types.Object]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || err != nil {
			return err == nil
		}
		obj := info.Uses[id]
		if obj == nil || seen[obj] || !x.packageValue(obj) {
			return true
		}
		seen[obj] = true
		var to graph.ID
		if to, err = x.m.GoID(obj.Pkg().Path(), obj.Name()); err != nil {
			return false
		}
		x.report.moduleEdge(x.module, owner(obj.Pkg().Path(), x.modules), graph.KindReferences, x.testFile(fn.Pos()) || x.testFile(obj.Pos()))
		err = x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindReferences, Derivation: graph.Resolved})
		return true
	})
	return err
}

// packageValue reports whether obj is a package-level constant or
// variable with a node in the graph.
func (x *extractor) packageValue(obj types.Object) bool {
	switch o := obj.(type) {
	case *types.Const:
	case *types.Var:
		if o.IsField() {
			return false
		}
	default:
		return false
	}
	p := obj.Pkg()
	return p != nil && obj.Parent() == p.Scope() && x.inGraph(p.Path()) && !x.outside(obj.Pos())
}
