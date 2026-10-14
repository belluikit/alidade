// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// linked builds a graph of every layer's node shapes, feeds it the given
// mentions from one section, and returns the documents edges Link added.
func linked(t *testing.T, mentions ...string) (map[string]string, *Report) {
	t.Helper()
	b := graph.NewBuilder("")
	m := b.Minter()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	add := func(id graph.ID, kind graph.Kind, name string) {
		must(b.AddNode(graph.Node{ID: id, Kind: kind, Name: name}))
	}
	mint := func(id graph.ID, err error) graph.ID { must(err); return id }
	add(mint(m.GoID("example.com/m/synth", "")), graph.KindPackage, "synth")
	add(mint(m.GoID("example.com/m/synth", "Report")), graph.KindType, "Report")
	add(mint(m.GoID("example.com/m/synth", "Report.Render")), graph.KindMethod, "Render")
	add(mint(m.GoID("example.com/m/synth", "Config")), graph.KindType, "Config")
	add(mint(m.GoID("example.com/m/other", "Config")), graph.KindType, "Config")
	add(mint(m.ProtoID("example.api.v1.EvaluationsService")), graph.KindService, "EvaluationsService")
	add(mint(m.ProtoID("example.api.v1.EvaluationsService.Create")), graph.KindRPC, "Create")
	add(mint(m.ProtoFileID("api/v1/evaluations.proto")), graph.KindProtoFile, "evaluations.proto")
	add(mint(m.ProtoFileID("api/v2/evaluations.proto")), graph.KindProtoFile, "evaluations.proto")
	add(mint(m.SQLID(graph.SQLTable, "app.users")), graph.KindTable, "users")
	add(mint(m.SQLID(graph.SQLColumn, "app.users.email")), graph.KindColumn, "email")
	add(mint(m.SQLQueryID("example.com/m/db", "GetUser")), graph.KindQuery, "GetUser")
	add(mint(m.PXFID("governance.pxf", "")), graph.KindPXFFile, "governance.pxf")
	add(mint(m.PXFID("governance.pxf", "manifesto.mission")), graph.KindField, "mission")
	add(mint(m.TSID("web/gen/user_pb.ts", "")), graph.KindModule, "user_pb.ts")
	add(mint(m.TSID("web/gen/user_pb.ts", "UserSchema")), graph.KindBlock, "UserSchema")
	must(b.AddNode(graph.Node{ID: mint(m.TSPackageID("react")), Kind: graph.KindPackage, Name: "react", Attrs: map[string]string{"external": "true"}}))
	add(mint(m.GoasmID("jit/arm64.goasm", "")), graph.KindGoasmFile, "arm64.goasm")
	add(mint(m.GoasmID("jit/arm64.goasm", "AddLocalsInt")), graph.KindTemplate, "AddLocalsInt")
	add(mint(m.MarkdownID("docs/x.md")), graph.KindDocument, "x.md")
	add(mint(m.MarkdownID("docs/x.md", "Report")), graph.KindSection, "Report")
	section := mint(m.MarkdownID("docs/y.md", "Notes"))
	add(section, graph.KindSection, "Notes")

	rep := &Report{}
	for _, text := range mentions {
		rep.Mentions = append(rep.Mentions, Mention{Section: section, Text: text})
	}
	Link(b, b.Build(), rep)
	g := b.Build()
	out := map[string]string{}
	for _, e := range g.Out(section) {
		if e.Kind == graph.KindDocuments {
			if e.Derivation != graph.Inferred {
				t.Errorf("documents edge %+v is not INFERRED", e)
			}
			out[e.Attrs["mention"]] = string(e.To)
		}
	}
	return out, rep
}

