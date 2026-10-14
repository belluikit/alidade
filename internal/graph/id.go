// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// An ID is a node's identity: the fully-qualified path of its declaration,
// prefixed by its layer scheme (ADR 002). It is an opaque, comparable,
// sortable string — compare IDs, never take them apart.
//
// Source position is deliberately absent. A node carries file, line and
// column for navigation, and none of them key anything: two builds of an
// unchanged tree must produce byte-identical IDs, and a build after an
// unrelated edit in the same file must not change a node's ID.
//
// # Grammar
//
// Every ID is scheme "://" body. The body is the layer's own declaration
// path, and only its shape is validated here — a segment may not contain
// "#" or "/" (both structural), whitespace, or a control character.
//
//	go://<import path>[#<decl>]      decl is dotted: Type.Method, Type, Func
//	proto://<fully-qualified name>   dotted descriptor path
//	proto://file/<repo-relative path>
//	pxf://<repo-relative path>#<block path>
//	sql://table/<name>  sql://column/<table.name>  sql://query/<package>#<name>
//	md://<repo-relative path>#<heading>[.<heading>...]
//
// Worked examples, matching ADR 002:
//
//	go://github.com/trendvidia/protolsp/analysis#Workspace.ReferencesFor
//	proto://protolsp.v1.WorkspaceConfig.registry
//	pxf://config/chain.pxf#registry.address
//	sql://table/users
//	md://docs/adr/002-node-identity.md#decision
//
// Every fragment after "#" is a dotted path, whatever the scheme: a Go
// declaration, a .pxf block path, a Markdown heading path (ADR 002 rules 7
// and 8). All of it is pinned byte-for-byte in TestMintedIDsAreStable so a
// change shows up as a failing test rather than as a silent re-keying.
type ID string

// Scheme names the layer a node's identity came from. It is the one part of
// the syntax common to all eight layers; everything after "://" belongs to
// the layer.
type Scheme string

// The eight schemes: ADR 003's five, goasm by its 2026-09-11 amendment,
// ts by ADR 007 and py by ADR 008. A ninth means a new layer, which means
// an ADR before this constant block grows.
const (
	SchemeGo       Scheme = "go"
	SchemeProto    Scheme = "proto"
	SchemePXF      Scheme = "pxf"
	SchemeSQL      Scheme = "sql"
	SchemeMarkdown Scheme = "md"
	SchemeGoasm    Scheme = "goasm"
	SchemeTS       Scheme = "ts"
	SchemePy       Scheme = "py"
)

const schemeSep = "://"

// String returns the ID as written.
func (id ID) String() string { return string(id) }

// Scheme returns the layer that minted the ID, and whether it is one of the
// eight known schemes. An unknown scheme is a sign an adapter built an ID by
// hand instead of asking the core for one.
func (id ID) Scheme() (Scheme, bool) {
	i := strings.Index(string(id), schemeSep)
	if i <= 0 {
		return "", false
	}
	s := Scheme(id[:i])
	switch s {
	case SchemeGo, SchemeProto, SchemePXF, SchemeSQL, SchemeMarkdown, SchemeGoasm, SchemeTS, SchemePy:
		return s, len(id) > i+len(schemeSep)
	}
	return s, false
}

// Valid reports whether the ID has a known scheme and a non-empty body.
func (id ID) Valid() bool {
	_, ok := id.Scheme()
	return ok
}

// A Minter turns a layer's native identity into an [ID]. It is the only way
// to make one.
//
// Adapters own extraction and never decide what a node is called (ADR 001).
// That indirection is the whole seam: it is what lets a .pxf @type
// reference and a .proto message definition converge on one node without
// either adapter knowing the other exists.
//
// A Minter performs no I/O — it never stats a path or reads a file — so
// minting is pure and cheap enough to call per declaration.
type Minter struct {
	root string
	// label is set for a secondary root (ADR 002 rule 12): every
	// file-anchored ID it mints, and every position it normalizes,
	// starts with "@label/", so two roots' files never share an ID.
	label string
}

// NewMinter returns a Minter for a repository rooted at root, which should
// be an absolute path. File-anchored IDs are repo-relative (ADR 002 rule
// 2), because an absolute path embeds the machine that built the graph and
// a CI-built graph would then share no nodes with a local one; root is what
// [Minter.RelPath] relativizes against.
//
// An empty root is allowed and means callers already hold repo-relative
// paths. Under an empty root an absolute path is an error rather than a
// guess, since there is nothing to make it relative to.
func NewMinter(root string) *Minter {
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Minter{root: root}
}

