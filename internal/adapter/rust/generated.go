// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// A site is where the workspace includes a file Cargo's build wrote: the
// document and module holding the include!, and the file.
type site struct {
	dc   *doc
	at   pos      // the include!, where the file's items are placed
	lit  pos      // inside the literal naming the file, asked for its definition
	path []string // the module's path in the document; empty at its top level
	mod  []*scip.Descriptor
	pkg  string // the Cargo package, as the index names it
	out  string // the included file, absolute, once the server has said it
	// hand is the included file, repo-relative, when it is a hand-written
	// one of the repository rather than Cargo's output (#84).
	hand string
	// items are the nodes of the file's items, by ID.
	items map[graph.ID]*def
}

// anchor is the file and the path in it under which the site's items are
// identified (ADR 002 rule 16): the include!'s, for code Cargo generated;
// the included file's own top level, for a hand-written one (#84).
func (s *site) anchor() (string, []string) {
	if s.hand != "" {
		return s.hand, nil
	}
	return s.dc.rel, s.path
}

// generated adds a node for every item of the code Cargo's build writes
// and the workspace includes (prost-build's output, a build script's), at
// the include! (ADR 002 rule 16; every item, the owner's decision on
// #72): `rust://prost-reflect-tests/src/lib.rs#proto.Scalars`. The index
// has no document for such a file, only symbols for what workspace code
// names; rust-analyzer's server says which file an include! reads (its
// definition of the path) and what the file declares (its outline). The
// index's symbols are then registered as these nodes' definitions, so
// calls into generated code are edges. A struct or enum prost-build wrote
// is joined to its .proto message (protoLink).
func (x *extractor) generated() error {
	var all []*site
	pkgs := x.packages()
	for _, ws := range x.indexed {
		sites, refs := x.includes(ws), x.firstReferences(ws, pkgs)
		if len(sites) == 0 && len(refs) == 0 {
			continue
		}
		l := x.server(ws)
		if l == nil {
			continue
		}
		for _, s := range sites {
			if err := x.place(l, s); err != nil {
				return err
			}
			all = append(all, s)
		}
		var err error
		if all, err = x.throughSymbols(l, ws, all, refs); err != nil {
			return err
		}
	}
	x.registerGenerated(all)
	return x.addIncluded()
}

// throughSymbols finds the generated files the workspace includes through
// a macro of its own or a dependency's (tonic's include_proto!), whose
// include! the index does not show: for each symbol the index lists as
// generated and no placed file declares, the server's definition of a
// reference to it names its file, placed under the innermost workspace
// module the symbol is inside. Only a file whose items the workspace names
// is found this way.
func (x *extractor) throughSymbols(l *lsp, ws string, all []*site, refs map[string]*reference) ([]*site, error) {
	mods := x.modules(ws)
	asked := map[string]bool{}
	for _, sym := range slices.Sorted(maps.Keys(x.external)) {
		s, err := scip.ParseSymbol(sym)
		if err != nil || s.GetPackage() == nil || refs[sym] == nil || declaredBy(all, x.m, sym, s) {
			continue
		}
		at := moduleOf(mods, s)
		if at == nil {
			continue
		}
		out := x.definedIn(l, refs[sym])
		if out == "" || asked[at.sym+"\x00"+out] || placedIn(all, out, at.sym) {
			continue
		}
		asked[at.sym+"\x00"+out] = true
		st := &site{dc: at.doc, at: at.start, path: slices.Clone(at.path), mod: descriptors(at.sym), pkg: s.GetPackage().GetName(),
			items: map[graph.ID]*def{}}
		if err := x.placeFile(l, st, out); err != nil {
			return nil, err
		}
		if st.out != "" {
			all = append(all, st)
		}
	}
	return all, nil
}

// definedIn is the file the server says the item named at ref is defined
// in; "" when it names no one file.
func (x *extractor) definedIn(l *lsp, ref *reference) string {
	locs, err := l.definition(ref.dc, ref.at, x.text)
	if err != nil || len(locs) != 1 {
		return ""
	}
	out, _ := locs[0].at()
	return out
}

// placedIn says whether a site of the module whose symbol is mod already
// reads the file out: a symbol its outline has no item for, a method a
// derive writes (ConfigMetadata[Clone].clone() on jj), leads back to it.
func placedIn(sites []*site, out, mod string) bool {
	ds := descriptors(mod)
	for _, st := range sites {
		if st.out == out && len(st.mod) == len(ds) && hasPrefix(st.mod, ds) {
			return true
		}
	}
	return false
}

