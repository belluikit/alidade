// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// fixture copies testdata/ws, a Cargo workspace of two packages, into a
// temporary directory, since indexing it runs Cargo's build there, with
// extra files at the given paths. A machine without the toolchain skips,
// and CI, which sets ALIDADE_REQUIRE_RUST, fails instead: a skipped test
// there would pass the layer unchecked.
func fixture(t *testing.T, extra map[string]string) string {
	t.Helper()
	return fixtureOf(t, "ws", extra)
}

// fixtureOf copies the workspace testdata/name, as fixture does testdata/ws.
func fixtureOf(t *testing.T, name string, extra map[string]string) string {
	t.Helper()
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", name))); err != nil {
		t.Fatal(err)
	}
	for rel, text := range extra {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if why := toolchainMissing(dir); why != "" {
		if os.Getenv("ALIDADE_REQUIRE_RUST") != "" {
			t.Fatalf("ALIDADE_REQUIRE_RUST is set and the toolchain is missing: %s", why)
		}
		t.Skip(why)
	}
	return dir
}

// tempDir is a temporary directory by its real path, as the build session
// gives every layer its root: macOS's is reached through a symbolic link.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func extract(t *testing.T, dir string) (*graph.Graph, *Report) {
	t.Helper()
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return b.Build(), rep
}

// edges lists a graph's edges of a kind as "from -> to", schemes dropped.
func edges(g *graph.Graph, kind graph.EdgeKind) []string {
	var out []string
	for e := range g.Edges() {
		if e.Kind == kind {
			if e.Derivation != graph.Resolved && kind != graph.KindContains {
				out = append(out, "not RESOLVED: "+string(e.Derivation))
			}
			out = append(out, strings.TrimPrefix(string(e.From), "rust://")+" -> "+strings.TrimPrefix(string(e.To), "rust://"))
		}
	}
	slices.Sort(out)
	return out
}

func nodes(g *graph.Graph) []string {
	var out []string
	for n := range g.Nodes() {
		out = append(out, strings.TrimPrefix(string(n.ID), "rust://")+" "+string(n.Kind))
	}
	slices.Sort(out)
	return out
}

func check(t *testing.T, what string, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("%s: missing %s", what, w)
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("%s: unexpected %s", what, g)
		}
	}
}

