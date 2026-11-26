// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// Public paths (#116, the owner's decision): the paths code outside a
// crate names an item by, `tokio::spawn` for
// tokio/src/task/spawn.rs#spawn(), go on the item's node as the attribute
// paths, so that a Markdown mention written as a crate path links. The
// Markdown layer reads the graph, never the index.
//
// An item's path is its parent's and its name. A crate root's path is
// the crate's name, the package's with "-" as "_". A module's parent is
// the module holding it, and a module has a path only when it is declared
// `pub mod`. A method's parent is its impl block's type. Every `pub use`
// adds the name it exports, `as` renames read, at each path of the module
// holding it: rust-analyzer resolves the name to the item's definition,
// through any other uses on the way. A glob, `pub use inner::*`, adds
// nothing.
//
// Only a package's library has paths: a file below its src/ other than
// src/main.rs and src/bin/. Its tests, benches, examples, binaries and
// build script share its symbols (ADR 002 rule 16), but no other crate
// names what they define.

// maxPaths is the most paths one node carries: the shortest, which are
// what docs write.
const maxPaths = 8

// pathKinds are the kinds of item a path names. A field is reached
// through a value, not a path.
var pathKinds = map[scip.SymbolInformation_Kind]bool{
	scip.SymbolInformation_Function: true, scip.SymbolInformation_Method: true,
	scip.SymbolInformation_StaticMethod: true, scip.SymbolInformation_TraitMethod: true,
	scip.SymbolInformation_Struct: true, scip.SymbolInformation_Enum: true,
	scip.SymbolInformation_Union: true, scip.SymbolInformation_Trait: true,
	scip.SymbolInformation_TypeAlias: true, scip.SymbolInformation_AssociatedType: true,
	scip.SymbolInformation_EnumMember: true, scip.SymbolInformation_Constant: true,
	scip.SymbolInformation_StaticVariable: true, scip.SymbolInformation_Module: true,
}

// A pathKey names an item by its package and descriptors, whatever
// spelling of its symbol an occurrence uses.
type pathKey string

type parsedSym struct {
	pkg string
	ds  []*scip.Descriptor
}

// An export is a name a `pub use` gives an item, at the module holding
// the use.
type export struct {
	site pathKey
	name string
}

type pathIndex struct {
	syms    map[pathKey]parsedSym
	pubMods map[pathKey]bool
	exports map[pathKey][]export
	types   map[pathKey]bool     // the library's types
	named   map[string][]pathKey // its types by package and name
	memo    map[pathKey][]string
	busy    map[pathKey]bool
}

// publicPaths reads every library file's modules and uses, and records
// the paths of each item and file module that has one, by node.
func (x *extractor) publicPaths() {
	ix := &pathIndex{syms: map[pathKey]parsedSym{}, pubMods: map[pathKey]bool{}, exports: map[pathKey][]export{},
		types: map[pathKey]bool{}, named: map[string][]pathKey{}, memo: map[pathKey][]string{}, busy: map[pathKey]bool{}}
	var lib []*doc
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		if dc := x.docs[rel]; x.tests.library(rel) {
			lib = append(lib, dc)
		}
	}
	for _, dc := range lib {
		ix.addTypes(x.defsOf[dc])
		ix.scanModules(dc)
	}
	for _, dc := range lib {
		x.scanUses(ix, dc)
	}
	x.pubPaths = map[graph.ID][]string{}
	for _, dc := range lib {
		x.assignPaths(ix, dc)
	}
}

// assignPaths records the paths of a library file's own module and items.
func (x *extractor) assignPaths(ix *pathIndex, dc *doc) {
	if k, ok := ix.key(dc.moduleSym); ok {
		if id, err := x.m.RustID(dc.rel); err == nil {
			x.pubPaths[id] = mergePaths(x.pubPaths[id], ix.paths(k))
		}
	}
	for _, d := range x.defsOf[dc] {
		if k, ok := ix.key(d.sym); ok && pathKinds[d.kind] {
			x.pubPaths[d.id] = mergePaths(x.pubPaths[d.id], ix.paths(k))
		}
	}
}

// key parses a symbol into its key, once.
func (ix *pathIndex) key(sym string) (pathKey, bool) {
	s, err := scip.ParseSymbol(sym)
	if err != nil || s.GetPackage() == nil || len(s.GetDescriptors()) == 0 {
		return "", false
	}
	return ix.keyOf(s.GetPackage().GetName(), s.GetDescriptors()), true
}

