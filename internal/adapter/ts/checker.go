// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tsgo "github.com/microsoft/typescript-go/shim"

	"github.com/trendvidia/alidade/internal/graph"
)

// The type-checked half of the layer (#215, ADR 007 amended): TypeScript
// 7's own compiler, typescript-go, run in process over each tsconfig
// project of the tree. It adds what the import scan cannot see:
//
//   - a node per declaration of a hand-written file — its top-level
//     functions, classes, interfaces, type aliases, enums and variables,
//     and one level of members: a class's or interface's, a type
//     literal's, and the function-valued properties of an object literal
//     a top-level variable holds — keyed by file and declaration path
//     (ts://app/src/api.ts#Client.fetch), with contains (EXTRACTED) from
//     the file or the declaration holding it;
//   - calls (RESOLVED): from the innermost declaration holding a call, a
//     new or a JSX element — the file when it is at module level — to
//     the declaration the checker resolves the callee to, through
//     imports and re-exports.
//
// A declaration's ID is derived from its place in the source, never from
// a table, so each project is checked, walked and released alone, and a
// call into a file another project walks lands on the ID that walk mints.
// A file two projects include is walked by the one whose tsconfig is
// deepest, the one its editor would use; a file none includes, by one
// project inferred for the leftovers. Svelte components are read as
// svelte2tsx translates them (components.go).
type checkPass struct {
	x *extractor
	// own maps the files the layer read, by the path the compiler names
	// them with, to their source; done marks those already walked.
	own  map[string]*source
	done map[string]bool
	// called records the calls edges emitted, so a callee called twice
	// from one declaration is one edge.
	called map[[2]graph.ID]bool
	// emitted records the declaration nodes emitted: overloads, merged
	// declarations and an accessor pair share an ID and one node.
	emitted map[graph.ID]bool
	// comps maps each translated component by the path its translation
	// is read at.
	comps map[string]*component
	// dispatch collects interface implementations and calls (#229).
	dispatch dispatch
}

// check runs the type checker over every project of the tree.
func (x *extractor) check(d *discovery, files []*source) error {
	p := &checkPass{x: x, own: map[string]*source{}, done: map[string]bool{},
		called: map[[2]graph.ID]bool{}, emitted: map[graph.ID]bool{}, comps: map[string]*component{},
		dispatch: dispatch{impls: map[graph.ID][]graph.ID{}}}
	for _, s := range files {
		if !strings.HasSuffix(s.rel, ".svelte") {
			p.own[filepath.ToSlash(s.abs)] = s
		}
	}
	virtual := p.translate(files)
	for f, body := range x.svelteKitConfigs(d) {
		virtual[f] = body
	}
	if len(p.own) == 0 && len(p.comps) == 0 {
		return nil
	}
	fs := tsgo.FileSystem()
	if len(virtual) > 0 {
		fs = tsgo.Overlay(fs, virtual)
	}
	seen := map[string]bool{}
	for _, cfg := range deepestFirst(d.configs) {
		if err := p.project(filepath.ToSlash(cfg), fs, seen); err != nil {
			return err
		}
	}
	if err := p.leftovers(fs); err != nil {
		return err
	}
	return p.emitDispatch()
}

// project checks one tsconfig's program. A solution config — no files of
// its own, only references, as Vite and electron-vite write one over
// tsconfig.app.json or tsconfig.web.json — stands for the projects it
// references, which are checked in its place: a file they include is
// checked under the options its editor uses.
func (p *checkPass) project(cfg string, fs tsgo.FS, seen map[string]bool) error {
	if seen[cfg] {
		return nil
	}
	seen[cfg] = true
	host := tsgo.NewHost(filepath.Dir(cfg), fs)
	parsed, diags := tsgo.ParseConfig(cfg, host)
	if parsed == nil || len(diags) > 0 {
		p.x.report.ConfigErrors++
		return nil
	}
	if len(tsgo.ConfigFiles(parsed)) == 0 {
		for _, ref := range parsed.ResolvedProjectReferencePaths() {
			if err := p.project(ref, fs, seen); err != nil {
				return err
			}
		}
		return nil
	}
	tsgo.AddFiles(parsed, p.componentsUnder(filepath.Dir(cfg)))
	return p.program(tsgo.NewProgram(parsed, host))
}

// deepestFirst orders tsconfig files so that a nested project comes
// before the one above it.
func deepestFirst(configs []string) []string {
	out := slices.Clone(configs)
	slices.SortStableFunc(out, func(a, b string) int {
		return strings.Count(filepath.ToSlash(b), "/") - strings.Count(filepath.ToSlash(a), "/")
	})
	return out
}

