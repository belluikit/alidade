// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
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

// extract runs the adapter over the fixture once and shares the immutable
// result.
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

func id(s string) graph.ID { return graph.ID("proto://" + s) }

func hasEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func wantEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID, d graph.Derivation) graph.Edge {
	t.Helper()
	e, ok := hasEdge(g, from, kind, to)
	if !ok {
		t.Errorf("missing edge %s -%s-> %s", from, kind, to)
	} else if e.Derivation != d {
		t.Errorf("edge %s -%s-> %s: derivation %s, want %s", from, kind, to, e.Derivation, d)
	}
	return e
}

func wantNode(t *testing.T, g *graph.Graph, id graph.ID, kind graph.Kind) graph.Node {
	t.Helper()
	n, ok := g.Node(id)
	if !ok {
		t.Errorf("missing node %s", id)
	} else if n.Kind != kind {
		t.Errorf("node %s: kind %s, want %s", id, n.Kind, kind)
	}
	return n
}

func TestFilesAndReport(t *testing.T) {
	g, rep := extract(t)
	if rep.Files != 6 || rep.Failed != 1 || rep.External != 1 || rep.Generates != 6 || rep.Testdata != 1 || rep.NamingErrors != 0 {
		t.Errorf("report = %+v", rep)
	}
	for n := range g.Nodes() {
		if e := n.Attrs["go_names_error"]; e != "" {
			t.Errorf("%s: go_names_error %q", n.ID, e)
		}
	}
	f := wantNode(t, g, id("file/proto/example/v1/shape.proto"), graph.KindProtoFile)
	if f.Attrs["package"] != "example.v1" || f.Attrs["go_package"] != "example.com/fixture/gen/example/v1" || f.Pos.File != "proto/example/v1/shape.proto" {
		t.Errorf("shape.proto node = %+v", f)
	}
	// A file outside every buf module is addressed relative to the root
	// and compiles on its own.
	wantNode(t, g, id("file/other/nopkg.proto"), graph.KindProtoFile)
	wantEdge(t, g, id("file/other/nopkg.proto"), graph.KindContains, id("other.Plain"), graph.Extracted)
}

func TestFailedFileHasNodeAndNothingElse(t *testing.T) {
	g, _ := extract(t)
	n := wantNode(t, g, id("file/other/broken.proto"), graph.KindProtoFile)
	if !strings.Contains(n.Attrs["error"], "broken.proto:7") {
		t.Errorf("error attr = %q, want the compiler's message with a position", n.Attrs["error"])
	}
	if len(g.Out(n.ID)) != 0 {
		t.Errorf("failed file has edges: %+v", g.Out(n.ID))
	}
	if _, ok := g.Node(id("other.Broken")); ok {
		t.Error("a declaration from a file that did not compile was emitted")
	}
}

func TestDeclarationsAndContains(t *testing.T) {
	g, _ := extract(t)
	for _, tt := range []struct {
		id   string
		kind graph.Kind
	}{
		{"example.v1.Shape", graph.KindMessage},
		{"example.v1.Shape.Side", graph.KindMessage},
		{"example.v1.Shape.Kind", graph.KindEnum},
		{"example.v1.Shape.KIND_CIRCLE", graph.KindEnumValue}, // sibling of the enum, as protobuf scopes it
		{"example.v1.Shape.name", graph.KindField},
		{"example.v1.Palette", graph.KindEnum},
		{"example.v1.ShapeService", graph.KindService},
		{"example.v1.ShapeService.GetShape", graph.KindRPC},
	} {
		wantNode(t, g, id(tt.id), tt.kind)
	}
	wantEdge(t, g, id("file/proto/example/v1/shape.proto"), graph.KindContains, id("example.v1.Shape"), graph.Extracted)
	wantEdge(t, g, id("example.v1.Shape"), graph.KindContains, id("example.v1.Shape.Side"), graph.Extracted)
	wantEdge(t, g, id("example.v1.Shape"), graph.KindContains, id("example.v1.Shape.name"), graph.Extracted)
	wantEdge(t, g, id("example.v1.Shape.Kind"), graph.KindContains, id("example.v1.Shape.KIND_CIRCLE"), graph.Extracted)
	wantEdge(t, g, id("example.v1.ShapeService"), graph.KindContains, id("example.v1.ShapeService.GetShape"), graph.Extracted)
	if _, ok := g.Node(id("example.v1.Shape.UnitsByNameEntry")); ok {
		t.Error("a map field's synthetic entry message became a node")
	}

	n := wantNode(t, g, id("example.v1.Shape.Side"), graph.KindMessage)
	if n.Pos.File != "proto/example/v1/shape.proto" || n.Pos.Line != 24 || n.Pos.Col != 3 {
		t.Errorf("Side position = %+v, want shape.proto:24:3", n.Pos)
	}
	f := wantNode(t, g, id("example.v1.Shape.units_by_name"), graph.KindField)
	if f.Attrs["number"] != "6" || f.Attrs["map"] != "true" || f.Attrs["repeated"] != "true" {
		t.Errorf("map field attrs = %v", f.Attrs)
	}
	if o := wantNode(t, g, id("example.v1.Shape.radius"), graph.KindField); o.Attrs["oneof"] != "size" {
		t.Errorf("oneof member attrs = %v", o.Attrs)
	}
}