func (ix *pathIndex) keyOf(pkg string, ds []*scip.Descriptor) pathKey {
	var b strings.Builder
	b.WriteString(pkg)
	for _, d := range ds {
		b.WriteString("\x00" + d.GetName() + "\x00" + strconv.Itoa(int(d.GetSuffix())))
	}
	k := pathKey(b.String())
	if _, ok := ix.syms[k]; !ok {
		ix.syms[k] = parsedSym{pkg, ds}
	}
	return k
}

// addTypes records a library file's types, which a method's impl block
// names by its last segment alone.
func (ix *pathIndex) addTypes(defs []*def) {
	for _, d := range defs {
		if kinds[d.kind] != graph.KindType {
			continue
		}
		if k, ok := ix.key(d.sym); ok {
			s := ix.syms[k]
			ix.types[k] = true
			n := s.pkg + "\x00" + s.ds[len(s.ds)-1].GetName()
			ix.named[n] = append(ix.named[n], k)
		}
	}
}

// pubModRE is a module declared public, its name following.
var pubModRE = regexp.MustCompile(`\bpub\s+mod\s+(?:r#)?$`)

// scanModules records the modules a library file declares `pub mod`:
// each occurrence of a module's name preceded by `pub mod`, its
// definition when inline (`pub mod inner {`), else its declaration
// (`pub mod codec;`).
func (ix *pathIndex) scanModules(dc *doc) {
	lines := dc.lines()
	for _, o := range dc.d.GetOccurrences() {
		k, ok := ix.key(o.GetSymbol())
		if !ok || strings.HasPrefix(o.GetSymbol(), "local ") {
			continue
		}
		ds := ix.syms[k].ds
		start, _, ok := occRange(o)
		if !ok || ds[len(ds)-1].GetSuffix() != scip.Descriptor_Namespace || int(start.line) >= len(lines) {
			continue
		}
		if line := lines[start.line]; int(start.col) <= len(line) && pubModRE.Match(line[:start.col]) {
			ix.pubMods[k] = true
		}
	}
}

// scanUses records the names a library file's `pub use` declarations
// export: each reference that ends a path of a public use's tree. One
// inside a function body is not told apart: the index gives a function a
// macro writes the macro call's whole range, which holds tokio's `pub use
// runtime::Runtime` beside it in `cfg_rt! { … }`, and a pub use in a body
// is next to never written.
func (x *extractor) scanUses(ix *pathIndex, dc *doc) {
	lines := dc.lines()
	site, ok := ix.key(dc.moduleSym)
	if !ok {
		return
	}
	for _, o := range dc.d.GetOccurrences() {
		sym, start, end, ok := referenceAt(o)
		if !ok {
			continue
		}
		if in, public := useAt(lines, start); !in || !public {
			continue
		}
		k, ok := ix.key(sym)
		if !ok {
			continue
		}
		if name := exportedName(lines, start, end, ix.syms[k]); name != "" {
			ix.exports[k] = append(ix.exports[k], export{x.moduleAt(ix, dc, start, site), name})
		}
	}
}

// moduleAt is the module holding a position in a file: the innermost
// inline module whose body holds it, else the file's own. The body is
// read from the text, its braces matched from the module's name: the
// index gives a module a macro writes the range of the whole macro call,
// so in tokio's `cfg_sync! { pub mod futures { … } … pub use
// mutex::Mutex; }` it would hold the use.
func (x *extractor) moduleAt(ix *pathIndex, dc *doc, p pos, file pathKey) pathKey {
	var best *def
	for _, d := range x.defsOf[dc] {
		if d.kind != scip.SymbolInformation_Module || p.before(d.start) {
			continue
		}
		if end, ok := bodyEnd(dc.lines(), d.start); !ok || !p.before(end) {
			continue
		}
		if best == nil || best.start.before(d.start) {
			best = d
		}
	}
	if best != nil {
		if k, ok := ix.key(best.sym); ok {
			return k
		}
	}
	return file
}

// bodyEnd is where the body of the item named at from ends: past the "}"
// matching the first "{" after the name, // comments left out; false when
// a ";" comes first, a module declared with no body. A brace inside a
// string literal is counted as code.
func bodyEnd(lines [][]byte, from pos) (pos, bool) {
	depth := 0
	for l := int(from.line); l < len(lines); l++ {
		line := stripComment(lines[l])
		i := 0
		if l == int(from.line) {
			i = min(int(from.col), len(line))
		}
		for ; i < len(line); i++ {
			switch c := line[i]; {
			case c == ';' && depth == 0:
				return pos{}, false
			case c == '{':
				depth++
			case c == '}':
				if depth--; depth == 0 {
					return pos{int32(l), int32(i + 1)}, true
				}
			}
		}
	}
	return pos{}, false
}

var asRE = regexp.MustCompile(`^\s+as\s+((?:r#)?[A-Za-z_][A-Za-z0-9_]*)`)

