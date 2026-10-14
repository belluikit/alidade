// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"strings"
	"testing"
)

// TestMintedIDsAreStable pins the exact bytes of every ID form. It is the
// enforcement of ADR 002: identity is the one decision in this repo that
// costs a migration rather than a revert, and an ID that changes shape
// silently re-keys every node in the graph. A diff in this table is not a
// test to update — it is a migration to think about.
func TestMintedIDsAreStable(t *testing.T) {
	m := NewMinter("/repo")

	tests := []struct {
		name string
		mint func() (ID, error)
		want ID
	}{
		// The five forms ADR 002 writes out by name.
		{
			name: "go method",
			mint: func() (ID, error) {
				return m.GoID("github.com/trendvidia/protolsp/analysis", "Workspace.ReferencesFor")
			},
			want: "go://github.com/trendvidia/protolsp/analysis#Workspace.ReferencesFor",
		},
		{
			name: "proto field",
			mint: func() (ID, error) { return m.ProtoID("protolsp.v1.WorkspaceConfig.registry") },
			want: "proto://protolsp.v1.WorkspaceConfig.registry",
		},
		{
			name: "pxf field",
			mint: func() (ID, error) { return m.PXFID("config/chain.pxf", "registry.address") },
			want: "pxf://config/chain.pxf#registry.address",
		},
		{
			name: "sql table",
			mint: func() (ID, error) { return m.SQLID(SQLTable, "users") },
			want: "sql://table/users",
		},
		{
			name: "sql query",
			mint: func() (ID, error) { return m.SQLQueryID("example.com/m/db", "GetUser") },
			want: "sql://query/example.com/m/db#GetUser",
		},
		{
			name: "markdown section",
			mint: func() (ID, error) { return m.MarkdownID("docs/adr/002-node-identity.md", "Decision") },
			want: "md://docs/adr/002-node-identity.md#decision",
		},

		// Forms the ADR implies but does not write out.
		{
			name: "go package carries its module path (rule 5)",
			mint: func() (ID, error) { return m.GoID("github.com/trendvidia/protolsp/analysis", "") },
			want: "go://github.com/trendvidia/protolsp/analysis",
		},
		{
			name: "proto file takes the file/ form",
			mint: func() (ID, error) { return m.ProtoFileID("proto/app/v1/user.proto") },
			want: "proto://file/proto/app/v1/user.proto",
		},
		{
			name: "pxf file",
			mint: func() (ID, error) { return m.PXFID("config/chain.pxf", "") },
			want: "pxf://config/chain.pxf",
		},
		{
			name: "sql column",
			mint: func() (ID, error) { return m.SQLID(SQLColumn, "users.email") },
			want: "sql://column/users.email",
		},
		{
			name: "markdown document",
			mint: func() (ID, error) { return m.MarkdownID("docs/schema.md") },
			want: "md://docs/schema.md",
		},
		{
			name: "nested markdown headings join with .",
			mint: func() (ID, error) {
				return m.MarkdownID("README.md", "Why identity is the whole problem", "Prior art worth copying")
			},
			want: "md://README.md#why-identity-is-the-whole-problem.prior-art-worth-copying",
		},
		{
			name: "goasm template (rule 10)",
			mint: func() (ID, error) { return m.GoasmID("jit/templates/arm64.goasm", "AddLocalsInt") },
			want: "goasm://jit/templates/arm64.goasm#AddLocalsInt",
		},
		{
			name: "goasm file",
			mint: func() (ID, error) { return m.GoasmID("jit/templates/arm64.goasm", "") },
			want: "goasm://jit/templates/arm64.goasm",
		},
		{
			name: "ts file (rule 11)",
			mint: func() (ID, error) { return m.TSID("web/src/routes/+page.svelte", "") },
			want: "ts://web/src/routes/+page.svelte",
		},
		{
			name: "ts generated export (rule 11)",
			mint: func() (ID, error) { return m.TSID("web/gen/app/v1/user_pb.ts", "UserSchema") },
			want: "ts://web/gen/app/v1/user_pb.ts#UserSchema",
		},
		{
			name: "ts declaration of a hand-written file (rule 11, #215)",
			mint: func() (ID, error) { return m.TSDeclID("app/src/api.ts", "fetchUser") },
			want: "ts://app/src/api.ts#fetchUser",
		},
		{
			name: "ts member of a declaration (rule 11, #215)",
			mint: func() (ID, error) { return m.TSDeclID("app/src/api.ts", "Client", "fetch") },
			want: "ts://app/src/api.ts#Client.fetch",
		},
		{
			name: "ts function nested in a body, under the object holding it (rule 11, #235)",
			mint: func() (ID, error) { return m.TSDeclID("app/src/Editor.tsx", "Editor", "handle", "save") },
			want: "ts://app/src/Editor.tsx#Editor.handle.save",
		},
		{
			name: "py file (rule 15, #237)",
			mint: func() (ID, error) { return m.PyID("bin/autofix/land.py") },
			want: "py://bin/autofix/land.py",
		},
		{
			name: "py method by its qualified name (rule 15, #237)",
			mint: func() (ID, error) { return m.PyID("pkg/shapes.py", "Square", "area") },
			want: "py://pkg/shapes.py#Square.area",
		},
		{
			name: "a ts top-level declaration and a generated export of one name are one ID (#215)",
			mint: func() (ID, error) { return m.TSDeclID("web/gen/app/v1/user_pb.ts", "UserSchema") },
			want: "ts://web/gen/app/v1/user_pb.ts#UserSchema",
		},
		{
			name: "ts external package takes the @pkg/ form (#124)",
			mint: func() (ID, error) { return m.TSPackageID("@bufbuild/protobuf") },
			want: "ts://@pkg/@bufbuild/protobuf",
		},
		{
			name: "heading punctuation collapses to one dash",
			mint: func() (ID, error) {
				return m.MarkdownID("docs/x.md", "ADR 002: Node Identity — Declaration-Anchored")
			},
			want: "md://docs/x.md#adr-002-node-identity-declaration-anchored",
		},

		// Rule 2: an absolute path is made repo-relative, so a CI-built
		// graph and a local one share nodes rather than sharing none.
		{
			name: "absolute path is relativized against the root",
			mint: func() (ID, error) { return m.MarkdownID("/repo/docs/schema.md") },
			want: "md://docs/schema.md",
		},
		{
			name: "path is cleaned",
			mint: func() (ID, error) { return m.MarkdownID("docs/../docs/./schema.md") },
			want: "md://docs/schema.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.mint()
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			if got != tt.want {
				t.Errorf("ID drifted\n got: %s\nwant: %s", got, tt.want)
			}
			if !got.Valid() {
				t.Errorf("%s does not validate", got)
			}
		})
	}
}