// program walks the files of one project no project walked before it.
func (p *checkPass) program(prog *tsgo.Program) error {
	if prog == nil {
		return nil
	}
	p.x.report.Programs++
	ctx := context.Background()
	for _, sf := range prog.GetSourceFiles() {
		if c := p.comps[sf.FileName()]; c != nil && !p.done[sf.FileName()] {
			p.done[sf.FileName()] = true
			c2, release := prog.GetTypeCheckerForFile(ctx, sf)
			err := p.walk(c2, sf, c.src, c)
			release()
			if err != nil {
				return err
			}
			continue
		}
		s := p.own[sf.FileName()]
		if s == nil || p.done[sf.FileName()] {
			continue
		}
		p.done[sf.FileName()] = true
		if s.gen != nil {
			continue // its exports are nodes already, read from its export lines
		}
		c, release := prog.GetTypeCheckerForFile(ctx, sf)
		err := p.walk(c, sf, s, nil)
		release()
		if err != nil {
			return err
		}
	}
	return nil
}

// A fileWalk is one source file's walk.
type fileWalk struct {
	p   *checkPass
	c   *tsgo.Checker
	sf  *tsgo.SourceFile
	rel string
	// comp is the component a translation is of; nil for a script file.
	comp *component
	err  error
}

// walk emits one file's declarations and calls, a script file's or a
// component's translation.
func (p *checkPass) walk(c *tsgo.Checker, sf *tsgo.SourceFile, s *source, comp *component) error {
	id, err := p.x.m.TSID(s.rel, "")
	if err != nil {
		return nil // the file is skipped, and recorded, by read
	}
	p.x.report.Checked++
	if comp != nil {
		p.x.report.Translated++
	}
	w := &fileWalk{p: p, c: c, sf: sf, rel: s.rel, comp: comp}
	sf.AsNode().ForEachChild(func(n *tsgo.Node) bool { return w.visit(n, id) })
	return w.err
}

// visit walks a node with the declaration holding it, and stops the walk
// at the first error.
func (w *fileWalk) visit(n *tsgo.Node, owner graph.ID) bool {
	if path := w.declPath(n); path != nil {
		id, err := w.p.x.m.TSDeclID(w.rel, path...)
		if err != nil {
			w.p.x.report.Unnamed++
		} else {
			w.err = w.declare(n, id, path, owner)
			if w.err == nil && n.Kind == tsgo.KindClassDeclaration {
				w.err = w.implements(n, id)
			}
			owner = id
		}
	}
	if w.err == nil {
		if target := callee(n); target != nil {
			w.err = w.call(owner, target)
		}
	}
	if w.err != nil {
		return true
	}
	n.ForEachChild(func(c *tsgo.Node) bool { return w.visit(c, owner) })
	return w.err != nil
}

