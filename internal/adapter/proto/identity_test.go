// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

func protoTree(t *testing.T, files map[string]string) *graph.Graph {
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

// edgeOrDrop finds an edge resolved or dropped (this layer alone has no
// Go nodes for a generates edge to land on).
func edgeOrDrop(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) bool {
	if _, ok := hasEdge(g, from, kind, to); ok {
		return true
	}
	for _, d := range g.Dropped() {
		if d.Edge.From == from && d.Edge.To == to && d.Edge.Kind == kind {
			return true
		}
	}
	return false
}

// TestSameImportPathInTwoModules pins #97: two modules holding the same
// import path each compile their own file — the shallower one's copy is
// no longer the deeper one's.
func TestSameImportPathInTwoModules(t *testing.T) {
	g := protoTree(t, map[string]string{
		"buf.yaml":                        "version: v2\nmodules:\n  - path: proto\n  - path: internal/schema\n",
		"proto/acme/v1/x.proto":           "syntax = \"proto3\";\npackage acme.v1;\nmessage Public {}\n",
		"internal/schema/acme/v1/x.proto": "syntax = \"proto3\";\npackage acme.v1;\nmessage Internal {}\n",
	})
	for file, msg := range map[string]string{"proto/acme/v1/x.proto": "acme.v1.Public", "internal/schema/acme/v1/x.proto": "acme.v1.Internal"} {
		wantNode(t, g, id(msg), graph.KindMessage)
		if _, ok := hasEdge(g, id("file/"+file), graph.KindContains, id(msg)); !ok {
			t.Errorf("%s does not contain %s", file, msg)
		}
	}
	for _, c := range g.Conflicts() {
		t.Errorf("conflict %+v", c)
	}
}

// TestEditionsFilesNameTheirGoTypes pins #97: an editions file links its
// messages to the Go types protoc-gen-go names, as a proto3 file does.
func TestEditionsFilesNameTheirGoTypes(t *testing.T) {
	g := protoTree(t, map[string]string{
		"ed.proto": "edition = \"2023\";\npackage ed.v1;\noption go_package = \"example.com/m/gen/edv1\";\nmessage Outer {\n  message Inner {}\n}\n",
	})
	if n, _ := g.Node(id("file/ed.proto")); n.Attrs["go_names_error"] != "" {
		t.Fatalf("go_names_error = %q", n.Attrs["go_names_error"])
	}
	for msg, goType := range map[string]string{"ed.v1.Outer": "Outer", "ed.v1.Outer.Inner": "Outer_Inner"} {
		if !edgeOrDrop(g, id(msg), graph.KindGenerates, graph.ID("go://example.com/m/gen/edv1#"+goType)) {
			t.Errorf("no generates edge %s -> %s", msg, goType)
		}
	}
}

// TestExtensionsReferenceTheirExtendee pins #97: an extension names the
// message it extends.
func TestExtensionsReferenceTheirExtendee(t *testing.T) {
	g := protoTree(t, map[string]string{
		"x.proto": "syntax = \"proto2\";\npackage x.v1;\nmessage Base { extensions 100 to 200; }\nextend Base { optional string note = 100; }\n",
	})
	e, ok := hasEdge(g, id("x.v1.note"), graph.KindReferences, id("x.v1.Base"))
	if !ok || e.Attrs["extends"] != "true" {
		t.Errorf("extension edge = %+v, %v", e, ok)
	}
}