// TestExtractGraph pins the layer on the fixture (ADR 009, ADR 002 rule
// 16, ADR 006 rule 9): an item's ID is its path in its file, with the
// function holding it, which rust-analyzer's symbol leaves out, and the
// trait of an impl; a call is a reference followed by "(", or by a
// turbofish and "(" (#100), from the innermost function holding it; a
// function named as a value, with a turbofish or without, is a
// reference; two crate roots of one package that define one symbol (each
// integration test's helper) are told apart by file.
func TestExtractGraph(t *testing.T) {
	dir := fixture(t, map[string]string{
		"scripts/loose.rs":       "fn main() {}\n",
		"shapes/testdata/old.rs": "fn old() {}\n",
	})
	g, rep := extract(t, dir)
	check(t, "nodes", nodes(g), []string{
		"scripts/loose.rs module",
		"getters/src/lib.rs module", // its derive is a macro, with no node
		"shapes/build.rs module", "shapes/build.rs#main() func",
		"shapes/src/geometry.rs module",
		"shapes/src/geometry.rs#Point type", "shapes/src/geometry.rs#Point.x field", "shapes/src/geometry.rs#Point.y field",
		"shapes/src/geometry.rs#Point.type field", // r#type
		"shapes/src/geometry.rs#Display type", "shapes/src/geometry.rs#Display.fmt() method",
		"shapes/src/geometry.rs#Write type", "shapes/src/geometry.rs#Write.line() method",
		"shapes/src/geometry.rs#Pair type", "shapes/src/geometry.rs#Pair.first() method", "shapes/src/geometry.rs#Pair.second() method",
		"shapes/src/geometry.rs#unit() func", "shapes/src/geometry.rs#unit_area() func",
		"shapes/src/lib.rs module",
		"shapes/src/lib.rs#Area type", "shapes/src/lib.rs#Area.area() method",
		"shapes/src/lib.rs#Circle type", "shapes/src/lib.rs#Circle.radius field",
		"shapes/src/lib.rs#Circle[Area].area() method",                                                                  // round_shape! writes it
		"shapes/src/lib.rs#Gauge type", "shapes/src/lib.rs#Gauge.level field", "shapes/src/lib.rs#Gauge.level() method", // one name, two nodes
		"shapes/src/lib.rs#Gauge[Area].area() method",                                              // impl Area for &Gauge
		"shapes/src/lib.rs#Square.side() method", "shapes/src/lib.rs#Square[Clone].clone() method", // derived
		"shapes/src/lib.rs#slice(u8)[Area].area() method", "shapes/src/lib.rs#tuple(i32,i32)[Area].area() method",
		"shapes/src/lib.rs#array(u8;4)[Area].area() method", "shapes/src/lib.rs#slice(tuple(u8,u16))[Area].area() method",
		"shapes/src/lib.rs#tuple(tuple(u8,u8),u8)[Area].area() method",
		"shapes/src/lib.rs#written() func",
		"shapes/src/lib.rs#Kind type", "shapes/src/lib.rs#Kind.Angular enum_value", "shapes/src/lib.rs#Kind.Round enum_value",
		"shapes/src/lib.rs#ORIGIN var", "shapes/src/lib.rs#SIDES const",
		"shapes/src/lib.rs#Square type", "shapes/src/lib.rs#Square.new() method", "shapes/src/lib.rs#Square.side field",
		"shapes/src/lib.rs#Square[Area].area() method",
		"shapes/src/lib.rs#Square[Debug].fmt() method", "shapes/src/lib.rs#Square[Display].fmt() method",
		"shapes/src/lib.rs#area_of() func", "shapes/src/lib.rs#helper_generic() func",
		"shapes/src/lib.rs#local_impl() func", "shapes/src/lib.rs#local_impl().Local type", "shapes/src/lib.rs#local_impl().Local[Area].area() method",
		"shapes/src/lib.rs#local_impl().Wrap type", "shapes/src/lib.rs#local_impl().Wrap[Write].line() method",
		"shapes/src/lib.rs#shown() func", "shapes/src/lib.rs#shown().Shown type", "shapes/src/lib.rs#shown().Shown[Display].fmt() method",
		"shapes/src/lib.rs#paired() func", "shapes/src/lib.rs#paired().Two type",
		"shapes/src/lib.rs#paired().Two[Pair].first() method", "shapes/src/lib.rs#paired().Two[Pair].second() method",
		"shapes/src/lib.rs#paired().Two[Pair].first().Inner type", "shapes/src/lib.rs#paired().Two[Pair].first().Inner[Area].area() method",
		"shapes/src/lib.rs#made module", "shapes/src/lib.rs#made() func", // a module and a function of one name
		"shapes/src/lib.rs#made.Made type", "shapes/src/lib.rs#made.Made.new() method", // build.rs writes them, made includes them
		"shapes/src/lib.rs#Named type", "shapes/src/lib.rs#Named.name() method",
		"shapes/src/lib.rs#T[Named].name() method", // a blanket impl's, held by its file
		"shapes/src/lib.rs#Wide type", "shapes/src/lib.rs#Wide[Area].area() method",
		"shapes/src/lib.rs#pen() func", "shapes/src/lib.rs#pen().Pen type", "shapes/src/lib.rs#pen().Pen[Write].write_str() method",
		"shapes/src/lib.rs#outer() func", "shapes/src/lib.rs#outer().inner() func",
		"shapes/src/lib.rs#outer().inner().deepest() func",                            // two holders, each once
		"shapes/src/lib.rs#made_first() func", "shapes/src/lib.rs#made_second() func", // a macro's, siblings
		"shapes/src/lib.rs#parse_side() func", "shapes/src/lib.rs#pick() func", "shapes/src/lib.rs#total() func",
		"shapes/src/lib.rs#tests module", "shapes/src/lib.rs#tests.check() func", "shapes/src/lib.rs#tests.squares() func",
		"shapes/tests/common/mod.rs module", "shapes/tests/common/mod.rs#sample() func",
		"shapes/tests/first.rs module", "shapes/tests/first.rs#first() func", "shapes/tests/first.rs#helper() func",
		"shapes/tests/second.rs module", "shapes/tests/second.rs#helper() func", "shapes/tests/second.rs#second() func",
		"tool/src/main.rs module", "tool/src/main.rs#main() func",
	})
	check(t, "calls", edges(g, graph.KindCalls), []string{
		"shapes/src/geometry.rs#unit() -> shapes/src/lib.rs#Square.new()",
		"shapes/src/geometry.rs#unit_area() -> shapes/src/geometry.rs#unit()",
		"shapes/src/geometry.rs#unit_area() -> shapes/src/lib.rs#Square[Area].area()",
		"shapes/src/lib.rs#area_of() -> shapes/src/lib.rs#Square[Area].area()",
		"shapes/src/lib.rs#local_impl() -> shapes/src/lib.rs#local_impl().Local[Area].area()",
		"shapes/src/lib.rs#local_impl() -> shapes/src/lib.rs#local_impl().Wrap[Write].line()",
		"shapes/src/lib.rs#outer() -> shapes/src/lib.rs#outer().inner()",
		"shapes/src/lib.rs#outer().inner() -> shapes/src/lib.rs#Square.new()",
		"shapes/src/lib.rs#outer().inner() -> shapes/src/lib.rs#area_of()",
		"shapes/src/lib.rs#outer().inner() -> shapes/src/lib.rs#outer().inner().deepest()",
		"shapes/src/lib.rs#paired() -> shapes/src/lib.rs#paired().Two[Pair].first()",
		"shapes/src/lib.rs#paired() -> shapes/src/lib.rs#paired().Two[Pair].second()",
		"shapes/src/lib.rs#paired().Two[Pair].first() -> shapes/src/lib.rs#paired().Two[Pair].first().Inner[Area].area()",
		"shapes/src/lib.rs#made() -> shapes/src/lib.rs#made.Made.new()",        // into generated code
		"shapes/src/lib.rs#parse_side() -> shapes/src/lib.rs#helper_generic()", // a turbofish, ::<
		"shapes/src/lib.rs#pen() -> shapes/src/lib.rs#pen().Pen[Write].write_str()",
		"shapes/src/lib.rs#tests.check() -> shapes/src/lib.rs#area_of()",
		"shapes/src/lib.rs#tests.squares() -> shapes/src/lib.rs#Square.new()",
		"shapes/src/lib.rs#tests.squares() -> shapes/src/lib.rs#tests.check()",
		"shapes/src/lib.rs#written() -> shapes/src/lib.rs#Circle[Area].area()",
		"shapes/src/lib.rs#written() -> shapes/src/lib.rs#Gauge.level()",
		"shapes/src/lib.rs#written() -> shapes/src/lib.rs#Square.new()",
		"shapes/src/lib.rs#written() -> shapes/src/lib.rs#Square.side()",
		"shapes/src/lib.rs#written() -> shapes/src/lib.rs#Square[Clone].clone()",
		"shapes/tests/common/mod.rs#sample() -> shapes/src/lib.rs#Square.new()",
		"shapes/tests/first.rs#first() -> shapes/tests/first.rs#helper()",
		"shapes/tests/first.rs#helper() -> shapes/src/lib.rs#area_of()",
		"shapes/tests/first.rs#helper() -> shapes/tests/common/mod.rs#sample()",
		"shapes/tests/second.rs#helper() -> shapes/src/lib.rs#total()",
		"shapes/tests/second.rs#helper() -> shapes/tests/common/mod.rs#sample()",
		"shapes/tests/second.rs#second() -> shapes/tests/second.rs#helper()",
		"tool/src/main.rs#main() -> shapes/src/geometry.rs#unit()",
		"tool/src/main.rs#main() -> shapes/src/lib.rs#total()",
	})
	check(t, "references", edges(g, graph.KindReferences), []string{
		"shapes/src/lib.rs#total() -> shapes/src/lib.rs#area_of()",       // .map(area_of)
		"shapes/src/lib.rs#pick() -> shapes/src/lib.rs#helper_generic()", // a turbofish alone: a value (#100)
	})
	check(t, "contains", edges(g, graph.KindContains), []string{
		"shapes/build.rs -> shapes/build.rs#main()",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#Display",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#Pair",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#Point",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#Write",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#unit()",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#unit_area()",
		"shapes/src/geometry.rs#Display -> shapes/src/geometry.rs#Display.fmt()",
		"shapes/src/geometry.rs#Pair -> shapes/src/geometry.rs#Pair.first()",
		"shapes/src/geometry.rs#Pair -> shapes/src/geometry.rs#Pair.second()",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.type",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.x",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.y",
		"shapes/src/geometry.rs#Write -> shapes/src/geometry.rs#Write.line()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Circle",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Gauge",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Kind",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Named",
		"shapes/src/lib.rs -> shapes/src/lib.rs#ORIGIN",
		"shapes/src/lib.rs -> shapes/src/lib.rs#SIDES",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Square",
		"shapes/src/lib.rs -> shapes/src/lib.rs#T[Named].name()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Wide",
		"shapes/src/lib.rs -> shapes/src/lib.rs#area_of()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#helper_generic()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#local_impl()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#made",
		"shapes/src/lib.rs#made -> shapes/src/lib.rs#made.Made", // generated, under the module that includes it
		"shapes/src/lib.rs#made.Made -> shapes/src/lib.rs#made.Made.new()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#made()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#made_first()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#made_second()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#outer()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#paired()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#parse_side()", "shapes/src/lib.rs -> shapes/src/lib.rs#pick()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#pen()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#shown()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#tests",
		"shapes/src/lib.rs -> shapes/src/lib.rs#array(u8;4)[Area].area()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#slice(tuple(u8,u16))[Area].area()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#slice(u8)[Area].area()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#total()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#tuple(i32,i32)[Area].area()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#tuple(tuple(u8,u8),u8)[Area].area()",
		"shapes/src/lib.rs -> shapes/src/lib.rs#written()",
		"shapes/src/lib.rs#Area -> shapes/src/lib.rs#Area.area()",
		"shapes/src/lib.rs#Circle -> shapes/src/lib.rs#Circle.radius",
		"shapes/src/lib.rs#Circle -> shapes/src/lib.rs#Circle[Area].area()",
		"shapes/src/lib.rs#Gauge -> shapes/src/lib.rs#Gauge.level",
		"shapes/src/lib.rs#Gauge -> shapes/src/lib.rs#Gauge.level()",
		"shapes/src/lib.rs#Gauge -> shapes/src/lib.rs#Gauge[Area].area()",
		"shapes/src/lib.rs#Kind -> shapes/src/lib.rs#Kind.Angular",
		"shapes/src/lib.rs#Kind -> shapes/src/lib.rs#Kind.Round",
		"shapes/src/lib.rs#Named -> shapes/src/lib.rs#Named.name()",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square.new()",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square.side",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square.side()",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Clone].clone()",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Area].area()", // an impl's method under its type
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Debug].fmt()",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Display].fmt()",
		"shapes/src/lib.rs#Wide -> shapes/src/lib.rs#Wide[Area].area()",
		"shapes/src/lib.rs#local_impl() -> shapes/src/lib.rs#local_impl().Local",
		"shapes/src/lib.rs#local_impl() -> shapes/src/lib.rs#local_impl().Wrap",
		"shapes/src/lib.rs#local_impl().Local -> shapes/src/lib.rs#local_impl().Local[Area].area()",
		"shapes/src/lib.rs#local_impl().Wrap -> shapes/src/lib.rs#local_impl().Wrap[Write].line()",
		"shapes/src/lib.rs#outer() -> shapes/src/lib.rs#outer().inner()",
		"shapes/src/lib.rs#outer().inner() -> shapes/src/lib.rs#outer().inner().deepest()",
		"shapes/src/lib.rs#paired() -> shapes/src/lib.rs#paired().Two",
		"shapes/src/lib.rs#paired().Two -> shapes/src/lib.rs#paired().Two[Pair].first()",
		"shapes/src/lib.rs#paired().Two -> shapes/src/lib.rs#paired().Two[Pair].second()",
		"shapes/src/lib.rs#paired().Two[Pair].first() -> shapes/src/lib.rs#paired().Two[Pair].first().Inner",
		"shapes/src/lib.rs#paired().Two[Pair].first().Inner -> shapes/src/lib.rs#paired().Two[Pair].first().Inner[Area].area()",
		"shapes/src/lib.rs#pen() -> shapes/src/lib.rs#pen().Pen",
		"shapes/src/lib.rs#pen().Pen -> shapes/src/lib.rs#pen().Pen[Write].write_str()",
		"shapes/src/lib.rs#shown() -> shapes/src/lib.rs#shown().Shown",
		"shapes/src/lib.rs#shown().Shown -> shapes/src/lib.rs#shown().Shown[Display].fmt()",
		"shapes/src/lib.rs#tests -> shapes/src/lib.rs#tests.check()",
		"shapes/src/lib.rs#tests -> shapes/src/lib.rs#tests.squares()",
		"shapes/tests/common/mod.rs -> shapes/tests/common/mod.rs#sample()",
		"shapes/tests/first.rs -> shapes/tests/first.rs#first()",
		"shapes/tests/first.rs -> shapes/tests/first.rs#helper()",
		"shapes/tests/second.rs -> shapes/tests/second.rs#helper()",
		"shapes/tests/second.rs -> shapes/tests/second.rs#second()",
		"tool/src/main.rs -> tool/src/main.rs#main()",
	})
	checkImplements(t, g, rep)
	checkReport(t, rep)
	for n := range g.Nodes() {
		unavailable := n.Attrs["calls_unavailable"]
		if (unavailable != "") != (n.Pos.File == "scripts/loose.rs") {
			t.Errorf("%s calls_unavailable %q: only a file outside every workspace says so", n.ID, unavailable)
		}
	}
}