func TestImports(t *testing.T) {
	g, _ := extract(t)
	shape := id("file/proto/example/v1/shape.proto")
	wantEdge(t, g, shape, graph.KindImports, id("file/proto/example/v1/unit.proto"), graph.Extracted)
	// An import no file in the repository answers is an external file
	// node, under the path it was imported by.
	wantEdge(t, g, shape, graph.KindImports, id("file/google/protobuf/timestamp.proto"), graph.Extracted)
	ext := wantNode(t, g, id("file/google/protobuf/timestamp.proto"), graph.KindProtoFile)
	if ext.Attrs["external"] != "true" || ext.Pos.File != "" {
		t.Errorf("external file node = %+v", ext)
	}
}

func TestReferences(t *testing.T) {
	g, _ := extract(t)
	wantEdge(t, g, id("example.v1.Shape.unit"), graph.KindReferences, id("example.v1.Unit"), graph.Resolved)          // cross-file message
	wantEdge(t, g, id("example.v1.Shape.kind"), graph.KindReferences, id("example.v1.Shape.Kind"), graph.Resolved)    // nested enum
	wantEdge(t, g, id("example.v1.Shape.children"), graph.KindReferences, id("example.v1.Shape"), graph.Resolved)     // self
	wantEdge(t, g, id("example.v1.Shape.units_by_name"), graph.KindReferences, id("example.v1.Unit"), graph.Resolved) // map value
	wantEdge(t, g, id("example.v1.ShapeService.GetShape"), graph.KindReferences, id("example.v1.GetShapeRequest"), graph.Resolved)
	wantEdge(t, g, id("example.v1.ShapeService.GetShape"), graph.KindReferences, id("example.v1.Shape"), graph.Resolved)
	if _, ok := hasEdge(g, id("example.v1.Shape.name"), graph.KindReferences, ""); ok {
		t.Error("scalar field references something")
	}
	// A type from outside the repository lands on its file's external
	// node, named in the attrs, rather than on an invented node.
	e := wantEdge(t, g, id("example.v1.Shape.created"), graph.KindReferences, id("file/google/protobuf/timestamp.proto"), graph.Resolved)
	if e.Attrs["boundary"] != "file" || e.Attrs["type"] != "google.protobuf.Timestamp" {
		t.Errorf("boundary reference attrs = %v", e.Attrs)
	}
}

// TestGeneratesFollowsProtocGenGo: the Go type names come from protogen,
// so nesting and casing match what protoc-gen-go emits. The fixture has no
// Go layer, so the edges drop at merge — which is the point: they resolve
// only when the Go layer saw the type.
func TestGeneratesFollowsProtocGenGo(t *testing.T) {
	g, _ := extract(t)
	want := map[string]string{
		"example.v1.Shape":           "go://example.com/fixture/gen/example/v1#Shape",
		"example.v1.Shape.Side":      "go://example.com/fixture/gen/example/v1#Shape_Side",
		"example.v1.Shape.Kind":      "go://example.com/fixture/gen/example/v1#Shape_Kind",
		"example.v1.Palette":         "go://example.com/fixture/gen/example/v1#Palette",
		"example.v1.Unit":            "go://example.com/fixture/gen/example/v1#Unit",
		"example.v1.GetShapeRequest": "go://example.com/fixture/gen/example/v1#GetShapeRequest",
	}
	got := map[string]string{}
	for _, d := range g.Dropped() {
		if d.Edge.Kind != graph.KindGenerates {
			t.Errorf("unexpected dropped edge %+v", d)
			continue
		}
		if d.Reason != graph.DropUnknownTarget || d.Edge.Derivation != graph.Declared || d.Edge.Attrs["via"] != "go_package" {
			t.Errorf("generates edge %+v: want unknown_target, DECLARED, via=go_package", d)
		}
		got[strings.TrimPrefix(string(d.Edge.From), "proto://")] = string(d.Edge.To)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("generates from %s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("generates edges = %v", got)
	}
	// No go_package, no Go type.
	for _, e := range g.Out(id("other.Plain")) {
		if e.Kind == graph.KindGenerates {
			t.Errorf("Plain has a generates edge with no go_package: %+v", e)
		}
	}
}

func TestNoConflicts(t *testing.T) {
	g, _ := extract(t)
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts: %+v", c)
	}
}

