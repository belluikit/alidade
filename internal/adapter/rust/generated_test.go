// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
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
    hand_mod::by_hand() + again::by_hand() + h.get() + h.area() + h.clone().n
}
`,
		"src/hand.rs": `#[derive(Clone)]
pub struct Hand {
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
		"src/hand.rs#Hand type 2:12 end 4 generated false",
		"src/hand.rs#Hand.get() method 7:12 end 9 generated false",
		"src/hand.rs#Hand.n field 3:9 end 3 generated false",
		"src/hand.rs#Hand[Area].area() method 13:8 end 15 generated false",
		"src/hand.rs#Hand[Clone].clone() method 2:12 end  generated true", // the derive's, at its type
		"src/hand.rs#by_hand() func 18:21 end 20 generated false",         // past the comment's six bytes of three characters
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
		"src/lib.rs#uses_hand() -> src/hand.rs#Hand[Clone].clone()",
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
		if n.ID == "rust://src/hand.rs" && !strings.Contains(n.Attrs["calls_unavailable"], "call hierarchy, which leaves out") {
			t.Errorf("hand.rs's calls_unavailable is %q; want it to say what the call hierarchy leaves out", n.Attrs["calls_unavailable"])
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

// TestGeneratedPlacedOnce: a call of a method a derive writes on a
// generated type, which the file's outline has no item for, does not lead
// the search through macros (throughSymbols) back to the file it is in and
// place it a second time. Measured on main before the fix: GeneratedFiles
// 2 for one file; on jj, hand-written secure_config.rs reported as
// included by two modules when one includes it. The method is a node
// under its type, at the include!, and the call an edge (ADR 009; on main
// it was CallsGenerated).
func TestGeneratedPlacedOnce(t *testing.T) {
	dir := fixtureOf(t, "gen", map[string]string{"src/lib.rs": `pub mod pb {
    include!(concat!(env!("OUT_DIR"), "/fixture.shapes.rs"));
}

pub fn cloned(s: &pb::Shape) -> pb::Shape {
    s.clone()
}
`})
	g, rep := extract(t, dir)
	if rep.GeneratedFiles != 1 || rep.MacroWritten != 1 || rep.CallsGenerated != 0 {
		t.Errorf("GeneratedFiles %d, MacroWritten %d, CallsGenerated %d; want 1, 1, 0", rep.GeneratedFiles, rep.MacroWritten, rep.CallsGenerated)
	}
	check(t, "calls", edges(g, graph.KindCalls), []string{"src/lib.rs#cloned() -> src/lib.rs#pb.Shape[Clone].clone()"})
	if !slices.Contains(generatedNodes(g), "src/lib.rs#pb.Shape[Clone].clone() method") {
		t.Errorf("generated nodes %v; want pb.Shape[Clone].clone() among them", generatedNodes(g))
	}
}

// TestCheckedInProst reads prost-build's output checked into the
// repository and included, as jj includes its protos: its items are nodes
// at the file, at their own positions, marked generated, and its message
// and enum are joined to their .proto ones (the owner's decision on #89).
// A file without prost-build's header stays hand-written (#84).
func TestCheckedInProst(t *testing.T) {
	dir := fixtureOf(t, "gen", map[string]string{
		"src/lib.rs": "pub mod shapes {\n    include!(\"shapes.rs\");\n}\n\npub mod hand_mod {\n    include!(\"hand.rs\");\n}\n",
		"src/shapes.rs": `// This file is @generated by prost-build.
#[derive(Clone, PartialEq, ::prost::Message)]
pub struct Shape {
    #[prost(string, tag = "1")]
    pub name: ::prost::alloc::string::String,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord, ::prost::Enumeration)]
#[repr(i32)]
pub enum Unit {
    Mm = 0,
}
`,
		"src/hand.rs": "#[derive(Clone, PartialEq, ::prost::Message)]\npub struct Hand {}\n",
	})
	b := graph.NewBuilder(dir)
	for _, name := range []string{"shapes.Shape", "shapes.Unit", "hand.Hand"} {
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
	var got []string
	for n := range g.Nodes() {
		if strings.HasPrefix(string(n.ID), "rust://src/shapes.rs#") || strings.HasPrefix(string(n.ID), "rust://src/hand.rs#") {
			got = append(got, fmt.Sprintf("%s %d:%d generated %v %s", strings.TrimPrefix(string(n.ID), "rust://"), n.Pos.Line, n.Pos.Col, n.Generated, n.Attrs["generated_in"]))
		}
	}
	check(t, "included nodes", got, []string{
		"src/hand.rs#Hand 2:12 generated false ",
		"src/shapes.rs#Shape 3:12 generated true shapes.rs",
		"src/shapes.rs#Shape.name 5:9 generated true shapes.rs",
		"src/shapes.rs#Unit 9:10 generated true shapes.rs",
		"src/shapes.rs#Unit.Mm 10:5 generated true shapes.rs",
	})
	check(t, "generates", generates(g), []string{"shapes.Shape -> src/shapes.rs#Shape", "shapes.Unit -> src/shapes.rs#Unit"})
	if rep.Included != 2 || rep.IncludedGenerated != 1 || rep.Generated != 4 {
		t.Errorf("Included %d, IncludedGenerated %d, Generated %d; want 2, 1, 4", rep.Included, rep.IncludedGenerated, rep.Generated)
	}
}

// TestProstOutput reads prost-build's header where it writes it, at the
// top: a file that mentions it further down, a hand-written one
// explaining its neighbours, is no output of prost-build's.
func TestProstOutput(t *testing.T) {
	lines := func(s string) [][]byte { return bytes.Split([]byte(s), []byte("\n")) }
	if !prostOutput(lines("// This file is @generated by prost-build.\npub struct A {}\n")) {
		t.Error("the header on the first line: want prost-build's output")
	}
	if prostOutput(lines("pub struct A {}\n\n\n// The files beside this one are @generated by prost-build.\n")) {
		t.Error("the phrase on the fourth line: want a hand-written file")
	}
}

// TestIncludedCallsAndImplementations reads the calls and implementations
// written in a hand-written included file (#88, the owner's decision): a
// call from its function, found by the server's call hierarchy and named
// by its definition, as the index would name it (the impl's method for
// crate::Area::area(&h)); an impl block of its outline joined to the one
// workspace trait of its name. Pinned divergences, which the server does
// not answer: a call of a method of an impl block the included file
// writes, by method or path syntax (h.area(), Hand::area(&h)); a function
// named as a value (.map(crate::twice)); an implementation elsewhere of a
// trait the file declares (Local's of Named).
func TestIncludedCallsAndImplementations(t *testing.T) {
	g, rep := extract(t, fixtureOf(t, "gen", map[string]string{
		"src/lib.rs": `pub mod hand_mod {
    include!("hand.rs");
}

pub trait Area {
    fn area(&self) -> i32;
}

pub fn helper() -> i32 {
    1
}

pub fn twice(x: i32) -> i32 {
    x * 2
}

pub struct L;

impl L {
    pub fn m(&self) -> i32 {
        1
    }
}

pub struct Local;

impl hand_mod::Named for Local {
    fn name(&self) -> i32 {
        3
    }
}
`,
		"src/hand.rs": `pub struct Hand {
    pub n: i32,
}

impl crate::Area for Hand {
    fn area(&self) -> i32 {
        self.n
    }
}

pub trait Named {
    fn name(&self) -> i32;
}

pub fn by_hand() -> i32 {
    let v: Vec<i32> = vec![1].into_iter().map(crate::twice).collect();
    let h = Hand { n: 1 };
    crate::helper() + other() + crate::Area::area(&h) + crate::L.m() + h.area() + Hand::area(&h) + v[0] + crate::helper()
}

fn other() -> i32 {
    2
}
`,
	}))
	var calls []string
	for _, e := range edges(g, graph.KindCalls) {
		if strings.HasPrefix(e, "src/hand.rs#") {
			calls = append(calls, e)
		}
	}
	check(t, "calls", calls, []string{
		"src/hand.rs#by_hand() -> src/hand.rs#Hand[Area].area()", // crate::Area::area(&h), as the index names it
		"src/hand.rs#by_hand() -> src/hand.rs#other()",
		"src/hand.rs#by_hand() -> src/lib.rs#L.m()",
		"src/hand.rs#by_hand() -> src/lib.rs#helper()",
	})
	if refs := edges(g, graph.KindReferences); len(refs) != 0 {
		t.Errorf("references %v; want none: the call hierarchy does not report .map(crate::twice)", refs)
	}
	check(t, "implements", edges(g, graph.KindImplements), []string{"src/hand.rs#Hand -> src/lib.rs#Area"})
	if rep.ImplementsUnplaced != 0 || rep.Calls != 4 {
		t.Errorf("ImplementsUnplaced %d, Calls %d; want 0: the server's answer in the included file is the outline's edge, and 4: helper() called twice is one edge",
			rep.ImplementsUnplaced, rep.Calls)
	}
}

// TestIncludedImplementsAmbiguous: an impl block of an included file whose
// trait's name two workspace traits share has no edge, and is counted.
func TestIncludedImplementsAmbiguous(t *testing.T) {
	g, rep := extract(t, fixtureOf(t, "gen", map[string]string{
		"src/lib.rs":  "pub mod hand_mod {\n    include!(\"hand.rs\");\n}\n\npub trait Area {\n    fn area(&self) -> i32;\n}\n\npub mod other {\n    pub trait Area {\n        fn area(&self) -> i32;\n    }\n}\n",
		"src/hand.rs": "pub struct Hand;\n\nimpl crate::Area for Hand {\n    fn area(&self) -> i32 {\n        1\n    }\n}\n",
	}))
	if impl := edges(g, graph.KindImplements); len(impl) != 0 || rep.ImplementsUnplaced != 1 {
		t.Errorf("implements %v, ImplementsUnplaced %d; want none, 1: two workspace traits are named Area", impl, rep.ImplementsUnplaced)
	}
}

// TestHolderDeclaredAfter: a method whose impl block comes before its
// type is contained by the type, as one after it is, in a crate's own
// file and in a file an include! reads, and a method a derive writes on
// such a type is too (#113). A member of a test type declared after its
// impl is test code, through its holder's mark (#106).
func TestHolderDeclaredAfter(t *testing.T) {
	g, _ := extract(t, fixtureOf(t, "gen", map[string]string{
		"src/lib.rs": `pub mod hand_mod {
    include!("hand.rs");
}

pub trait Area {
    fn area(&self) -> i32;
}

impl Late {
    pub fn new() -> Self {
        Late
    }
}

impl Area for Late {
    fn area(&self) -> i32 {
        1
    }
}

#[derive(Clone)]
pub struct Late;

pub fn uses() -> i32 {
    Late::new().clone().area()
}

impl Probe {
    fn probe(&self) {}
}

#[cfg(test)]
struct Probe;
`,
		"src/hand.rs": `impl crate::Area for Hand {
    fn area(&self) -> i32 {
        self.n
    }
}

impl Hand {
    pub fn get(&self) -> i32 {
        self.n
    }
}

pub struct Hand {
    pub n: i32,
}
`,
	}))
	var contains []string
	for _, e := range edges(g, graph.KindContains) {
		if strings.Contains(e, "#Late") || strings.Contains(e, "#Hand") || strings.Contains(e, "#Probe") {
			contains = append(contains, e)
		}
	}
	check(t, "contains", contains, []string{
		"src/hand.rs -> src/hand.rs#Hand",
		"src/hand.rs#Hand -> src/hand.rs#Hand.get()",
		"src/hand.rs#Hand -> src/hand.rs#Hand.n",
		"src/hand.rs#Hand -> src/hand.rs#Hand[Area].area()",
		"src/lib.rs -> src/lib.rs#Late",
		"src/lib.rs -> src/lib.rs#Probe",
		"src/lib.rs#Late -> src/lib.rs#Late.new()",
		"src/lib.rs#Late -> src/lib.rs#Late[Area].area()",
		"src/lib.rs#Late -> src/lib.rs#Late[Clone].clone()",
		"src/lib.rs#Probe -> src/lib.rs#Probe.probe()",
	})
	check(t, "implements", edges(g, graph.KindImplements), []string{
		"src/hand.rs#Hand -> src/lib.rs#Area",
		"src/lib.rs#Late -> src/lib.rs#Area",
	})
	for n := range g.Nodes() {
		if n.ID == "rust://src/lib.rs#Probe.probe()" && !n.Test {
			t.Errorf("%s is not test code; want it marked through Probe", n.ID)
		}
	}
}
