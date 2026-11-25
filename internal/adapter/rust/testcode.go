// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
)

// Test code (#106, the owner's decision on #99): an item that exists only
// in a crate's test build. rust-analyzer indexes that build, cfg(test) on,
// so its items have nodes; they are marked test, and so is every edge
// touching one (ADR 004 rule 4, #81). A call that resolves to a test mock
// only because cfg(test) is on (tokio's File::poll_write reaching
// fs::mocks::spawn_mandatory_blocking) is then a test edge, which
// exclude_tests leaves out.
//
// An item is test code when its own attributes require cfg(test)
// (`#[cfg(test)]`, `#[cfg(all(test, unix))]`) or make it a test
// (`#[test]`, `#[tokio::test]`); when the function holding it is; when a
// module its symbol names is (written inline, declared
// `#[cfg(test)] mod mocks;`, or opened with `#![cfg(test)]`); or when its
// file is an integration test's, below a package's `tests/` directory.

// testMarks are the modules found to be test code, by symbol, and the
// packages' directories, which an integration test's file is below.
// production says whether the file also declares a's name for the
// library build, behind not(test): `#[cfg(not(test))] const MAX: usize =
// 1 << 20;` beside `#[cfg(test)] const MAX: usize = 512;`. The two have
// one ID, and the node is the production build's too, so a cfg(test) on
// the one rust-analyzer read does not make it test code.
func (t testMarks) production(d *def) bool {
	names, ok := t.prodNames[d.doc]
	if !ok {
		names = notTestNames(d.doc.lines())
		t.prodNames[d.doc] = names
	}
	return names[strings.TrimSuffix(d.path[len(d.path)-1], "()")]
}

var itemNameRE = regexp.MustCompile(`\b(?:fn|const|static|struct|enum|union|type|trait|mod)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// notTestNames are the names of the items a file declares behind an
// attribute that needs not(test).
func notTestNames(lines [][]byte) map[string]bool {
	out := map[string]bool{}
	pending := false
	for _, line := range lines {
		t := strings.TrimSpace(string(stripComment(line)))
		// The attributes the line opens with, then what follows them.
		for strings.HasPrefix(t, "#[") {
			attr := attrAt(t)
			if attr == "" {
				break
			}
			if m := cfgRE.FindStringIndex(attr); m != nil {
				if pred, ok := parenBody(attr[m[1]-1:]); ok && needsNotTest(pred) {
					pending = true
				}
			}
			t = strings.TrimSpace(t[len(attr):])
		}
		if t == "" || strings.HasPrefix(t, "#[") {
			continue
		}
		if pending {
			if m := itemNameRE.FindStringSubmatch(t); m != nil {
				out[m[1]] = true
			}
			pending = false
		}
	}
	return out
}

// needsNotTest says whether a cfg predicate holds only without test:
// not(test), or all(…) with one that does.
func needsNotTest(pred string) bool {
	pred = strings.TrimSpace(pred)
	if rest, ok := strings.CutPrefix(pred, "not"); ok {
		body, ok := parenBody(strings.TrimSpace(rest))
		return ok && needsTest(body)
	}
	if rest, ok := strings.CutPrefix(pred, "all"); ok {
		if body, ok := parenBody(strings.TrimSpace(rest)); ok {
			for _, a := range splitTop(body, ',') {
				if needsNotTest(a) {
					return true
				}
			}
		}
	}
	return false
}

type testMarks struct {
	mods  map[string]bool
	pkgs  []string
	impls map[implKey]bool // an impl block's header, by line: test or not
	// prodNames are, by file, the names it declares behind not(test).
	prodNames map[*doc]map[string]bool
}

// markTests reads, over every document, which modules are test code.
func (x *extractor) markTests() {
	x.tests = testMarks{mods: map[string]bool{}, impls: map[implKey]bool{}, prodNames: map[*doc]map[string]bool{}}
	x.testIDs = map[graph.ID]bool{}
	for _, ms := range x.manifests {
		for _, m := range ms {
			if rel, ok := x.relPath(filepath.Dir(m)); ok {
				x.tests.pkgs = append(x.tests.pkgs, rel)
			}
		}
	}
	// A file two crates compile (a build script's `#[path]` module) is
	// test code only when every declaration of its module is.
	decls := map[string][]bool{}
	for _, dc := range x.docs {
		// An integration test is found by its path. Every crate root of a
		// package shares one module symbol, the library's among them, so
		// a crate root's is never marked.
		if innerTestAttr(dc.text) {
			if m := fileModule(dc); m != "" && !crateRootSym(m) {
				x.tests.mods[m] = true
			}
		}
		modDecls(dc, decls)
	}
	for sym, test := range decls {
		if !slices.Contains(test, false) {
			x.tests.mods[sym] = true
		}
	}
}

