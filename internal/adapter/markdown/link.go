// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/trendvidia/alidade/internal/adapter/ts"
	"github.com/trendvidia/alidade/internal/graph"
)

// Link resolves the mentions Extract collected against the merged graph
// and adds a documents edge for each that names exactly one node. It is
// the one step that reads another layer's nodes, which is why it runs
// after every adapter has reported and before the graph is built again.
//
// The rules are conservative by design, and each is a test:
//
//   - a mention is only ever a code span; prose is never scanned;
//   - a path — anything with a "/" or a known extension — matches a file
//     node by exact repo-relative path, or by basename when only one
//     file has it;
//   - a dotted name matches a Go declaration by "pkg.Decl" or "Type.Method",
//     a .proto declaration by full name or by its trailing segments, a
//     SQL entity by its name, a .pxf entry by its block path;
//   - a frontend or goasm file matches by path like any file, and a
//     generated export or a template by its name (#98);
//   - a bare name matches a node by exact name, unless it is all capitals
//     (a value, an environment variable, a constant such as INFERRED);
//   - an RPC procedure path, "pkg.v1.Service/Method" behind any URL
//     prefix, matches that rpc, in a code span or a fenced block — the one
//     thing read out of a fence (#158);
//   - a bare lowercase word naming a nested declaration — a field, an
//     rpc, a method, a .pxf block — needs its parent's name in the
//     section or its heading path (#167): `status`, `done`, `prod` are
//     usually a value or a concept, not the one field that shares the word;
//   - Markdown nodes are never targets: a doc that names another doc
//     does so by link.
//
// An edge into a declaration the Go layer marked as a test one is a test
// edge, and this package does not say so: the core derives it from the
// endpoints when the graph is built (ADR 001).
//
// In every case the match must be unique across the graph; two nodes
// answering one mention is an ambiguity, not an edge — except that a
// mention answered by exactly one production node and any number of test
// declarations is the production node's, as in a build without tests
// (#139).
func Link(b *graph.Builder, g *graph.Graph, rep *Report) {
	idx := buildIndex(g)
	seen := map[[2]graph.ID]bool{}
	for _, m := range rep.Mentions {
		var ids []graph.ID
		if m.Fence {
			ids = idx.byProcedure[m.Text]
		} else {
			ids = schemaOf(g, production(g, idx.bareFile(g, m.Section, m.Text, idx.lookup(m.Text))))
			if len(ids) == 0 {
				ids = idx.sqlExpression(m.Text)
			}
		}
		name := m.Text
		if len(ids) == 0 && !m.Fence {
			// A span written as a call or a signature, only when nothing
			// answers it whole (#7): the name it calls answers it.
			if callee := calleeOf(m.Text); callee != "" {
				name = callee
				ids = schemaOf(g, production(g, idx.lookup(callee)))
			}
		}
		if len(ids) == 1 && !parentNamed(g, name, ids[0], rep.Words[m.Section]) {
			rep.Unparented++
			continue
		}
		switch len(ids) {
		case 0:
			rep.Unmatched++
			continue
		case 1:
		default:
			rep.Ambiguous++
			continue
		}
		rep.Resolved++
		key := [2]graph.ID{m.Section, ids[0]}
		if seen[key] {
			continue
		}
		seen[key] = true
		_ = b.AddEdge(graph.Edge{From: m.Section, To: ids[0], Kind: graph.KindDocuments, Derivation: graph.Inferred,
			Attrs: map[string]string{"mention": m.Text}})
	}
}

// frontendExts is the frontend layer's extensions as a regexp
// alternation, so a mention of any file it reads is read as a path.
func frontendExts() string {
	var out []string
	for _, e := range slices.Sorted(maps.Keys(ts.Extensions)) {
		out = append(out, strings.TrimPrefix(e, "."))
	}
	return strings.Join(out, "|")
}

