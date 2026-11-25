// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// kinds maps the index's kinds of item to the graph's. The rest — a
// function's parameters and locals, type parameters, macros — have no
// node.
var kinds = map[scip.SymbolInformation_Kind]graph.Kind{
	scip.SymbolInformation_Function:       graph.KindFunc,
	scip.SymbolInformation_Method:         graph.KindMethod,
	scip.SymbolInformation_StaticMethod:   graph.KindMethod,
	scip.SymbolInformation_TraitMethod:    graph.KindMethod,
	scip.SymbolInformation_Struct:         graph.KindType,
	scip.SymbolInformation_Enum:           graph.KindType,
	scip.SymbolInformation_Union:          graph.KindType,
	scip.SymbolInformation_Trait:          graph.KindType,
	scip.SymbolInformation_TypeAlias:      graph.KindType,
	scip.SymbolInformation_AssociatedType: graph.KindType,
	scip.SymbolInformation_Field:          graph.KindField,
	scip.SymbolInformation_EnumMember:     graph.KindEnumValue,
	scip.SymbolInformation_Constant:       graph.KindConst,
	scip.SymbolInformation_StaticVariable: graph.KindVar,
	scip.SymbolInformation_Module:         graph.KindModule,
}

// isFunc says whether an item of the kind is a function, which is what
// holds a body and what a call reaches.
func isFunc(k scip.SymbolInformation_Kind) bool {
	switch k {
	case scip.SymbolInformation_Function, scip.SymbolInformation_Method,
		scip.SymbolInformation_StaticMethod, scip.SymbolInformation_TraitMethod:
		return true
	}
	return false
}

// A def is one item an index defines in a file.
type def struct {
	doc    *doc
	sym    string
	kind   scip.SymbolInformation_Kind
	start  pos // of its name
	end    pos // of the last character of the item, its enclosing range
	encl   bool
	own    []string // the path its symbol states, inside the file's module
	path   []string // inside the file, its holders' before its own: ADR 002 rule 16
	id     graph.ID
	holder *def // the innermost function holding it, if one does
}

type pos struct{ line, col int32 }

func (a pos) before(b pos) bool { return a.line < b.line || a.line == b.line && a.col < b.col }

// occRange reads an occurrence's range, and encRange the range of the item
// it names when it is a definition: the typed fields an index may write,
// or the packed [line, start, end] or [start line, start, end line, end]
// that rust-analyzer 1.99 writes.
func occRange(o *scip.Occurrence) (start, end pos, ok bool) {
	if r := o.GetSingleLineRange(); r != nil {
		return pos{r.GetLine(), r.GetStartCharacter()}, pos{r.GetLine(), r.GetEndCharacter()}, true
	}
	if r := o.GetMultiLineRange(); r != nil {
		return pos{r.GetStartLine(), r.GetStartCharacter()}, pos{r.GetEndLine(), r.GetEndCharacter()}, true
	}
	return span(o.GetRange()) //nolint:staticcheck // what rust-analyzer 1.99 writes
}

func encRange(o *scip.Occurrence) (start, end pos, ok bool) {
	if r := o.GetSingleLineEnclosingRange(); r != nil {
		return pos{r.GetLine(), r.GetStartCharacter()}, pos{r.GetLine(), r.GetEndCharacter()}, true
	}
	if r := o.GetMultiLineEnclosingRange(); r != nil {
		return pos{r.GetStartLine(), r.GetStartCharacter()}, pos{r.GetEndLine(), r.GetEndCharacter()}, true
	}
	return span(o.GetEnclosingRange()) //nolint:staticcheck // what rust-analyzer 1.99 writes
}

func span(r []int32) (start, end pos, ok bool) {
	switch len(r) {
	case 3:
		return pos{r[0], r[1]}, pos{r[0], r[2]}, true
	case 4:
		return pos{r[0], r[1]}, pos{r[2], r[3]}, true
	}
	return pos{}, pos{}, false
}