// modDecls records, for each `mod name;` a document declares, whether its
// attributes make the module test code. An inline module is a def, marked
// by its own attributes instead.
func modDecls(dc *doc, decls map[string][]bool) {
	lines := dc.lines()
	for _, o := range dc.d.GetOccurrences() {
		sym := o.GetSymbol()
		if !strings.HasSuffix(sym, "/") || strings.HasPrefix(sym, "local ") || o.GetSymbolRoles()&int32(scip.SymbolRole_Definition) != 0 {
			continue
		}
		if start, _, ok := occRange(o); ok && start != (pos{}) && modDecl(lines, start) {
			decls[sym] = append(decls[sym], requiresTest(attrsAbove(lines, start)))
		}
	}
}

// lines is a document's text, split at its newlines, once.
func (dc *doc) lines() [][]byte {
	if dc.split == nil {
		dc.split = bytes.Split(dc.text, []byte("\n"))
	}
	return dc.split
}

// fileTest says whether a file's own module is test code.
func (x *extractor) fileTest(rel string) bool {
	if x.tests.integration(rel) {
		return true
	}
	dc := x.docs[rel]
	if dc == nil {
		return false
	}
	return x.tests.mods[fileModule(dc)]
}

// crateRootSym says whether a module symbol is a crate's root.
func crateRootSym(sym string) bool {
	s, err := scip.ParseSymbol(sym)
	return err != nil || isCrateRoot(s.GetDescriptors())
}

// fileModule is the symbol of a document's own module: the module defined
// at its first character, which items reads too.
func fileModule(dc *doc) string {
	for _, o := range dc.d.GetOccurrences() {
		sym := o.GetSymbol()
		if o.GetSymbolRoles()&int32(scip.SymbolRole_Definition) == 0 || !strings.HasSuffix(sym, "/") {
			continue
		}
		if start, _, ok := occRange(o); ok && start == (pos{}) {
			return sym
		}
	}
	return ""
}

var implRE = regexp.MustCompile(`^\s*(?:#\[[^\]]*\]\s*)*(?:unsafe\s+)?impl\b`)

// inTestImpl says whether a method sits in an impl block whose attributes
// make it test code: `#[cfg(test)] impl PdfCraftApp { … }`. The index has
// no symbol of an impl block, so its header is found in the source: the
// nearest line above the method, indented less, that opens an impl, past
// the header's `where` and bounds. A method's nearest such line is always
// its impl's header: the index names only an impl block's methods so.
func (x *extractor) inTestImpl(d *def) bool {
	if !strings.Contains(d.sym, "impl#[") || !d.encl {
		return false
	}
	lines := d.doc.lines()
	indent := func(l []byte) int { return len(l) - len(bytes.TrimLeft(l, " \t")) }
	if int(d.encStart.line) >= len(lines) {
		return false
	}
	own := indent(lines[d.encStart.line])
	for l := int(d.encStart.line) - 1; l >= 0; l-- {
		line := lines[l]
		if len(bytes.TrimSpace(line)) == 0 || indent(line) >= own {
			continue
		}
		m := implRE.FindIndex(line)
		if m == nil {
			continue // the header's own lines: `where`, a bound, the `{`
		}
		key := implKey{d.doc, l}
		t, ok := x.tests.impls[key]
		if !ok {
			// Its attributes: the lines above, and the line's own before
			// `impl` (`#[cfg(test)] impl Gate {`).
			at := m[1] - len("impl")
			t = requiresTest(attrsAbove(lines, pos{int32(l), int32(at)}))
			x.tests.impls[key] = t
		}
		return t
	}
	return false
}

type implKey struct {
	doc  *doc
	line int
}

