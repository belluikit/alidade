// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// TestGeneratedCode reads testdata/gen, whose build script writes what
// prost-build does for three .proto files, and the crate includes them:
// one in a module, one at its root, one through a macro of its own, as
// tonic's include_proto! does. Every item of the two files is a node at
// its include!, a call into one is an edge, and each struct or enum written
// for a .proto message or enum is joined to it (ADR 009, the owner's
// decision on #72). This pins ADR 009's assumption that the index leaves
// generated files out and the server's definition lands in them.
func TestGeneratedCode(t *testing.T) {
	dir := fixtureOf(t, "gen", nil)
	b := graph.NewBuilder(dir)
	// The proto layer's nodes, which a generates edge needs at its end.
	for _, name := range []string{"fixture.shapes.Shape", "fixture.shapes.Shape.Unit", "fixture.shapes.Stated",
		"fixture.shapes.Misnamed", "other.Misnamed", "fixture.shapes.Named", "fixture.shapes.Renamed", "other.Renamed", "Loose", "wrapped.Via"} {
		id, err := b.Minter().ProtoID(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddNode(graph.Node{ID: id, Kind: graph.KindMessage, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	check(t, "generated nodes", generatedNodes(g), []string{
		"src/lib.rs#Loose type", // _.rs, at the crate root
		"src/lib.rs#pb.Misnamed type",
		"src/lib.rs#pb.Named type",
		"src/lib.rs#pb.Named[Name].NAME const", "src/lib.rs#pb.Named[Name].PACKAGE const",
		"src/lib.rs#pb.Renamed type",
		"src/lib.rs#pb.Renamed[Name].NAME const", "src/lib.rs#pb.Renamed[Name].PACKAGE const",
		"src/lib.rs#pb.Shape type", "src/lib.rs#pb.Shape.kind field", "src/lib.rs#pb.Shape.name field",
		"src/lib.rs#pb.Stated type",
		"src/lib.rs#pb.shape module",
		"src/lib.rs#pb.shape.Kind type", "src/lib.rs#pb.shape.Kind.Side enum_value", // a oneof's
		"src/lib.rs#pb.shape.Unit type", "src/lib.rs#pb.shape.Unit.In enum_value", "src/lib.rs#pb.shape.Unit.Mm enum_value",
		"src/lib.rs#pb.shape.Unit.as_str_name() method",
		"src/lib.rs#wrapped.Via type", // through include_proto!, found by the reference to it
	})
	check(t, "generates", generates(g), []string{
		"Loose -> src/lib.rs#Loose",
		"fixture.shapes.Named -> src/lib.rs#pb.Named", // impl ::prost::Name states it
		"fixture.shapes.Shape -> src/lib.rs#pb.Shape",
		"fixture.shapes.Shape.Unit -> src/lib.rs#pb.shape.Unit", // nested, its derive over lines
		"fixture.shapes.Stated -> src/lib.rs#pb.Stated",         // one of two stated names agrees
		"wrapped.Via -> src/lib.rs#wrapped.Via",
	})
	if !slices.Contains(edges(g, graph.KindCalls), "src/lib.rs#unit_name() -> src/lib.rs#pb.shape.Unit.as_str_name()") {
		t.Errorf("calls %v; want unit_name's call of the generated as_str_name", edges(g, graph.KindCalls))
	}
	checkGeneratedReport(t, rep)
}

// checkGeneratedReport pins TestGeneratedCode's counts, and that each
// struct stating a name the naming rule does not give is reported: by
// prost-reflect's attribute (Misnamed), and by prost's impl Name (Renamed).
func checkGeneratedReport(t *testing.T, rep *Report) {
	t.Helper()
	if rep.GeneratedFiles != 3 || rep.Generated != 20 || rep.Generates != 6 || rep.GeneratesConflicting != 2 || rep.CallsGenerated != 0 {
		t.Errorf("GeneratedFiles %d, Generated %d, Generates %d, GeneratesConflicting %d, CallsGenerated %d; want 3, 20, 6, 2, 0",
			rep.GeneratedFiles, rep.Generated, rep.Generates, rep.GeneratesConflicting, rep.CallsGenerated)
	}
	var reasons []string
	for _, s := range rep.Skipped {
		reasons = append(reasons, s.Reason)
	}
	want := []string{"pb.Misnamed states the .proto names other.Misnamed,", "pb.Renamed states the .proto names other.Renamed,"}
	if len(reasons) != len(want) || !strings.Contains(strings.Join(reasons, "\n"), want[0]) || !strings.Contains(strings.Join(reasons, "\n"), want[1]) {
		t.Errorf("skipped %v; want the conflicting names of pb.Misnamed and pb.Renamed, alone", reasons)
	}
}

// generatedNodes lists the Rust nodes marked generated.
func generatedNodes(g *graph.Graph) []string {
	var out []string
	for n := range g.Nodes() {
		if n.Generated && strings.HasPrefix(string(n.ID), "rust://") {
			out = append(out, strings.TrimPrefix(string(n.ID), "rust://")+" "+string(n.Kind))
		}
	}
	slices.Sort(out)
	return out
}

// generates lists the generates edges, DECLARED and via prost, as
// "message -> type".
func generates(g *graph.Graph) []string {
	var out []string
	for e := range g.Edges() {
		if e.Kind != graph.KindGenerates {
			continue
		}
		s := strings.TrimPrefix(string(e.From), "proto://") + " -> " + strings.TrimPrefix(string(e.To), "rust://")
		if e.Derivation != graph.Declared || e.Attrs["via"] != "prost" {
			s += " (" + string(e.Derivation) + ", via " + e.Attrs["via"] + ")"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// TestFirstLiteral reads the literal an include! call holds, and none
// outside the call.
func TestFirstLiteral(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       pos
		found      bool
	}{
		{"one line", `include!("a.rs");`, pos{0, 10}, true},
		{"over lines", "include!(concat!(\n    env!(\"OUT_DIR\"),\n    \"/a.rs\"\n));", pos{1, 10}, true},
		{"spaced", `include! ( "a.rs" );`, pos{0, 12}, true},
		{"no literal in the call", "include!(PATH);\nlet s = \"after\";", pos{}, false},
		{"no call", "include;\nlet s = \"after\";", pos{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// end is where the macro's name ends, after "include".
			got, found := firstLiteral([]byte(c.text), pos{0, 7})
			if got != c.want || found != c.found {
				t.Errorf("got %v, %v; want %v, %v", got, found, c.want, c.found)
			}
		})
	}
}

// TestGeneratedThroughMacroAlone reads a crate whose only include is
// through a macro, so the index shows no include! where it is called: the
// workspace naming a generated item is what asks the server.
func TestGeneratedThroughMacroAlone(t *testing.T) {
	dir := fixtureOf(t, "gen", map[string]string{"src/lib.rs": `macro_rules! include_proto {
    ($package: tt) => {
        include!(concat!(env!("OUT_DIR"), concat!("/", $package, ".rs")));
    };
}

pub mod wrapped {
    include_proto!("wrapped");
}

pub fn via() -> wrapped::Via {
    wrapped::Via {}
}
`})
	g, rep := extract(t, dir)
	check(t, "generated nodes", generatedNodes(g), []string{"src/lib.rs#wrapped.Via type"})
	if rep.GeneratedFiles != 1 || rep.Generated != 1 {
		t.Errorf("GeneratedFiles %d, Generated %d; want 1, 1", rep.GeneratedFiles, rep.Generated)
	}
}
