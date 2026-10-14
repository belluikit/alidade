// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"github.com/belluikit/alidade/internal/adapter/ts/svelte"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

var (
	once    sync.Once
	fixture *graph.Graph
	report  *Report
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func extract(t *testing.T) (*graph.Graph, *Report) {
	t.Helper()
	once.Do(func() {
		dir := fixtureDir(t)
		b := graph.NewBuilder(dir)
		rep, err := Extract(b, dir, DefaultOptions())
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		fixture, report = b.Build(), rep
	})
	return fixture, report
}

func id(s string) graph.ID { return graph.ID("ts://" + s) }

func node(t *testing.T, g *graph.Graph, id graph.ID) graph.Node {
	t.Helper()
	n, ok := g.Node(id)
	if !ok {
		t.Fatalf("no node %s", id)
	}
	return n
}

func edge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func wantEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID, d graph.Derivation) {
	t.Helper()
	e, ok := edge(g, from, kind, to)
	if !ok {
		t.Errorf("missing edge %s -%s-> %s", from, kind, to)
	} else if e.Derivation != d {
		t.Errorf("edge %s -%s-> %s: derivation %s, want %s", from, kind, to, e.Derivation, d)
	}
}

func TestReport(t *testing.T) {
	_, rep := extract(t)
	want := Report{
		Files: 8, Components: 2, Generated: 1,
		Exports:    10, // file_app_v1_user, 3 types, 3 schemas, Role, RoleSchema, UserService
		Imports:    9,  // api->gen, api->util, page->api, page->Card, page->gen, Card->gen, all->gen, legacy->util, App->api
		External:   5,  // api->@bufbuild, App->react, gen->codegenv2 x2 (type and value clauses), gen->@bufbuild/protobuf
		References: 5,  // api: UserSchema, Role, User; page: Role; Card: User
		Unreadable: 1,  // all.ts: namespace import
		Assets:     1,  // +page.svelte imports app.css
		Generates:  10, // file + 3 messages x 2 + enum x 2 + service
		Testdata:   1,
		// The type checker's pass, which testdata/checker pins case by
		// case (checker_test.go); here only its totals over this tree.
		Programs: 2, Inferred: 1, Checked: 5, Unchecked: 2, Declarations: 5,
		CallSites: 5, Calls: 3, CallsExternal: 1, CallsUnresolved: 1,
	}
	if svelte.Available() {
		// Both components translated (#221): Card's $props(), the page's
		// newUser(…) and <Card>, all three unresolved here — no svelte
		// types, no $lib alias in this fixture's tsconfig — and the
		// page's user and admin declarations.
		want.Checked, want.Unchecked, want.Translated, want.Declarations = 7, 0, 2, 7
		want.CallSites, want.CallsUnresolved = 8, 4
	}
	got := *rep
	got.Elapsed = 0
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Report = %+v\nwant     %+v", got, want)
	}
}

func TestFileNodes(t *testing.T) {
	g, _ := extract(t)
	page := node(t, g, id("web/src/routes/+page.svelte"))
	if page.Kind != graph.KindComponent || page.Name != "+page.svelte" || page.Generated {
		t.Errorf("+page.svelte = %+v", page)
	}
	api := node(t, g, id("web/src/lib/api.ts"))
	if api.Kind != graph.KindModule || api.Pos.File != "web/src/lib/api.ts" {
		t.Errorf("api.ts = %+v", api)
	}
	for _, rel := range []string{"web/src/legacy.js", "web/src/App.tsx", "web/src/util.ts"} {
		if n := node(t, g, id(rel)); n.Kind != graph.KindModule {
			t.Errorf("%s kind = %s", rel, n.Kind)
		}
	}
	gen := node(t, g, id("web/gen/app/v1/user_pb.ts"))
	if !gen.Generated || gen.Attrs["generator"] != "protoc-gen-es" || gen.Attrs["proto"] != "app/v1/user.proto" {
		t.Errorf("user_pb.ts = %+v", gen)
	}
}