// checkReport pins TestExtractGraph's report: the fixture's files and
// declarations, the one file a build script wrote and the call into it, no
// item skipped, and the workspace and engine that read it.
func checkReport(t *testing.T, rep *Report) {
	t.Helper()
	if rep.Files != 9 || rep.Testdata != 1 || rep.Unanalyzed != 1 || rep.Declarations != 82 || rep.MacroWritten != 3 {
		t.Errorf("Files %d, Testdata %d, Unanalyzed %d, Declarations %d, MacroWritten %d; want 9, 1, 1, 82, 3", rep.Files, rep.Testdata, rep.Unanalyzed, rep.Declarations, rep.MacroWritten)
	}
	// The call to made::Made::new() reaches code a build script wrote,
	// which the index leaves out (ADR 009), and the server reads: an edge.
	if rep.CallsGenerated != 0 || rep.Calls != 33 || rep.References != 2 {
		t.Errorf("CallsGenerated %d, Calls %d, References %d; want 0, 33, 2", rep.CallsGenerated, rep.Calls, rep.References)
	}
	if rep.GeneratedFiles != 1 || rep.Generated != 2 || rep.Generates != 0 {
		t.Errorf("GeneratedFiles %d, Generated %d, Generates %d; want 1, 2, 0: made.rs is no prost-build output", rep.GeneratedFiles, rep.Generated, rep.Generates)
	}
	checkEngine(t, rep)
}