// TestMintingRejectsAmbiguousInput checks that the failures are loud. An ID
// this package cannot form is an error at the call site; the alternative is
// an adapter writing a malformed key and the graph losing the node quietly.
func TestMintingRejectsAmbiguousInput(t *testing.T) {
	m := NewMinter("/repo")

	tests := []struct {
		name string
		mint func() (ID, error)
	}{
		{"empty package path", func() (ID, error) { return m.GoID("", "X") }},
		{"package path with a scheme in it", func() (ID, error) { return m.GoID("http://x", "X") }},
		{"package path with an empty segment", func() (ID, error) { return m.GoID("a//b", "X") }},
		{"package path with a leading slash", func() (ID, error) { return m.GoID("/a/b", "X") }},
		{"declaration with whitespace", func() (ID, error) { return m.GoID("a/b", "Work space") }},
		{"declaration with an empty segment", func() (ID, error) { return m.GoID("a/b", "A..B") }},
		{"declaration containing #", func() (ID, error) { return m.GoID("a/b", "A#B") }},
		{"declaration containing /", func() (ID, error) { return m.GoID("a/b", "A/B") }},
		{"empty proto name", func() (ID, error) { return m.ProtoID("") }},
		{"proto name with a control character", func() (ID, error) { return m.ProtoID("a.\x00b") }},
		{"unknown sql kind", func() (ID, error) { return m.SQLID(SQLKind("view"), "v") }},
		{"empty sql name", func() (ID, error) { return m.SQLID(SQLTable, "") }},
		{"empty pxf file", func() (ID, error) { return m.PXFID("", "a") }},
		{"pxf block with a slash", func() (ID, error) { return m.PXFID("c.pxf", "a/b") }},
		{"heading that slugs to nothing", func() (ID, error) { return m.MarkdownID("d.md", "———") }},
		{"path outside the root", func() (ID, error) { return m.MarkdownID("/elsewhere/x.md") }},
		{"path escaping the root", func() (ID, error) { return m.MarkdownID("../x.md") }},
		{"empty path", func() (ID, error) { return m.MarkdownID("") }},
		{"proto file outside the root", func() (ID, error) { return m.ProtoFileID("/elsewhere/x.proto") }},
		{"empty goasm file", func() (ID, error) { return m.GoasmID("", "X") }},
		{"goasm template name with a dot", func() (ID, error) { return m.GoasmID("a.goasm", "A.B") }},
		{"goasm template name with whitespace", func() (ID, error) { return m.GoasmID("a.goasm", "A B") }},
		{"goasm template name containing #", func() (ID, error) { return m.GoasmID("a.goasm", "A#B") }},
		{"empty ts file", func() (ID, error) { return m.TSID("", "X") }},
		{"ts export with a dot", func() (ID, error) { return m.TSID("a.ts", "A.B") }},
		{"ts export with whitespace", func() (ID, error) { return m.TSID("a.ts", "A B") }},
		{"ts package with a leading slash", func() (ID, error) { return m.TSPackageID("/react") }},
		{"empty ts package", func() (ID, error) { return m.TSPackageID("") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.mint()
			if err == nil {
				t.Fatalf("minted %q, want an error", got)
			}
			if got != "" {
				t.Errorf("returned %q alongside an error, want the zero ID", got)
			}
		})
	}
}

