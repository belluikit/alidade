// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// TestRustKey names rustc's functions, from demangle's v0 output, as the
// layer's are named.
func TestRustKey(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"protowire_pb::big::corrupt", "big::corrupt"},                              // a free function keeps its module
		{"<protowire_pb::wire::Reader>::len_view", "Reader::len_view"},              // an inherent method
		{"<protowire_pb::wire::Reader>::len_view::{closure#0}", "Reader::len_view"}, // a closure is its function's
		{"<protowire_pb::wire::Reader>::take<4>", "Reader::take"},                   // a const argument
		{"<protowire_pb::big::BigInt as protowire_pb::codec::Message>::encode_to", "<BigInt as Message>::encode_to"},
		{"<protowire_pb::big::BigInt as core::cmp::PartialEq>::eq", "<BigInt as PartialEq>::eq"},
		{"protowire_pb::codec::write_message::<protowire_pb::big::BigInt>", "codec::write_message"}, // an instantiation
		{"<&protowire_pb::wire::Kind as core::fmt::Display>::fmt", ""},                              // the standard library's impl for &T
		{"<[u8] as protowire_pb::codec::MapEntryField>::write_to", ""},                              // a type outside the crate
		{"<core::option::Option<u32>>::and_then::<&&str, protowire_pb::big::corrupt::{closure#0}>", ""},
		{"core::ptr::drop_in_place::<protowire_pb::wire::Error>", ""},
		// a generic argument holding a trait's method: " as " is the argument's
		{"<protowire_pb::wire::Reader<<protowire_pb::big::BigInt as protowire_pb::codec::Message>::encode_to::{closure#0}>>::len_view", "Reader::len_view"},
	} {
		if got := rustKey(c.name, "protowire_pb"); got != c.want {
			t.Errorf("rustKey(%q) = %q; want %q", c.name, got, c.want)
		}
	}
}

// TestLayerKey names the layer's functions, by ID, as rustKey names
// rustc's: the file below the library's directory is the module.
func TestLayerKey(t *testing.T) {
	dir := "crates/protowire-pb/src"
	for _, c := range []struct{ id, want string }{
		{"rust://crates/protowire-pb/src/big.rs#corrupt()", "big::corrupt"},
		{"rust://crates/protowire-pb/src/wire.rs#Reader.len_view()", "Reader::len_view"},
		{"rust://crates/protowire-pb/src/big.rs#BigInt[Message].encode_to()", "<BigInt as Message>::encode_to"},
		{"rust://crates/protowire-pb/src/lib.rs#marshal()", "marshal"},
		{"rust://crates/protowire-pb/src/wkt/mod.rs#any()", "wkt::any"},
		{"rust://crates/protowire-pb/src/codec.rs#Message.encode_to()", "Message::encode_to"}, // a trait's own method
		{"rust://crates/protowire-pb/tests/it.rs#case()", ""},                                 // outside the library
		{"go://example.com/m#F", ""},
	} {
		if got := layerKey(graph.ID(c.id), dir); got != c.want {
			t.Errorf("layerKey(%q) = %q; want %q", c.id, got, c.want)
		}
	}
}

// irFixture is the IR of `pub fn g() -> i32 { 1 } pub fn f() -> i32 {
// g() + 1 }` as rustc 1.100 writes it at opt-level 0, cut to what is
// read: f's definition, its call of g and of core's overflow panic; and a
// call g makes of itself, written in, which is no edge.
const irFixture = `; lib::f
define i32 @_RNvCs2xfZtAFMfmm_3lib1f() unnamed_addr #0 !guid !6 {
start:
; call lib::g
  %_1 = call i32 @_RNvCs2xfZtAFMfmm_3lib1g()
  call void @_RNvNtNtCsjSAAZcmtVt9_4core9panicking11panic_const24panic_const_add_overflow(ptr align 8 @alloc_e41f) #3
  unreachable
}

; lib::g
define i32 @_RNvCs2xfZtAFMfmm_3lib1g() unnamed_addr #0 {
start:
  %_2 = call i32 @_RNvCs2xfZtAFMfmm_3lib1g()
  ret i32 1
}
`