// exportedName is the name a use exports for the reference from start to
// end: the reference's own, the name after `as`, or for `self` the
// module's; "" when the reference is not the end of a path (`codec` in
// `codec::parse`, or a glob's module) or is exported as `_`.
func exportedName(lines [][]byte, start, end pos, s parsedSym) string {
	if start.line != end.line || int(end.line) >= len(lines) || int(end.col) > len(lines[end.line]) {
		return ""
	}
	line := lines[end.line]
	if bytes.HasPrefix(bytes.TrimLeft(line[end.col:], " \t"), []byte("::")) {
		return ""
	}
	name := string(line[start.col:end.col])
	if m := asRE.FindSubmatch(line[end.col:]); m != nil {
		name = string(m[1])
	} else if name == "self" {
		name = s.ds[len(s.ds)-1].GetName()
	}
	if name = unraw(name); name == "_" {
		return ""
	}
	return name
}

// paths are an item's paths, the shortest maxPaths of them. One reached
// again while its own are being read, through a use that leads back to
// it, adds none.
func (ix *pathIndex) paths(k pathKey) []string {
	if p, ok := ix.memo[k]; ok {
		return p
	}
	if ix.busy[k] {
		return nil
	}
	ix.busy[k] = true
	var out []string
	if crate, ok := ix.crateRoot(k); ok {
		out = append(out, crate)
	} else if parent, name, ok := ix.parentOf(k); ok {
		for _, p := range ix.paths(parent) {
			out = append(out, p+"::"+name)
		}
	}
	for _, e := range ix.exports[k] {
		for _, p := range ix.paths(e.site) {
			out = append(out, p+"::"+e.name)
		}
	}
	out = mergePaths(nil, out)
	delete(ix.busy, k)
	ix.memo[k] = out
	return out
}

// crateRoot is a crate root's path, its crate's name.
func (ix *pathIndex) crateRoot(k pathKey) (string, bool) {
	s := ix.syms[k]
	if len(s.ds) == 1 && s.ds[0].GetName() == "crate" && s.ds[0].GetSuffix() == scip.Descriptor_Namespace {
		return strings.ReplaceAll(s.pkg, "-", "_"), true
	}
	return "", false
}

// parentOf is what an item's path continues, and the name it adds: the
// module or type holding it, or an impl block's type. A module not
// declared `pub mod` has none, and nor does a method of an impl block
// whose type is not found.
func (ix *pathIndex) parentOf(k pathKey) (parent pathKey, name string, ok bool) {
	s := ix.syms[k]
	last := s.ds[len(s.ds)-1]
	name = unraw(last.GetName())
	if last.GetSuffix() == scip.Descriptor_Namespace && !ix.pubMods[k] {
		return "", "", false
	}
	for i, d := range s.ds[:len(s.ds)-1] {
		if d.GetSuffix() == scip.Descriptor_Type && d.GetName() == "impl" {
			t, ok := ix.implType(s.pkg, s.ds[:i], s.ds[i+1:])
			return t, name, ok
		}
	}
	return ix.holder(s.pkg, s.ds[:len(s.ds)-1]), name, true
}

// holder is the key of the module or type at ds, the crate root when ds
// is empty.
func (ix *pathIndex) holder(pkg string, ds []*scip.Descriptor) pathKey {
	if len(ds) == 0 {
		ds = []*scip.Descriptor{{Name: "crate", Suffix: scip.Descriptor_Namespace}}
	}
	return ix.keyOf(pkg, ds)
}

// implType is the type an impl block in module mod names first after it:
// the type of that name in the same module, else the library's one type
// of that name. rust-analyzer names a method by the impl block's module,
// which need not be its type's.
func (ix *pathIndex) implType(pkg string, mod, after []*scip.Descriptor) (pathKey, bool) {
	if len(after) == 0 || after[0].GetSuffix() != scip.Descriptor_TypeParameter {
		return "", false
	}
	name := typeName(after[0].GetName())
	if name == "" {
		return "", false
	}
	same := ix.keyOf(pkg, append(slices.Clone(mod), &scip.Descriptor{Name: name, Suffix: scip.Descriptor_Type}))
	if ix.types[same] {
		return same, true
	}
	if ks := ix.named[pkg+"\x00"+name]; len(ks) == 1 {
		return ks[0], true
	}
	return "", false
}

// mergePaths is a's and b's paths, once each, the shortest maxPaths of
// them, shortest first and then in order.
func mergePaths(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.SortFunc(out, func(p, q string) int { return cmp.Or(cmp.Compare(len(p), len(q)), cmp.Compare(p, q)) })
	out = slices.Compact(out)
	if len(out) > maxPaths {
		out = out[:maxPaths]
	}
	return out
}
