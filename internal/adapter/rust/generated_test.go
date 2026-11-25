// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"fmt"
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

// handFixture is the crate of the generated-code fixture, its build script
// kept, whose lib.rs includes a hand-written file of its own, hand.rs, in
// two modules.
func handFixture(t *testing.T) string {
	t.Helper()
	return fixtureOf(t, "gen", map[string]string{
		"src/lib.rs": `pub mod hand_mod {
    include!("hand.rs");
}

pub mod again {
    include!("hand.rs");
}

pub trait Area {
    fn area(&self) -> i32;
}

pub fn uses_hand(h: &hand_mod::Hand) -> i32 {
    hand_mod::by_hand() + again::by_hand() + h.get() + h.area()
}
`,
		"src/hand.rs": `pub struct Hand {
    pub n: i32,
}

impl Hand {
    pub fn get(&self) -> i32 {
        self.n
    }
}

impl crate::Area for Hand {
    fn area(&self) -> i32 {
        2
    }
}

/* ééé */ pub fn by_hand() -> i32 {
    1
}
`,
	})
}

// TestHandWrittenInclude reads a hand-written file that an include! reads
// in two modules: its items are nodes at the file, where they are written,
// not generated, once for both include!s, and the workspace's calls into
// them are edges (ADR 002 rule 16, the owner's decision on #84). What the
// file itself calls is not read, and its file node says so (#88).
func TestHandWrittenInclude(t *testing.T) {
	g, rep := extract(t, handFixture(t))
	var hand []string
	for n := range g.Nodes() {
		if strings.HasPrefix(string(n.ID), "rust://src/hand.rs#") {
			hand = append(hand, fmt.Sprintf("%s %s %d:%d end %s generated %v", strings.TrimPrefix(string(n.ID), "rust://"),
				n.Kind, n.Pos.Line, n.Pos.Col, n.Attrs["end_line"], n.Generated))
		}
	}
	slices.Sort(hand)
	check(t, "hand.rs nodes", hand, []string{
		"src/hand.rs#Hand type 1:12 end 3 generated false",
		"src/hand.rs#Hand.get() method 6:12 end 8 generated false",
		"src/hand.rs#Hand.n field 2:9 end 2 generated false",
		"src/hand.rs#Hand[Area].area() method 12:8 end 14 generated false",
		"src/hand.rs#by_hand() func 17:21 end 19 generated false", // past the comment's six bytes of three characters
	})
	var calls []string
	for _, e := range edges(g, graph.KindCalls) {
		if strings.HasPrefix(e, "src/lib.rs#uses_hand()") {
			calls = append(calls, e)
		}
	}
	check(t, "calls", calls, []string{ // again::by_hand() is the same node
		"src/lib.rs#uses_hand() -> src/hand.rs#Hand.get()",
		"src/lib.rs#uses_hand() -> src/hand.rs#Hand[Area].area()",
		"src/lib.rs#uses_hand() -> src/hand.rs#by_hand()",
	})
	checkHandReport(t, g, rep)
}

// checkHandReport pins TestHandWrittenInclude's counts, the report of the
// second include!, and hand.rs's file node saying what is not read.
func checkHandReport(t *testing.T, g *graph.Graph, rep *Report) {
	t.Helper()
	if rep.Included != 1 || rep.IncludedAgain != 1 || rep.CallsGenerated != 0 || rep.Unanalyzed != 0 {
		t.Errorf("Included %d, IncludedAgain %d, CallsGenerated %d, Unanalyzed %d; want 1, 1, 0, 0",
			rep.Included, rep.IncludedAgain, rep.CallsGenerated, rep.Unanalyzed)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].File != "src/lib.rs" || rep.Skipped[0].Line != 6 ||
		!strings.Contains(rep.Skipped[0].Reason, "src/hand.rs is included here and in another module") {
		t.Errorf("skipped %+v; want the second include!, at src/lib.rs:6, alone", rep.Skipped)
	}
	for n := range g.Nodes() {
		if n.ID == "rust://src/hand.rs" && !strings.Contains(n.Attrs["calls_unavailable"], "an include! reads it") {
			t.Errorf("hand.rs's calls_unavailable is %q; want it to say an include! reads it", n.Attrs["calls_unavailable"])
		}
	}
}

// TestHandWrittenIncludeInUTF16: a server counting in UTF-16 has an
// included item's column converted to bytes, as the index counts.
func TestHandWrittenIncludeInUTF16(t *testing.T) {
	old := lspEncodings
	lspEncodings = []string{"utf-16"}
	t.Cleanup(func() { lspEncodings = old })
	g, rep := extract(t, handFixture(t))
	if rep.ServerColumns != "utf-16" {
		t.Fatalf("the server counted in %q, want utf-16", rep.ServerColumns)
	}
	col := -1
	for n := range g.Nodes() {
		if n.ID == "rust://src/hand.rs#by_hand()" {
			col = n.Pos.Col
		}
	}
	if col != 21 {
		t.Errorf("by_hand() at column %d (-1: no node), want 21: the comment's characters are two bytes and one UTF-16 unit each", col)
	}
}
