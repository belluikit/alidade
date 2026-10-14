// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"maps"
	"os/exec"
	"slices"
	"testing"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// TestPXFReaderFollowsTheSpec: each directive shape the protowire
// specification gives, read from raw text; comments and strings hide
// what looks like a directive; ${env} and ${ctx.*} name no field.
func TestPXFReaderFollowsTheSpec(t *testing.T) {
	p := &PXF{}
	p.file("a.pxf", []byte(`# @type not.This
@type pkg.v1.Doc
/* @entry no.Not { } */
@entry pkg.v1.Typed { }
@entry label pkg.v1.Labelled { }
@entry bare { }
@entry { }
@header pkg.v1.Header { x = 1 }
@proto """
message Inline { string s = 1; }
"""
name = "@type not.This ${name} ${env} ${ctx.region}"
other = """${other.path}"""
`))
	p.file("b.pxf", []byte("@dataset pkg.v1.Row (a, b)\n(1, 2)\n"))
	p.file("c.pxf", []byte("@dataset (a)\n"))
	want := [][2]string{
		{"a.pxf", "pkg.v1.Doc"}, {"a.pxf", "pkg.v1.Typed"}, {"a.pxf", "pkg.v1.Labelled"}, {"a.pxf", "pkg.v1.Header"},
		{"b.pxf", "pkg.v1.Row"},
	}
	if !slices.Equal(p.Types, want) {
		t.Errorf("types %v, want %v", p.Types, want)
	}
	if refs := [][2]string{{"a.pxf", "name"}, {"a.pxf", "other.path"}}; !slices.Equal(p.Refs, refs) {
		t.Errorf("refs %v, want %v", p.Refs, refs)
	}
	if len(p.Unread) != 0 {
		t.Errorf("unread %v", p.Unread)
	}
}

// pxfFixture is a typed document with an interpolation, beside the
// proto it names.
var pxfFixture = map[string]string{
	"schema/doc.proto": "syntax = \"proto3\";\npackage pkg.v1;\nmessage Doc { string name = 1; string greeting = 2; }\n",
	"doc.pxf":          "@type pkg.v1.Doc\nname = \"world\"\ngreeting = \"hello ${name}\"\n",
}

// TestPXFAgrees: the reader and the layer agree on the fixture, and a
// dropped and a bogus oracle reference are each caught.
func TestPXFAgrees(t *testing.T) {
	dir := write(t, pxfFixture)
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	p, err := PXFFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := ComparePXF(g, p, map[string]bool{"pkg.v1.Doc": true})
	if r.Types != [2]int{1, 1} || r.Refs != [2]int{1, 1} || len(r.ExtraTypes)+len(r.ExtraRefs)+len(r.Undeclared) != 0 {
		t.Fatalf("not exact:\n%s", r)
	}
	p.Types = [][2]string{{"doc.pxf", "pkg.v1.Other"}}
	p.Refs = nil
	r = ComparePXF(g, p, map[string]bool{"pkg.v1.Doc": true})
	if len(r.MissingTypes) != 1 || len(r.ExtraTypes) != 1 || len(r.ExtraRefs) != 1 || len(r.Undeclared) != 1 {
		t.Errorf("want one missing, extra and undeclared type and one extra ref:\n%s", r)
	}
	// Without a declaration set nothing is confirmed, and it says so.
	if r := ComparePXF(g, p, nil); r.Unconfirmed != 1 || len(r.Undeclared) != 0 {
		t.Errorf("no declarations: unconfirmed %d, undeclared %v", r.Unconfirmed, r.Undeclared)
	}
}

// TestCensusCatchesChanges: a judged link gone, loaded in other
// environments, or a link nobody judged, each reported.
func TestCensusCatchesChanges(t *testing.T) {
	b := graph.NewBuilder(t.TempDir())
	m := b.Minter()
	chain, _ := m.PXFID("chain.pxf", "")
	base, _ := m.PXFID("base.pxf", "")
	extra, _ := m.PXFID("extra.pxf", "")
	for _, id := range []graph.ID{chain, base, extra} {
		if err := b.AddNode(graph.Node{ID: id, Kind: graph.KindPXFFile, Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, to := range []graph.ID{base, extra} {
		if err := b.AddEdge(graph.Edge{From: chain, To: to, Kind: graph.KindReferences, Derivation: graph.Extracted, Attrs: map[string]string{"envs": "dev prod"}}); err != nil {
			t.Fatal(err)
		}
	}
	g := b.Build()
	s := &Sample{Edges: []Labelled{{From: chain, To: base, Envs: "dev"}, {From: chain, To: "pxf://gone.pxf", Envs: "dev"}}}
	r := CompareCensus(g, s)
	if len(r.Gone) != 1 || len(r.EnvsChanged) != 1 || len(r.Unjudged) != 1 {
		t.Errorf("want one of each:\n%s", r)
	}
}

// schemaDocs is a schema and two layers of one chain, one typed (#190).
var schemaDocs = map[string]string{
	"schema/doc.proto": "syntax = \"proto3\";\npackage pkg.v1;\nmessage Doc { string name = 1; repeated Item items = 2; }\nmessage Item { string name = 1; string label = 2; }\n",
	"config/base.pxf":  "@type pkg.v1.Doc\nname = \"base\"\nitems = [ { name = \"x\" label = \"\" } ]\n",
	"config/top.pxf":   "name = \"top\"\n",
	"config/chain.pxf": "@type chameleon.v1.ChainSpec\nversion = \"1.0.0\"\ndefault_environment = \"dev\"\nenvironments = [ { name = \"dev\" } ]\nlayers = [\n  { id = \"base\" path = \"base.pxf\" for_each = ONCE required = true }\n  { id = \"top\" path = \"top.pxf\" for_each = ONCE }\n]\n",
}

// TestPXFSchemaAgrees: the decoder's fields and the graph's schema edges
// agree, the untyped layer typed by the census, a default written
// inside a list element counted apart; a dropped and a bogus graph-side
// field are caught.
func TestPXFSchemaAgrees(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc is the schema's oracle")
	}
	dir := write(t, schemaDocs)
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := PXFFiles(dir)
	pr, err := Protoc(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	census := &Sample{Edges: []Labelled{{From: "pxf://config/chain.pxf", To: "pxf://config/base.pxf"}, {From: "pxf://config/chain.pxf", To: "pxf://config/top.pxf"}}}
	types := CensusTypes(census, p.FileTypes)
	if types["config/top.pxf"] != "pkg.v1.Doc" {
		t.Fatalf("census types %v", types)
	}
	maps.Copy(types, p.FileTypes)
	sets, unplaced, failed := SchemaFields(dir, types, pr)
	r := ComparePXFSchema(g, sets, unplaced, failed)
	if r.Files != 2 || r.Fields[0] == 0 || r.Fields[0] != r.Fields[1] || len(r.Extra) != 0 {
		t.Fatalf("not exact:\n%s", r)
	}
	delete(sets["config/base.pxf"], "pkg.v1.Doc.items") // only Doc has items: no default hides it
	sets["config/top.pxf"]["pkg.v1.Doc.items"] = true
	if r := ComparePXFSchema(g, sets, unplaced, failed); len(r.Missing) != 1 || len(r.Extra) != 1 {
		t.Errorf("missing %v, extra %v; want one of each", r.Missing, r.Extra)
	}
}