// Root returns the repository root the Minter relativizes against.
func (m *Minter) Root() string { return m.root }

// Label returns the secondary root's label, or "" for the primary root.
func (m *Minter) Label() string { return m.label }

// Secondary returns a Minter for a second repository entering the same
// graph (ADR 002 rule 12): its file-anchored IDs are labelled, its
// declaration-keyed ones — a Go import path, a .proto full name, a SQL
// name — are not, which is what lets a schema root's message be the node
// the primary root's generated code names. The label is one path
// segment, and is written as "@label" so it cannot be mistaken for a
// directory of the primary root.
func (m *Minter) Secondary(label, root string) (*Minter, error) {
	if label == "" || strings.HasPrefix(label, "@") {
		return nil, fmt.Errorf("secondary root: label %q is empty or starts with %q", label, "@")
	}
	if label == "pkg" {
		// ts://@pkg/ names an npm package (#124); a root labelled pkg
		// would write its files there too.
		return nil, fmt.Errorf("secondary root: label %q is reserved for packages", label)
	}
	if err := checkChars(label); err != nil {
		return nil, fmt.Errorf("secondary root: label: %w", err)
	}
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("secondary root %q must be an absolute path", root)
	}
	return &Minter{root: filepath.Clean(root), label: label}, nil
}

// A Path is a file's path as an ID or a position carries it: repo-relative,
// forward-slashed, a leading "@" escaped and a secondary root's label in
// front (ADR 002 rule 12). Only a [Minter] makes one, from a path as the
// file system names it, so a path is labelled exactly once: a string that
// already starts with "@label/" is a real directory of that name, not a
// path labelled before (#124).
type Path string

// RelPath converts p to the repo-relative, forward-slash form that
// file-anchored IDs use. A relative path is cleaned and returned; an
// absolute one is made relative to the Minter's root. A path outside the
// root is an error — silently keeping it would put a machine-specific
// absolute path into an ID.
func (m *Minter) RelPath(p string) (Path, error) {
	rel, err := m.relClean(p)
	if err != nil {
		return "", err
	}
	if err := checkSlashPath(rel); err != nil {
		return "", err
	}
	return m.path(rel), nil
}

// PosPath is [Minter.RelPath] for a node's position rather than its
// identity: the same repo-relative form, but a name an ID could not carry —
// a space in "docs/My Notes.md" — is kept, since a position keys nothing
// (#92). Outside the root is still an error.
func (m *Minter) PosPath(p string) (Path, error) {
	rel, err := m.relClean(p)
	if err != nil {
		return "", err
	}
	return m.path(rel), nil
}

// Rel returns p relative to the Minter's root, cleaned and forward-slashed,
// as the file system names it: not escaped, not labelled. It is for
// matching a path against the root's own configuration — a buf.gen.yaml
// input directory, a sqlc schema path — never for an ID or a position.
func (m *Minter) Rel(p string) (string, error) {
	return m.relClean(p)
}

// FilePath reads the path back out of an ID's or a position's path, as
// the file system names it in the primary root: an escaped "@@x" is the
// directory "@x". A labelled path — a secondary root's — is returned as
// it is (#124).
func FilePath(p string) string {
	if strings.HasPrefix(p, "@@") {
		return p[1:]
	}
	return p
}

// path writes a clean repo-relative path as IDs and positions carry it.
// A leading "@" of a real path is doubled, so a single one always starts
// a label (#124, the owner's decision, ADR 002 rule 12): "@types/x.d.ts"
// is "@@types/x.d.ts", and in the root labelled protolsp,
// "@protolsp/x.proto" is "@protolsp/@@protolsp/x.proto".
func (m *Minter) path(rel string) Path {
	if strings.HasPrefix(rel, "@") {
		rel = "@" + rel
	}
	if m.label != "" {
		rel = "@" + m.label + "/" + rel
	}
	return Path(rel)
}

// relClean is the part of RelPath every caller shares: relative to the
// root, cleaned, forward slashes, and inside the root.
func (m *Minter) relClean(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		if m.root == "" {
			return "", fmt.Errorf("absolute path %q with no repository root: file-anchored IDs are repo-relative (ADR 002 rule 2)", p)
		}
		rel, err := filepath.Rel(m.root, filepath.Clean(p))
		if err != nil {
			return "", fmt.Errorf("relativize %q against %q: %w", p, m.root, err)
		}
		p = rel
	}
	p = path.Clean(filepath.ToSlash(p))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("path %q is outside the repository root %q", p, m.root)
	}
	return p, nil
}