// relPath is an absolute path's path below the root, slashed.
func (x *extractor) relPath(abs string) (string, bool) {
	rel, err := filepath.Rel(x.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// integration says whether a file is below a package's tests/ directory,
// where Cargo finds integration tests.
func (t testMarks) integration(rel string) bool {
	for _, p := range t.pkgs {
		dir := "tests/"
		if p != "" && p != "." {
			dir = p + "/tests/"
		}
		if strings.HasPrefix(rel, dir) {
			return true
		}
	}
	return false
}

// markTest is isTest, or its owner's mark (a field of a test struct, a
// method of a test trait), recording a test item's ID for its members and
// what a macro writes on it.
func (x *extractor) markTest(id, owner graph.ID, d *def) bool {
	t := x.testIDs[owner] || x.isTest(d)
	if t {
		x.testIDs[id] = true
	}
	return t
}

// isTest says whether a def is test code by its own attributes, its impl
// block's, a module its symbol names, or its file. What holds it (a test
// function, a test type, an inline test module) marks it through
// markTest.
func (x *extractor) isTest(d *def) bool {
	if x.tests.integration(d.doc.rel) || x.inTestImpl(d) {
		return true
	}
	if d.test == nil {
		t := d.encl && requiresTest(attrText(d.doc.lines(), d.encStart, d.start)) && !x.tests.production(d)
		d.test = &t
	}
	if *d.test {
		return true
	}
	// A module's symbol is a prefix of each of its items' symbols, ending
	// at a namespace's "/".
	for i := 0; i < len(d.sym); i++ {
		if d.sym[i] == '/' && x.tests.mods[d.sym[:i+1]] {
			return true
		}
	}
	return false
}

// attrText is the source from an item's range's start to its name: its
// attributes, visibility and keyword, comments left out.
func attrText(lines [][]byte, from, to pos) string {
	var b strings.Builder
	for l := int(from.line); l <= int(to.line) && l < len(lines); l++ {
		line := lines[l]
		if l == int(to.line) && int(to.col) <= len(line) {
			line = line[:to.col]
		}
		if l == int(from.line) && int(from.col) <= len(line) {
			line = line[from.col:]
		}
		b.Write(stripComment(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// attrsAbove is the attributes on a declaration's line before it and on
// the lines above, up to the line that ends the item before.
func attrsAbove(lines [][]byte, at pos) string {
	var above []string
	for l := int(at.line) - 1; l >= 0; l-- {
		t := bytes.TrimSpace(stripComment(lines[l]))
		if len(t) == 0 {
			if bytes.HasPrefix(bytes.TrimSpace(lines[l]), []byte("//")) {
				continue // a comment line
			}
			break
		}
		if last := t[len(t)-1]; (last == ';' || last == '{' || last == '}') && !bytes.HasPrefix(t, []byte("#")) {
			break
		}
		above = append([]string{string(t)}, above...)
	}
	line := lines[at.line]
	if int(at.col) <= len(line) {
		line = line[:at.col]
	}
	return strings.Join(above, "\n") + "\n" + string(stripComment(line))
}

// stripComment leaves out a line's // comment, doc comments included.
func stripComment(line []byte) []byte {
	if i := bytes.Index(line, []byte("//")); i >= 0 {
		return line[:i]
	}
	return line
}

var modDeclRE = regexp.MustCompile(`\bmod\s+$`)

// modDecl says whether a module's reference at p is the name of a
// `mod name;` declaration, which brings in another file.
func modDecl(lines [][]byte, p pos) bool {
	if int(p.line) >= len(lines) || int(p.col) > len(lines[p.line]) {
		return false
	}
	line := lines[p.line]
	return modDeclRE.Match(line[:p.col]) && bytes.HasPrefix(bytes.TrimLeft(line[p.col:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_ \t"), []byte(";"))
}

var innerCfgRE = regexp.MustCompile(`(?m)^\s*#!\[\s*cfg\s*\(`)

// innerTestAttr says whether a file opens with an inner attribute that
// requires cfg(test), `#![cfg(test)]`, which makes its whole module test
// code.
func innerTestAttr(text []byte) bool {
	for _, m := range innerCfgRE.FindAllIndex(text, -1) {
		if s := attrAt(string(text[m[0]:])); s != "" && requiresTest(s) {
			return true
		}
	}
	return false
}

// attrAt is the attribute that s starts with, through its closing "]".
func attrAt(s string) string {
	depth := 0
	for i, c := range s {
		switch c {
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}

var (
	cfgRE  = regexp.MustCompile(`#!?\[\s*cfg\s*\(`)
	testRE = regexp.MustCompile(`#\[\s*(?:[A-Za-z_][A-Za-z0-9_]*\s*::\s*)*test\s*(?:\]|\()`)
)

// requiresTest says whether attributes make what they are on test code:
// a #[test] (or a framework's, #[tokio::test]), or a cfg whose predicate
// holds only with test.
func requiresTest(attrs string) bool {
	if testRE.MatchString(attrs) {
		return true
	}
	for _, m := range cfgRE.FindAllStringIndex(attrs, -1) {
		if pred, ok := parenBody(attrs[m[1]-1:]); ok && needsTest(pred) {
			return true
		}
	}
	return false
}

// parenBody is what the parenthesis s opens with holds.
func parenBody(s string) (string, bool) {
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return s[1:i], true
			}
		}
	}
	return "", false
}

// needsTest says whether a cfg predicate holds only when test does: test
// itself, all(…) with one that does, any(…) whose every one does. not(…)
// never does: not(test) is the production build's.
func needsTest(pred string) bool {
	pred = strings.TrimSpace(pred)
	if pred == "test" {
		return true
	}
	for _, op := range []string{"all", "any"} {
		rest, ok := strings.CutPrefix(pred, op)
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "(") {
			continue
		}
		body, ok := parenBody(strings.TrimSpace(rest))
		if !ok {
			return false
		}
		args := splitTop(body, ',')
		if op == "all" {
			for _, a := range args {
				if needsTest(a) {
					return true
				}
			}
			return false
		}
		n := 0
		for _, a := range args {
			if strings.TrimSpace(a) == "" {
				continue
			}
			if !needsTest(a) {
				return false
			}
			n++
		}
		return n > 0
	}
	return false
}
