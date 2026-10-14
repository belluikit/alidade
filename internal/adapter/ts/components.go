// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	tsgo "github.com/microsoft/typescript-go/shim"

	"github.com/trendvidia/alidade/internal/adapter/ts/svelte"
	"github.com/trendvidia/alidade/internal/graph"
)

// Svelte components reach the type checker as svelte2tsx translates them
// (#221, the owner's decision; ADR 007 amended): the TypeScript it writes
// for a component — the instance script inside a generated $$render
// function, the markup as code below it — is read beside the component as
// Card.svelte.tsx, through an overlay of the file system, so an import of
// ./Card.svelte resolves to it as TypeScript resolves an import's
// extensions. A component joins the program of the deepest tsconfig
// whose directory holds it, under that project's options and aliases.
//
// What is a node in a translation: its module-level declarations, as in
// any file, and the statements of $$render's body, the instance script's
// declarations — never the names svelte2tsx makes ($$render, $$_…,
// __sveltets_…, X__SvelteComponent_). The component's default export is
// the component's own file node, so a component rendered in markup,
// which svelte2tsx writes as new of a constant made by
// __sveltets_2_ensureComponent(Card), is a calls edge to the Card
// component. The calls svelte2tsx writes for itself are no call sites.
// Positions are mapped back through the translation's source map.

// A component is one translated Svelte component.
type component struct {
	src *source
	out *svelte.Output
	// path is where the checker reads the translation: the component's
	// path with .tsx appended.
	path   string
	placed bool
}

// shimDir is where the translations' type shims are read, inside no
// directory of the tree.
const shimDir = "__alidade_svelte_shims__"

// translate translates every component, and returns the files the
// overlay serves: each translation, and the shims they refer to. A build
// without V8, or a component svelte2tsx refuses, leaves the component
// unchecked and saying so.
func (p *checkPass) translate(files []*source) map[string]string {
	virtual := map[string]string{}
	for _, s := range files {
		if !strings.HasSuffix(s.rel, ".svelte") {
			continue
		}
		out, err := p.convert(s)
		if err != nil {
			p.x.report.Unchecked++
			p.unavailable(s, err)
			continue
		}
		c := &component{src: s, out: out, path: filepath.ToSlash(s.abs) + ".tsx"}
		p.comps[c.path] = c
		virtual[c.path] = out.Code
	}
	if len(p.comps) > 0 {
		for name, body := range svelte.Shims {
			virtual[p.shim(name)] = body
		}
	}
	return virtual
}

func (p *checkPass) convert(s *source) (*svelte.Output, error) {
	if !svelte.Available() {
		return nil, svelte.ErrUnavailable
	}
	src, err := os.ReadFile(s.abs)
	if err != nil {
		return nil, err
	}
	return svelte.Convert(string(src), s.abs)
}

// unavailable marks a component whose calls are not in the graph, and why.
func (p *checkPass) unavailable(s *source, err error) {
	id, ierr := p.x.m.TSID(s.rel, "")
	if ierr != nil {
		return
	}
	reason := "the type checker does not read it: " + err.Error()
	if err != svelte.ErrUnavailable {
		p.x.skip(s.abs, fmt.Errorf("svelte2tsx: %w", err))
	}
	_ = p.x.b.AddNode(graph.Node{ID: id, Kind: graph.KindComponent, Name: filepath.Base(s.rel),
		Pos: graph.Position{File: s.rel}, Attrs: map[string]string{"calls_unavailable": reason}})
}

func (p *checkPass) shim(name string) string {
	return filepath.ToSlash(filepath.Join(p.x.root, shimDir, name))
}

// componentsUnder takes the translations of the components under dir no
// program has taken, with the shims, as further root files.
func (p *checkPass) componentsUnder(dir string) []string {
	var out []string
	for _, c := range p.sortedComponents() {
		if !c.placed && inside(dir, c.src.abs) {
			c.placed = true
			out = append(out, c.path)
		}
	}
	if len(out) > 0 {
		out = append(out, p.shimFiles()...)
	}
	return out
}

func (p *checkPass) sortedComponents() []*component {
	out := make([]*component, 0, len(p.comps))
	for _, c := range p.comps {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *component) int { return strings.Compare(a.path, b.path) })
	return out
}

func (p *checkPass) shimFiles() []string {
	var out []string
	for name := range svelte.Shims {
		out = append(out, p.shim(name))
	}
	slices.Sort(out)
	return out
}