// TestAbsolutePathNeedsARoot pins the rule that an absolute path is never
// guessed at: with no root there is nothing to relativize against, and
// keeping the absolute path would embed the build machine in the ID.
func TestAbsolutePathNeedsARoot(t *testing.T) {
	m := NewMinter("")
	if _, err := m.MarkdownID("/repo/docs/x.md"); err == nil {
		t.Fatal("absolute path accepted with no root, want an error")
	}
	if got, err := m.MarkdownID("docs/x.md"); err != nil || got != "md://docs/x.md" {
		t.Fatalf("relative path with no root: got %q, %v", got, err)
	}
	if m.Root() != "" {
		t.Errorf("Root() = %q, want empty", m.Root())
	}
}

func TestRootIsCleaned(t *testing.T) {
	m := NewMinter("/repo/./sub/")
	if m.Root() != "/repo/sub" {
		t.Fatalf("Root() = %q, want /repo/sub", m.Root())
	}
	got, err := m.MarkdownID("/repo/sub/docs/x.md")
	if err != nil || got != "md://docs/x.md" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestIDScheme(t *testing.T) {
	tests := []struct {
		id     ID
		scheme Scheme
		valid  bool
	}{
		{"go://a/b#C", SchemeGo, true},
		{"proto://a.B", SchemeProto, true},
		{"pxf://a.pxf#b", SchemePXF, true},
		{"sql://table/users", SchemeSQL, true},
		{"md://a.md#b", SchemeMarkdown, true},
		{"goasm://jit/templates/arm64.goasm#AddLocalsInt", SchemeGoasm, true},
		{"ts://web/src/app.ts", SchemeTS, true},
		{"go://", SchemeGo, false},
		{"rust://a", Scheme("rust"), false},
		{"no-scheme", "", false},
		{"", "", false},
		{"://body", "", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			scheme, ok := tt.id.Scheme()
			if scheme != tt.scheme || ok != tt.valid {
				t.Errorf("Scheme() = %q, %v; want %q, %v", scheme, ok, tt.scheme, tt.valid)
			}
			if tt.id.Valid() != tt.valid {
				t.Errorf("Valid() = %v, want %v", tt.id.Valid(), tt.valid)
			}
			if tt.id.String() != string(tt.id) {
				t.Errorf("String() = %q, want %q", tt.id.String(), string(tt.id))
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Decision", "decision"},
		{"Why identity is the whole problem", "why-identity-is-the-whole-problem"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"ADR 002: Node Identity", "adr-002-node-identity"},
		{"a---b", "a-b"},
		{"Ünïcode Stäys", "ünïcode-stäys"},
		{"—", ""},
		{"", ""},
		// Rule 8's assumption: the separator of a heading path never
		// survives slugging, so a heading with a dot in it cannot fake a
		// level. Nor can a slash, which was the separator before the
		// amendment.
		{"v1.2 release", "v1-2-release"},
		{"docs/adr", "docs-adr"},
		{"pkg.go", "pkg-go"},
	}
	for _, tt := range tests {
		got := slug(tt.in)
		if got != tt.want {
			t.Errorf("slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.ContainsAny(got, "./#") {
			t.Errorf("slug(%q) = %q contains a structural character", tt.in, got)
		}
	}
}

// TestPositionNeverReachesTheID is rule 1 stated as a test: the same
// declaration at a different position is the same node. Every edit above a
// declaration moves it, and a graph that re-keyed on that would be valid
// only against the commit that built it.
func TestPositionNeverReachesTheID(t *testing.T) {
	m := NewMinter("/repo")
	id, err := m.GoID("github.com/x/app", "Store.Save")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(id), "0123456789") {
		t.Fatalf("%q contains a digit; positions must not key anything", id)
	}

	b := NewBuilder("/repo")
	for _, line := range []int{10, 400} {
		if err := b.AddNode(Node{ID: id, Kind: KindMethod, Name: "Save", Pos: Position{File: "store.go", Line: line}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.Build().NodeCount(); got != 1 {
		t.Fatalf("NodeCount() = %d, want 1: a moved declaration is the same node", got)
	}
}

// TestSecondaryRootLabelsFileIDs pins rule 12: a secondary root's
// file-anchored IDs and positions carry "@label/", its declaration-keyed
// IDs do not, and relativizing an already-labelled path is idempotent.
func TestSecondaryRootLabelsFileIDs(t *testing.T) {
	m, err := NewMinter("/repo").Secondary("protolsp", "/elsewhere/protolsp")
	if err != nil {
		t.Fatal(err)
	}
	if m.Label() != "protolsp" || m.Root() != "/elsewhere/protolsp" {
		t.Errorf("Label %q Root %q", m.Label(), m.Root())
	}
	for _, tt := range []struct {
		name string
		mint func() (ID, error)
		want ID
	}{
		{"proto file", func() (ID, error) { return m.ProtoFileID("/elsewhere/protolsp/proto/a/v1/x.proto") }, "proto://file/@protolsp/proto/a/v1/x.proto"},
		{"relative proto file", func() (ID, error) { return m.ProtoFileID("proto/a/v1/x.proto") }, "proto://file/@protolsp/proto/a/v1/x.proto"},
		// A path that starts with the label is a real directory of that
		// name, escaped and labelled once: no longer guessed to be a
		// path labelled before, which made it collide with the root's
		// proto/a/v1/x.proto (#124).
		{"a real @label directory", func() (ID, error) { return m.ProtoFileID("@protolsp/proto/a/v1/x.proto") }, "proto://file/@protolsp/@@protolsp/proto/a/v1/x.proto"},
		{"markdown", func() (ID, error) { return m.MarkdownID("/elsewhere/protolsp/README.md", "Decision") }, "md://@protolsp/README.md#decision"},
		{"proto message is not labelled", func() (ID, error) { return m.ProtoID("a.v1.X") }, "proto://a.v1.X"},
		{"go is keyed by module, not labelled", func() (ID, error) { return m.GoID("github.com/x/protolsp/gen", "X") }, "go://github.com/x/protolsp/gen#X"},
	} {
		got, err := tt.mint()
		if err != nil || got != tt.want {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if _, err := m.ProtoFileID("/repo/proto/x.proto"); err == nil {
		t.Error("a primary-root path minted through the secondary minter")
	}
	for _, bad := range []struct{ label, root string }{{"", "/x"}, {"@a", "/x"}, {"a/b", "/x"}, {"a b", "/x"}, {"a", "relative"}, {"a", ""}} {
		if _, err := NewMinter("/repo").Secondary(bad.label, bad.root); err == nil {
			t.Errorf("Secondary(%q, %q) accepted", bad.label, bad.root)
		}
	}
}

// TestQueryIDsCarryTheirPackage pins #118: a query is minted with its
// package, never by name alone, and its name is one segment.
func TestQueryIDsCarryTheirPackage(t *testing.T) {
	m := NewMinter("")
	if id, err := m.SQLID(SQLQuery, "GetUser"); err == nil {
		t.Errorf("SQLID minted a query without its package: %s", id)
	}
	if id, err := m.SQLQueryID("example.com/m/db", "GetUser"); err != nil || id != "sql://query/example.com/m/db#GetUser" {
		t.Errorf("SQLQueryID = %q, %v", id, err)
	}
	for _, bad := range [][2]string{{"", "GetUser"}, {"example.com/m/db", ""}, {"example.com/m/db", "a.b"}, {"example.com/m/db", "a#b"}, {"/abs", "GetUser"}} {
		if id, err := m.SQLQueryID(bad[0], bad[1]); err == nil {
			t.Errorf("SQLQueryID(%q, %q) = %s, want an error", bad[0], bad[1], id)
		}
	}
}

// TestIDNamespacesDoNotCollide pins #124 (the owner's decision, ADR 002
// rules 11 and 12): each pair minted the same ID before. A real path's
// leading "@" is doubled, so a single one always starts a label; a path
// is labelled once, never guessed to be labelled already; and a never-read
// npm package lives under "@pkg/", which no file's path can start with.
func TestIDNamespacesDoNotCollide(t *testing.T) {
	prim := NewMinter("/r")
	sec, err := prim.Secondary("protolsp", "/s")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		a, b func() (ID, error)
	}{
		{"a primary @label directory and the labelled root",
			func() (ID, error) { return prim.ProtoFileID("@protolsp/proto/x.proto") },
			func() (ID, error) { return sec.ProtoFileID("proto/x.proto") }},
		{"a secondary root's own @label directory",
			func() (ID, error) { return sec.ProtoFileID("@protolsp/proto/x.proto") },
			func() (ID, error) { return sec.ProtoFileID("proto/x.proto") }},
		{"a file under pkg/ and the package it names",
			func() (ID, error) { return prim.TSID("pkg/chart.js", "") },
			func() (ID, error) { return prim.TSPackageID("chart.js") }},
		{"a file under @pkg/ and the package it names",
			func() (ID, error) { return prim.TSID("@pkg/chart.js", "") },
			func() (ID, error) { return prim.TSPackageID("chart.js") }},
	} {
		a, aerr := tt.a()
		b, berr := tt.b()
		if aerr != nil || berr != nil || a == b {
			t.Errorf("%s: %q, %v and %q, %v; want two IDs", tt.name, a, aerr, b, berr)
		}
	}
}

// TestEscapedPathsReadBack pins the other half of #124's escape: a
// position carries the escaped path, and FilePath reads the file system's
// path back out of it.
func TestEscapedPathsReadBack(t *testing.T) {
	m := NewMinter("/r")
	for real, want := range map[string]Path{
		"@types/x.d.ts": "@@types/x.d.ts",
		"@@odd/x.ts":    "@@@odd/x.ts",
		"src/@x/y.ts":   "src/@x/y.ts",
	} {
		got, err := m.PosPath(real)
		if err != nil || got != want {
			t.Errorf("PosPath(%q) = %q, %v; want %q", real, got, err, want)
		}
		if back := FilePath(string(got)); back != real {
			t.Errorf("FilePath(%q) = %q, want %q", got, back, real)
		}
	}
	b := NewBuilder("/r")
	id := mustID(m.TSID("@types/x.d.ts", ""))
	if err := b.AddNode(Node{ID: id, Kind: KindModule, Name: "x.d.ts", Pos: Position{File: "/r/@types/x.d.ts"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Build().Node(id); n.Pos.File != "@@types/x.d.ts" || id != "ts://@@types/x.d.ts" {
		t.Errorf("node %s at %q; want ts://@@types/x.d.ts at @@types/x.d.ts", id, n.Pos.File)
	}
}

// TestPkgIsNotALabel: "@pkg/" names packages (#124), so no root may be
// labelled pkg.
func TestPkgIsNotALabel(t *testing.T) {
	if _, err := NewMinter("/r").Secondary("pkg", "/s"); err == nil {
		t.Error("a secondary root was labelled pkg")
	}
}
