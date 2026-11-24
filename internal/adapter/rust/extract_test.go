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
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "ws"))); err != nil {
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
// trait of an impl; a call is a reference followed by "(" or "::<", from
// the innermost function holding it; a function named as a value is a
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
		"shapes/build.rs module", "shapes/build.rs#main func",
		"shapes/src/geometry.rs module",
		"shapes/src/geometry.rs#Point type", "shapes/src/geometry.rs#Point.x field", "shapes/src/geometry.rs#Point.y field",
		"shapes/src/geometry.rs#Point.type field", // r#type
		"shapes/src/geometry.rs#unit func", "shapes/src/geometry.rs#unit_area func",
		"shapes/src/lib.rs module",
		"shapes/src/lib.rs#Area type", "shapes/src/lib.rs#Area.area method",
		"shapes/src/lib.rs#Circle type", "shapes/src/lib.rs#Circle.radius field",
		"shapes/src/lib.rs#Kind type", "shapes/src/lib.rs#Kind.Angular enum_value", "shapes/src/lib.rs#Kind.Round enum_value",
		"shapes/src/lib.rs#ORIGIN var", "shapes/src/lib.rs#SIDES const",
		"shapes/src/lib.rs#Square type", "shapes/src/lib.rs#Square.new method", "shapes/src/lib.rs#Square.side field",
		"shapes/src/lib.rs#Square[Area].area method",
		"shapes/src/lib.rs#Square[Debug].fmt method", "shapes/src/lib.rs#Square[Display].fmt method",
		"shapes/src/lib.rs#area_of func", "shapes/src/lib.rs#helper_generic func",
		"shapes/src/lib.rs#local_impl func", "shapes/src/lib.rs#local_impl.Local type", "shapes/src/lib.rs#local_impl.Local[Area].area method",
		"shapes/src/lib.rs#made module",
		"shapes/src/lib.rs#outer func", "shapes/src/lib.rs#outer.inner func",
		"shapes/src/lib.rs#parse_side func", "shapes/src/lib.rs#total func",
		"shapes/src/lib.rs#tests module", "shapes/src/lib.rs#tests.check func", "shapes/src/lib.rs#tests.squares func",
		"shapes/tests/common/mod.rs module", "shapes/tests/common/mod.rs#sample func",
		"shapes/tests/first.rs module", "shapes/tests/first.rs#first func", "shapes/tests/first.rs#helper func",
		"shapes/tests/second.rs module", "shapes/tests/second.rs#helper func", "shapes/tests/second.rs#second func",
		"tool/src/main.rs module", "tool/src/main.rs#main func",
	})
	check(t, "calls", edges(g, graph.KindCalls), []string{
		"shapes/src/geometry.rs#unit -> shapes/src/lib.rs#Square.new",
		"shapes/src/geometry.rs#unit_area -> shapes/src/geometry.rs#unit",
		"shapes/src/geometry.rs#unit_area -> shapes/src/lib.rs#Square[Area].area",
		"shapes/src/lib.rs#area_of -> shapes/src/lib.rs#Square[Area].area",
		"shapes/src/lib.rs#local_impl -> shapes/src/lib.rs#local_impl.Local[Area].area",
		"shapes/src/lib.rs#outer -> shapes/src/lib.rs#outer.inner",
		"shapes/src/lib.rs#outer.inner -> shapes/src/lib.rs#Square.new",
		"shapes/src/lib.rs#outer.inner -> shapes/src/lib.rs#area_of",
		"shapes/src/lib.rs#parse_side -> shapes/src/lib.rs#helper_generic", // a turbofish, ::<
		"shapes/src/lib.rs#tests.check -> shapes/src/lib.rs#area_of",
		"shapes/src/lib.rs#tests.squares -> shapes/src/lib.rs#Square.new",
		"shapes/src/lib.rs#tests.squares -> shapes/src/lib.rs#tests.check",
		"shapes/tests/common/mod.rs#sample -> shapes/src/lib.rs#Square.new",
		"shapes/tests/first.rs#first -> shapes/tests/first.rs#helper",
		"shapes/tests/first.rs#helper -> shapes/src/lib.rs#area_of",
		"shapes/tests/first.rs#helper -> shapes/tests/common/mod.rs#sample",
		"shapes/tests/second.rs#helper -> shapes/src/lib.rs#total",
		"shapes/tests/second.rs#helper -> shapes/tests/common/mod.rs#sample",
		"shapes/tests/second.rs#second -> shapes/tests/second.rs#helper",
		"tool/src/main.rs#main -> shapes/src/geometry.rs#unit",
		"tool/src/main.rs#main -> shapes/src/lib.rs#total",
	})
	check(t, "references", edges(g, graph.KindReferences), []string{
		"shapes/src/lib.rs#total -> shapes/src/lib.rs#area_of", // .map(area_of)
	})
	check(t, "contains", edges(g, graph.KindContains), []string{
		"shapes/build.rs -> shapes/build.rs#main",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#Point",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#unit",
		"shapes/src/geometry.rs -> shapes/src/geometry.rs#unit_area",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.type",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.x",
		"shapes/src/geometry.rs#Point -> shapes/src/geometry.rs#Point.y",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Area",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Circle",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Kind",
		"shapes/src/lib.rs -> shapes/src/lib.rs#ORIGIN",
		"shapes/src/lib.rs -> shapes/src/lib.rs#SIDES",
		"shapes/src/lib.rs -> shapes/src/lib.rs#Square",
		"shapes/src/lib.rs -> shapes/src/lib.rs#area_of",
		"shapes/src/lib.rs -> shapes/src/lib.rs#helper_generic",
		"shapes/src/lib.rs -> shapes/src/lib.rs#local_impl",
		"shapes/src/lib.rs -> shapes/src/lib.rs#made",
		"shapes/src/lib.rs -> shapes/src/lib.rs#outer",
		"shapes/src/lib.rs -> shapes/src/lib.rs#parse_side",
		"shapes/src/lib.rs -> shapes/src/lib.rs#tests",
		"shapes/src/lib.rs -> shapes/src/lib.rs#total",
		"shapes/src/lib.rs#Area -> shapes/src/lib.rs#Area.area",
		"shapes/src/lib.rs#Circle -> shapes/src/lib.rs#Circle.radius",
		"shapes/src/lib.rs#Kind -> shapes/src/lib.rs#Kind.Angular",
		"shapes/src/lib.rs#Kind -> shapes/src/lib.rs#Kind.Round",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square.new",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square.side",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Area].area", // an impl's method under its type
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Debug].fmt",
		"shapes/src/lib.rs#Square -> shapes/src/lib.rs#Square[Display].fmt",
		"shapes/src/lib.rs#local_impl -> shapes/src/lib.rs#local_impl.Local",
		"shapes/src/lib.rs#local_impl.Local -> shapes/src/lib.rs#local_impl.Local[Area].area",
		"shapes/src/lib.rs#outer -> shapes/src/lib.rs#outer.inner",
		"shapes/src/lib.rs#tests -> shapes/src/lib.rs#tests.check",
		"shapes/src/lib.rs#tests -> shapes/src/lib.rs#tests.squares",
		"shapes/tests/common/mod.rs -> shapes/tests/common/mod.rs#sample",
		"shapes/tests/first.rs -> shapes/tests/first.rs#first",
		"shapes/tests/first.rs -> shapes/tests/first.rs#helper",
		"shapes/tests/second.rs -> shapes/tests/second.rs#helper",
		"shapes/tests/second.rs -> shapes/tests/second.rs#second",
		"tool/src/main.rs -> tool/src/main.rs#main",
	})
	if rep.Files != 8 || rep.Testdata != 1 || rep.Unanalyzed != 1 || rep.Declarations != 41 {
		t.Errorf("Files %d, Testdata %d, Unanalyzed %d, Declarations %d; want 8, 1, 1, 41", rep.Files, rep.Testdata, rep.Unanalyzed, rep.Declarations)
	}
	// The call to made::Made::new() reaches code a build script wrote,
	// which the index leaves out (ADR 009): counted, not an edge.
	if rep.CallsGenerated != 1 || rep.Calls != 21 || rep.References != 1 {
		t.Errorf("CallsGenerated %d, Calls %d, References %d; want 1, 21, 1", rep.CallsGenerated, rep.Calls, rep.References)
	}
	if len(rep.Skipped) != 0 {
		t.Errorf("skipped %v, want none", rep.Skipped)
	}
	if !slices.Equal(rep.Workspaces, []string{"."}) || !strings.HasPrefix(rep.Engine, "rust-analyzer ") {
		t.Errorf("Workspaces %v, Engine %q", rep.Workspaces, rep.Engine)
	}
	for n := range g.Nodes() {
		unavailable := n.Attrs["calls_unavailable"]
		if (unavailable != "") != (n.Pos.File == "scripts/loose.rs") {
			t.Errorf("%s calls_unavailable %q: only a file outside every workspace says so", n.ID, unavailable)
		}
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
// library's source, every file is a node saying its declarations and
// calls are not read, and why, and nothing else is read (ADR 009).
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
			if !strings.Contains(rep.Unavailable, tc.want) {
				t.Errorf("Unavailable %q, want it to say %q", rep.Unavailable, tc.want)
			}
			if rep.Files != 7 || rep.Unanalyzed != 7 || rep.Declarations != 0 || len(rep.Workspaces) != 0 {
				t.Errorf("Files %d, Unanalyzed %d, Declarations %d, Workspaces %v; want 7, 7, 0, none", rep.Files, rep.Unanalyzed, rep.Declarations, rep.Workspaces)
			}
			for n := range g.Nodes() {
				if !strings.Contains(n.Attrs["calls_unavailable"], tc.want) {
					t.Errorf("%s calls_unavailable %q, want it to say %q", n.ID, n.Attrs["calls_unavailable"], tc.want)
				}
			}
		})
	}
}