// An index answers mentions from the graph's nodes.
type index struct {
	byName   map[string][]graph.ID // node name
	byDotted map[string][]graph.ID // qualified forms, see add
	byPath   map[string][]graph.ID // file nodes by repo-relative path
	byBase   map[string][]graph.ID // file nodes by basename
	// byProcedure holds rpcs by procedure path, "pkg.Service/Method".
	byProcedure map[string][]graph.ID
	// The forms tried only when the ones above answer nothing (#189), so
	// no mention that resolved before can become ambiguous: a method as
	// pkg.Method, its receiver left out; a Go package by import path or
	// by the directory its declarations are in.
	byPkgMethod, byPackagePath map[string][]graph.ID
	// byField holds Go struct fields by bare name (#193): a field's name
	// is often an everyday one (ID, Name, limit), and answering it first
	// would make a mention that resolved before ambiguous.
	byField map[string][]graph.ID
	// byFrontend holds the frontend layer's hand-written declarations
	// (#215), and the Python layer's (#237): a module-level one by name, a
	// member as Holder.member. The
	// type checker made thousands of them at once — a field per property
	// of every interface — and indexed with the rest they made mentions
	// that resolved before ambiguous (steward's `behavioral`, the proto
	// field, met a TypeScript interface's), so they answer last.
	byFrontend map[string][]graph.ID
	// byPathSuffix holds Go packages by every ending of their import path
	// of two segments or more, each with a leading "/"; byCommand holds a
	// main package by its directory's name, as its command is called
	// (#7). Both answer only when nothing else does.
	byPathSuffix, byCommand map[string][]graph.ID
}

func newIndex() *index {
	return &index{byName: map[string][]graph.ID{}, byDotted: map[string][]graph.ID{}, byPath: map[string][]graph.ID{},
		byBase: map[string][]graph.ID{}, byProcedure: map[string][]graph.ID{},
		byPkgMethod: map[string][]graph.ID{}, byPackagePath: map[string][]graph.ID{}, byField: map[string][]graph.ID{},
		byFrontend: map[string][]graph.ID{}, byPathSuffix: map[string][]graph.ID{}, byCommand: map[string][]graph.ID{}}
}

func (ix *index) put(m map[string][]graph.ID, key string, id graph.ID) {
	if key != "" {
		m[key] = append(m[key], id)
	}
}

// buildIndex builds the lookup tables from every non-Markdown node.
func buildIndex(g *graph.Graph) *index {
	ix := newIndex()
	for n := range g.Nodes() {
		scheme, _ := n.ID.Scheme()
		if scheme == graph.SchemeMarkdown {
			continue
		}
		ix.add(n, scheme)
	}
	return ix
}

// add records a node under every form a code span might write it in.
func (ix *index) add(n graph.Node, scheme graph.Scheme) {
	body := strings.TrimPrefix(string(n.ID), string(scheme)+"://")
	switch scheme {
	case graph.SchemeGo:
		ix.addGo(n, body)
	case graph.SchemeProto:
		ix.addProto(n, body)
	case graph.SchemeSQL:
		ix.addSQL(n, body)
	case graph.SchemePXF:
		ix.addPXF(n, body)
	case graph.SchemeTS, graph.SchemeGoasm, graph.SchemePy:
		ix.addFileDecl(n, body)
	}
}

// addGo: a package by name; a declaration by name, by Type.Method, and
// by pkg.Decl or pkg.Type.Method with the package's last path segment.
func (ix *index) addGo(n graph.Node, body string) {
	if n.Kind == graph.KindPackageTests {
		// A package's test files' node (#128) carries the package's
		// name and is not what a mention of the package means; indexed,
		// it made every such mention ambiguous with tests loaded (#139).
		return
	}
	if n.Kind == graph.KindField {
		ix.put(ix.byField, n.Name, n.ID)
	} else {
		ix.put(ix.byName, n.Name, n.ID)
	}
	pkg, decl, hasDecl := strings.Cut(body, "#")
	if !hasDecl {
		if n.Kind == graph.KindPackage {
			ix.put(ix.byPackagePath, pkg, n.ID)
			ix.addPackageForms(n, pkg)
		}
		return
	}
	ix.put(ix.byDotted, decl, n.ID)
	last := pkg[strings.LastIndexByte(pkg, '/')+1:]
	ix.put(ix.byDotted, last+"."+decl, n.ID)
	if n.Kind == graph.KindMethod {
		_, method, _ := strings.Cut(decl, ".")
		ix.put(ix.byPkgMethod, last+"."+method, n.ID)
	}
	if n.Pos.File != "" {
		ix.putOnce(ix.byPackagePath, path.Dir(n.Pos.File), graph.ID("go://"+pkg))
	}
}