// checkEngine pins what read the fixture: the server's columns, no item
// skipped, the one workspace and rust-analyzer.
func checkEngine(t *testing.T, rep *Report) {
	t.Helper()
	if rep.ServerColumns != "utf-8" {
		t.Errorf("the server counted in %q, want utf-8, which the client asks for first", rep.ServerColumns)
	}
	if len(rep.Skipped) != 0 {
		t.Errorf("skipped %v, want none", rep.Skipped)
	}
	if !slices.Equal(rep.Workspaces, []string{"."}) || !strings.HasPrefix(rep.Engine, "rust-analyzer ") {
		t.Errorf("Workspaces %v, Engine %q", rep.Workspaces, rep.Engine)
	}
}

// checkImplements pins the implementations (ADR 009): rust-analyzer's
// server gives Square's and Wide's, and Circle's, which round_shape!
// writes; inside a body, where the server finds none, the impl's header
// gives local_impl's Local and Wrap, whose header runs over three lines.
// Square's fmt::Display is not geometry::Display, though both name a trait
// Display with fmt; inside a body neither is Shown's, nor Pen's fmt::Write
// geometry::Write. The blanket impl's type is a parameter, with no node.
func checkImplements(t *testing.T, g *graph.Graph, rep *Report) {
	t.Helper()
	check(t, "implements", edges(g, graph.KindImplements), []string{
		"shapes/src/lib.rs#Circle -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs#Gauge -> shapes/src/lib.rs#Area", // through &Gauge
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs#Wide -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs#local_impl().Local -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs#local_impl().Wrap -> shapes/src/geometry.rs#Write", // the trait on its header's third line
		"shapes/src/lib.rs#paired().Two -> shapes/src/geometry.rs#Pair",       // past Inner's header, nearer
		"shapes/src/lib.rs#paired().Two[Pair].first().Inner -> shapes/src/lib.rs#Area",
	})
	if rep.Implements != 8 || rep.ImplementsUnplaced != 6 {
		t.Errorf("Implements %d, ImplementsUnplaced %d; want 8, 6 (the blanket impl, the slices, the tuples, the array)", rep.Implements, rep.ImplementsUnplaced)
	}
}