func TestDeterministic(t *testing.T) {
	dir := fixtureDir(t)
	var outs [2]bytes.Buffer
	for i := range outs {
		b := graph.NewBuilder(dir)
		if _, err := Extract(b, dir, DefaultOptions()); err != nil {
			t.Fatal(err)
		}
		if err := b.Build().WritePXF(&outs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outs[0].Bytes(), outs[1].Bytes()) {
		t.Error("two extractions of an unchanged tree differ")
	}
}

func TestIncludeRoots(t *testing.T) {
	dir := fixtureDir(t)
	c, err := discover(dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	roots := c.roots
	// The buf.yaml module, the root inferred from legacy/api/x/a.proto's
	// import, and the repository root last.
	want := []string{filepath.Join(dir, "legacy", "api"), filepath.Join(dir, "proto"), dir}
	if strings.Join(roots, ",") != strings.Join(want, ",") {
		t.Errorf("roots = %v, want %v", roots, want)
	}
	if len(c.files) != 6 || c.testdata != 1 {
		t.Errorf("discover: %d files, %d testdata; want 6 and 1", len(c.files), c.testdata)
	}
	if got := importPath(roots, filepath.Join(dir, "proto", "example", "v1", "unit.proto")); got != "example/v1/unit.proto" {
		t.Errorf("importPath under module = %q", got)
	}
	if got := importPath(roots, filepath.Join(dir, "other", "nopkg.proto")); got != "other/nopkg.proto" {
		t.Errorf("importPath outside module = %q", got)
	}
	if locate(roots, "example/v1/unit.proto") == "" || locate(roots, "google/protobuf/timestamp.proto") != "" {
		t.Error("locate finds the wrong files")
	}

}

// TestBufWorkAndSkippedDirs: buf.work.yaml v1 directories are roots, and
// node_modules is never entered.
func TestBufWorkAndSkippedDirs(t *testing.T) {
	work := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(work, "buf.work.yaml"), []byte("version: v1\ndirectories:\n  - api\n"), 0o644))
	must(os.MkdirAll(filepath.Join(work, "api", "x"), 0o755))
	must(os.MkdirAll(filepath.Join(work, "node_modules", "dep"), 0o755))
	must(os.WriteFile(filepath.Join(work, "api", "x", "a.proto"), []byte("syntax = \"proto3\";\npackage x;\n"), 0o644))
	must(os.WriteFile(filepath.Join(work, "node_modules", "dep", "b.proto"), []byte("syntax = \"proto3\";\n"), 0o644))
	wc, err := discover(work, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wc.roots) != 2 || wc.roots[0] != filepath.Join(work, "api") {
		t.Errorf("buf.work.yaml roots = %v", wc.roots)
	}
	if len(wc.files) != 1 || !strings.HasSuffix(wc.files[0], "a.proto") {
		t.Errorf("discover = %v; want only api/x/a.proto", wc.files)
	}
}