// A reference is where a document names a symbol.
type reference struct {
	dc *doc
	at pos
}

// firstReferences are, for each symbol of a workspace package that the
// index lists without a definition (what Cargo's build generated) and the
// workspace's documents name, the first place they do.
func (x *extractor) firstReferences(ws string, pkgs map[string]bool) map[string]*reference {
	out := map[string]*reference{}
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		dc := x.docs[rel]
		if dc.ws != ws {
			continue
		}
		for _, o := range dc.d.GetOccurrences() {
			sym, start, _, ok := referenceAt(o)
			if !ok || !x.external[sym] || out[sym] != nil {
				continue
			}
			if s, err := scip.ParseSymbol(sym); err == nil && s.GetPackage() != nil && pkgs[s.GetPackage().GetName()] {
				out[sym] = &reference{dc: dc, at: start}
			}
		}
	}
	return out
}

// modules are the workspace's inline and file modules with a node.
func (x *extractor) modules(ws string) []*def {
	var out []*def
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		dc := x.docs[rel]
		if dc.ws != ws {
			continue
		}
		for _, d := range x.defsOf[dc] {
			if d.kind == scip.SymbolInformation_Module && d.encl {
				out = append(out, d)
			}
		}
	}
	return out
}

// moduleOf is the innermost of mods a symbol is inside, by its
// descriptors and package; nil when none.
func moduleOf(mods []*def, s *scip.Symbol) *def {
	var best *def
	bestLen := 0
	for _, m := range mods {
		ms, err := scip.ParseSymbol(m.sym)
		if err != nil || ms.GetPackage().GetName() != s.GetPackage().GetName() {
			continue
		}
		md := ms.GetDescriptors()
		if len(md) > bestLen && len(md) < len(s.GetDescriptors()) && hasPrefix(s.GetDescriptors(), md) {
			best, bestLen = m, len(md)
		}
	}
	return best
}

// descriptors are a symbol's descriptors, none when it does not parse.
func descriptors(sym string) []*scip.Descriptor {
	s, err := scip.ParseSymbol(sym)
	if err != nil {
		return nil
	}
	return s.GetDescriptors()
}

// declaredBy says whether a placed file has a node for the symbol.
func declaredBy(sites []*site, m *graph.Minter, sym string, s *scip.Symbol) bool {
	for _, st := range sites {
		if st.declares(m, sym, s) != nil {
			return true
		}
	}
	return false
}

// includes finds the workspace's calls of include!, as sites whose file is
// still to be asked for.
func (x *extractor) includes(ws string) []*site {
	var out []*site
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		dc := x.docs[rel]
		if dc.ws != ws {
			continue
		}
		for _, o := range dc.d.GetOccurrences() {
			if !isInclude(o) {
				continue
			}
			start, end, ok := occRange(o)
			if !ok {
				continue
			}
			lit, found := firstLiteral(dc.text, end)
			if s := x.siteAt(dc, start); found && s != nil {
				s.at, s.lit = start, lit
				out = append(out, s)
			}
		}
	}
	return out
}

// isInclude says whether an occurrence calls the standard library's
// include! macro.
func isInclude(o *scip.Occurrence) bool {
	sym, _, _, ok := referenceAt(o)
	if !ok {
		return false
	}
	s, err := scip.ParseSymbol(sym)
	if err != nil || s.GetPackage() == nil {
		return false
	}
	ds := s.GetDescriptors()
	if len(ds) == 0 {
		return false
	}
	last := ds[len(ds)-1]
	pkg := s.GetPackage().GetName()
	return last.GetSuffix() == scip.Descriptor_Macro && last.GetName() == "include" && (pkg == "core" || pkg == "std")
}

// siteAt is the site an include! at p in dc would be: the innermost module
// of the document holding p, or the document's own module.
func (x *extractor) siteAt(dc *doc, p pos) *site {
	var best *def
	for _, d := range x.defsOf[dc] {
		if d.kind != scip.SymbolInformation_Module || !d.encl || p.before(d.start) || !p.before(d.end) {
			continue
		}
		if best == nil || best.start.before(d.start) {
			best = d
		}
	}
	sym := dc.moduleSym
	var path []string
	if best != nil {
		sym, path = best.sym, best.path
	}
	s, err := scip.ParseSymbol(sym)
	if err != nil || s.GetPackage() == nil {
		return nil
	}
	return &site{dc: dc, path: slices.Clone(path), mod: s.GetDescriptors(), pkg: s.GetPackage().GetName(),
		items: map[graph.ID]*def{}}
}

