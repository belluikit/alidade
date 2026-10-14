// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// The kinds of clause, named as esbuild names them in its metafile.
const (
	kindImport  = "import-statement"
	kindExport  = "export-statement" // export … from "x"
	kindDynamic = "dynamic-import"   // import("x")
	kindRequire = "require-call"     // require("x"), import x = require("x")
)

// An importClause is one statement that names another module: its
// specifier, the names it binds from it (empty for a namespace, default
// or side-effect import, which the layer cannot map to an export), and
// after resolution what esbuild made of the specifier.
type importClause struct {
	spec  string
	kind  string
	names []string

	path     string // resolved file, absolute
	external bool   // a bare specifier: a package
	err      string // esbuild could resolve it to neither
}

func (c importClause) resolveKind() api.ResolveKind {
	switch c.kind {
	case kindDynamic:
		return api.ResolveJSDynamicImport
	case kindRequire:
		return api.ResolveJSRequireCall
	default:
		return api.ResolveJSImportStatement
	}
}

var (
	scriptRE = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)
	// importRE matches the clause forms of an import statement: named,
	// namespace, default plus named, default plus namespace, default, and
	// a bare side-effect import; "type" may precede the clause or a name.
	importRE = regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?(?:(\{[^}]*\})|(\*\s+as\s+\w+)|(\w+)\s*,\s*(\{[^}]*\})|(\w+)\s*,\s*\*\s+as\s+\w+|(\w+))\s+from\s+['"]([^'"]+)['"]|^\s*import\s+['"]([^'"]+)['"]`)
	// exportFromRE matches a re-export: export { A } from "x", export *
	// from "x".
	exportFromRE = regexp.MustCompile(`(?m)^\s*export\s+(?:type\s+)?(?:(\{[^}]*\})|(\*(?:\s+as\s+\w+)?))\s+from\s+['"]([^'"]+)['"]`)
	dynamicRE    = regexp.MustCompile(`\bimport\(\s*['"]([^'"]+)['"]\s*\)`)
	// requireRE matches a CommonJS require and TypeScript's
	// "import x = require(…)", not a method named require.
	requireRE = regexp.MustCompile(`(?:^|[^.\w$])require\(\s*['"]([^'"]+)['"]\s*\)`)
)

// scriptBlocks returns the contents of a component's <script> elements,
// joined; the markup is not read.
func scriptBlocks(src []byte) string {
	var sb strings.Builder
	for _, m := range scriptRE.FindAllSubmatch(src, -1) {
		sb.Write(m[1])
		sb.WriteString("\n")
	}
	return sb.String()
}

// importClauses reads every statement of a source file that names
// another module. The patterns run over a mask of the source — comments
// blanked, the contents of every string and template literal filled —
// so an import written inside a template literal or a comment is not
// one, and a comment inside a clause hides no name after it (#99); the
// mask keeps every offset, so a specifier is read from the source at the
// position its quotes hold in the mask.
func importClauses(src []byte) []importClause {
	masked := mask(src)
	at := func(m []int, g int) string {
		if m[2*g] < 0 {
			return ""
		}
		return string(src[m[2*g]:m[2*g+1]])
	}
	var out []importClause
	for _, m := range importRE.FindAllSubmatchIndex(masked, -1) {
		if spec := at(m, 8); spec != "" {
			out = append(out, importClause{spec: spec, kind: kindImport})
			continue
		}
		c := importClause{spec: at(m, 7), kind: kindImport}
		switch {
		case at(m, 1) != "":
			c.names = bindingNames(string(masked[m[2]:m[3]]))
		case at(m, 4) != "":
			c.names = bindingNames(string(masked[m[8]:m[9]])) // default plus named: the named half
		}
		out = append(out, c)
	}
	for _, m := range exportFromRE.FindAllSubmatchIndex(masked, -1) {
		c := importClause{spec: at(m, 3), kind: kindExport}
		if at(m, 1) != "" {
			c.names = bindingNames(string(masked[m[2]:m[3]]))
		}
		out = append(out, c)
	}
	for _, m := range dynamicRE.FindAllSubmatchIndex(masked, -1) {
		out = append(out, importClause{spec: at(m, 1), kind: kindDynamic})
	}
	for _, m := range requireRE.FindAllSubmatchIndex(masked, -1) {
		out = append(out, importClause{spec: at(m, 1), kind: kindRequire})
	}
	return out
}

// mask returns a copy of a script with its comments blanked to spaces and
// the contents of its string and template literals filled with "_",
// newlines kept, so every offset and line is the source's. A template's
// ${…} is filled with it: an import written inside one is not read. A
// regular expression literal is not told from a division; a quote inside
// one can only mask code up to the next quote on its line.
func mask(src []byte) []byte {
	m := masker{src: src, out: append([]byte(nil), src...)}
	for i := 0; i < len(src); i++ {
		i = m.at(i)
	}
	return m.out
}

type masker struct{ src, out []byte }

// fill hides one byte of a literal, keeping a newline.
func (m masker) fill(i int) {
	if m.out[i] != '\n' {
		m.out[i] = '_'
	}
}

// blank hides src[i:j] as a comment, newlines kept.
func (m masker) blank(i, j int) {
	for ; i < j; i++ {
		if m.src[i] != '\n' {
			m.out[i] = ' '
		}
	}
}

// at masks whatever starts at i and returns the index of its last byte.
func (m masker) at(i int) int {
	src := m.src
	switch c := src[i]; {
	case c == '/' && i+1 < len(src) && src[i+1] == '/':
		end := bytes.IndexByte(src[i:], '\n')
		if end < 0 {
			end = len(src) - i
		}
		m.blank(i, i+end)
		return i + end - 1
	case c == '/' && i+1 < len(src) && src[i+1] == '*':
		end := len(src)
		if j := bytes.Index(src[i+2:], []byte("*/")); j >= 0 {
			end = i + 2 + j + 2
		}
		m.blank(i, end)
		return end - 1
	case c == '\'' || c == '"':
		return m.quoted(i)
	case c == '`':
		return m.template(i)
	}
	return i
}