// GoID mints the ID of a Go declaration. pkgPath is the package's import
// path, which carries the module path (ADR 002 rule 5) — pass
// "github.com/trendvidia/protolsp/analysis", never "analysis". decl is the
// declaration path within the package, dotted: "Workspace" for a type,
// "Workspace.ReferencesFor" for a method, "New" for a function. An empty
// decl mints the package node itself.
//
// A method's receiver is written without the pointer star: "T.M", not
// "(*T).M". Go forbids a type from having both a value-receiver and a
// pointer-receiver method of the same name, so the star carries no
// identity — only noise that would differ between an SSA name and a
// source-level one.
//
// Two vendored copies of a package have the same import path, so an
// adapter that wants to tell them apart must supply a path that does; the
// core cannot see the difference from the string alone.
func (m *Minter) GoID(pkgPath, decl string) (ID, error) {
	if pkgPath == "" {
		return "", fmt.Errorf("go: empty package path")
	}
	if err := checkSlashPath(pkgPath); err != nil {
		return "", fmt.Errorf("go: package path: %w", err)
	}
	if decl == "" {
		return newID(SchemeGo, pkgPath)
	}
	if err := checkDotted(decl); err != nil {
		return "", fmt.Errorf("go: declaration: %w", err)
	}
	return newID(SchemeGo, pkgPath+"#"+decl)
}

// GoTestID mints the ID of a package's in-package test files, its
// [KindPackageTests] node: GoTestID("example.com/m/p") is
// go://example.com/m/p#(test). A Go identifier cannot contain "(", so no
// declaration can share it (#128, ADR 002 rule 5).
func (m *Minter) GoTestID(pkgPath string) (ID, error) {
	id, err := m.GoID(pkgPath, "")
	if err != nil {
		return "", err
	}
	return id + "#(test)", nil
}

// ProtoID mints the ID of a .proto declaration from its fully-qualified
// descriptor path: "protolsp.v1.WorkspaceConfig" for a message,
// "protolsp.v1.WorkspaceConfig.registry" for one of its fields.
func (m *Minter) ProtoID(fqName string) (ID, error) {
	if err := checkDotted(fqName); err != nil {
		return "", fmt.Errorf("proto: %w", err)
	}
	return newID(SchemeProto, fqName)
}

// ProtoFileID mints the ID of a .proto file (ADR 002 rule 7). A file has no
// descriptor path, so it takes the "file/" form; the "/" is what
// distinguishes it from a descriptor path, which never contains one.
func (m *Minter) ProtoFileID(file string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("proto file: %w", err)
	}
	return newID(SchemeProto, "file/"+string(rel))
}

// PXFID mints the ID of a .pxf block or field from its file and block path:
// PXFID("config/chain.pxf", "registry.address").
func (m *Minter) PXFID(file, blockPath string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("pxf: %w", err)
	}
	if blockPath == "" {
		return newID(SchemePXF, string(rel))
	}
	if err := checkDotted(blockPath); err != nil {
		return "", fmt.Errorf("pxf: block path: %w", err)
	}
	return newID(SchemePXF, string(rel)+"#"+blockPath)
}

// GoasmID mints the ID of a goasm template from its file and name (ADR 002
// rule 10): GoasmID("jit/templates/arm64.goasm", "AddLocalsInt"). An empty
// name mints the file itself, by its bare repo-relative path (rule 7). A
// template name is one segment — it becomes part of a Go method name — so
// a "." in it is rejected rather than read as a path.
func (m *Minter) GoasmID(file, name string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("goasm: %w", err)
	}
	if name == "" {
		return newID(SchemeGoasm, string(rel))
	}
	if strings.Contains(name, ".") {
		return "", fmt.Errorf("goasm: template name %q contains %q", name, ".")
	}
	if err := checkChars(name); err != nil {
		return "", fmt.Errorf("goasm: template name: %w", err)
	}
	return newID(SchemeGoasm, string(rel)+"#"+name)
}

// TSID mints the ID of a frontend file, or of one export of a generated
// one (ADR 002 rule 11): TSID("web/gen/app/v1/user_pb.ts", "UserSchema").
// An empty export mints the file by its bare repo-relative path (rule 7).
// An export name is one segment.
func (m *Minter) TSID(file, export string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("ts: %w", err)
	}
	if export == "" {
		return newID(SchemeTS, string(rel))
	}
	if strings.Contains(export, ".") {
		return "", fmt.Errorf("ts: export name %q contains %q", export, ".")
	}
	if err := checkChars(export); err != nil {
		return "", fmt.Errorf("ts: export name: %w", err)
	}
	return newID(SchemeTS, string(rel)+"#"+export)
}