// addPackageForms records a package under the endings of its import path
// and, for a main package, the name of its command (#7).
func (ix *index) addPackageForms(n graph.Node, pkg string) {
	segs := strings.Split(pkg, "/")
	for i := 1; i < len(segs)-1; i++ {
		ix.put(ix.byPathSuffix, "/"+strings.Join(segs[i:], "/"), n.ID)
	}
	if n.Name == "main" && len(segs) > 1 {
		ix.put(ix.byCommand, segs[len(segs)-1], n.ID)
	}
}

// putOnce records id under key unless it is there already.
func (ix *index) putOnce(m map[string][]graph.ID, key string, id graph.ID) {
	if !slices.Contains(m[key], id) {
		ix.put(m, key, id)
	}
}

// addProto: a file by path; a declaration by name, by full name, and by
// every suffix of two or more segments.
func (ix *index) addProto(n graph.Node, body string) {
	if rel, ok := strings.CutPrefix(body, "file/"); ok {
		ix.file(rel, n.ID)
		return
	}
	ix.put(ix.byName, n.Name, n.ID)
	if n.Kind == graph.KindRPC {
		if i := strings.LastIndexByte(body, '.'); i > 0 {
			ix.put(ix.byProcedure, body[:i]+"/"+body[i+1:], n.ID)
		}
	}
	segs := strings.Split(body, ".")
	for i := range segs {
		if len(segs)-i >= 2 || i == 0 {
			ix.put(ix.byDotted, strings.Join(segs[i:], "."), n.ID)
		}
	}
}

// addSQL: an entity by name and by its qualified name (app.users,
// users.email); an unqualified table or query by that name too.
func (ix *index) addSQL(n graph.Node, body string) {
	ix.put(ix.byName, n.Name, n.ID)
	if pkg, name, ok := strings.Cut(strings.TrimPrefix(body, "query/"), "#"); ok {
		// A query, sql://query/<package>#<Name> (#118): by its name, and
		// by the package's last segment and the name, as Go writes
		// db.Queries' method's package.
		ix.put(ix.byDotted, pkg[strings.LastIndexByte(pkg, '/')+1:]+"."+name, n.ID)
		return
	}
	if _, name, ok := strings.Cut(body, "/"); ok {
		ix.put(ix.byDotted, name, n.ID)
		if !strings.Contains(name, ".") {
			ix.put(ix.byName, name, n.ID)
		}
	}
}

// addPXF: a file by path; an entry by name and by block path.
func (ix *index) addPXF(n graph.Node, body string) {
	rel, blockPath, hasPath := strings.Cut(body, "#")
	if !hasPath {
		ix.file(rel, n.ID)
		return
	}
	ix.put(ix.byDotted, blockPath, n.ID)
	ix.put(ix.byName, n.Name, n.ID)
}

// addFileDecl: the frontend and goasm layers' file nodes by path, and
// the declarations in them — a generated export, a template — by name
// (#98). An external package (ts://@pkg/…, #124) is not a file of the
// tree.
func (ix *index) addFileDecl(n graph.Node, body string) {
	if strings.HasPrefix(body, "@pkg/") && n.Attrs["external"] == "true" {
		return
	}
	rel, decl, hasDecl := strings.Cut(body, "#")
	switch {
	case !hasDecl:
		ix.file(rel, n.ID)
	case (strings.HasPrefix(string(n.ID), "ts://") || strings.HasPrefix(string(n.ID), "py://")) && !n.Generated:
		// A hand-written TypeScript or Python declaration answers a
		// mention only when no other layer's does (#215, #237): a name a
		// Go repository's script shares with its Go code is the Go one.
		ix.put(ix.byFrontend, decl, n.ID)
	default:
		ix.put(ix.byName, n.Name, n.ID)
	}
}

// file indexes a file node by its path as a link or a mention writes it:
// the ID's path with its escape read back (#124).
func (ix *index) file(rel string, id graph.ID) {
	rel = graph.FilePath(rel)
	ix.put(ix.byPath, rel, id)
	ix.put(ix.byBase, rel[strings.LastIndexByte(rel, '/')+1:], id)
}

