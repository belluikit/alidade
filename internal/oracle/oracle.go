// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package oracle measures the graph against independent sources of truth
// (#148): for each layer, what an established tool says the relationships
// are, compared with what the graph holds, as recall and precision.
//
// The Go calls oracle reads every call expression of a module through the
// type checker (go/types over go/packages) and resolves what it calls. It
// shares no code with the Go layer, which builds its calls edges from
// caliper's SSA call graph; agreement between the two is evidence, and a
// disagreement is a miss to explain. The type checker sees only what a
// call expression names, so it is the truth for static calls, calls
// through an interface (to the interface method) and calls leaving the
// module, and says nothing about which function a function value holds.
package oracle

import (
	"fmt"
	"github.com/trendvidia/alidade/internal/scope"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/trendvidia/alidade/internal/adapter/golang"
	"github.com/trendvidia/alidade/internal/graph"
)

// A CallKind is how the type checker resolves a call expression.
type CallKind string

const (
	// Static is a call to a function or a concrete method of the graph.
	Static CallKind = "static"
	// Interface is a call through a named interface, to its method.
	Interface CallKind = "interface"
	// External is a call to a function outside the graph's modules, an
	// edge to its package node.
	External CallKind = "external"
)

// A Call is one relationship the type checker establishes: From calls To.
type Call struct {
	From, To graph.ID
	Kind     CallKind
	At       string // one call site, file:line, for a reader
}

// Calls is the oracle's view of one repository's Go calls.
type Calls struct {
	// Calls are the resolved relationships, one per (From, To).
	Calls []Call
	// Dynamic are the declarations holding a call through a function
	// value — a variable, a parameter, a field — whose callee the type
	// checker cannot name.
	Dynamic map[graph.ID]bool
	// Interfaces maps a declaration to the names of the interface
	// methods it calls through.
	Interfaces map[graph.ID]map[string]bool
	// Packages and Sites count what was read, the positive marker that
	// the oracle ran.
	Packages, Sites int
	// Imports are each loaded package's direct imports as `go list`
	// reports them, package to package (#148).
	Imports [][2]graph.ID
	// Modules are the module paths the walk found, the denominator the
	// three lists below are read against.
	Modules []string
	// Unloaded are the modules that gave the type checker no package — a
	// go.mod `go list` refuses, say — where there is no oracle to compare
	// with. The Go layer keeps such a module as a failed package node.
	Unloaded []string
	// Failed are the packages that do not type-check, with the first
	// error of each. There is no oracle for them, so their calls leave
	// recall's denominator: counted here, because a denominator that
	// shrinks in silence makes a recall of 1 mean nothing.
	Failed []string
	// Uses are [function or method, constant or variable] pairs: each
	// package-level constant or variable of the graph a body names (#180).
	Uses [][2]graph.ID
	// Fields are the struct fields of named types, and FieldRefs each
	// field's references to named types of the graph (#193).
	Fields    []graph.ID
	FieldRefs [][2]graph.ID
	// LoadedPackages are the package IDs the oracle read, which an extra
	// edge must come from to count as one.
	LoadedPackages map[graph.ID]bool
	// Skipped are the go.mod files the module walk refused — one that
	// declares no module path, say (#92). Their packages are read by
	// neither side, and an import path under them is taken for external.
	Skipped []graph.Skip
}