// TestManagedMode: unit.proto has no go_package; buf.gen.yaml's managed
// mode gives it example.com/fixture/gen + example/v1, and its Go type
// follows.
func TestManagedMode(t *testing.T) {
	g, _ := extract(t)
	f := wantNode(t, g, id("file/proto/example/v1/unit.proto"), graph.KindProtoFile)
	if f.Attrs["go_package"] != "example.com/fixture/gen/example/v1" {
		t.Errorf("unit.proto go_package = %q, want managed mode's answer", f.Attrs["go_package"])
	}
	// legacy/ is outside the config's inputs directory, so no Go path.
	if l := wantNode(t, g, id("file/legacy/api/x/a.proto"), graph.KindProtoFile); l.Attrs["go_package"] != "" {
		t.Errorf("a.proto go_package = %q, want none", l.Attrs["go_package"])
	}
	cfgs := loadGenConfigs(fixtureDir(t))
	if len(cfgs) != 1 {
		t.Fatalf("loadGenConfigs = %+v", cfgs)
	}
	for _, tt := range []struct{ rel, imp, want string }{
		{"proto/example/v1/unit.proto", "example/v1/unit.proto", "example.com/fixture/gen/example/v1"},
		{"proto/top.proto", "top.proto", "example.com/fixture/gen"},
		{"other/nopkg.proto", "other/nopkg.proto", ""},
	} {
		if got := goImportPathFor(cfgs, tt.rel, tt.imp); got != tt.want {
			t.Errorf("goImportPathFor(%s) = %q, want %q", tt.rel, got, tt.want)
		}
	}
	// Overrides: later and path-specific ones win, inputs paths and
	// excludes select.
	cfgs = []genConfig{
		{excludes: []string{"proto/pub"}, overrides: []prefixOverride{{"", "m/internal/gen"}, {"internal", "m/internal/gen/internal_"}}},
		{paths: []string{"proto/pub"}, overrides: []prefixOverride{{"", "m/gen"}}},
	}
	for _, tt := range []struct{ rel, imp, want string }{
		{"proto/a/b.proto", "a/b.proto", "m/internal/gen/a"},
		{"internal/proto/internal/c.proto", "internal/c.proto", "m/internal/gen/internal_/internal"},
		{"proto/pub/d.proto", "pub/d.proto", "m/gen/pub"},
	} {
		if got := goImportPathFor(cfgs, tt.rel, tt.imp); got != tt.want {
			t.Errorf("goImportPathFor(%s) = %q, want %q", tt.rel, got, tt.want)
		}
	}
	// A disable rule keeps a path out of managed go_package (#148).
	cfgs = []genConfig{{overrides: []prefixOverride{{"", "m/gen"}}, disabled: []string{"vendor"}}}
	for _, tt := range []struct{ rel, imp, want string }{
		{"proto/a/b.proto", "a/b.proto", "m/gen/a"},
		{"proto/vendor/v.proto", "vendor/v.proto", ""},
		{"wellknownimports/fs/google/protobuf/any.proto", "google/protobuf/any.proto", ""},
	} {
		if got := goImportPathFor(cfgs, tt.rel, tt.imp); got != tt.want {
			t.Errorf("goImportPathFor(%s) = %q, want %q", tt.rel, got, tt.want)
		}
	}
}

// TestInferredRoot: legacy/api has no buf configuration; a.proto's import
// of x/b.proto makes legacy/api a root, so both compile and link.
func TestInferredRoot(t *testing.T) {
	g, _ := extract(t)
	wantEdge(t, g, id("file/legacy/api/x/a.proto"), graph.KindImports, id("file/legacy/api/x/b.proto"), graph.Extracted)
	wantEdge(t, g, id("x.A.b"), graph.KindReferences, id("x.B"), graph.Resolved)
}

// TestTestdataIsOptional: the copy of unit.proto under proto/testdata is
// out by default; in, it declares example.v1.Unit a second time and the
// merge reports the conflict rather than picking quietly.
func TestTestdataIsOptional(t *testing.T) {
	g, _ := extract(t)
	if _, ok := g.Node(id("file/proto/testdata/example/v1/unit.proto")); ok {
		t.Error("testdata file present by default")
	}
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, Options{Testdata: true})
	if err != nil {
		t.Fatal(err)
	}
	g = b.Build()
	if rep.Files != 7 || rep.Testdata != 0 {
		t.Errorf("report with testdata = %+v", rep)
	}
	wantNode(t, g, id("file/proto/testdata/example/v1/unit.proto"), graph.KindProtoFile)
	found := false
	for _, c := range g.Conflicts() {
		if c.Subject == "proto://example.v1.Unit" && c.Field == "position" {
			found = true
		}
	}
	if !found {
		t.Errorf("duplicate declaration not reported as a conflict: %+v", g.Conflicts())
	}
}

func TestEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil || rep.Files != 0 || b.Build().NodeCount() != 0 {
		t.Errorf("empty repository: rep=%+v err=%v", rep, err)
	}
}