var (
	bareRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	dottedRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	extRE    = regexp.MustCompile(`\.(md|proto|pxf|goasm|py|` + frontendExts() + `)$`)
)

// lookup returns every node a mention names, deduplicated. The caller
// treats anything but exactly one as no edge.
func (ix *index) lookup(text string) []graph.ID {
	text = strings.TrimSpace(text)
	var ids []graph.ID
	switch {
	case text == "" || len(text) < 3:
		return nil
	case strings.Contains(text, "/") || extRE.MatchString(text):
		ids = ix.pathLookup(text)
	case dottedRE.MatchString(text):
		ids = ix.dotted(text)
	case bareRE.MatchString(text) && strings.ToUpper(text) != text, hyphenRE.MatchString(text):
		ids = ix.bare(text)
	}
	return uniq(ids)
}

// dotted answers a qualified name: a declaration path first, then the
// later forms (#189), the frontend's last (#215).
func (ix *index) dotted(text string) []graph.ID {
	if ids := ix.byDotted[text]; len(ids) > 0 {
		return ids
	}
	if ids := ix.byPkgMethod[text]; len(ids) > 0 {
		return ids
	}
	return ix.byFrontend[text]
}

// bare answers a bare name: a declaration's name first, then a field's,
// then a frontend declaration's. A field or frontend declaration named by
// one lowercase word (timeout, main) is the word the docs use for a
// value, not the declaration (#193).
func (ix *index) bare(text string) []graph.ID {
	if ids := ix.byName[text]; len(ids) > 0 {
		return ids
	}
	if strings.Contains(text, "-") {
		// A command's name, protoc-gen-chameleon-go (#7): only one joined
		// by hyphens, since a single word is as often the project's or
		// the tool's own name as its main package.
		if ids := ix.byCommand[text]; len(ids) > 0 {
			return ids
		}
	}
	if lowerWordRE.MatchString(text) {
		return nil
	}
	if ids := ix.byField[text]; len(ids) > 0 {
		return ids
	}
	return ix.byFrontend[text]
}

// hyphenRE is a name joined by hyphens, as a .pxf block or a frontend
// export may be named: wrong-audience (#189).
var hyphenRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(-[A-Za-z0-9_]+)+$`)

// pathLookup answers a mention written as a path: an rpc's procedure, a
// file, else a Go package by import path or directory (#189).
func (ix *index) pathLookup(text string) []graph.ID {
	if ids := ix.procedure(text); len(ids) > 0 {
		return ids
	}
	rel := strings.TrimPrefix(text, "./")
	if ids := ix.byPath[rel]; len(ids) > 0 {
		return ids
	}
	if ids := ix.byBase[text]; len(ids) > 0 {
		return ids
	}
	rel = strings.TrimSuffix(rel, "/")
	if ids := ix.byPackagePath[rel]; len(ids) > 0 {
		return ids
	}
	return ix.packageSuffix(rel)
}

// packageSuffix answers a Go package written as the end of its import
// path, two segments or more — chameleon/sops for
// github.com/trendvidia/chameleon/sops (#7) — when no file, directory or
// whole import path does.
func (ix *index) packageSuffix(rel string) []graph.ID {
	if !strings.Contains(rel, "/") || strings.ContainsAny(rel, " .") {
		return nil
	}
	return ix.byPathSuffix["/"+rel]
}

// callRE is a span written as a call or a signature: a name, perhaps
// dotted, perhaps with type arguments, then "(" — Engine.Failed(),
// Validate(msg, present) error. methodValueRE is a method reached through
// a value or a receiver: UnmarshalOptions{SkipValidate: true}.Unmarshal,
// (*Resolver[T]).Resolve(ctx, req).
var (
	callRE        = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)(?:\[[^\]]*\])?\(`)
	methodValueRE = regexp.MustCompile(`^(?:\(\*?([A-Za-z_][A-Za-z0-9_.]*)(?:\[[^\]]*\])?\)|([A-Za-z_][A-Za-z0-9_.]*)\{[^{}]*\})\.([A-Za-z_][A-Za-z0-9_]*)(?:$|\()`)
)