// GoCalls loads every Go module under root without tests and resolves
// each call expression. m must be the graph's minter, so IDs compare.
func GoCalls(m *graph.Minter, root string) (*Calls, error) {
	mods, skipped, err := golang.Modules(root, skipOf(root))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(mods))
	for _, mod := range mods {
		paths = append(paths, mod.Path)
	}
	w := &walker{m: m, modules: paths, out: &Calls{Dynamic: map[graph.ID]bool{}, Interfaces: map[graph.ID]map[string]bool{}, Modules: paths, Skipped: skipped, LoadedPackages: map[graph.ID]bool{}},
		seen: map[[2]graph.ID]bool{}, usesSeen: map[[2]graph.ID]bool{}}
	for _, mod := range mods {
		// Each module on its own, workspace off, as the Go layer loads
		// it (caliper sets GOWORK=off): a go.work that does not list a
		// nested module would otherwise refuse it.
		pkgs, err := packages.Load(&packages.Config{
			Dir:  mod.Dir,
			Env:  append(os.Environ(), "GOWORK=off"),
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		}, "./...")
		if err != nil {
			return nil, fmt.Errorf("oracle: load %s: %w", mod.Path, err)
		}
		if len(pkgs) == 0 {
			w.out.Unloaded = append(w.out.Unloaded, mod.Path)
		}
		for _, pkg := range pkgs {
			if pkg.TypesInfo == nil || len(pkg.Errors) > 0 {
				// A package that does not type-check is no oracle. Named,
				// not passed over in silence: its calls are out of
				// recall's denominator, and a reader must see how many.
				w.out.Failed = append(w.out.Failed, failure(pkg))
				continue
			}
			w.out.Packages++
			w.out.LoadedPackages[w.id(pkg.PkgPath, "")] = true
			w.imports(pkg)
			w.fields(pkg)
			for _, f := range pkg.Syntax {
				w.file(pkg, f)
			}
		}
	}
	sort.Slice(w.out.Calls, func(i, j int) bool {
		a, b := w.out.Calls[i], w.out.Calls[j]
		return a.From < b.From || a.From == b.From && a.To < b.To
	})
	return w.out, nil
}

type walker struct {
	m        *graph.Minter
	modules  []string
	out      *Calls
	seen     map[[2]graph.ID]bool
	usesSeen map[[2]graph.ID]bool
}

// imports records a package's direct imports, each an edge to the
// imported package's node: in the graph or outside it, a package's ID is
// its import path.
func (w *walker) imports(pkg *packages.Package) {
	from := w.id(pkg.PkgPath, "")
	for path := range pkg.Imports {
		if to := w.id(path, ""); from != "" && to != "" {
			w.out.Imports = append(w.out.Imports, [2]graph.ID{from, to})
		}
	}
}

// file walks one file's top-level declarations, attributing each call to
// the declaration that holds it, as the Go layer does: a function or
// method, its closures folded in, and a package-level variable's
// initializer to the variable.
func (w *walker) file(pkg *packages.Package, f *ast.File) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			fn, ok := pkg.TypesInfo.Defs[d.Name].(*types.Func)
			if ok && d.Body != nil {
				w.calls(pkg, w.funcID(fn), d.Body)
				w.uses(pkg, w.funcID(fn), d.Body)
			}
		case *ast.GenDecl:
			// Variables only, as the Go layer reads them: its
			// initializerSpans takes `gd.Tok != token.VAR` out
			// (internal/adapter/golang/extract.go), so a const or a type
			// spec is no declaration either side attributes a call to.
			if d.Tok != token.VAR {
				continue
			}
			for _, s := range d.Specs {
				if vs, ok := s.(*ast.ValueSpec); ok {
					w.valueSpec(pkg, vs)
				}
			}
		}
	}
}

// valueSpec attributes a variable initializer's calls: value i to name i
// when there is one value per name, every value to every name otherwise,
// and a blank name to the package.
func (w *walker) valueSpec(pkg *packages.Package, vs *ast.ValueSpec) {
	ids := make([]graph.ID, len(vs.Names))
	for i, n := range vs.Names {
		if n.Name == "_" {
			ids[i] = w.id(pkg.PkgPath, "")
		} else {
			ids[i] = w.id(pkg.PkgPath, n.Name)
		}
	}
	for i, v := range vs.Values {
		if len(vs.Values) == len(vs.Names) {
			w.calls(pkg, ids[i], v)
			continue
		}
		for _, id := range ids {
			w.calls(pkg, id, v)
		}
	}
}

// calls resolves every call expression under n as a call from from.
func (w *walker) calls(pkg *packages.Package, from graph.ID, n ast.Node) {
	if from == "" {
		return
	}
	ast.Inspect(n, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.CallExpr:
			w.out.Sites++
			w.call(pkg, from, x.Fun, x.Pos())
		case *ast.RangeStmt:
			// Ranging over a function calls it (Go 1.23): `for v :=
			// range d.iter` is a call of d.iter with no call expression.
			if tv, ok := pkg.TypesInfo.Types[x.X]; ok {
				if _, isFunc := tv.Type.Underlying().(*types.Signature); isFunc {
					w.out.Sites++
					w.call(pkg, from, x.X, x.X.Pos())
				}
			}
		}
		return true
	})
}

