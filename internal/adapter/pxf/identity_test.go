// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

func pxfTree(t *testing.T, files map[string]string) *graph.Graph {
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

// wantTypeEdge finds a references edge to a type, resolved or — the .pxf
// layer alone has no .proto nodes — dropped as unknown_target.
func wantTypeEdge(t *testing.T, g *graph.Graph, from, to graph.ID) {
	t.Helper()
	if _, ok := hasEdge(g, from, graph.KindReferences, to); ok {
		return
	}
	for _, d := range g.Dropped() {
		if d.Edge.From == from && d.Edge.To == to && d.Edge.Kind == graph.KindReferences {
			return
		}
	}
	t.Errorf("no references edge %s -> %s", from, to)
}

func noConflicts(t *testing.T, g *graph.Graph) {
	t.Helper()
	for _, c := range g.Conflicts() {
		t.Errorf("conflict %+v", c)
	}
}

// TestRepeatedDirectivesAreOrdinals pins #97 and ADR 002 rule 9: a second
// directive of a name is keyed by an ordinal, not merged into the first.
func TestRepeatedDirectivesAreOrdinals(t *testing.T) {
	g := pxfTree(t, map[string]string{"a.pxf": "@entry users.v1.User { id = 1 }\n@entry users.v1.User { id = 2 }\n@entry users.v1.User { id = 3 }\n"})
	for _, want := range []string{"a.pxf#@entry", "a.pxf#@entry1", "a.pxf#@entry2"} {
		wantTypeEdge(t, g, id(want), "proto://users.v1.User")
	}
	noConflicts(t, g)
}

// TestEntryPrefixes pins #97: @entry's prefixes are a label, a type or
// both, a single one being a type only when dotted.
func TestEntryPrefixes(t *testing.T) {
	g := pxfTree(t, map[string]string{"a.pxf": "@entry alice users.v1.User { id = 1 }\n@entry bob { id = 2 }\n"})
	wantTypeEdge(t, g, id("a.pxf#@entry"), "proto://users.v1.User")
	if n, _ := g.Node(id("a.pxf#@entry")); n.Attrs["label"] != "alice" {
		t.Errorf("@entry attrs = %v, want label alice", n.Attrs)
	}
	if n, _ := g.Node(id("a.pxf#@entry1")); n.Attrs["label"] != "bob" {
		t.Errorf("@entry1 attrs = %v, want label bob", n.Attrs)
	}
	for _, e := range g.Out(id("a.pxf#@entry1")) {
		if e.Kind == graph.KindReferences {
			t.Errorf("a bare label is not a type: edge to %s", e.To)
		}
	}
	for _, d := range g.Dropped() {
		if d.Edge.From == id("a.pxf#@entry1") {
			t.Errorf("a bare label is not a type: dropped %+v", d)
		}
	}
}

// TestEmbeddedProtoIsVisible pins #97: an @proto directive is a node,
// with its shape and the type a named one declares.
func TestEmbeddedProtoIsVisible(t *testing.T) {
	g := pxfTree(t, map[string]string{"a.pxf": "@proto local.v1.Thing { string name = 1; }\n@entry local.v1.Thing { name = \"x\" }\n"})
	n, ok := g.Node(id("a.pxf#@proto"))
	if !ok || n.Attrs["proto"] != "named" || n.Attrs["declares"] != "local.v1.Thing" {
		t.Errorf("@proto = %+v, %v", n, ok)
	}
}

// TestRepeatedBlocksAreElements pins #97: a block written more than once
// is a repeated message, each occurrence an element keyed by its name or
// index, not one node merged with conflicts.
func TestRepeatedBlocksAreElements(t *testing.T) {
	g := pxfTree(t, map[string]string{"a.pxf": "server {\n  name = \"web\"\n  port = 80\n}\nserver {\n  name = \"api\"\n  port = 81\n}\nworker { n = 1 }\nworker { n = 2 }\n"})
	if n, ok := g.Node(id("a.pxf#server")); !ok || n.Attrs["repeated"] != "2" {
		t.Errorf("server = %+v, %v; want a repeated block of 2", n, ok)
	}
	for _, el := range []string{"server.web", "server.api", "worker.0", "worker.1"} {
		parent := el[:strings.LastIndexByte(el, '.')]
		wantEdge(t, g, id("a.pxf#"+parent), graph.KindContains, id("a.pxf#"+el), graph.Extracted)
	}
	for _, f := range []string{"server.web.port", "server.api.port", "worker.0.n", "worker.1.n"} {
		if _, ok := g.Node(id("a.pxf#" + f)); !ok {
			t.Errorf("missing a.pxf#%s", f)
		}
	}
	noConflicts(t, g)
}