func TestSkipped(t *testing.T) {
	g, _ := extract(t)
	for _, rel := range []string{
		"web/node_modules/@bufbuild/protobuf/index.ts", // dependency
		"web/dist/bundle.js",                           // build output
		"site/deployed.ts",                             // named by .gitignore
		"web/src/types.d.ts",                           // declarations
		"testdata/sample.ts",                           // testdata
	} {
		if _, ok := g.Node(id(rel)); ok {
			t.Errorf("%s was extracted", rel)
		}
	}
	var ts []string
	for n := range g.Nodes() {
		if s, _ := n.ID.Scheme(); s == graph.SchemeTS && n.Kind != graph.KindPackage && !strings.Contains(string(n.ID), "#") {
			ts = append(ts, string(n.ID))
		}
	}
	sort.Strings(ts)
	want := []string{
		"ts://web/gen/app/v1/user_pb.ts", "ts://web/src/App.tsx", "ts://web/src/legacy.js",
		"ts://web/src/lib/Card.svelte", "ts://web/src/lib/all.ts", "ts://web/src/lib/api.ts",
		"ts://web/src/routes/+page.svelte", "ts://web/src/util.ts",
	}
	if strings.Join(ts, "\n") != strings.Join(want, "\n") {
		t.Errorf("file nodes:\n%s\nwant:\n%s", strings.Join(ts, "\n"), strings.Join(want, "\n"))
	}
}

// TestImportsResolve pins what esbuild's resolver gives that a scan of
// specifiers cannot: the $gen alias from svelte.config.js, the $lib
// default, an extension-less "./util", and ".js" meaning ".ts".
func TestImportsResolve(t *testing.T) {
	g, _ := extract(t)
	gen := id("web/gen/app/v1/user_pb.ts")
	for _, tt := range []struct{ from, to string }{
		{"web/src/lib/api.ts", "web/gen/app/v1/user_pb.ts"},        // $gen alias, .js -> .ts
		{"web/src/lib/api.ts", "web/src/util.ts"},                  // ../util
		{"web/src/routes/+page.svelte", "web/src/lib/api.ts"},      // $lib default
		{"web/src/routes/+page.svelte", "web/src/lib/Card.svelte"}, // component import
		{"web/src/routes/+page.svelte", "web/gen/app/v1/user_pb.ts"},
		{"web/src/lib/Card.svelte", "web/gen/app/v1/user_pb.ts"},
		{"web/src/legacy.js", "web/src/util.ts"}, // js importing ts, no extension
		{"web/src/App.tsx", "web/src/lib/api.ts"},
	} {
		wantEdge(t, g, id(tt.from), graph.KindImports, id(tt.to), graph.Resolved)
	}
	_ = gen
	for _, tt := range []struct{ from, pkg string }{
		{"web/src/lib/api.ts", "@bufbuild/protobuf"},
		{"web/src/App.tsx", "react"},
	} {
		wantEdge(t, g, id(tt.from), graph.KindImports, id("@pkg/"+tt.pkg), graph.Extracted)
		if n := node(t, g, id("@pkg/"+tt.pkg)); n.Kind != graph.KindPackage || n.Attrs["external"] != "true" {
			t.Errorf("package node %s = %+v", tt.pkg, n)
		}
	}
	// The generated file's own imports of the runtime are package edges
	// too, not file edges into node_modules.
	wantEdge(t, g, gen, graph.KindImports, id("@pkg/@bufbuild/protobuf"), graph.Extracted)
}

