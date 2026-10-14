// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package goasm

import (
	"os"
	"path/filepath"
	"reflect"
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

func id(s string) graph.ID { return graph.ID("goasm://" + s) }

func node(t *testing.T, g *graph.Graph, id graph.ID) graph.Node {
	t.Helper()
	n, ok := g.Node(id)
	if !ok {
		t.Fatalf("no node %s", id)
	}
	return n
}

func edgeTo(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func TestReport(t *testing.T) {
	_, rep := extract(t)
	want := Report{Files: 4, Failed: 1, Templates: 5, Directives: 2, Linked: 4, Unlinked: 1, Testdata: 1}
	got := *rep
	got.Elapsed = 0
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Report = %+v\nwant     %+v", got, want)
	}
}

func TestFileNodes(t *testing.T) {
	g, _ := extract(t)
	f := node(t, g, id("jit/templates/arm64.goasm"))
	if f.Kind != graph.KindGoasmFile || f.Name != "arm64.goasm" || f.Pos.File != "jit/templates/arm64.goasm" {
		t.Errorf("file node = %+v", f)
	}
	for k, want := range map[string]string{
		"arch":    "arm64",
		"package": "example.com/fixture/jit",
		"aliases": "BP=X22 CTX=X19 SP=X21 TAG_INT=X28",
	} {
		if f.Attrs[k] != want {
			t.Errorf("arm64.goasm attr %s = %q, want %q", k, f.Attrs[k], want)
		}
	}
	// A file no directive names has no target, and says so by omission.
	loose := node(t, g, id("stray/loose.goasm"))
	for _, k := range []string{"arch", "package", "aliases"} {
		if _, ok := loose.Attrs[k]; ok {
			t.Errorf("loose.goasm carries attr %s = %q", k, loose.Attrs[k])
		}
	}
}

func TestTemplateNode(t *testing.T) {
	g, _ := extract(t)
	add := node(t, g, id("jit/templates/arm64.goasm#AddLocalsInt"))
	if add.Kind != graph.KindTemplate || add.Name != "AddLocalsInt" {
		t.Errorf("template node = %+v", add)
	}
	if add.Pos.File != "jit/templates/arm64.goasm" || add.Pos.Line != 6 {
		t.Errorf("AddLocalsInt position = %+v, want line 6 of the file", add.Pos)
	}
	want := map[string]string{"params": "dst uint8, src1 uint8, src2 uint8", "instructions": "8"}
	if len(add.Attrs) != len(want) {
		t.Errorf("AddLocalsInt attrs = %v, want %v", add.Attrs, want)
	}
	for k, v := range want {
		if add.Attrs[k] != v {
			t.Errorf("AddLocalsInt attr %s = %q, want %q", k, add.Attrs[k], v)
		}
	}
}

func TestTemplateAttrs(t *testing.T) {
	g, _ := extract(t)
	for _, tt := range []struct {
		id           string
		line         int
		params, want string
	}{
		{"jit/templates/arm64.goasm#PushInt", 16, "reg int", "2"},
		{"jit/templates/amd64.goasm#Breakpoint", 14, "", "1"},
		{"stray/loose.goasm#Nop", 2, "", "1"},
	} {
		n := node(t, g, id(tt.id))
		if n.Pos.Line != tt.line || n.Attrs["params"] != tt.params || n.Attrs["instructions"] != tt.want {
			t.Errorf("%s = line %d attrs %v; want line %d, params %q, instructions %s", tt.id, n.Pos.Line, n.Attrs, tt.line, tt.params, tt.want)
		}
	}
}

func TestContains(t *testing.T) {
	g, _ := extract(t)
	for _, tt := range []struct{ file, name string }{
		{"jit/templates/arm64.goasm", "AddLocalsInt"},
		{"jit/templates/arm64.goasm", "PushInt"},
		{"jit/templates/amd64.goasm", "AddLocalsInt"},
		{"jit/templates/amd64.goasm", "Breakpoint"},
		{"stray/loose.goasm", "Nop"},
	} {
		e, ok := edgeTo(g, id(tt.file), graph.KindContains, id(tt.file+"#"+tt.name))
		if !ok {
			t.Errorf("missing contains edge %s -> %s", tt.file, tt.name)
		} else if e.Derivation != graph.Extracted {
			t.Errorf("contains %s#%s derivation = %s", tt.file, tt.name, e.Derivation)
		}
	}
}

// TestGeneratesEdges pins the naming rule: a template's emitter is the
// method Emit<Name> on Emitter in the package the directive's -out
// lands in. The adapter runs alone here, so every edge drops for an
// unknown target; the edges themselves are what is checked, and the
// build package's test checks that they resolve against the Go layer.
func TestGeneratesEdges(t *testing.T) {
	g, _ := extract(t)
	want := map[graph.ID]string{
		id("jit/templates/arm64.goasm#AddLocalsInt"): "arm64",
		id("jit/templates/arm64.goasm#PushInt"):      "arm64",
		id("jit/templates/amd64.goasm#AddLocalsInt"): "amd64",
		id("jit/templates/amd64.goasm#Breakpoint"):   "amd64",
	}
	seen := map[graph.ID]bool{}
	for _, d := range g.Dropped() {
		e := d.Edge
		if e.Kind != graph.KindGenerates {
			t.Errorf("dropped a non-generates edge: %+v", d)
			continue
		}
		arch, ok := want[e.From]
		if !ok {
			t.Errorf("unexpected generates edge from %s", e.From)
			continue
		}
		seen[e.From] = true
		name := string(e.From)[strings.LastIndexByte(string(e.From), '#')+1:]
		if wantTo := graph.ID("go://example.com/fixture/jit#Emitter.Emit" + name); e.To != wantTo {
			t.Errorf("%s generates %s, want %s", e.From, e.To, wantTo)
		}
		if e.Derivation != graph.Declared || e.Attrs["via"] != "goasm" || e.Attrs["arch"] != arch {
			t.Errorf("%s generates edge = derivation %s attrs %v", e.From, e.Derivation, e.Attrs)
		}
		if d.Reason != graph.DropUnknownTarget {
			t.Errorf("%s dropped for %s, want unknown target", e.From, d.Reason)
		}
	}
	for from := range want {
		if !seen[from] {
			t.Errorf("no generates edge from %s", from)
		}
	}
	if _, ok := edgeTo(g, id("stray/loose.goasm#Nop"), graph.KindGenerates, ""); ok {
		t.Error("loose.goasm#Nop has a generates edge with no directive naming its file")
	}
}

func TestBrokenFileKeepsItsNode(t *testing.T) {
	g, _ := extract(t)
	n := node(t, g, id("stray/broken.goasm"))
	if n.Attrs["error"] == "" {
		t.Errorf("broken.goasm node carries no error: %+v", n)
	}
	if len(g.Out(n.ID)) != 0 {
		t.Errorf("broken.goasm has %d edges, want none", len(g.Out(n.ID)))
	}
}

func TestTestdataSkipped(t *testing.T) {
	g, _ := extract(t)
	if _, ok := g.Node(id("testdata/skipped.goasm")); ok {
		t.Error("testdata/skipped.goasm was extracted by default")
	}
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, Options{Testdata: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 5 || rep.Testdata != 0 {
		t.Errorf("with Testdata: Files = %d, Testdata = %d", rep.Files, rep.Testdata)
	}
	if _, ok := b.Build().Node(id("testdata/skipped.goasm#Skipped")); !ok {
		t.Error("with Testdata: testdata/skipped.goasm#Skipped missing")
	}
}

func TestParseDirective(t *testing.T) {
	for _, tt := range []struct {
		line string
		want directive
	}{
		{"go run github.com/trendvidia/goasm/cmd/goasm@v1.1.0 -arch arm64 -in templates/arm64.goasm -out emit_arm64.go",
			directive{arch: "arm64", in: "templates/arm64.goasm", out: "emit_arm64.go"}},
		{"../../goasm/goasm -arch=amd64 -in=../../goasm/templates/amd64.goasm -out=emit_amd64.go",
			directive{arch: "amd64", in: "../../goasm/templates/amd64.goasm", out: "emit_amd64.go"}},
		{"go run ./cmd/goasm -in templates/arm64.goasm", directive{in: "templates/arm64.goasm"}},
		{"stringer -type=Op", directive{}},
		// go generate's own quoting and variables (#99).
		{`goasm -arch arm64 -in "templates/arm64.goasm" -out "emit arm64.go"`,
			directive{arch: "arm64", in: "templates/arm64.goasm", out: "emit arm64.go"}},
		{"goasm -arch=$GOARCH -in t.goasm -out emit_$GOFILE",
			directive{in: "t.goasm", out: "emit_gen.go"}},
	} {
		if got := parseDirective(generateArgs(tt.line), "gen.go"); got != tt.want {
			t.Errorf("parseDirective(%q) = %+v, want %+v", tt.line, got, tt.want)
		}
	}
}

func TestGoPackage(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	for _, tt := range []struct{ dir, want string }{
		{root, "example.com/m"},
		{filepath.Join(root, "jit"), "example.com/m/jit"},
		{filepath.Join(root, "a", "b"), "example.com/m/a/b"},
		{filepath.Join(root, "..", "other"), ""},
	} {
		if got := goPackage(root, "example.com/m", tt.dir); got != tt.want {
			t.Errorf("goPackage(%s) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestNoModuleLinksNothing(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := mkdirWrite(p, body); err != nil {
			t.Fatal(err)
		}
	}
	write("gen.go", "package x\n\n//go:generate go run ./cmd/goasm -arch arm64 -in t.goasm -out e.go\n")
	write("t.goasm", "# TEMPLATE Nop()\n    NOP\n")
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Directives != 1 || rep.Linked != 0 || rep.Unlinked != 1 {
		t.Errorf("Report = %+v", rep)
	}
	if n := len(b.Build().Dropped()); n != 0 {
		t.Errorf("%d edges emitted with no module to name a package", n)
	}
}

// TestModuleBelowTheRoot pins #99: a repository whose go.mod sits below
// the scan root links its templates through the nearest go.mod above the
// directive, as the Markdown layer reads it.
func TestModuleBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go/go.mod":                    "module example.com/g\n\ngo 1.26\n",
		"go/jit/gen.go":                "package jit\n\n//go:generate goasm -arch arm64 -in templates/arm64.goasm -out emit_arm64.go\n",
		"go/jit/templates/arm64.goasm": "",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	targets, n, _ := directives(root, []string{filepath.Join(root, "go/jit/gen.go")})
	got := targets[filepath.Join(root, "go/jit/templates/arm64.goasm")]
	if n != 1 || got.pkg != "example.com/g/jit" || got.arch != "arm64" {
		t.Errorf("targets = %+v (n=%d), want the template linked to example.com/g/jit", targets, n)
	}
}