func TestLinkRules(t *testing.T) {
	got, rep := linked(t,
		"synth.Report", "Report.Render", "synth.Report.Render", "Render", // Go: pkg.Decl, Type.Method, pkg.Type.Method, bare
		"synth",                                                          // a package by name
		"EvaluationsService.Create", "example.api.v1.EvaluationsService", // proto: suffix, full name
		"app.users", "users", "app.users.email", "GetUser", "db.GetUser", // SQL: qualified, bare table, column, query by name and by package (#118)
		"manifesto.mission", "api/v1/evaluations.proto", // pxf block path, proto file by path
	)
	want := map[string]string{
		"synth.Report":                      "go://example.com/m/synth#Report",
		"Report.Render":                     "go://example.com/m/synth#Report.Render",
		"synth.Report.Render":               "go://example.com/m/synth#Report.Render",
		"Render":                            "go://example.com/m/synth#Report.Render",
		"synth":                             "go://example.com/m/synth",
		"EvaluationsService.Create":         "proto://example.api.v1.EvaluationsService.Create",
		"example.api.v1.EvaluationsService": "proto://example.api.v1.EvaluationsService",
		"app.users":                         "sql://table/app.users",
		"users":                             "sql://table/app.users",
		"app.users.email":                   "sql://column/app.users.email",
		"GetUser":                           "sql://query/example.com/m/db#GetUser",
		"db.GetUser":                        "sql://query/example.com/m/db#GetUser",
		"manifesto.mission":                 "pxf://governance.pxf#manifesto.mission",
		"api/v1/evaluations.proto":          "proto://file/api/v1/evaluations.proto",
	}
	// One edge per (section, target): several spellings of one node in a
	// section make one edge, under the first spelling. So compare the
	// targets reached, and the resolution count per mention.
	targets := map[string]bool{}
	for _, to := range got {
		targets[to] = true
	}
	for k, v := range want {
		if !targets[v] {
			t.Errorf("%q did not reach %q; edges %v", k, v, got)
		}
	}
	if rep.Resolved != len(want) || rep.Ambiguous != 0 || rep.Unmatched != 0 {
		t.Errorf("report %+v, want %d resolved", rep, len(want))
	}
	for _, first := range []string{"synth.Report", "Report.Render", "app.users"} {
		if _, ok := got[first]; !ok {
			t.Errorf("edge for %q should carry the first spelling as its mention: %v", first, got)
		}
	}
}

// TestLinkRefuses is the conservative half: every mention here names
// nothing, or more than one thing, and none becomes an edge.
func TestLinkRefuses(t *testing.T) {
	got, rep := linked(t,
		"Config",              // two types share the name
		"evaluations.proto",   // two files share the basename
		"Report",              // the Go type and a Markdown section named Report: md is never a target, so this resolves — see below
		"INFERRED", "ENV_VAR", // all capitals
		"Missing", "no.such.thing", "docs/nowhere.md", "x.md", // nothing has these
		"ab", "", "not an identifier", "Report.", // too short, empty, prose, malformed
	)
	if _, ok := got["Report"]; !ok || got["Report"] != "go://example.com/m/synth#Report" {
		t.Errorf("Report should match the single Go type, not the Markdown section: %v", got)
	}
	delete(got, "Report")
	if len(got) != 0 {
		t.Errorf("unexpected edges: %v", got)
	}
	if rep.Resolved != 1 || rep.Ambiguous != 2 || rep.Unmatched != 10 {
		t.Errorf("report = %+v, want 1 resolved, 2 ambiguous, 10 unmatched", rep)
	}
	// The same mention twice in one section is one edge.
	got, _ = linked(t, "synth.Report", "synth.Report")
	if len(got) != 1 {
		t.Errorf("duplicate mention made %d edges", len(got))
	}
	if !strings.HasPrefix(got["synth.Report"], "go://") {
		t.Errorf("edge = %v", got)
	}
}

