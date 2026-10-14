// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// The PXF oracle (#187) reads each .pxf file's directives from its raw
// text by the protowire specification (protowire/docs/grammar.ebnf and
// draft-trendvidia-protowire.md), sharing no code with protowire-go's
// parser, which the layer walks:
//
//   - "@type X" names the body's message type;
//   - "@dataset X (cols)" names the row type; "@dataset (cols)" none;
//   - "@entry" names a type by its second prefix, or by its one prefix
//     when that is dotted (the spec's disambiguation rule);
//   - "@proto" declares a schema and names none;
//   - any other directive follows the spec's stated convention, one
//     prefix naming the inner block's type.
//
// And the "${path}" interpolations in its strings, "${env}" and
// "${ctx.*}" excepted, which name a context rather than a field.

// PXF is the oracle's view of a repository's .pxf files.
type PXF struct {
	// Types are [file, message] pairs: the files are repository-relative
	// paths, the messages full names.
	Types [][2]string
	// Refs are [file, path] pairs, one per distinct "${path}".
	Refs  [][2]string
	Files int
	// FileTypes is each document's own @type, by file (#190).
	FileTypes map[string]string
	// Unread are directives the reader could not place, as "file: text".
	Unread []string
}

// PXFFiles reads every .pxf file under root, testdata left out as the
// layer leaves it.
func PXFFiles(root string) (*PXF, error) {
	p := &PXF{FileTypes: map[string]string{}}
	skip := skipOf(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root && skip.SkipsDir(root, path) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".tmp", "testdata":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".pxf") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		p.file(filepath.ToSlash(rel), src)
		return nil
	})
	return p, err
}