// TestImplementsInUTF16: a server that counts columns in UTF-16, as LSP
// does by default, has its answers converted to the index's UTF-8 bytes,
// so Wide, named after a comment with two-byte characters on its line, is
// still found.
func TestImplementsInUTF16(t *testing.T) {
	old := lspEncodings
	lspEncodings = []string{"utf-16"}
	t.Cleanup(func() { lspEncodings = old })
	g, rep := extract(t, fixture(t, nil))
	if rep.ServerColumns != "utf-16" {
		t.Fatalf("the server counted in %q, want utf-16", rep.ServerColumns)
	}
	if !slices.Contains(edges(g, graph.KindImplements), "shapes/src/lib.rs#Wide -> shapes/src/lib.rs#Area") {
		t.Errorf("implements %v, want Wide -> Area among them", edges(g, graph.KindImplements))
	}
}

// fakeTools puts scripts named like the toolchain's on the PATH: each
// answers --version, and rustc --print sysroot prints sysroot.
func fakeTools(t *testing.T, sysroot string) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"cargo", "rustc", "rust-analyzer"} {
		script := "#!/bin/sh\nif [ \"$1\" = --print ]; then echo " + sysroot + "; else echo " + name + " 1.0; fi\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

// TestToolchainMissing: without cargo, rust-analyzer, or the standard
// library's source, every file is a node saying
// its declarations and calls are not read, and why, the report says it
// once, and nothing else is read (ADR 009).
func TestToolchainMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{"no cargo", func(t *testing.T) { t.Setenv("PATH", t.TempDir()) }, "cargo is not on the PATH"},
		{"no rust-src", func(t *testing.T) { fakeTools(t, t.TempDir()) }, "rustup component add rust-src"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "ws"))); err != nil {
				t.Fatal(err)
			}
			tc.setup(t)
			g, rep := extract(t, dir)
			checkUnavailable(t, g, rep, tc.want)
		})
	}
}