// TestLinkMarksTestTargets: a mention resolving to a declaration the Go
// layer marked as a test one yields a test documents edge, so
// exclude_tests leaves it out — neither the declaration nor the edge is
// in a build without tests (#81). This package sets nothing: the core
// derives the edge from its endpoints, and the assertion is that the
// result holds through Link without Link knowing about it.
func TestLinkMarksTestTargets(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	mint := func(id graph.ID, err error) graph.ID {
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	helper := mint(m.GoID("example.com/m/synth", "assertReady"))
	plain := mint(m.GoID("example.com/m/synth", "Render"))
	section := mint(m.MarkdownID("docs/y.md", "Notes"))
	for _, n := range []graph.Node{
		{ID: helper, Kind: graph.KindFunc, Name: "assertReady", Test: true},
		{ID: plain, Kind: graph.KindFunc, Name: "Render"},
		{ID: section, Kind: graph.KindSection, Name: "Notes"},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Mentions: []Mention{{Section: section, Text: "assertReady"}, {Section: section, Text: "Render"}}}
	Link(b, b.Build(), rep)
	for _, e := range b.Build().Out(section) {
		if e.Kind == graph.KindDocuments && e.Test != (e.To == helper) {
			t.Errorf("documents edge to %s: Test = %v, want %v", e.To, e.Test, e.To == helper)
		}
	}
	if rep.Resolved != 2 {
		t.Errorf("resolved = %d, want both mentions", rep.Resolved)
	}
}

// TestMentionsReachFrontendAndGoasm pins #98: a code span naming a
// frontend file, a generated export, a goasm file or a template resolves
// like a mention of any other layer; an external package is no file of
// the tree.
func TestMentionsReachFrontendAndGoasm(t *testing.T) {
	got, _ := linked(t, "web/gen/user_pb.ts", "UserSchema", "arm64.goasm", "AddLocalsInt", "react", "@pkg/react")
	for mention, want := range map[string]string{
		"web/gen/user_pb.ts": "ts://web/gen/user_pb.ts",
		"UserSchema":         "ts://web/gen/user_pb.ts#UserSchema",
		"arm64.goasm":        "goasm://jit/arm64.goasm",
		"AddLocalsInt":       "goasm://jit/arm64.goasm#AddLocalsInt",
	} {
		if got[mention] != want {
			t.Errorf("%s -> %q, want %q", mention, got[mention], want)
		}
	}
	for _, mention := range []string{"react", "@pkg/react"} {
		if to, ok := got[mention]; ok {
			t.Errorf("%s resolved to %s, an external package", mention, to)
		}
	}
}

// TestPackageTestsNodeIsNoMentionTarget pins #139: a package's test files'
// node (#128) carries the package's name, and a mention of the package is
// the package, as it is in a graph built without tests.
func TestPackageTestsNodeIsNoMentionTarget(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	pkg, err := m.GoID("example.com/m/synth", "")
	if err != nil {
		t.Fatal(err)
	}
	tests, err := m.GoTestID("example.com/m/synth")
	if err != nil {
		t.Fatal(err)
	}
	section, err := m.MarkdownID("docs/x.md", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []graph.Node{
		{ID: pkg, Kind: graph.KindPackage, Name: "synth"},
		{ID: tests, Kind: graph.KindPackageTests, Name: "synth", Test: true},
		{ID: section, Kind: graph.KindSection, Name: "Notes"},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Mentions: []Mention{{Section: section, Text: "synth"}}}
	Link(b, b.Build(), rep)
	if rep.Resolved != 1 || rep.Ambiguous != 0 {
		t.Fatalf("resolved %d, ambiguous %d; want the mention resolved", rep.Resolved, rep.Ambiguous)
	}
	if _, ok := hasDocuments(b.Build(), section, pkg); !ok {
		t.Error("the mention does not document the package")
	}
}

func hasDocuments(g *graph.Graph, from, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == graph.KindDocuments && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

// TestMentionPrefersTheProductionCandidate pins #139 (the owner's
// decision): a mention matching one production declaration and test ones
// of the same name documents the production one, as in a build without
// tests; two production candidates stay ambiguous, test ones or not.
func TestMentionPrefersTheProductionCandidate(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	id := func(pkg, decl string) graph.ID {
		v, err := m.GoID(pkg, decl)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	section, err := m.MarkdownID("docs/adr.md", "Decision")
	if err != nil {
		t.Fatal(err)
	}
	write, fake := id("example.com/m/capture", "Writer.Write"), id("example.com/m/capture", "failingOpener.Write")
	for _, n := range []graph.Node{
		{ID: section, Kind: graph.KindSection, Name: "Decision"},
		{ID: write, Kind: graph.KindMethod, Name: "Write"},
		{ID: fake, Kind: graph.KindMethod, Name: "Write", Test: true},
		{ID: id("example.com/m/a", "Run"), Kind: graph.KindFunc, Name: "Run"},
		{ID: id("example.com/m/b", "Run"), Kind: graph.KindFunc, Name: "Run"},
		{ID: id("example.com/m/b", "fakeRun.Run"), Kind: graph.KindMethod, Name: "Run", Test: true},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Mentions: []Mention{{Section: section, Text: "Write"}, {Section: section, Text: "Run"}}}
	Link(b, b.Build(), rep)
	if rep.Resolved != 1 || rep.Ambiguous != 1 {
		t.Errorf("resolved %d, ambiguous %d; want Write resolved and Run ambiguous", rep.Resolved, rep.Ambiguous)
	}
	if _, ok := hasDocuments(b.Build(), section, write); !ok {
		t.Error("Write does not document the production Writer.Write")
	}
}

// TestProcedurePaths pins #158 (the owner's decision, option b): an RPC
// procedure path names that rpc in a code span, behind any URL prefix,
// and in a fenced block — where it is the one thing read; a fenced
// block's bare word is not a mention.
func TestProcedurePaths(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	rpc, _ := m.ProtoID("example.api.v1.EvaluationsService.Create")
	section, _ := m.MarkdownID("docs/y.md", "Notes")
	for _, n := range []graph.Node{{ID: rpc, Kind: graph.KindRPC, Name: "Create"}, {ID: section, Kind: graph.KindSection, Name: "Notes"}} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Mentions: []Mention{
		{Section: section, Text: "/api/example.api.v1.EvaluationsService/Create"},
		{Section: section, Text: "example.api.v1.EvaluationsService/Create"},
		{Section: section, Text: "example.api.v1.EvaluationsService/Create", Fence: true},
		{Section: section, Text: "Create", Fence: true},
		{Section: section, Text: "example.api.v1.EvaluationsService/Delete", Fence: true},
	}}
	Link(b, b.Build(), rep)
	if rep.Resolved != 3 || rep.Unmatched != 2 {
		t.Errorf("resolved %d, unmatched %d; want 3 and 2", rep.Resolved, rep.Unmatched)
	}
	if _, ok := hasDoc(b.Build(), section, rpc); !ok {
		t.Errorf("no documents edge to %s", rpc)
	}
}

func hasDoc(g *graph.Graph, from, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == graph.KindDocuments && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

// TestFenceProcedures: a fenced block gives a mention for each procedure
// path in it and nothing else; a code span keeps its own text.
func TestFenceProcedures(t *testing.T) {
	dir := t.TempDir()
	doc := "# API\n\nCall `/api/x.v1.S/Get` first.\n\n```\nPOST /api/x.v1.S/Create\ncurl https://h/x.v1.S/List -d '{}'\nCreate Get\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var fence, span []string
	for _, m := range rep.Mentions {
		if m.Fence {
			fence = append(fence, m.Text)
		} else {
			span = append(span, m.Text)
		}
	}
	if strings.Join(fence, " ") != "x.v1.S/Create x.v1.S/List" || strings.Join(span, " ") != "/api/x.v1.S/Get" {
		t.Errorf("fence mentions %q, span mentions %q", fence, span)
	}
}

// TestBareWordNeedsParent pins #167: a bare lowercase word naming a
// nested declaration links only when the section names its parent; a
// qualified mention, a capitalised one and a top-level declaration are
// untouched.
func TestBareWordNeedsParent(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	mint := func(id graph.ID, err error) graph.ID {
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	status := mint(m.ProtoID("x.v1.Check.status"))
	derive := mint(m.GoID("example.com/m/lifecycle", "Machine.derive"))
	push := mint(m.GoID("example.com/m/lokiship", "shared.push"))
	prod := mint(m.PXFID("chain.pxf", "environments.prod"))
	scan := mint(m.GoID("example.com/m/definition", "unseeable"))
	plain, withParent := mint(m.MarkdownID("docs/a.md", "Plain")), mint(m.MarkdownID("docs/a.md", "Parent"))
	for _, n := range []graph.Node{
		{ID: status, Kind: graph.KindField, Name: "status"}, {ID: derive, Kind: graph.KindMethod, Name: "derive"},
		{ID: push, Kind: graph.KindMethod, Name: "push"}, {ID: prod, Kind: graph.KindBlock, Name: "prod"},
		{ID: scan, Kind: graph.KindFunc, Name: "unseeable"},
		{ID: plain, Kind: graph.KindSection, Name: "Plain"}, {ID: withParent, Kind: graph.KindSection, Name: "Parent"},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Words: map[graph.ID]map[string]bool{
		plain:      {"the": true, "report": true, "says": true},
		withParent: {"machine": true, "check": true, "environments": true},
	}}
	for _, sec := range []graph.ID{plain, withParent} {
		for _, text := range []string{"status", "derive", "push", "prod", "Check.status", "unseeable"} {
			rep.Mentions = append(rep.Mentions, Mention{Section: sec, Text: text})
		}
	}
	Link(b, b.Build(), rep)
	g := b.Build()
	for sec, want := range map[graph.ID][]graph.ID{plain: {status, scan}, withParent: {status, derive, prod, scan}} {
		var got []graph.ID
		for _, e := range g.Out(sec) {
			if e.Kind == graph.KindDocuments {
				got = append(got, e.To)
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s links %v, want %v", sec, got, want)
		}
	}
	// plain: status, derive, push and prod each lack a parent; status
	// still links once through Check.status. withParent: push lacks one.
	if rep.Unparented != 5 {
		t.Errorf("unparented %d, want 5", rep.Unparented)
	}
}

// TestLinkRecallForms: the spellings the recall sample found unanswered
// (#189) — a method as pkg.Method, a Go package by import path or by its
// directory, a hyphenated name — and the guard that keeps each from
// costing a link that resolved before: a package-level function still
// wins its pkg.Name over a method of that name, and a lowercase
// hyphenated name nested in a block needs its parent named.
func TestLinkRecallForms(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	node := func(pkg, decl string, kind graph.Kind, name, file string) {
		id, err := m.GoID(pkg, decl)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddNode(graph.Node{ID: id, Kind: kind, Name: name, Pos: graph.Position{File: file}}); err != nil {
			t.Fatal(err)
		}
	}
	node("example.com/m/sport/tennis", "", graph.KindPackage, "tennis", "")
	node("example.com/m/sport/tennis", "Adapter", graph.KindType, "Adapter", "sport/tennis/adapter.go")
	node("example.com/m/sport/tennis", "Adapter.Parse", graph.KindMethod, "Parse", "sport/tennis/adapter.go")
	node("example.com/m/run", "", graph.KindPackage, "run", "")
	node("example.com/m/run", "Start", graph.KindFunc, "Start", "run/run.go")
	node("example.com/m/run", "Machine.Start", graph.KindMethod, "Start", "run/run.go")
	if err := b.AddNode(graph.Node{ID: "go://go/ast", Kind: graph.KindPackage, Name: "ast", Attrs: map[string]string{"external": "true"}}); err != nil {
		t.Fatal(err)
	}
	persona, _ := m.PXFID("config.pxf", "auth.personas.wrong-audience")
	if err := b.AddNode(graph.Node{ID: persona, Kind: graph.KindBlock, Name: "wrong-audience"}); err != nil {
		t.Fatal(err)
	}
	with, _ := m.MarkdownID("docs/a.md", "Personas")
	without, _ := m.MarkdownID("docs/a.md", "Other")
	for _, id := range []graph.ID{with, without} {
		if err := b.AddNode(graph.Node{ID: id, Kind: graph.KindSection, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	rep := &Report{Words: map[graph.ID]map[string]bool{with: {"personas": true}}}
	for _, text := range []string{"tennis.Parse", "run.Start", "go/ast", "sport/tennis/", "./sport/tennis", "wrong-audience"} {
		rep.Mentions = append(rep.Mentions, Mention{Section: with, Text: text})
	}
	rep.Mentions = append(rep.Mentions, Mention{Section: without, Text: "wrong-audience"})
	Link(b, b.Build(), rep)
	g := b.Build()
	want := map[graph.ID]bool{
		"go://example.com/m/sport/tennis#Adapter.Parse": true,
		"go://example.com/m/run#Start":                  true, // the function, not Machine.Start
		"go://go/ast":                                   true,
		"go://example.com/m/sport/tennis":               true,
		persona:                                         true,
	}
	got := map[graph.ID]bool{}
	for _, e := range g.Out(with) {
		got[e.To] = true
	}
	if !maps.Equal(got, want) {
		t.Errorf("linked %v, want %v", got, want)
	}
	if out := g.Out(without); len(out) != 0 {
		t.Errorf("a hyphenated persona linked from a section not naming its parent: %v", out)
	}
}

// TestLinkPrefersTheSchemaOverItsInstances is #190's linker half: a
// mention answered by a proto field and by .pxf fields that set it names
// the schema; a .pxf field naming something else keeps it ambiguous.
func TestLinkPrefersTheSchemaOverItsInstances(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	schema, _ := m.ProtoID("pkg.v1.Manifesto.globs")
	inst, _ := m.PXFID("a.pxf", "manifesto.globs")
	other, _ := m.PXFID("b.pxf", "globs")
	sec, _ := m.MarkdownID("docs/a.md", "Ref")
	for id, kind := range map[graph.ID]graph.Kind{schema: graph.KindField, inst: graph.KindField, other: graph.KindField, sec: graph.KindSection} {
		if err := b.AddNode(graph.Node{ID: id, Kind: kind, Name: "globs"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.AddEdge(graph.Edge{From: inst, To: schema, Kind: graph.KindReferences, Derivation: graph.Resolved}); err != nil {
		t.Fatal(err)
	}
	link := func(ids ...graph.ID) []graph.ID { return schemaOf(b.Build(), ids) }
	if got := link(schema, inst); len(got) != 1 || got[0] != schema {
		t.Errorf("schema and its instance: %v, want the schema", got)
	}
	if got := link(schema, inst, other); len(got) != 2 {
		t.Errorf("with an unrelated field: %v, want it kept ambiguous", got)
	}
	if got := link(inst); len(got) != 1 {
		t.Errorf("one candidate: %v", got)
	}
}

// TestLinkBareFileNames is #191: a file named by its base name alone is
// the one in the mention's own directory; a .pxf file elsewhere is not
// linked by its name, which is its convention's name; a code file is.
func TestLinkBareFileNames(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	add := func(id graph.ID, kind graph.Kind, file string) {
		if err := b.AddNode(graph.Node{ID: id, Kind: kind, Name: id.String(), Pos: graph.Position{File: file}}); err != nil {
			t.Fatal(err)
		}
	}
	root, _ := m.PXFID("governance.pxf", "")
	example, _ := m.PXFID("docs/example/governance.pxf", "")
	ts, _ := m.TSID("web/src/client.ts", "")
	add(root, graph.KindPXFFile, "governance.pxf")
	add(example, graph.KindPXFFile, "docs/example/governance.pxf")
	add(ts, graph.KindModule, "web/src/client.ts")
	beside, _ := m.MarkdownID("docs/example/README.md", "Setup")
	elsewhere, _ := m.MarkdownID("docs/guide.md", "Setup")
	add(beside, graph.KindSection, "docs/example/README.md")
	add(elsewhere, graph.KindSection, "docs/guide.md")
	rep := &Report{Mentions: []Mention{
		{Section: beside, Text: "governance.pxf"},
		{Section: elsewhere, Text: "governance.pxf"},
		{Section: elsewhere, Text: "client.ts"},
	}}
	Link(b, b.Build(), rep)
	g := b.Build()
	targets := func(sec graph.ID) []graph.ID {
		var out []graph.ID
		for _, e := range g.Out(sec) {
			if e.Kind == graph.KindDocuments {
				out = append(out, e.To)
			}
		}
		return out
	}
	if got := targets(beside); len(got) != 1 || got[0] != example {
		t.Errorf("beside the example: %v, want its sibling %s", got, example)
	}
	if got := targets(elsewhere); len(got) != 1 || got[0] != ts {
		t.Errorf("elsewhere: %v, want only the code file %s", got, ts)
	}
}

// TestLinkSQLExpressions is #192's expression half: a span written as a
// SQL clause links to the one table or column its qualified names name,
// and to nothing when they name two.
func TestLinkSQLExpressions(t *testing.T) {
	got, _ := linked(t, "DELETE FROM app.users WHERE id = $1", "app.users.email IS NOT NULL")
	if got["DELETE FROM app.users WHERE id = $1"] != "sql://table/app.users" {
		t.Errorf("clause naming one table: %v", got)
	}
	if got["app.users.email IS NOT NULL"] != "sql://column/app.users.email" {
		t.Errorf("expression naming one column: %v", got)
	}
	got, _ = linked(t, "SELECT app.users.email FROM app.users")
	if len(got) != 0 {
		t.Errorf("a clause naming a table and a column linked %v", got)
	}
}

// TestFrontendDeclarationsAnswerLast pins #215's rule for the linker: a
// hand-written TypeScript declaration answers a mention only when nothing
// else does, so the type checker's thousands of members cannot make a
// mention that resolved before ambiguous — steward's `behavioral`, a proto
// field, met a TypeScript interface's field of the same name — while a
// name only the frontend has still resolves, and a generated export stays
// where it was.
func TestFrontendDeclarationsAnswerLast(t *testing.T) {
	b := graph.NewBuilder(t.TempDir())
	for _, n := range []graph.Node{
		{ID: "proto://gov.v1.Alternatives.behavioral", Kind: graph.KindField, Name: "behavioral"},
		{ID: "ts://app/src/alternatives.ts#Alternatives.behavioral", Kind: graph.KindField, Name: "behavioral"},
		{ID: "ts://app/src/rpc.ts#useRpcClients", Kind: graph.KindFunc, Name: "useRpcClients"},
		{ID: "ts://app/scripts/build.mjs#main", Kind: graph.KindFunc, Name: "main"},
		{ID: "ts://app/gen/user_pb.ts#UserSchema", Kind: graph.KindConst, Name: "UserSchema", Generated: true},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	ix := buildIndex(b.Build())
	for text, want := range map[string]string{
		"behavioral":              "proto://gov.v1.Alternatives.behavioral",
		"Alternatives.behavioral": "proto://gov.v1.Alternatives.behavioral",
		"useRpcClients":           "ts://app/src/rpc.ts#useRpcClients",
		"main":                    "", // one lowercase word is the word, not the declaration
		"UserSchema":              "ts://app/gen/user_pb.ts#UserSchema",
	} {
		got := ix.lookup(text)
		if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || string(got[0]) != want) {
			t.Errorf("lookup(%q) = %v, want %q", text, got, want)
		}
	}
}

// TestLinkSpanForms: the forms the redrawn recall sample found unanswered
// (#7) — a call or a signature, a method reached through a value or a
// receiver, a package by the end of its import path, a command by its
// name — and a proto message with the Go type generated from it, which
// is one answer, the message. Each guard is pinned too: a call form is
// read only when the span answers nothing whole, a one-segment path is
// no package ending, a single word is no command, and a Go type with no
// generates edge from the message is still a second answer.
func TestLinkSpanForms(t *testing.T) {
	b := graph.NewBuilder("")
	m := b.Minter()
	goNode := func(pkg, decl string, kind graph.Kind, name string) graph.ID {
		t.Helper()
		id, err := m.GoID(pkg, decl)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddNode(graph.Node{ID: id, Kind: kind, Name: name}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failed := goNode("example.com/m/analysis", "Engine.Failed", graph.KindMethod, "Failed")
	goNode("example.com/m/analysis", "Engine", graph.KindType, "Engine")
	validate := goNode("example.com/m/validate", "Validate", graph.KindFunc, "Validate")
	unmarshal := goNode("example.com/m/pxf", "UnmarshalOptions.Unmarshal", graph.KindMethod, "Unmarshal")
	resolve := goNode("example.com/m/resolver", "Resolver.Resolve", graph.KindMethod, "Resolve")
	sops := goNode("example.com/m/sops", "", graph.KindPackage, "sops")
	plugin := goNode("example.com/m/cmd/protoc-gen-thing-go", "", graph.KindPackage, "main")
	goNode("example.com/m/cmd/thing", "", graph.KindPackage, "main")
	goNode("example.com/m/run", "Start", graph.KindFunc, "Start")
	// Started answers whole; its call form must not be read.
	goNode("example.com/m/run", "Started", graph.KindFunc, "Started")
	msg := graph.ID("proto://m.v1.Manifest")
	if err := b.AddNode(graph.Node{ID: msg, Kind: graph.KindMessage, Name: "Manifest"}); err != nil {
		t.Fatal(err)
	}
	gen := goNode("example.com/m/gen/v1", "Manifest", graph.KindType, "Manifest")
	if err := b.AddEdge(graph.Edge{From: msg, To: gen, Kind: graph.KindGenerates, Derivation: graph.Declared}); err != nil {
		t.Fatal(err)
	}
	// Report is a Go type and a message with no generates edge between them.
	goNode("example.com/m/report", "Report", graph.KindType, "Report")
	if err := b.AddNode(graph.Node{ID: "proto://m.v1.Report", Kind: graph.KindMessage, Name: "Report"}); err != nil {
		t.Fatal(err)
	}
	sec, _ := m.MarkdownID("docs/a.md", "Uses")
	if err := b.AddNode(graph.Node{ID: sec, Kind: graph.KindSection, Name: "Uses"}); err != nil {
		t.Fatal(err)
	}
	rep := &Report{Words: map[graph.ID]map[string]bool{}}
	for _, text := range []string{
		"Engine.Failed()", "Validate(msg, present) error", "UnmarshalOptions{SkipValidate: true}.Unmarshal",
		"(*Resolver[T]).Resolve(ctx, req)", "m/sops", "protoc-gen-thing-go", "Manifest",
		"sops/", "thing", "Report", "Started()",
	} {
		rep.Mentions = append(rep.Mentions, Mention{Section: sec, Text: text})
	}
	Link(b, b.Build(), rep)
	got := map[graph.ID]string{}
	for _, e := range b.Build().Out(sec) {
		got[e.To] = e.Attrs["mention"]
	}
	want := map[graph.ID]string{
		failed: "Engine.Failed()", validate: "Validate(msg, present) error",
		unmarshal: "UnmarshalOptions{SkipValidate: true}.Unmarshal", resolve: "(*Resolver[T]).Resolve(ctx, req)",
		sops: "m/sops", plugin: "protoc-gen-thing-go", msg: "Manifest",
		"go://example.com/m/run#Started": "Started()",
	}
	if !maps.Equal(got, want) {
		t.Errorf("linked %v\nwant %v", got, want)
	}
}

func TestCalleeOf(t *testing.T) {
	for text, want := range map[string]string{
		"Engine.Failed()":                  "Engine.Failed",
		"Validate(msg, present) error":     "Validate",
		"New[T proto.Message](sample T)":   "New",
		"UnmarshalOptions{A: 1}.Unmarshal": "UnmarshalOptions.Unmarshal",
		"(*Resolver[T]).Resolve(ctx)":      "Resolver.Resolve",
		"(Options).Expand(spec)":           "Options.Expand",
		"Engine.Failed":                    "",
		"go test ./...":                    "",
		"if err != nil { return err }":     "",
	} {
		if got := calleeOf(text); got != want {
			t.Errorf("calleeOf(%q) = %q, want %q", text, got, want)
		}
	}
}