func (p *PXF) file(rel string, src []byte) {
	p.Files++
	toks := lexPXF(string(src))
	for _, d := range topDirectives(toks) {
		typ, ok := d.typeName()
		if !ok {
			p.Unread = append(p.Unread, rel+": @"+d.name+" "+strings.Join(d.prefixes, " "))
			continue
		}
		if typ != "" {
			p.Types = append(p.Types, [2]string{rel, typ})
			if d.name == "type" && p.FileTypes != nil {
				p.FileTypes[rel] = typ
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range toks {
		if t.kind != tokString {
			continue
		}
		for _, m := range interpolationRE.FindAllStringSubmatch(t.text, -1) {
			if seg, _, _ := strings.Cut(m[1], "."); seg == "env" || seg == "ctx" || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			p.Refs = append(p.Refs, [2]string{rel, m[1]})
		}
	}
}

// interpolationRE is a "${path}" in a string.
var interpolationRE = regexp.MustCompile(`\$\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}`)

type tokKind int

const (
	tokIdent tokKind = iota
	tokAt
	tokOpen  // { or (
	tokClose // } or )
	tokString
	tokNewline
	tokOther
)

type pxfTok struct {
	kind tokKind
	text string
}

// lexPXF cuts PXF text into the tokens the directive reader needs:
// identifiers (dotted ones whole), "@", brackets, strings (simple and
// triple-quoted, contents kept), newlines; comments are dropped.
func lexPXF(s string) []pxfTok {
	l := &pxfLexer{s: s}
	for l.i < len(s) && l.step() {
	}
	return l.out
}

type pxfLexer struct {
	s   string
	i   int
	out []pxfTok
}

// step reads one token or skips one span; false ends the text (an
// unterminated comment or string).
func (l *pxfLexer) step() bool {
	rest := l.s[l.i:]
	switch c := rest[0]; {
	case c == '#' || strings.HasPrefix(rest, "//"):
		l.skipLine()
	case strings.HasPrefix(rest, "/*"):
		return l.span("/*", "*/", false)
	case strings.HasPrefix(rest, `"""`):
		return l.span(`"""`, `"""`, true)
	case c == '"':
		l.simpleString()
	case isIdentStart(c):
		l.ident()
	default:
		l.punct(c)
	}
	return true
}

func (l *pxfLexer) skipLine() {
	for l.i < len(l.s) && l.s[l.i] != '\n' {
		l.i++
	}
}

// span reads from open to close: a block comment, or a triple-quoted
// string kept as a token. Its newlines are kept as tokens.
func (l *pxfLexer) span(open, close string, keep bool) bool {
	end := strings.Index(l.s[l.i+len(open):], close)
	if end < 0 {
		return false
	}
	body := l.s[l.i+len(open) : l.i+len(open)+end]
	if keep {
		l.out = append(l.out, pxfTok{tokString, body})
	}
	l.out = append(l.out, newlines(body)...)
	l.i += len(open) + end + len(close)
	return true
}

func (l *pxfLexer) simpleString() {
	j := l.i + 1
	for j < len(l.s) && l.s[j] != '"' && l.s[j] != '\n' {
		if l.s[j] == '\\' {
			j++
		}
		j++
	}
	l.out = append(l.out, pxfTok{tokString, l.s[l.i+1 : min(j, len(l.s))]})
	l.i = j + 1
}

func (l *pxfLexer) ident() {
	j := l.i
	for j < len(l.s) && (isIdentStart(l.s[j]) || l.s[j] >= '0' && l.s[j] <= '9' || l.s[j] == '.') {
		j++
	}
	l.out = append(l.out, pxfTok{tokIdent, l.s[l.i:j]})
	l.i = j
}

// punct reads one character: a newline, "@", a bracket, or another
// character; blanks are skipped.
func (l *pxfLexer) punct(c byte) {
	l.i++
	switch c {
	case ' ', '\t', '\r':
	case '\n':
		l.out = append(l.out, pxfTok{tokNewline, ""})
	case '@':
		l.out = append(l.out, pxfTok{tokAt, "@"})
	case '{', '(', '[':
		l.out = append(l.out, pxfTok{tokOpen, string(c)})
	case '}', ')', ']':
		l.out = append(l.out, pxfTok{tokClose, string(c)})
	default:
		l.out = append(l.out, pxfTok{tokOther, string(c)})
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// newlines keeps the line count of a skipped span.
func newlines(s string) []pxfTok {
	out := make([]pxfTok, strings.Count(s, "\n"))
	for i := range out {
		out[i] = pxfTok{tokNewline, ""}
	}
	return out
}

type pxfDirective struct {
	name     string
	prefixes []string
	paren    bool // a "(" follows the prefixes: a dataset's column list
}

// topDirectives reads the directives at the top level of a document: an
// "@" outside every bracket, its name, and the identifiers after it on
// its line before a bracket.
func topDirectives(toks []pxfTok) []pxfDirective {
	var out []pxfDirective
	depth := 0
	for i := 0; i < len(toks); i++ {
		switch toks[i].kind {
		case tokOpen:
			depth++
		case tokClose:
			depth--
		case tokAt:
			if depth != 0 || i+1 >= len(toks) || toks[i+1].kind != tokIdent {
				continue
			}
			d := pxfDirective{name: toks[i+1].text}
			j := i + 2
			for ; j < len(toks) && toks[j].kind == tokIdent; j++ {
				d.prefixes = append(d.prefixes, toks[j].text)
			}
			d.paren = j < len(toks) && toks[j].kind == tokOpen && toks[j].text == "("
			out = append(out, d)
			i = j - 1
		}
	}
	return out
}

// typeName is the type a directive names by the specification, "" for
// none; ok is false for a shape the specification does not give.
func (d pxfDirective) typeName() (string, bool) {
	switch d.name {
	case "type":
		if len(d.prefixes) == 1 {
			return d.prefixes[0], true
		}
	case "dataset":
		if len(d.prefixes) <= 1 && d.paren {
			return strings.Join(d.prefixes, ""), true
		}
	case "proto":
		return "", true
	case "entry":
		switch len(d.prefixes) {
		case 0:
			return "", true
		case 1:
			if strings.Contains(d.prefixes[0], ".") {
				return d.prefixes[0], true
			}
			return "", true
		case 2:
			return d.prefixes[1], true
		}
	default:
		switch len(d.prefixes) {
		case 0:
			return "", true
		case 1:
			return d.prefixes[0], true
		}
	}
	return "", false
}

// A PXFResult compares the graph's PXF references with the oracle's.
// Counts are [oracle, graph has].
type PXFResult struct {
	Files                    int
	Types, Refs              [2]int
	MissingTypes, ExtraTypes []string
	MissingRefs, ExtraRefs   []string
	Undeclared, Unread       []string
	// Unconfirmed counts the types no declaration set could confirm:
	// protoc compiled nothing in the repository.
	Unconfirmed int
}

// ComparePXF measures g's references edges out of .pxf files. A type is
// compared by file: the graph has it when some node of the file
// references the message, kept or dropped. declared, when not nil, is
// the set of message full names protoc compiled in the repository; a
// type it lacks is listed as undeclared, a name the repository's own
// schemas do not confirm.
func ComparePXF(g *graph.Graph, p *PXF, declared map[string]bool) *PXFResult {
	r := &PXFResult{Files: p.Files, Unread: p.Unread}
	types, refs := pxfEdges(g)
	for _, t := range p.Types {
		r.Types[0]++
		if types[t] {
			r.Types[1]++
			delete(types, t)
		} else {
			r.MissingTypes = append(r.MissingTypes, t[0]+" -> "+t[1])
		}
		switch {
		case declared == nil:
			r.Unconfirmed++
		case !declared[t[1]]:
			r.Undeclared = append(r.Undeclared, t[0]+" -> "+t[1])
		}
	}
	for t := range types {
		r.ExtraTypes = append(r.ExtraTypes, t[0]+" -> "+t[1])
	}
	for _, ref := range p.Refs {
		r.Refs[0]++
		if refs[ref] {
			r.Refs[1]++
			delete(refs, ref)
		} else {
			r.MissingRefs = append(r.MissingRefs, ref[0]+": ${"+ref[1]+"}")
		}
	}
	for ref := range refs {
		r.ExtraRefs = append(r.ExtraRefs, ref[0]+": ${"+ref[1]+"}")
	}
	sortAll(&r.MissingTypes, &r.ExtraTypes, &r.MissingRefs, &r.ExtraRefs, &r.Undeclared, &r.Unread)
	return r
}

// pxfEdges reads the graph's references out of .pxf files, kept and
// dropped: to a message as [file, full name], and to a node of the same
// file as [file, block path].
func pxfEdges(g *graph.Graph) (types, refs map[[2]string]bool) {
	types, refs = map[[2]string]bool{}, map[[2]string]bool{}
	note := func(e graph.Edge) {
		if e.Kind != graph.KindReferences || !strings.HasPrefix(string(e.From), "pxf://") {
			return
		}
		file, _, _ := strings.Cut(strings.TrimPrefix(string(e.From), "pxf://"), "#")
		switch to := string(e.To); {
		case strings.HasPrefix(to, "proto://") && e.Derivation == graph.Extracted:
			// A type a directive writes; an entry's edge to the field it
			// sets is RESOLVED or DECLARED, the schema oracle's (#190).
			types[[2]string{file, strings.TrimPrefix(to, "proto://")}] = true
		case strings.HasPrefix(to, "pxf://"+file+"#") && e.Derivation == graph.Extracted:
			refs[[2]string{file, strings.TrimPrefix(to, "pxf://"+file+"#")}] = true
		}
	}
	for e := range g.Edges() {
		note(e)
	}
	for _, d := range g.Dropped() {
		note(d.Edge)
	}
	return types, refs
}

func (r *PXFResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pxf files %d, type references %d / %d, %d extra, %d not declared by the repository's protos, %d unconfirmed (protoc compiled nothing); ${} references %d / %d, %d extra; directives not read %d\n",
		r.Files, r.Types[1], r.Types[0], len(r.ExtraTypes), len(r.Undeclared), r.Unconfirmed, r.Refs[1], r.Refs[0], len(r.ExtraRefs), len(r.Unread))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing type", r.MissingTypes}, {"extra type", r.ExtraTypes}, {"undeclared type", r.Undeclared}, {"missing ref", r.MissingRefs}, {"extra ref", r.ExtraRefs}, {"unread directive", r.Unread}} {
		for i, s := range l.items {
			if i == 8 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-8)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}

// declaredMessages is the set of message full names in a proto oracle.
func declaredMessages(p *Proto) map[string]bool {
	if p == nil || len(p.Files) == 0 {
		return nil
	}
	out := map[string]bool{}
	for id, kind := range p.Decls {
		if name, ok := strings.CutPrefix(string(id), "proto://"); ok && kind == graph.KindMessage {
			out[name] = true
		}
	}
	return out
}

// A CensusResult is the chain census read against a graph.
type CensusResult struct {
	Links                       int
	Gone, EnvsChanged, Unjudged []string
}

// CompareCensus checks every judged chain link against the graph — there,
// and loaded in the judged environments — and lists the graph's chain
// links from the census's chain files that nobody judged.
func CompareCensus(g *graph.Graph, s *Sample) *CensusResult {
	r := &CensusResult{Links: len(s.Edges)}
	judged := map[[2]graph.ID]bool{}
	chains := map[graph.ID]bool{}
	for _, l := range s.Edges {
		judged[[2]graph.ID{l.From, l.To}] = true
		chains[l.From] = true
		e, ok := findEdge(g, l.From, graph.KindReferences, l.To)
		switch {
		case !ok:
			r.Gone = append(r.Gone, string(l.From)+" -> "+string(l.To))
		case e.Attrs["envs"] != l.Envs:
			r.EnvsChanged = append(r.EnvsChanged, fmt.Sprintf("%s -> %s: envs %q, judged %q", l.From, l.To, e.Attrs["envs"], l.Envs))
		}
	}
	for e := range g.Edges() {
		if e.Kind == graph.KindReferences && chains[e.From] && strings.HasPrefix(string(e.To), "pxf://") && !judged[[2]graph.ID{e.From, e.To}] {
			r.Unjudged = append(r.Unjudged, string(e.From)+" -> "+string(e.To))
		}
	}
	sortAll(&r.Gone, &r.EnvsChanged, &r.Unjudged)
	return r
}

// findEdge is the edge of a kind between two nodes, if the graph has it.
func findEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func (r *CensusResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain census: %d / %d links as judged, %d gone, %d with other environments, %d not judged\n",
		r.Links-len(r.Gone)-len(r.EnvsChanged), r.Links, len(r.Gone), len(r.EnvsChanged), len(r.Unjudged))
	for _, l := range []struct {
		name  string
		items []string
	}{{"gone", r.Gone}, {"environments changed", r.EnvsChanged}, {"not judged", r.Unjudged}} {
		for _, s := range l.items {
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