// quoted fills a quoted string opening at i, which ends at its closing
// quote or, unterminated, at the end of the line.
func (m masker) quoted(i int) int {
	q := m.src[i]
	for i++; i < len(m.src) && m.src[i] != q && m.src[i] != '\n'; i++ {
		if m.src[i] == '\\' && i+1 < len(m.src) {
			m.fill(i)
			i++
		}
		m.fill(i)
	}
	return i
}

// template fills a template literal opening at i, and returns the index
// of its closing backquote; a ${…} inside it nests braces and further
// templates.
func (m masker) template(i int) int {
	src, depth := m.src, 0
	for i++; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			m.fill(i)
			i++
		case c == '`' && depth == 0:
			return i
		case c == '`':
			i = m.template(i)
		case c == '$' && i+1 < len(src) && src[i+1] == '{':
			depth++
			m.fill(i)
			i++
		case c == '{' || c == '}':
			depth += braceStep(c, depth)
		}
		m.fill(i)
	}
	return i
}

// braceStep is how a brace inside a template changes the depth of its
// ${…}: nothing outside one.
func braceStep(c byte, depth int) int {
	switch {
	case depth == 0:
		return 0
	case c == '{':
		return 1
	}
	return -1
}

// bindingNames reads "{ A, B as C, type D }" to the exported names it
// binds: A, B, D. The local alias after "as" is not the export.
func bindingNames(clause string) []string {
	clause = strings.Trim(clause, "{}")
	var names []string
	for _, part := range strings.Split(clause, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "type ")
		if i := strings.Index(part, " as "); i >= 0 {
			part = part[:i]
		}
		part = strings.TrimSpace(part)
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}
