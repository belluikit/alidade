// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// schemaFixture is a schema, a document typed by it — a scalar, a nested
// message, a list of messages keyed by name, a map — and a chameleon
// chain of that document and an untyped layer (#190).
var schemaFixture = map[string]string{
	"schema/doc.proto": `syntax = "proto3";
package pkg.v1;
message Doc {
  string name = 1;
  Owner owner = 2;
  repeated Item items = 3;
  map<string, Item> by_key = 4;
}
message Owner { string login = 1; }
message Item { string name = 1; string label = 2; }
`,
	"config/base.pxf": `@type pkg.v1.Doc
name = "base"
owner { login = "a" }
items = [ { name = "x" label = "first" } ]
by_key = { "k": { label = "kept" } }
`,
	"config/secret.pxf": "name = \"secret\"\nunknown_key = 1\n",
	"config/chain.pxf": `@type chameleon.v1.ChainSpec
version = "1.0.0"
default_environment = "dev"
environments = [ { name = "dev" } ]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "secret" path = "secret.pxf" for_each = ONCE }
]
`,
}

func writeTree(t *testing.T, files map[string]string) string {
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
	return dir
}

func schemaEdge(g *graph.Graph, from, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == graph.KindReferences && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

// TestEntriesReferenceTheirSchemaField: every entry of a typed document
// references the proto field it sets, RESOLVED; an element of a list or
// a map sets its collection's field; an untyped layer of a chain takes
// the chain's one type, DECLARED via=chain; and an entry the schema does
// not have is left unlinked and counted.
func TestEntriesReferenceTheirSchemaField(t *testing.T) {
	g, rep, err := build.Build(writeTree(t, schemaFixture), build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for from, to := range map[graph.ID]graph.ID{
		"pxf://config/base.pxf#name":           "proto://pkg.v1.Doc.name",
		"pxf://config/base.pxf#owner":          "proto://pkg.v1.Doc.owner",
		"pxf://config/base.pxf#owner.login":    "proto://pkg.v1.Owner.login",
		"pxf://config/base.pxf#items":          "proto://pkg.v1.Doc.items",
		"pxf://config/base.pxf#items.x":        "proto://pkg.v1.Doc.items",
		"pxf://config/base.pxf#items.x.label":  "proto://pkg.v1.Item.label",
		"pxf://config/base.pxf#by_key.k.label": "proto://pkg.v1.Item.label",
	} {
		e, ok := schemaEdge(g, from, to)
		if !ok || e.Derivation != graph.Resolved {
			t.Errorf("%s -> %s: present %v, derivation %s; want RESOLVED", from, to, ok, e.Derivation)
		}
	}
	e, ok := schemaEdge(g, "pxf://config/secret.pxf#name", "proto://pkg.v1.Doc.name")
	if !ok || e.Derivation != graph.Declared || e.Attrs["via"] != "chain" {
		t.Errorf("untyped layer: present %v, %s %v; want DECLARED via chain", ok, e.Derivation, e.Attrs)
	}
	if rep.PXF.SchemaInferred != 1 || rep.PXF.SchemaUnresolved != 1 {
		t.Errorf("inferred %d, unresolved %d; want 1 and 1", rep.PXF.SchemaInferred, rep.PXF.SchemaUnresolved)
	}
}

// TestChainOfTwoTypesInfersNothing: a chain whose layers declare two
// types gives its untyped layer neither.
func TestChainOfTwoTypesInfersNothing(t *testing.T) {
	files := map[string]string{}
	for k, v := range schemaFixture {
		files[k] = v
	}
	files["schema/doc.proto"] += "message Other { string name = 1; }\n"
	files["config/other.pxf"] = "@type pkg.v1.Other\nname = \"o\"\n"
	files["config/chain.pxf"] = `@type chameleon.v1.ChainSpec
version = "1.0.0"
default_environment = "dev"
environments = [ { name = "dev" } ]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "other" path = "other.pxf" for_each = ONCE }
  { id = "secret" path = "secret.pxf" for_each = ONCE }
]
`
	g, rep, err := build.Build(writeTree(t, files), build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []graph.ID{"proto://pkg.v1.Doc.name", "proto://pkg.v1.Other.name"} {
		if _, ok := schemaEdge(g, "pxf://config/secret.pxf#name", to); ok {
			t.Errorf("untyped layer of a two-type chain references %s", to)
		}
	}
	if rep.PXF.SchemaInferred != 0 {
		t.Errorf("inferred %d, want 0", rep.PXF.SchemaInferred)
	}
}