// call resolves one call of fun.
func (w *walker) call(pkg *packages.Package, from graph.ID, fun ast.Expr, pos token.Pos) {
	switch obj := pkg.TypesInfo.Uses[callee(fun)].(type) {
	case *types.Func:
		w.resolved(pkg, from, pos, obj.Origin())
	case *types.Var:
		w.out.Dynamic[from] = true
	case nil:
		// A call of a call's result, of a func literal, of an index
		// expression's value: a function value too, when it is not a
		// conversion.
		if tv, ok := pkg.TypesInfo.Types[fun]; ok && !tv.IsType() {
			w.out.Dynamic[from] = true
		}
	}
}

// resolved records a call the type checker names.
func (w *walker) resolved(pkg *packages.Package, from graph.ID, pos token.Pos, f *types.Func) {
	at := pkg.Fset.Position(pos)
	site := fmt.Sprintf("%s:%d", at.Filename, at.Line)
	sig := f.Type().(*types.Signature)
	if recv := sig.Recv(); recv != nil {
		if iface, ok := types.Unalias(derefType(recv.Type())).Underlying().(*types.Interface); ok && iface != nil {
			// Recorded first, error.Error included: its implementations
			// are edges the call graph gives the caller (#148).
			if w.out.Interfaces[from] == nil {
				w.out.Interfaces[from] = map[string]bool{}
			}
			w.out.Interfaces[from][f.Name()] = true
			named, ok := types.Unalias(derefType(recv.Type())).(*types.Named)
			if !ok || f.Pkg() == nil || !w.inGraph(f.Pkg().Path()) || named.Obj().Pkg() == nil || named.Obj().Parent() != named.Obj().Pkg().Scope() {
				return // an anonymous, universe or outside interface has no node (#129)
			}
			w.add(Call{From: from, To: w.id(f.Pkg().Path(), named.Origin().Obj().Name()+"."+f.Name()), Kind: Interface, At: site})
			return
		}
	}
	if f.Pkg() == nil {
		return // a function of the universe has no package node
	}
	if !w.inGraph(f.Pkg().Path()) {
		w.add(Call{From: from, To: w.id(f.Pkg().Path(), ""), Kind: External, At: site})
		return
	}
	w.add(Call{From: from, To: w.funcID(f), Kind: Static, At: site})
}

func (w *walker) add(c Call) {
	if c.To == "" {
		return
	}
	k := [2]graph.ID{c.From, c.To}
	if w.seen[k] {
		return
	}
	w.seen[k] = true
	w.out.Calls = append(w.out.Calls, c)
}

// funcID is a function's or a method's ID: the method keyed by its
// receiver's named type, generic origin and pointer looked through.
func (w *walker) funcID(f *types.Func) graph.ID {
	decl := f.Name()
	if recv := f.Type().(*types.Signature).Recv(); recv != nil {
		named, ok := types.Unalias(derefType(recv.Type())).(*types.Named)
		if !ok {
			return ""
		}
		decl = named.Origin().Obj().Name() + "." + decl
	}
	return w.id(f.Pkg().Path(), decl)
}

func (w *walker) id(pkg, decl string) graph.ID {
	id, err := w.m.GoID(pkg, decl)
	if err != nil {
		return ""
	}
	return id
}

// inGraph reports whether an import path is one of the graph's modules',
// the longest module path owning it, as the Go layer decides.
func (w *walker) inGraph(path string) bool {
	return !strings.HasSuffix(path, ".test") && golang.Owner(path, w.modules) != ""
}

// callee is the identifier a call expression's function names: f, x.f,
// pkg.f, f[T] — or nil for anything else.
func callee(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x
		case *ast.SelectorExpr:
			return x.Sel
		default:
			return nil
		}
	}
}

// failure names a package the type checker could not read, with the
// first error it gave.
func failure(pkg *packages.Package) string {
	if len(pkg.Errors) > 0 {
		return fmt.Sprintf("%s: %s", pkg.PkgPath, pkg.Errors[0])
	}
	return pkg.PkgPath + ": no type information"
}

func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// skipOf is the list of directories the build leaves out of root, so
// the oracle reads what the layers read; a configuration that does not
// read is the build's error to report, and the oracle takes the default.
func skipOf(root string) *scope.Dirs {
	d, err := scope.Load(root)
	if err != nil {
		return scope.New(scope.Default)
	}
	return d
}