// TSDeclID mints the ID of a declaration of a frontend file, read by the
// type checker (ADR 002 rule 11, amended for #215): its file and its
// declaration path, a top-level name and, for a member, the name of the
// declaration holding it — TSDeclID("app/src/api.ts", "Client", "fetch")
// is ts://app/src/api.ts#Client.fetch. A top-level declaration's ID is
// the one a generated file's export of that name has, which is the point:
// both are the same declaration. Each name is one segment; a function
// nested in a body (#235) adds its own, and the objects' holding it:
// TSDeclID("app/src/Editor.tsx", "Editor", "handle", "save").
func (m *Minter) TSDeclID(file string, path ...string) (ID, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("ts: declaration of %s has no name", file)
	}
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("ts: %w", err)
	}
	for _, name := range path {
		if name == "" || strings.Contains(name, ".") {
			return "", fmt.Errorf("ts: declaration name %q is empty or contains %q", name, ".")
		}
		if err := checkChars(name); err != nil {
			return "", fmt.Errorf("ts: declaration name: %w", err)
		}
	}
	return newID(SchemeTS, string(rel)+"#"+strings.Join(path, "."))
}

// PyID mints the ID of a Python file, or of a declaration in it (ADR 002
// rule 15, #237): its file and the declaration's qualified name, each
// enclosing class or function a segment — PyID("bin/autofix/land.py",
// "resolve_conflict"), PyID("pkg/shapes.py", "Square", "area"),
// PyID("app.py", "main", "inner"). No path mints the file. The file, not
// the dotted module name, anchors the ID: a module's name depends on the
// import path it is reached through, and one file can have two or none.
func (m *Minter) PyID(file string, path ...string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("py: %w", err)
	}
	if len(path) == 0 {
		return newID(SchemePy, string(rel))
	}
	for _, name := range path {
		if name == "" || strings.Contains(name, ".") {
			return "", fmt.Errorf("py: declaration name %q is empty or contains %q", name, ".")
		}
		if err := checkChars(name); err != nil {
			return "", fmt.Errorf("py: declaration name: %w", err)
		}
	}
	return newID(SchemePy, string(rel)+"#"+strings.Join(path, "."))
}

// TSPackageID mints the ID of a package imported by bare specifier and
// never read — "@bufbuild/protobuf", "react", "node:fs" — under an "@pkg/"
// segment: ts://@pkg/@bufbuild/protobuf. A file's path never starts with
// a single "@", since [Minter.RelPath] doubles a real one, so a package
// and a file cannot share an ID — as "pkg/" let the file pkg/chart.js and
// the package chart.js do (#124, the owner's decision, ADR 002 rule 11).
func (m *Minter) TSPackageID(name string) (ID, error) {
	if err := checkSlashPath(name); err != nil {
		return "", fmt.Errorf("ts package: %w", err)
	}
	return newID(SchemeTS, "@pkg/"+name)
}

// An SQLKind is the sort of thing a sql:// ID names. SQL is the one layer
// whose entities are not file-anchored — a table declared by a migration is
// the same table however the migration is later split — so the kind is part
// of the ID rather than derivable from a path.
type SQLKind string

// The SQL entity kinds. A migration file is not among them: it is where a
// table is declared, not a thing the graph joins on.
const (
	SQLTable  SQLKind = "table"
	SQLColumn SQLKind = "column"
	SQLQuery  SQLKind = "query"
)

// SQLID mints the ID of a SQL entity: SQLID(SQLTable, "users"),
// SQLID(SQLColumn, "users.email"); a query is SQLQueryID's. Names keep
// the case the schema gives them.
func (m *Minter) SQLID(kind SQLKind, name string) (ID, error) {
	switch kind {
	case SQLTable, SQLColumn:
	case SQLQuery:
		return "", fmt.Errorf("sql: a query's ID carries its package; mint it with SQLQueryID (#118)")
	default:
		return "", fmt.Errorf("sql: unknown entity kind %q", kind)
	}
	if err := checkDotted(name); err != nil {
		return "", fmt.Errorf("sql: %w", err)
	}
	return newID(SchemeSQL, string(kind)+"/"+name)
}