// firstLiteral finds, inside the first string literal of the macro call
// whose name ends at end, its first character: in `include!(concat!(
// env!("OUT_DIR"), "/test.rs"))` the "OUT_DIR" one, from which
// rust-analyzer resolves the whole argument to the file it names, as from
// any literal in it. found is false when no call follows the name, or it
// holds no literal.
func firstLiteral(text []byte, end pos) (lit pos, found bool) {
	i := offsetOf(text, end)
	for i >= 0 && i < len(text) && bytes.IndexByte([]byte("! \t\r\n"), text[i]) >= 0 {
		i++
	}
	if i < 0 || i >= len(text) || text[i] != '(' {
		return pos{}, false
	}
	depth := 0
	for ; i < len(text); i++ {
		switch text[i] {
		case '"':
			return posAt(text, i+1), true
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return pos{}, false
			}
		}
	}
	return pos{}, false
}

// offsetOf is a position's byte offset in text, -1 past its end.
func offsetOf(text []byte, p pos) int {
	off := 0
	for ln := int32(0); ln < p.line; ln++ {
		nl := bytes.IndexByte(text[off:], '\n')
		if nl < 0 {
			return -1
		}
		off += nl + 1
	}
	return off + int(p.col)
}

// posAt is the position of a byte offset in text.
func posAt(text []byte, off int) pos {
	before := text[:off]
	line := bytes.Count(before, []byte("\n"))
	return pos{int32(line), int32(off - (bytes.LastIndexByte(before, '\n') + 1))}
}

// place asks which file a site's include! reads, and adds its items.
func (x *extractor) place(l *lsp, s *site) error {
	locs, err := l.definition(s.dc, s.lit, x.text)
	if err != nil {
		x.skip(s.dc.abs, int(s.at.line)+1, err)
		return nil
	}
	if len(locs) != 1 {
		return nil // a literal rust-analyzer does not read as a file
	}
	out, _ := locs[0].at()
	return x.placeFile(l, s, out)
}

// placeFile adds the items of the file out under a site, when the index
// has no document of it: Cargo's output, outside the repository or in a
// directory the layer leaves out, or a hand-written file of the repository
// that no module declares (placeHand).
func (x *extractor) placeFile(l *lsp, s *site, out string) error {
	rel, err := x.m.Rel(out)
	switch {
	case err == nil && x.docs[rel] != nil:
		return nil // a module of a crate, read as itself
	case err == nil && x.reading[rel]:
		return x.placeHand(l, s, out, rel)
	}
	syms, err := l.symbols(out)
	if err != nil {
		x.skip(s.dc.abs, int(s.at.line)+1, err)
		return nil
	}
	s.out = out
	x.report.GeneratedFiles++
	g := &genFile{s: s, l: l, lines: bytes.Split(x.text(out), []byte("\n")), pkg: protoPackage(out)}
	return x.items(g, syms, slices.Clone(s.path), x.owner(s.dc.rel, s.path), []string{})
}

// placeHand adds the items of a hand-written file of the repository that
// the workspace includes, at the file, where they are written: `src/
// hand.rs#by_hand()`, at their own positions, not generated (ADR 002 rule
// 16, the owner's decision on #84). A file included twice has one node per
// item for both, which is counted and reported at the second include!. A
// file whose header says prost-build wrote it, checked in as jj checks in
// its protos, is prost-build's output (the owner's decision on #89): its
// items are marked generated and joined to their .proto messages, at the
// file still.
func (x *extractor) placeHand(l *lsp, s *site, out, rel string) error {
	x.handSites[s.dc.rel] = append(x.handSites[s.dc.rel], s.at)
	x.includedTest(s, rel)
	if items := x.included[rel]; items != nil {
		s.out, s.hand, s.items = out, rel, items
		x.report.IncludedAgain++
		x.skip(s.dc.abs, int(s.at.line)+1, fmt.Errorf("%s is included here and in another module: its items are one node each, at the file, for both", rel))
		return nil
	}
	syms, err := l.symbols(out)
	if err != nil {
		x.skip(s.dc.abs, int(s.at.line)+1, err)
		return nil
	}
	s.out, s.hand = out, rel
	x.included[rel] = s.items
	x.report.Included++
	file, err := x.m.RustID(rel)
	if err != nil {
		return err
	}
	text := x.text(out)
	g := &genFile{s: s, l: l, lines: bytes.Split(text, []byte("\n")), hand: &doc{rel: rel, abs: out, ws: s.dc.ws, text: text}}
	var proto []string // nil: no item is a .proto message's
	if g.prost = prostOutput(g.lines); g.prost {
		g.pkg, proto = protoPackage(out), []string{}
		x.report.IncludedGenerated++
	}
	return x.items(g, syms, []string{}, file, proto)
}