func TestGeneratedExports(t *testing.T) {
	g, _ := extract(t)
	gen := "web/gen/app/v1/user_pb.ts"
	for name, tt := range map[string]struct {
		kind graph.Kind
		from string
	}{
		"file_app_v1_user":      {graph.KindConst, ""},
		"User":                  {graph.KindType, "app.v1.User"},
		"UserSchema":            {graph.KindConst, "app.v1.User"},
		"GetUserRequest":        {graph.KindType, "app.v1.GetUserRequest"},
		"GetUserRequestSchema":  {graph.KindConst, "app.v1.GetUserRequest"},
		"GetUserResponse":       {graph.KindType, "app.v1.GetUserResponse"},
		"GetUserResponseSchema": {graph.KindConst, "app.v1.GetUserResponse"},
		"Role":                  {graph.KindEnum, "app.v1.Role"},
		"RoleSchema":            {graph.KindConst, "app.v1.Role"},
		"UserService":           {graph.KindConst, "app.v1.UserService"},
	} {
		n := node(t, g, id(gen+"#"+name))
		if n.Kind != tt.kind || !n.Generated || n.Attrs["proto"] != tt.from || n.Pos.Line == 0 {
			t.Errorf("%s = %+v, want kind %s from %q", name, n, tt.kind, tt.from)
		}
		wantEdge(t, g, id(gen), graph.KindContains, id(gen+"#"+name), graph.Extracted)
	}
}

// TestGeneratesEdges checks the DECLARED links from the .proto layer's
// IDs. They drop here, since no .proto layer ran; the build package's
// test is where they resolve.
func TestGeneratesEdges(t *testing.T) {
	g, _ := extract(t)
	gen := "web/gen/app/v1/user_pb.ts"
	want := map[string]string{
		"proto://file/proto/app/v1/user.proto": gen,
		"proto://app.v1.User":                  gen + "#User",
		"proto://app.v1.Role":                  gen + "#RoleSchema",
		"proto://app.v1.UserService":           gen + "#UserService",
	}
	got := map[string]bool{}
	for _, d := range g.Dropped() {
		e := d.Edge
		if e.Kind != graph.KindGenerates {
			continue
		}
		if d.Reason != graph.DropUnknownSource {
			t.Errorf("%s dropped for %s, want unknown source", e.From, d.Reason)
		}
		if e.Derivation != graph.Declared || e.Attrs["via"] != "protoc-gen-es" {
			t.Errorf("%s generates: derivation %s attrs %v", e.From, e.Derivation, e.Attrs)
		}
		if to, ok := want[string(e.From)]; ok && string(e.To) == "ts://"+to {
			got[string(e.From)] = true
		}
	}
	for from := range want {
		if !got[from] {
			t.Errorf("no generates edge %s -> ts://%s", from, want[from])
		}
	}
}

func TestReferences(t *testing.T) {
	g, _ := extract(t)
	gen := "web/gen/app/v1/user_pb.ts"
	for _, tt := range []struct{ from, name string }{
		{"web/src/lib/api.ts", "UserSchema"},
		{"web/src/lib/api.ts", "Role"},
		{"web/src/lib/api.ts", "User"}, // "type User" in a named clause
		{"web/src/routes/+page.svelte", "Role"},
		{"web/src/lib/Card.svelte", "User"}, // "import type { User }"
	} {
		wantEdge(t, g, id(tt.from), graph.KindReferences, id(gen+"#"+tt.name), graph.Extracted)
	}
	// A stylesheet import resolves to no node and drops nothing.
	for _, d := range g.Dropped() {
		if strings.HasSuffix(string(d.Edge.To), ".css") {
			t.Errorf("asset import was emitted and dropped: %+v", d)
		}
	}
	// A namespace import keeps the file-level edge and no references.
	wantEdge(t, g, id("web/src/lib/all.ts"), graph.KindImports, id(gen), graph.Resolved)
	for _, e := range g.Out(id("web/src/lib/all.ts")) {
		if e.Kind == graph.KindReferences {
			t.Errorf("all.ts has a references edge to %s from a namespace import", e.To)
		}
	}
}