// TestSchemaRootManagedMode pins what #124's typed path turned up: a
// schema root's buf.gen.yaml inputs are its own directories, and the file
// path matched against them was its labelled one, "@label/proto/…", which
// no input directory is. So a schema root's file without go_package got
// no managed-mode answer; it is matched by its path in the tree now.
func TestSchemaRootManagedMode(t *testing.T) {
	dir := fixtureDir(t)
	primary := graph.NewBuilder(t.TempDir())
	b, err := primary.Secondary("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(b, dir, DefaultOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	f := wantNode(t, primary.Build(), id("file/@fixture/proto/example/v1/unit.proto"), graph.KindProtoFile)
	if f.Attrs["go_package"] != "example.com/fixture/gen/example/v1" {
		t.Errorf("unit.proto go_package = %q, want managed mode's answer", f.Attrs["go_package"])
	}
	if f.Pos.File != "@fixture/proto/example/v1/unit.proto" {
		t.Errorf("unit.proto is at %q, labelled once", f.Pos.File)
	}
}

// TestManagedModeReplacesWrittenGoPackage pins what the proto oracle
// found on protovalidate (#148): managed mode replaces a go_package the
// file writes, so the generated type is the one under the prefix; a
// disable rule keeps a path's written option.
func TestManagedModeReplacesWrittenGoPackage(t *testing.T) {
	g := extractTree(t, map[string]string{
		"buf.yaml": "version: v2\nmodules:\n  - path: proto\n",
		"buf.gen.yaml": "version: v2\nmanaged:\n  enabled: true\n  override:\n    - file_option: go_package_prefix\n      value: example.com/m/internal/gen\n" +
			"  disable:\n    - file_option: go_package\n      path: keep\n",
		"proto/v/v.proto":    "syntax = \"proto3\";\npackage v;\noption go_package = \"buf.build/gen/go/acme/v\";\nmessage V {}\n",
		"proto/keep/k.proto": "syntax = \"proto3\";\npackage keep;\noption go_package = \"example.com/m/keep\";\nmessage K {}\n",
	})
	for file, want := range map[string]string{"proto/v/v.proto": "example.com/m/internal/gen/v", "proto/keep/k.proto": "example.com/m/keep"} {
		if n := wantNode(t, g, id("file/"+file), graph.KindProtoFile); n.Attrs["go_package"] != want {
			t.Errorf("%s go_package = %q, want %q", file, n.Attrs["go_package"], want)
		}
	}
	// The generates edge follows: it is protogen's answer, and it must be
	// given the managed path, not take the written one.
	for from, to := range map[graph.ID]graph.ID{id("v.V"): "go://example.com/m/internal/gen/v#V", id("keep.K"): "go://example.com/m/keep#K"} {
		if !droppedEdge(g, from, graph.KindGenerates, to) {
			t.Errorf("no generates %s -> %s", from, to)
		}
	}
}

// extractTree writes files into a temporary directory and extracts it.
func extractTree(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := graph.NewBuilder(dir)
	if _, err := Extract(b, dir, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	return b.Build()
}

func droppedEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) bool {
	for _, d := range g.Dropped() {
		if d.Edge.From == from && d.Edge.Kind == kind && d.Edge.To == to {
			return true
		}
	}
	return false
}

// TestModuleFilesIgnoreInferredRoots pins what the proto oracle found on
// protocompile (#148): a buf module's file importing a well-known type
// takes it from outside the repository, as buf does, though a subtree no
// buf configuration covers holds a copy; that subtree's own files still
// resolve through the root inferred for them.
func TestModuleFilesIgnoreInferredRoots(t *testing.T) {
	g := extractTree(t, map[string]string{
		"buf.yaml":                         "version: v2\nmodules:\n  - path: proto\n",
		"proto/a/a.proto":                  "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/any.proto\";\nmessage A { google.protobuf.Any x = 1; }\n",
		"copies/google/protobuf/any.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Any { string type_url = 1; bytes value = 2; }\n",
		"copies/google/protobuf/api.proto": "syntax = \"proto3\";\npackage google.protobuf;\nimport \"google/protobuf/any.proto\";\nmessage Api { Any a = 1; }\n",
	})
	wantEdge(t, g, id("file/proto/a/a.proto"), graph.KindImports, id("file/google/protobuf/any.proto"), graph.Extracted)
	noEdgeTo(t, g, id("file/proto/a/a.proto"), id("file/copies/google/protobuf/any.proto"))
	wantEdge(t, g, id("file/copies/google/protobuf/api.proto"), graph.KindImports, id("file/copies/google/protobuf/any.proto"), graph.Extracted)
}

func noEdgeTo(t *testing.T, g *graph.Graph, from, to graph.ID) {
	t.Helper()
	for _, e := range g.Out(from) {
		if e.To == to {
			t.Errorf("unexpected %s -%s-> %s", from, e.Kind, to)
		}
	}
}