// calleeOf is the name a span written as a call, a signature or a method
// value names (#7), or "": Engine.Failed for Engine.Failed(),
// UnmarshalOptions.Unmarshal for UnmarshalOptions{SkipValidate: true}.Unmarshal.
func calleeOf(text string) string {
	text = strings.TrimSpace(text)
	if m := methodValueRE.FindStringSubmatch(text); m != nil {
		return m[1] + m[2] + "." + m[3]
	}
	if m := callRE.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// qualifiedRE is a dotted name inside a longer span.
var qualifiedRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+`)

// sqlExpression answers a span written as a SQL expression or clause —
// app.org_subscriptions.seat_limit IS NOT NULL — by the one table or
// column of the graph its qualified names resolve to (#192), when
// nothing else answers the span whole.
func (ix *index) sqlExpression(text string) []graph.ID {
	if !strings.ContainsAny(strings.TrimSpace(text), " \t") {
		return nil
	}
	seen := map[graph.ID]bool{}
	var out []graph.ID
	for _, q := range qualifiedRE.FindAllString(text, -1) {
		for _, id := range ix.byDotted[q] {
			if strings.HasPrefix(string(id), "sql://") && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// bareFile settles a file named by its base name alone (#191): the file
// of that name in the mention's own directory when there is one, a
// sibling being what a README beside it means; else, for a .pxf file,
// nothing. A configuration file's name is the name of its convention —
// governance.pxf, chain.pxf — and measured on the corpus a bare one
// meant the repository's own file 3 times in 15: the docs that name it
// describe the format, any repository's file. Code files keep their
// links: 13 of 13 sampled bare .ts and .proto names meant that file.
func (ix *index) bareFile(g *graph.Graph, section graph.ID, text string, ids []graph.ID) []graph.ID {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "/") || !extRE.MatchString(text) {
		return ids
	}
	doc, _, _ := strings.Cut(strings.TrimPrefix(string(section), "md://"), "#")
	dir := path.Dir(graph.FilePath(doc))
	var sibling []graph.ID
	for _, id := range ix.byBase[text] {
		if n, ok := g.Node(id); ok && path.Dir(n.Pos.File) == dir {
			sibling = append(sibling, id)
		}
	}
	switch {
	case len(sibling) == 1:
		return sibling
	case strings.HasSuffix(text, ".pxf"):
		return nil
	}
	return ids
}

// procedureRE is an RPC procedure path, the route Connect and gRPC give
// a method: a dotted service name and a method name, after the start, a
// "/" or a space — "/api/pkg.v1.Service/Method" or "pkg.v1.Service/Method".
var procedureRE = regexp.MustCompile(`(?:^|[/\s"'(])([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+/[A-Za-z_][A-Za-z0-9_]*)\b`)

// procedures lists the procedure paths in a line, as "pkg.Service/Method".
func procedures(line string) []string {
	var out []string
	for _, m := range procedureRE.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// procedure is the rpc a code span's one procedure path names.
func (ix *index) procedure(text string) []graph.ID {
	ps := procedures(text)
	if len(ps) != 1 {
		return nil
	}
	return ix.byProcedure[ps[0]]
}

// lowerWordRE is a bare all-lowercase word, or words joined by hyphens:
// no capital, no underscore, no dot — the shape a value or an ordinary
// word has.
var lowerWordRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// parentNamed reports whether a mention may name the node it resolved
// to. A bare lowercase word naming a nested declaration must find the
// declaration's parent among the section's words: measured on the
// corpus (#167), 22 of 39 sampled such links were wrong — a status value
// (`done`, `failed`), an environment (`prod`), a channel or command
// (`ticker`, `status`) — and requiring the parent kept 11 of the 17
// right ones and 1 of the 22 wrong. Anything else passes.
func parentNamed(g *graph.Graph, text string, id graph.ID, words map[string]bool) bool {
	n, ok := g.Node(id)
	if !ok {
		return true
	}
	if !lowerWordRE.MatchString(text) && !bareGoField(n, text) {
		return true
	}
	parent := parentName(n)
	if parent == "" {
		return true
	}
	return words[strings.ToLower(parent)]
}

// bareGoField reports whether a mention names a Go struct field by its
// bare name. A field's name is the everyday word of its type's domain
// (Funcs, unknown, Halted), and a bare one is answered only from the
// fields' fallback index; measured on the corpus (#193), a sample of
// such links held 3 wrong of about 25 until the field's type was
// required among the section's words, as a lowercase word's parent is.
func bareGoField(n graph.Node, text string) bool {
	scheme, _ := n.ID.Scheme()
	return scheme == graph.SchemeGo && n.Kind == graph.KindField && !strings.Contains(text, ".")
}

// parentName is the name of a nested declaration's parent — a field's
// or rpc's message or service, a Go method's or field's type, a .pxf block's
// enclosing block or, at the top, its file — or "" for a node that is
// not nested.
func parentName(n graph.Node) string {
	scheme, _ := n.ID.Scheme()
	body := strings.TrimPrefix(string(n.ID), string(scheme)+"://")
	switch {
	case scheme == graph.SchemeProto && (n.Kind == graph.KindField || n.Kind == graph.KindRPC):
		segs := strings.Split(body, ".")
		return segs[len(segs)-2]
	case scheme == graph.SchemeGo && (n.Kind == graph.KindMethod || n.Kind == graph.KindField):
		_, decl, _ := strings.Cut(body, "#")
		recv, _, _ := strings.Cut(decl, ".")
		return recv
	case scheme == graph.SchemePXF && (n.Kind == graph.KindBlock || n.Kind == graph.KindField):
		file, path, _ := strings.Cut(body, "#")
		if segs := strings.Split(path, "."); len(segs) >= 2 {
			return segs[len(segs)-2]
		}
		base := file[strings.LastIndexByte(file, '/')+1:]
		return strings.TrimPrefix(strings.TrimSuffix(base, ".pxf"), ".")
	}
	return ""
}

// schemaOf drops, from several candidates, the generated nodes generated
// from one of the others (#7), and the .pxf entries that set one of the
// others — a field of a document set against a proto field that
// the mention also names (#190, the owner's decision): such a mention
// names the schema, and its instances answer the same name only because
// they set it. blocked_module_globs is the proto field, not the six
// documents that give it a value.
func schemaOf(g *graph.Graph, ids []graph.ID) []graph.ID {
	if len(ids) < 2 {
		return ids
	}
	in := map[graph.ID]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var out []graph.ID
	for _, id := range ids {
		if !setsOneOf(g, id, in) && !generatedFromOneOf(g, id, in) {
			out = append(out, id)
		}
	}
	return out
}

// generatedFromOneOf reports whether a generated node is generated from
// another candidate: a proto message and the Go type protoc-gen-go made
// from it are one answer, the schema (#7), as a proto field and the .pxf
// entries that set it are (#190).
func generatedFromOneOf(g *graph.Graph, id graph.ID, ids map[graph.ID]bool) bool {
	for _, e := range g.In(id) {
		if e.Kind == graph.KindGenerates && ids[e.From] && e.From != id {
			return true
		}
	}
	return false
}

// setsOneOf reports whether a .pxf entry has a references edge to a
// proto field among ids.
func setsOneOf(g *graph.Graph, id graph.ID, ids map[graph.ID]bool) bool {
	if !strings.HasPrefix(string(id), "pxf://") {
		return false
	}
	for _, e := range g.Out(id) {
		if e.Kind == graph.KindReferences && ids[e.To] && strings.HasPrefix(string(e.To), "proto://") {
			return true
		}
	}
	return false
}

// production narrows several candidates to the one production node among
// them, when there is exactly one: a mention a build without tests
// resolves resolves the same way when test declarations of the same name
// are loaded too (#139, the owner's decision). Docs name production code
// — every such mention measured on govm, voya, drover and gameflow named
// the production declaration, the test one a fake or helper sharing its
// name.
func production(g *graph.Graph, ids []graph.ID) []graph.ID {
	if len(ids) < 2 {
		return ids
	}
	var prod []graph.ID
	for _, id := range ids {
		if n, ok := g.Node(id); ok && !n.Test {
			prod = append(prod, id)
		}
	}
	if len(prod) == 1 {
		return prod
	}
	return ids
}

func uniq(ids []graph.ID) []graph.ID {
	seen := map[graph.ID]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