func inside(dir, file string) bool {
	rel, err := filepath.Rel(dir, file)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// synthesized reports whether a name is one svelte2tsx makes.
func synthesized(name string) bool {
	return name == "$$render" || strings.HasPrefix(name, "$$") || strings.HasPrefix(name, "__sveltets") ||
		strings.HasSuffix(name, "__SvelteComponent_")
}

// componentDeclPath is declPath for a translation: its own module-level
// declarations, and the instance script's, the statements of $$render's
// body, with svelte2tsx's names left out.
func componentDeclPath(n *tsgo.Node) []string {
	if path := declPath(n); path != nil {
		if synthesized(path[0]) {
			return nil
		}
		return path
	}
	name := renderLevelName(n)
	if name == "" || synthesized(name) {
		return nil
	}
	return []string{name}
}

// renderLevelName is the name of a declaration made directly in
// $$render's body: a function, class, interface, type alias or enum, or a
// variable of a variable statement.
func renderLevelName(n *tsgo.Node) string {
	stmt := n
	switch n.Kind {
	case tsgo.KindFunctionDeclaration, tsgo.KindClassDeclaration, tsgo.KindInterfaceDeclaration,
		tsgo.KindTypeAliasDeclaration, tsgo.KindEnumDeclaration:
	case tsgo.KindVariableDeclaration:
		if n.Parent == nil || n.Parent.Parent == nil || n.Parent.Parent.Kind != tsgo.KindVariableStatement {
			return ""
		}
		stmt = n.Parent.Parent
	default:
		return ""
	}
	if !inRender(stmt) {
		return ""
	}
	return identifierName(n.Name())
}

// inRender reports whether a statement is directly in the body of the
// module-level function $$render.
func inRender(stmt *tsgo.Node) bool {
	body := stmt.Parent
	if body == nil || body.Kind != tsgo.KindBlock {
		return false
	}
	fn := body.Parent
	return fn != nil && fn.Kind == tsgo.KindFunctionDeclaration && atModuleLevel(fn) && identifierName(fn.Name()) == "$$render"
}

// isComponentExport reports whether a declaration is the component class
// svelte2tsx exports by default: const Card__SvelteComponent_.
func isComponentExport(d *tsgo.Node) bool {
	if d.Kind != tsgo.KindVariableDeclaration || d.Parent == nil || d.Parent.Parent == nil {
		return false
	}
	return atModuleLevel(d.Parent.Parent) && strings.HasSuffix(identifierName(d.Name()), "__SvelteComponent_")
}

// rendered is, for a symbol declared as svelte2tsx's stand-in for a
// component used in markup — const $$_draC0C =
// __sveltets_2_ensureComponent(Card) — the expression naming the
// component; nil for any other symbol.
func rendered(sym *tsgo.Symbol) *tsgo.Node {
	if sym == nil || len(sym.Declarations) == 0 {
		return nil
	}
	d := sym.Declarations[0]
	if d.Kind != tsgo.KindVariableDeclaration || !strings.HasPrefix(identifierName(d.Name()), "$$_") {
		return nil
	}
	init := d.Initializer()
	if init == nil || init.Kind != tsgo.KindCallExpression || len(init.Arguments()) != 1 {
		return nil
	}
	if e := init.Expression(); e == nil || e.Kind != tsgo.KindIdentifier || e.Text() != "__sveltets_2_ensureComponent" {
		return nil
	}
	return init.Arguments()[0]
}

// synthesizedCall reports whether a call in a translation is one
// svelte2tsx writes for itself: its helpers, $$render, and the elements
// of markup (svelteHTML.createElement).
func synthesizedCall(target *tsgo.Node) bool {
	switch target.Kind {
	case tsgo.KindIdentifier:
		t := target.Text()
		return strings.HasPrefix(t, "__sveltets") || t == "$$render"
	case tsgo.KindPropertyAccessExpression:
		e := target.Expression()
		return e != nil && e.Kind == tsgo.KindIdentifier && e.Text() == "svelteHTML"
	}
	return false
}

// original maps a position of a translation back to the component: its
// 1-based line and byte column, or false where svelte2tsx wrote the code.
func (c *component) original(sf *tsgo.SourceFile, pos int) (int, int, bool) {
	line, b := tsgo.LineAndByte(sf, pos)
	genLine := lineText(c.out.Code, line)
	ol, oc, ok := c.out.Original(line, utf16Col(genLine, b))
	if !ok {
		return 0, 0, false
	}
	src, err := os.ReadFile(c.src.abs)
	if err != nil {
		return ol + 1, oc + 1, true
	}
	return ol + 1, byteCol(lineText(string(src), ol), oc) + 1, true
}

// lineText is the 0-based line n of s.
func lineText(s string, n int) string {
	for i := 0; i < n; i++ {
		j := strings.IndexByte(s, '\n')
		if j < 0 {
			return ""
		}
		s = s[j+1:]
	}
	if j := strings.IndexByte(s, '\n'); j >= 0 {
		return s[:j]
	}
	return s
}

// utf16Col is the UTF-16 column of a byte offset in a line: a source
// map's columns are JavaScript's.
func utf16Col(line string, b int) int {
	n := 0
	for i, r := range line {
		if i >= b {
			break
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// byteCol is the byte offset of a UTF-16 column in a line.
func byteCol(line string, u int) int {
	n := 0
	for i, r := range line {
		if n >= u {
			return i
		}
		if r == utf8.RuneError {
			n++
			continue
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return len(line)
}