// checkUnavailable checks a build of testdata/ws that read nothing says
// why, want, in its report, once in Skipped and on every node.
func checkUnavailable(t *testing.T, g *graph.Graph, rep *Report, want string) {
	t.Helper()
	if !strings.Contains(rep.Unavailable, want) {
		t.Errorf("Unavailable %q, want it to say %q", rep.Unavailable, want)
	}
	if rep.Files != 8 || rep.Unanalyzed != 8 || rep.Declarations != 0 || len(rep.Workspaces) != 0 {
		t.Errorf("Files %d, Unanalyzed %d, Declarations %d, Workspaces %v; want 8, 8, 0, none", rep.Files, rep.Unanalyzed, rep.Declarations, rep.Workspaces)
	}
	// Skipped says it too, once, at the workspace's manifest: graph_stats
	// shows Skipped, not node attributes (#109).
	if len(rep.Skipped) != 1 || rep.Skipped[0].File != "Cargo.toml" || !strings.HasPrefix(rep.Skipped[0].Reason, "no Rust file's declarations or calls are read: ") || !strings.Contains(rep.Skipped[0].Reason, want) {
		t.Errorf("Skipped %+v, want one entry at Cargo.toml saying no file is read and %q", rep.Skipped, want)
	}
	for n := range g.Nodes() {
		if !strings.Contains(n.Attrs["calls_unavailable"], want) {
			t.Errorf("%s calls_unavailable %q, want it to say %q", n.ID, n.Attrs["calls_unavailable"], want)
		}
	}
}

// TestShallowest: the toolchain's absence is reported at the manifest
// nearest the root, though a directory named before it sorts first (#109).
func TestShallowest(t *testing.T) {
	sep := string(filepath.Separator)
	in := []string{sep + filepath.Join("r", "Bench", "Cargo.toml"), sep + filepath.Join("r", "Cargo.toml"), sep + filepath.Join("r", "a", "Cargo.toml")}
	if got, want := shallowest(in), in[1]; got != want {
		t.Errorf("shallowest(%v) = %s, want %s", in, got, want)
	}
}