// prostHeader is what prost-build writes on a file's first line: `// This
// file is @generated by prost-build.`
var prostHeader = []byte("@generated by prost-build")

// prostOutput says whether a file's header says prost-build wrote it: in
// its first lines, before any item.
func prostOutput(lines [][]byte) bool {
	for _, l := range lines[:min(len(lines), 3)] {
		if bytes.Contains(l, prostHeader) {
			return true
		}
	}
	return false
}

// A genFile is an included file being read: its site, the server that
// outlined it, its text and the .proto package prost-build named it after.
// hand is a hand-written one's doc, with no index document: what its
// items' defs are in, so a method a derive writes on one of its types
// (macroWritten) is at the file too. prost says such a file is
// prost-build's output, checked in (#89).
type genFile struct {
	s       *site
	l       *lsp
	lines   [][]byte
	pkg     string
	hand    *doc
	prost   bool
	notTest map[string]bool // the names it declares behind not(test), once read
}

// at is where an item of a hand-written file is, its name's start and the
// item's last line, columns in bytes whatever the server counts in.
func (g *genFile) at(sy symbol) (start pos, endLine int32) {
	start = pos{sy.SelectionRange.Start.Line, sy.SelectionRange.Start.Character}
	if !g.l.utf8 && int(start.line) < len(g.lines) {
		start.col = byteCol(g.lines[start.line], start.col)
	}
	return start, sy.Range.End.Line
}

// symbolKinds are the index's kinds for the server's (LSP's SymbolKind) of
// the items that get a node. An impl block (Object) has none: its items
// are under its type.
var symbolKinds = map[int]scip.SymbolInformation_Kind{
	2: scip.SymbolInformation_Module, 23: scip.SymbolInformation_Struct, 10: scip.SymbolInformation_Enum,
	11: scip.SymbolInformation_Trait, 26: scip.SymbolInformation_TypeAlias, 8: scip.SymbolInformation_Field,
	22: scip.SymbolInformation_EnumMember, 12: scip.SymbolInformation_Function, 6: scip.SymbolInformation_Method,
	14: scip.SymbolInformation_Constant, 13: scip.SymbolInformation_StaticVariable,
}

const lspImpl = 19 // LSP's SymbolKind Object, which rust-analyzer gives an impl block

// items adds the nodes of a list of a generated file's items, at path in
// the site's document, contained by parent. proto is the .proto path of
// the message holding them, nil when they are not inside one.
func (x *extractor) items(g *genFile, syms []symbol, path []string, parent graph.ID, proto []string) error {
	for _, sy := range syms {
		if sy.Kind == lspImpl {
			continue
		}
		kind, ok := symbolKinds[sy.Kind]
		if !ok {
			continue
		}
		name := unraw(sy.Name)
		if isFunc(kind) {
			name += "()"
		}
		d, err := x.declareItem(g, sy, append(slices.Clone(path), name), kind, parent, false)
		if err != nil {
			return err
		}
		if d == nil {
			continue
		}
		if err := x.protoLink(g, sy, syms, d, proto); err != nil {
			return err
		}
		if err := x.items(g, sy.Children, d.path, d.id, nestedProto(sy, syms, proto)); err != nil {
			return err
		}
	}
	// An impl block's items are placed after the items beside it, so its
	// type has its node wherever the impl is written (#113).
	for _, sy := range syms {
		if sy.Kind == lspImpl {
			if err := x.implItems(g, sy, path, parent); err != nil {
				return err
			}
		}
	}
	return nil
}

// implItems adds the items of an impl block, under its type: `MyEnum.
// as_str_name()` for `impl MyEnum`, `Scalars[Name].NAME` for `impl
// ::prost::Name for Scalars`.
func (x *extractor) implItems(g *genFile, sy symbol, path []string, parent graph.ID) error {
	ty, trait := implName(sy.Name)
	if ty == "" {
		return nil
	}
	seg := ty
	if trait != "" {
		seg += "[" + trait + "]"
	}
	owner := parent
	file, _ := g.s.anchor()
	held := g.ownTest(sy) // #[cfg(test)] impl Held { … }
	if id, err := x.m.RustID(file, append(slices.Clone(path), ty)...); err == nil && x.emitted[id] {
		owner = id
		if g.hand != nil && trait != "" {
			x.outlineImpls = append(x.outlineImpls, outlineImpl{ty: id, trait: trait})
		}
	}
	for _, c := range sy.Children {
		kind, ok := symbolKinds[c.Kind]
		if !ok {
			continue
		}
		name := unraw(c.Name)
		if kind == scip.SymbolInformation_Function || kind == scip.SymbolInformation_Method {
			kind, name = scip.SymbolInformation_Method, name+"()"
		}
		if _, err := x.declareItem(g, c, append(slices.Clone(path), seg, name), kind, owner, held); err != nil {
			return err
		}
	}
	return nil
}