// declare emits a declaration's node, once, and the contains edge from
// the file or declaration holding it.
func (w *fileWalk) declare(n *tsgo.Node, id graph.ID, path []string, owner graph.ID) error {
	if w.p.emitted[id] {
		return nil
	}
	w.p.emitted[id] = true
	w.p.x.report.Declarations++
	at := n
	if name := n.Name(); name != nil {
		at = name
	}
	pos, end := w.span(tsgo.SkipTrivia(w.sf.Text(), at.Pos()), n.End())
	if err := w.p.x.b.AddNode(graph.Node{ID: id, Kind: declKind(n), Name: path[len(path)-1],
		Pos: pos, Attrs: map[string]string{"end_line": strconv.Itoa(end)}}); err != nil {
		return err
	}
	return w.p.x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// call emits the edge from the declaration holding a call to the
// declaration the checker resolves its callee to, or counts why there
// is none.
func (w *fileWalk) call(owner graph.ID, target *tsgo.Node) error {
	if w.comp != nil && synthesizedCall(target) {
		return nil
	}
	w.p.x.report.CallSites++
	sym := w.resolve(target)
	if e := rendered(sym); e != nil {
		sym = w.resolve(e) // a component used in markup: the component
	}
	if sym == nil || len(sym.Declarations) == 0 {
		w.p.x.report.CallsUnresolved++
		return nil
	}
	to, where := w.p.targetOf(sym.Declarations)
	if where == targetNode && interfaceMember(sym.Declarations[0]) {
		w.p.dispatch.calls = append(w.p.dispatch.calls, ifaceCall{from: owner, member: to})
	}
	switch where {
	case targetExternal:
		w.p.x.report.CallsExternal++
		return nil
	case targetUnplaced:
		w.p.x.report.CallsUnplaced++
		return nil
	}
	if key := [2]graph.ID{owner, to}; !w.p.called[key] {
		w.p.called[key] = true
		w.p.x.report.Calls++
		return w.p.x.b.AddEdge(graph.Edge{From: owner, To: to, Kind: graph.KindCalls, Derivation: graph.Resolved})
	}
	return nil
}

type targetPlace int

const (
	targetNode     targetPlace = iota // a declaration with a node
	targetExternal                    // outside the tree's sources: a package, a lib, a .d.ts
	targetUnplaced                    // in the tree, but no node: a local function, a parameter
)

// resolve is the symbol an expression names, through import aliases.
func (w *fileWalk) resolve(e *tsgo.Node) *tsgo.Symbol {
	sym := w.c.GetSymbolAtLocation(e)
	if sym != nil && sym.Flags&tsgo.SymbolFlagsAlias != 0 {
		sym = w.c.GetAliasedSymbol(sym)
	}
	return sym
}

func (w *fileWalk) declPath(n *tsgo.Node) []string {
	if w.comp != nil {
		return fullPath(n, componentDeclPath)
	}
	return fullPath(n, declPath)
}

// span is a declaration's position and last line: in a translation,
// mapped back to the component, the last line its start's when its end
// is code svelte2tsx wrote.
func (w *fileWalk) span(start, end int) (graph.Position, int) {
	if w.comp == nil {
		line, col := tsgo.LineAndByte(w.sf, start)
		last, _ := tsgo.LineAndByte(w.sf, end)
		return graph.Position{File: w.sf.FileName(), Line: line + 1, Col: col + 1}, last + 1
	}
	line, col, ok := w.comp.original(w.sf, start)
	if !ok {
		return graph.Position{File: w.comp.src.abs}, 0
	}
	last, _, ok := w.comp.original(w.sf, end)
	if !ok || last < line {
		last = line
	}
	return graph.Position{File: w.comp.src.abs, Line: line, Col: col}, last
}

// targetOf is the node of the first of a symbol's declarations that has
// one, or where the declarations are when none has.
func (p *checkPass) targetOf(decls []*tsgo.Node) (graph.ID, targetPlace) {
	place := targetExternal
	for _, d := range decls {
		sf := tsgo.GetSourceFileOfNode(d)
		if c := p.comps[sf.FileName()]; c != nil {
			place = targetUnplaced
			if id, ok := p.componentTarget(c, d); ok {
				return id, targetNode
			}
			continue
		}
		s := p.own[sf.FileName()]
		if s == nil {
			continue
		}
		place = targetUnplaced
		path := fullPath(d, declPath)
		if path == nil || s.gen != nil && len(path) > 1 {
			continue // a generated file's exports are nodes, its members are not
		}
		if id, err := p.x.m.TSDeclID(s.rel, path...); err == nil {
			return id, targetNode
		}
	}
	return "", place
}

// callee is the expression a call, a new or a JSX element names, or nil
// for any other node. The checker resolves a property access, a.b.c, to
// the member it names.
func callee(n *tsgo.Node) *tsgo.Node {
	switch n.Kind {
	case tsgo.KindCallExpression, tsgo.KindNewExpression:
		return n.Expression()
	case tsgo.KindJsxOpeningElement, tsgo.KindJsxSelfClosingElement:
		return n.TagName()
	}
	return nil
}

// componentTarget is the node a declaration of a translation is: the
// component itself for its default export, else the declaration's.
func (p *checkPass) componentTarget(c *component, d *tsgo.Node) (graph.ID, bool) {
	if isComponentExport(d) {
		id, err := p.x.m.TSID(c.src.rel, "")
		return id, err == nil
	}
	path := fullPath(d, componentDeclPath)
	if path == nil {
		return "", false
	}
	id, err := p.x.m.TSDeclID(c.src.rel, path...)
	return id, err == nil
}

// leftovers checks the files no project took, and the components no
// project's directory holds, in one inferred project.
func (p *checkPass) leftovers(fs tsgo.FS) error {
	var left []string
	for f, s := range p.own {
		if !p.done[f] && s.gen == nil {
			left = append(left, f)
		}
	}
	slices.Sort(left)
	p.x.report.Inferred = len(left)
	left = append(left, p.componentsUnder(p.x.root)...)
	if len(left) == 0 {
		return nil
	}
	return p.program(tsgo.NewProgram(tsgo.InferredConfig(left), tsgo.NewHost(p.x.root, fs)))
}