// SQLQueryID mints the ID of a named query (ADR 002, amended by #118):
// SQLQueryID("example.com/m/db", "GetUser") is
// sql://query/example.com/m/db#GetUser. sqlc scopes a query's name to its
// package — two packages may each declare GetUser — so the package is part
// of the identity, written as Go writes it: the import path of the
// package sqlc generates the query into, before "#", the name after. A
// query in no package that generates Go is keyed by the repo-relative
// path of its file instead; a slash path either way.
func (m *Minter) SQLQueryID(pkg, name string) (ID, error) {
	if err := checkSlashPath(pkg); err != nil {
		return "", fmt.Errorf("sql query package: %w", err)
	}
	if err := checkChars(name); err != nil || name == "" || strings.Contains(name, ".") {
		return "", fmt.Errorf("sql: query name %q is not one segment", name)
	}
	return newID(SchemeSQL, string(SQLQuery)+"/"+pkg+"#"+name)
}

// MarkdownID mints the ID of a document or one of its sections (ADR 002
// rule 8). With no headings it names the document; with headings it names
// the section reached by that path of nested headings, each slugged and
// joined with ".", so the fragment is a dotted path like every other.
//
// Slugging loses information — two headings that differ only in punctuation
// slug alike and would converge on one node. That is tolerable here and
// nowhere else: Markdown edges are INFERRED (ADR 006), so a consumer is
// already told not to reason from them as facts.
func (m *Minter) MarkdownID(file string, headings ...string) (ID, error) {
	rel, err := m.RelPath(file)
	if err != nil {
		return "", fmt.Errorf("markdown: %w", err)
	}
	if len(headings) == 0 {
		return newID(SchemeMarkdown, string(rel))
	}
	slugs := make([]string, 0, len(headings))
	for _, h := range headings {
		s := slug(h)
		if s == "" {
			return "", fmt.Errorf("markdown: heading %q slugs to nothing", h)
		}
		slugs = append(slugs, s)
	}
	return newID(SchemeMarkdown, string(rel)+"#"+strings.Join(slugs, "."))
}

// newID assembles an ID and guards the one invariant [ID.Scheme] depends
// on: the body may not itself contain "://", or the scheme would be
// ambiguous to read back.
func newID(s Scheme, body string) (ID, error) {
	if body == "" {
		return "", fmt.Errorf("%s: empty body", s)
	}
	if strings.Contains(body, schemeSep) {
		return "", fmt.Errorf("%s: body %q contains %q", s, body, schemeSep)
	}
	return ID(string(s) + schemeSep + body), nil
}

// slug reduces a heading to its anchor form: lowercase, with every run of
// other characters collapsed to a single "-". Letters and digits outside
// ASCII are kept, since dropping them would collide every heading in a
// non-English document onto one node. A slug never contains "." — a dot is
// one of the collapsed characters — which is what lets "." separate the
// levels of a heading path.
func slug(h string) string {
	var b strings.Builder
	b.Grow(len(h))
	dash := false
	for _, r := range strings.ToLower(h) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// checkDotted validates a dotted declaration path: at least one segment,
// none of them empty, none containing a structural or invisible character.
// It deliberately does not validate the layer's own syntax — the core keys
// on these strings, it does not parse them.
func checkDotted(s string) error {
	if s == "" {
		return fmt.Errorf("empty declaration path")
	}
	for _, seg := range strings.Split(s, ".") {
		if seg == "" {
			return fmt.Errorf("declaration path %q has an empty segment", s)
		}
		if err := checkChars(seg); err != nil {
			return err
		}
	}
	return nil
}

// checkSlashPath validates a slash-separated path used whole inside an ID,
// such as a Go import path.
func checkSlashPath(s string) error {
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return fmt.Errorf("path %q has a leading or trailing %q", s, "/")
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" {
			return fmt.Errorf("path %q has an empty segment", s)
		}
		if err := checkChars(seg); err != nil {
			return err
		}
	}
	return nil
}

// checkChars rejects the characters that would make an ID ambiguous to read
// or ugly to print: "#" and "/" are structural, and whitespace or a control
// character in a declaration name means something upstream went wrong.
func checkChars(s string) error {
	for _, r := range s {
		switch {
		case r == '#':
			return fmt.Errorf("segment %q contains %q, which separates a path from a declaration", s, "#")
		case r == '/':
			return fmt.Errorf("segment %q contains %q, which separates path segments", s, "/")
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return fmt.Errorf("segment %q contains whitespace or a control character", s)
		}
	}
	return nil
}