// implName reads the server's name of an impl block, "impl MyEnum" or
// "impl ::prost::Name for Scalars", as its type and trait; "" for a type
// no ID can hold.
func implName(name string) (ty, trait string) {
	rest := strings.TrimSpace(strings.TrimPrefix(name, "impl"))
	if strings.HasPrefix(rest, "<") {
		depth := 0
		for i, c := range rest {
			if c == '<' {
				depth++
			} else if c == '>' {
				if depth--; depth == 0 {
					rest = strings.TrimSpace(rest[i+1:])
					break
				}
			}
		}
	}
	if t, ty, ok := strings.Cut(rest, " for "); ok {
		return typeName(ty), typeName(t)
	}
	return typeName(rest), ""
}

// declareItem adds the node of one item of an included file, sy, under
// its site's anchor, contained by parent: a generated one at the include!,
// a hand-written one where it is. nil when its ID cannot be minted or
// another item already has it (two included files declaring one name in
// one module).
func (x *extractor) declareItem(g *genFile, sy symbol, path []string, kind scip.SymbolInformation_Kind, parent graph.ID, held bool) (*def, error) {
	s := g.s
	file, _ := s.anchor()
	id, err := x.m.RustID(file, path...)
	if err != nil {
		x.skip(s.dc.abs, int(s.at.line)+1, err)
		return nil, nil
	}
	if x.emitted[id] {
		return nil, nil
	}
	x.emitted[id] = true
	d := &def{doc: s.dc, kind: kind, start: s.at, path: path, id: id}
	s.items[id] = d
	n := graph.Node{ID: id, Kind: kinds[kind], Name: strings.TrimSuffix(path[len(path)-1], "()"),
		Pos: graph.Position{File: file, Line: int(s.at.line) + 1, Col: int(s.at.col) + 1}, Generated: true,
		Attrs: map[string]string{"generated_in": filepathBase(s.out)}}
	if g.hand != nil {
		start, end := g.at(sy)
		d.doc, d.start = g.hand, start
		n.Pos.Line, n.Pos.Col, n.Generated = int(start.line)+1, int(start.col)+1, g.prost
		n.Attrs = map[string]string{"end_line": strconv.Itoa(int(end) + 1)}
		if g.prost {
			n.Attrs["generated_in"] = filepathBase(s.out)
		}
	}
	if n.Generated {
		x.report.Generated++
	} else {
		x.report.Declarations++
	}
	x.pending = append(x.pending, pendingItem{n: n, parent: parent, own: held || g.ownTest(sy)})
	return d, nil
}

// registerGenerated makes each generated node the definition of the index's
// symbol for it, which workspace code names: `proto/Scalars#double.` is
// the field `proto.Scalars.double` of the site in module proto. A symbol
// reads as a path under a site's module; the site whose file declares
// that path is the one.
func (x *extractor) registerGenerated(sites []*site) {
	if len(sites) == 0 {
		return
	}
	for _, sym := range slices.Sorted(maps.Keys(x.external)) {
		s, err := scip.ParseSymbol(sym)
		if err != nil || s.GetPackage() == nil || x.bySym[sym] != nil {
			continue
		}
		for _, st := range sites {
			if d := st.declares(x.m, sym, s); d != nil {
				d.sym = sym
				x.bySym[sym] = []*def{d}
				break
			}
		}
	}
}

// declares returns the node a site's file has for a symbol, or nil.
func (st *site) declares(m *graph.Minter, sym string, s *scip.Symbol) *def {
	if st.out == "" || s.GetPackage().GetName() != st.pkg {
		return nil
	}
	if !isCrateRoot(st.mod) && !hasPrefix(s.GetDescriptors(), st.mod) {
		return nil
	}
	segs, err := segments(sym, st.mod)
	if err != nil {
		return nil
	}
	file, path := st.anchor()
	id, err := m.RustID(file, append(slices.Clone(path), segs...)...)
	if err != nil {
		return nil
	}
	return st.items[id]
}