func TestTestdataOption(t *testing.T) {
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, Options{Testdata: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Testdata != 0 || rep.Files != 9 {
		t.Errorf("with Testdata: %+v", rep)
	}
	if _, ok := b.Build().Node(id("testdata/sample.ts")); !ok {
		t.Error("with Testdata: testdata/sample.ts missing")
	}
}

func TestImportClauses(t *testing.T) {
	src := `import { A, B as C, type D } from "./x";
import type { E } from './y';
import * as ns from "./z";
import Def from "./d";
import Def2, { F } from "./e";
import "./side";
  import {
    G,
    H,
  } from "$gen/g_pb.js";
export { R } from "./r";
export * from "./all";
const lazy = () => import("./lazy");
`
	got := importClauses([]byte(src))
	type clause struct {
		spec  string
		names []string
	}
	want := []clause{
		{"./x", []string{"A", "B", "D"}},
		{"./y", []string{"E"}},
		{"./z", nil},
		{"./d", nil},
		{"./e", []string{"F"}},
		{"./side", nil},
		{"$gen/g_pb.js", []string{"G", "H"}},
		{"./r", []string{"R"}},
		{"./all", nil},
		{"./lazy", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d clauses %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].spec != want[i].spec || strings.Join(got[i].names, ",") != strings.Join(want[i].names, ",") {
			t.Errorf("clause %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	kinds := []string{kindImport, kindImport, kindImport, kindImport, kindImport, kindImport, kindImport, kindExport, kindExport, kindDynamic}
	for i, k := range kinds {
		if got[i].kind != k {
			t.Errorf("clause %d kind = %s, want %s", i, got[i].kind, k)
		}
	}
}

func TestPackageName(t *testing.T) {
	for spec, want := range map[string]string{
		"react": "react", "@bufbuild/protobuf": "@bufbuild/protobuf", "@bufbuild/protobuf/codegenv2": "@bufbuild/protobuf",
		"node:fs": "node:fs", "$app/stores": "$app", "svelte/store": "svelte", "./x": "", "/abs": "", "": "",
	} {
		if got := packageName(spec); got != want {
			t.Errorf("packageName(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestGitignoredDirs(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, ".gitignore"), "# c\nsite/\n/out\nbuild\n*.log\n!keep/\nnested/dir/\n"); err != nil {
		t.Fatal(err)
	}
	s := gitignoredDirs(dir)
	for _, tt := range []struct {
		name, rel string
		want      bool
	}{
		{"site", "a/site", true}, {"out", "out", true}, {"out", "a/out", false},
		{"build", "x/build", true}, {"keep", "keep", false}, {"dir", "nested/dir", true}, {"src", "src", false},
	} {
		if got := s.matches(tt.name, tt.rel); got != tt.want {
			t.Errorf("matches(%q, %q) = %v, want %v", tt.name, tt.rel, got, tt.want)
		}
	}
}

func TestSvelteConfigAliases(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "app", "svelte.config.js")
	if err := writeFile(cfg, "const config = { kit: { alias: { $gen: '../gen', \"$gen/*\": '../gen/*', $docs: \"./docs\" } } };\n"); err != nil {
		t.Fatal(err)
	}
	d := &discovery{}
	d.svelteConfig(dir, cfg)
	if len(d.apps) != 1 {
		t.Fatalf("apps = %+v, want one", d.apps)
	}
	aliases := d.apps[0].aliases
	want := map[string]string{"$gen": "./gen", "$docs": "./app/docs", "$lib": "./app/src/lib"}
	if len(aliases) != len(want) {
		t.Errorf("aliases = %v, want %v", aliases, want)
	}
	for k, v := range want {
		if aliases[k] != v {
			t.Errorf("alias %s = %q, want %q", k, aliases[k], v)
		}
	}
}

func TestParseGeneratedRejectsHandWritten(t *testing.T) {
	if gf := parseGenerated([]byte("export const x = 1;\n// not @generated by protoc-gen-es\n")); gf == nil {
		t.Error("a mention of the marker in the first lines counts; this one should be generated")
	}
	deep := strings.Repeat("// filler\n", 300) + "// @generated by protoc-gen-es\n"
	if gf := parseGenerated([]byte(deep)); gf != nil {
		t.Error("a marker past the header made the file generated")
	}
}