// TestReadIR reads the functions an IR module defines and the crate's
// functions each calls.
func TestReadIR(t *testing.T) {
	rc, err := readIR(strings.NewReader(irFixture), RustLib{Crate: "lib"})
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Funcs["f"] || !rc.Funcs["g"] || len(rc.Funcs) != 2 {
		t.Errorf("funcs %v; want f and g", rc.Funcs)
	}
	if !rc.Calls["f"]["g"] || len(rc.Calls) != 1 || len(rc.Calls["f"]) != 1 {
		t.Errorf("calls %v; want f -> g alone: core's panic is outside the crate, g -> g a function calling itself", rc.Calls)
	}
}

// rustGraph is a graph of the Rust nodes at the given paths of
// src/lib.rs, by name, methods where the path holds a type, and calls
// edges between them.
func rustGraph(t *testing.T, nodes map[string][]string, calls [][2]string) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder("/repo")
	ids := map[string]graph.ID{}
	for name, path := range nodes {
		id, err := b.Minter().RustID("src/lib.rs", path...)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
		kind := graph.KindFunc
		if len(path) > 1 {
			kind = graph.KindMethod
		}
		if err := b.AddNode(graph.Node{ID: id, Kind: kind, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range calls {
		if err := b.AddEdge(graph.Edge{From: ids[c[0]], To: ids[c[1]], Kind: graph.KindCalls, Derivation: graph.Resolved}); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build()
}

// TestCompareRustCalls classes each edge only one side has: to a function
// with no node, through a generic trait bound (both sides' edges), an
// operator's, through dyn, or listed.
func TestCompareRustCalls(t *testing.T) {
	g := rustGraph(t, map[string][]string{
		"write": {"write()"}, "helper": {"helper()"}, "gone": {"gone()"}, "encode": {"Message", "encode()"},
		"eq": {"Big[PartialEq]", "eq()"}, "bigEncode": {"Big[Message]", "encode()"},
		"visit": {"Visitor", "visit()"}, "implVisit": {"Printer[Visitor]", "visit()"},
		"case": {"tests", "case()"}, // an inline tests module: no test code in a library's IR
	}, [][2]string{{"write", "encode"}, {"write", "helper"}, {"write", "gone"}, {"write", "visit"}, {"case", "helper"}})
	rc := &RustCalls{Lib: RustLib{Package: "p", Crate: "p", Dir: "src"}, Calls: map[string]map[string]bool{
		"write": {"<Big as Message>::encode": true, "helper": true, "<Big as PartialEq>::eq": true, "derived": true},
	}, Funcs: map[string]bool{"<Printer as Visitor>::visit": true}} // compiled, called from nowhere rustc names: through dyn
	r := CompareRustCalls(g, rc)
	got := []int{r.LayerCallers, r.Both, r.Agree, r.RustcOnly, r.LayerOnly, len(r.NoNode), r.BoundRustc, r.BoundLayer, r.Operator, r.Dyn}
	if want := []int{1, 1, 1, 3, 3, 1, 1, 1, 1, 1}; !slices.Equal(got, want) {
		t.Errorf("layer callers, both, agree, rustc only, layer only, no node, bound rustc/layer, operator, dyn = %v; want %v", got, want)
	}
	if len(r.OtherRustc) != 0 || !slices.Equal(r.OtherLayer, []string{"write -> gone"}) {
		t.Errorf("other: rustc %v, layer %v; want none, and write -> gone", r.OtherRustc, r.OtherLayer)
	}
}

// TestRustProvided: rustc's copy of a trait's provided method for a type
// that does not override it is the trait's method, as the layer names it;
// a method the type declares keeps its own name.
func TestRustProvided(t *testing.T) {
	g := rustGraph(t, map[string][]string{
		"run": {"run()"}, "size": {"Message", "size()"}, "encode": {"Message", "encode()"}, "bigEncode": {"Big[Message]", "encode()"},
	}, [][2]string{{"run", "size"}, {"run", "bigEncode"}})
	rc := &RustCalls{Lib: RustLib{Crate: "p", Dir: "src"}, Calls: map[string]map[string]bool{
		"run": {"<Big as Message>::size": true, "<Big as Message>::encode": true},
	}}
	r := CompareRustCalls(g, rc)
	if r.Agree != 2 || r.RustcOnly != 0 || r.LayerOnly != 0 {
		t.Errorf("agree %d, rustc only %d, layer only %d; want 2, 0, 0", r.Agree, r.RustcOnly, r.LayerOnly)
	}
}

// TestRustOutsideCode: what rustc compiles from a trait outside the crate
// for a crate type, and the crate does not declare, is left out and
// counted; the crate's own implementation of such a trait is compared.
func TestRustOutsideCode(t *testing.T) {
	if !foreignTrait("<p::Big as core::convert::Into<u8>>::into", "p") || foreignTrait("<p::Big as p::Message>::encode", "p") ||
		foreignTrait("<p::Big<<p::Small as core::convert::Into<u8>>::into::{closure#0}> as p::Message>::encode", "p") {
		t.Fatal("foreignTrait: want Into outside the crate, Message inside it, whatever a generic argument names")
	}
	g := rustGraph(t, map[string][]string{"run": {"run()"}, "fmt": {"Big[Display]", "fmt()"}}, [][2]string{{"run", "fmt"}})
	rc := &RustCalls{Lib: RustLib{Crate: "p", Dir: "src"}, Calls: map[string]map[string]bool{
		"run": {"<Big as Display>::fmt": true, "<Big as Into>::into": true},
	}, Foreign: map[string]bool{"<Big as Display>::fmt": true, "<Big as Into>::into": true}}
	r := CompareRustCalls(g, rc)
	if r.Agree != 1 || r.RustcOnly != 0 || r.Outside != 1 {
		t.Errorf("agree %d, rustc only %d, outside %d; want 1, 0, 1", r.Agree, r.RustcOnly, r.Outside)
	}
}

// TestReadRustCallsBodies compiles a crate and reads the two calls the IR
// left out before #98: one of an #[inline(always)] function, which LLVM
// inlined even at opt-level 0, and the calls in an async method's body and
// in a closure a returned iterator holds, which rustc compiled only where
// something polls the future or uses the iterator.
func TestReadRustCallsBodies(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on the PATH")
	}
	ws := fxCrate(t, "", `#[inline(always)]
fn inlined() -> u32 { 1 }
pub fn calls_inlined() -> u32 { inlined() }
fn helper() -> u32 { 2 }
fn mapped(i: u32) -> u32 { i }
pub struct S;
impl S {
    pub async fn peek(&self) -> u32 { helper() }
}
pub fn items() -> impl Iterator<Item = u32> { (0..3).map(|i| mapped(i)) }
`)
	rc, err := ReadRustCalls(ws, t.TempDir(), RustLib{Package: "fx", Crate: "fx", Dir: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Calls["calls_inlined"]["inlined"] || !rc.Calls["S::peek"]["helper"] || !rc.Calls["items"]["mapped"] {
		t.Errorf("calls %v; want calls_inlined -> inlined, S::peek -> helper and items -> mapped", rc.Calls)
	}
}

// TestReadRustCallsFeature builds a library with RustFeature when its
// package declares it, and with its default features otherwise.
func TestReadRustCallsFeature(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on the PATH")
	}
	ws := fxCrate(t, "\n[features]\nextra = []\n", `fn helper() -> u32 { 2 }
#[cfg(feature = "extra")]
pub fn gated() -> u32 { helper() }
`)
	libs, err := RustLibs(ws, ws)
	if err != nil || len(libs) != 1 || !libs[0].Features["extra"] {
		t.Fatalf("libs %+v, %v; want fx, with its feature extra", libs, err)
	}
	defer func(f string) { RustFeature = f }(RustFeature)
	for _, f := range []string{"", "extra", "missing"} {
		RustFeature = f
		rc, err := ReadRustCalls(ws, t.TempDir(), libs[0])
		if err != nil {
			t.Fatalf("feature %q: %v", f, err)
		}
		if got := rc.Calls["gated"]["helper"]; got != (f == "extra") {
			t.Errorf("feature %q: gated -> helper is %v", f, got)
		}
	}
}

// fxCrate writes a crate named fx, with manifest text after its package
// table and lib.rs, and returns its directory.
func fxCrate(t *testing.T, manifest, lib string) string {
	t.Helper()
	ws := t.TempDir()
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"fx\"\nversion = \"0.1.0\"\nedition = \"2021\"\n" + manifest,
		"src/lib.rs": lib,
	}
	for name, text := range files {
		p := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}