// declarations adds a node per item of every document, contained by the
// item holding it or by its file. Two of one ID in one file are one node,
// at the first (ADR 002 rule 16).
func (x *extractor) declarations() error {
	x.bySym, x.funcs, x.defsOf = map[string][]*def{}, map[*doc][]*def{}, map[*doc][]*def{}
	for _, rel := range slices.Sorted(maps.Keys(x.docs)) {
		defs := x.defs(x.docs[rel])
		for _, d := range defs {
			if err := x.declare(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// defs reads a document's items and gives each its path in the file: the
// one its symbol states, inside the file's own module, and before it the
// path of the function holding it, which a symbol leaves out (`inner().`
// for a function nested in `outer`).
func (x *extractor) defs(dc *doc) []*def {
	module, defs := items(dc)
	var out []*def
	for _, d := range defs {
		segs, err := segments(d.sym, module)
		if err != nil {
			x.skip(dc.abs, int(d.start.line)+1, err)
			continue
		}
		d.holder = innermost(defs, d.start, d)
		d.own = segs
		out = append(out, d)
	}
	for _, d := range out {
		d.path = fullPath(d)
	}
	return out
}

// items reads a document's definitions of the kinds with a node, and the
// symbol of the file's own module, which every item's symbol in the file
// starts with: the module defined at the file's first character.
func items(dc *doc) (module []*scip.Descriptor, defs []*def) {
	kind := map[string]scip.SymbolInformation_Kind{}
	for _, s := range dc.d.GetSymbols() {
		kind[s.GetSymbol()] = s.GetKind()
	}
	for _, o := range dc.d.GetOccurrences() {
		sym := o.GetSymbol()
		if o.GetSymbolRoles()&int32(scip.SymbolRole_Definition) == 0 || sym == "" || strings.HasPrefix(sym, "local ") {
			continue
		}
		start, _, ok := occRange(o)
		k := kind[sym]
		switch {
		case !ok:
		case k == scip.SymbolInformation_Module && start == (pos{}):
			if s, err := scip.ParseSymbol(sym); err == nil {
				module = s.GetDescriptors()
			}
		case kinds[k] != "":
			d := &def{doc: dc, sym: sym, kind: k, start: start}
			if _, end, ok := encRange(o); ok {
				d.end, d.encl = end, true
			}
			defs = append(defs, d)
		}
	}
	return module, defs
}

// fullPath is a def's path in its file: its holder's, then its own. It
// reads only own, so a holder whose path is already set is not counted
// twice.
func fullPath(d *def) []string {
	if d.holder == nil {
		return slices.Clone(d.own)
	}
	return append(fullPath(d.holder), d.own...)
}

// innermost returns the function among defs whose body holds p, the
// innermost one, other than self; nil when none does. A function holds
// another item only when its range ends after the item's: rust-analyzer
// gives every function a macro writes the range of the whole macro call
// (the four of a proptest! block on prost-reflect share one), and those
// are siblings, not nested.
func innermost(defs []*def, p pos, self *def) *def {
	var best *def
	for _, f := range defs {
		if f == self || !isFunc(f.kind) || !f.encl || p.before(f.start) || !p.before(f.end) {
			continue
		}
		if self != nil && self.encl && !self.end.before(f.end) {
			continue
		}
		if best == nil || best.start.before(f.start) {
			best = f
		}
	}
	return best
}

var errMacro = errors.New("a macro has no node")

// segments turns a symbol into its path inside the file whose own module
// is module. rust-analyzer names a method of an impl block by the impl's
// type and trait, `impl#[Square][Area]area().`, which becomes
// "Square[Area]", "area"; an inherent impl's, `impl#[Square]new().`,
// "Square", "new" (ADR 002 rule 16).
func segments(sym string, module []*scip.Descriptor) ([]string, error) {
	s, err := scip.ParseSymbol(sym)
	if err != nil {
		return nil, fmt.Errorf("symbol %q: %w", sym, err)
	}
	ds := s.GetDescriptors()
	if !isCrateRoot(module) && hasPrefix(ds, module) {
		ds = ds[len(module):]
	}
	var out []string
	for i := 0; i < len(ds); i++ {
		d := ds[i]
		switch {
		case d.GetSuffix() == scip.Descriptor_Macro:
			return nil, errMacro
		case d.GetSuffix() == scip.Descriptor_Type && d.GetName() == "impl":
			seg, n := implSegment(ds[i+1:])
			if seg == "" {
				return nil, fmt.Errorf("the type of the impl holding %s has no name an ID can hold", sym)
			}
			out = append(out, seg)
			i += n
		default:
			out = append(out, unraw(d.GetName()))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("symbol %q names nothing inside its file", sym)
	}
	return out, nil
}

// implSegment reads the type and the trait an impl's descriptor is
// followed by, `[Square][Area]`, into one segment, "Square[Area]", and
// says how many descriptors it read; "" when the type has no name.
func implSegment(ds []*scip.Descriptor) (string, int) {
	var names []string
	for _, d := range ds {
		if len(names) == 2 || d.GetSuffix() != scip.Descriptor_TypeParameter {
			break
		}
		names = append(names, typeName(d.GetName()))
	}
	if len(names) == 0 || names[0] == "" {
		return "", len(names)
	}
	if len(names) == 2 && names[1] != "" {
		return names[0] + "[" + names[1] + "]", 2
	}
	return names[0], len(names)
}

// unraw is a raw identifier's name: r#type is the identifier type, written
// raw because type is a keyword. No other identifier can be type, so the
// name is the declaration's, and "#" separates an ID's path from its
// declaration.
func unraw(name string) string {
	return strings.TrimPrefix(name, "r#")
}

func isCrateRoot(module []*scip.Descriptor) bool {
	return len(module) == 1 && module[0].GetName() == "crate"
}

func hasPrefix(ds, prefix []*scip.Descriptor) bool {
	if len(prefix) > len(ds) {
		return false
	}
	for i, p := range prefix {
		if ds[i].GetName() != p.GetName() || ds[i].GetSuffix() != p.GetSuffix() {
			return false
		}
	}
	return true
}

// typeName is the name of an impl's type or trait as a segment: its last
// path segment, without its generic arguments, a reference or a lifetime
// (`MarshalOptions<'a>` is MarshalOptions, `&mut fmt::Formatter` is
// Formatter); "" when what is left is not a name, as for a tuple or a
// slice.
func typeName(s string) string {
	s = unraw(strings.Trim(s, "`"))
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	for _, p := range []string{"&", "mut ", "dyn ", "impl "} {
		s = strings.TrimPrefix(strings.TrimSpace(s), p)
	}
	if strings.HasPrefix(s, "'") {
		if _, rest, ok := strings.Cut(s, " "); ok {
			s = rest
		}
	}
	if i := strings.LastIndex(s, "::"); i >= 0 {
		s = s[i+2:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
	}
	return s
}

// declare adds an item's node and the edge from what contains it.
func (x *extractor) declare(d *def) error {
	id, err := x.m.RustID(d.doc.rel, d.path...)
	if err != nil {
		x.skip(d.doc.abs, int(d.start.line)+1, err)
		return nil
	}
	d.id = id
	x.bySym[d.sym] = append(x.bySym[d.sym], d)
	x.defsOf[d.doc] = append(x.defsOf[d.doc], d)
	if isFunc(d.kind) {
		x.funcs[d.doc] = append(x.funcs[d.doc], d)
	}
	if x.emitted[id] {
		return nil
	}
	x.emitted[id] = true
	x.report.Declarations++
	attrs := map[string]string{}
	if d.encl {
		attrs["end_line"] = strconv.Itoa(int(d.end.line) + 1)
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: kinds[d.kind], Name: d.path[len(d.path)-1],
		Pos: graph.Position{File: d.doc.rel, Line: int(d.start.line) + 1, Col: int(d.start.col) + 1}, Attrs: attrs}); err != nil {
		return err
	}
	return x.b.AddEdge(graph.Edge{From: x.owner(d.doc.rel, d.path[:len(d.path)-1]), To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// owner is the node of the item at path in a file: the innermost one that
// has a node, or the file. A method of an impl block is contained by its
// type, when the type is declared in the same file: "Square[Area]" is
// held by "Square".
func (x *extractor) owner(file string, path []string) graph.ID {
	for n := len(path); n > 0; n-- {
		prefix := slices.Clone(path[:n])
		for _, p := range [][]string{prefix, typeOnly(prefix)} {
			if p == nil {
				continue
			}
			if id, err := x.m.RustID(file, p...); err == nil && x.emitted[id] {
				return id
			}
		}
	}
	id, _ := x.m.RustID(file)
	return id
}

// typeOnly is path with its last segment's trait taken off, or nil when
// it carries none.
func typeOnly(path []string) []string {
	last := path[len(path)-1]
	ty, _, ok := strings.Cut(last, "[")
	if !ok {
		return nil
	}
	out := slices.Clone(path)
	out[len(out)-1] = ty
	return out
}
