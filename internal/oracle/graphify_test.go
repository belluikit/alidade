// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// TestOracleGraphifyRust scores the Rust layer's calls edges and
// graphify's against rustc's, one library at a time, on the same
// reference (RustReference). ALIDADE_GRAPHIFY_RUST names the repository
// and the graph.json `graphify update` wrote for a copy of it at the same
// commit, as root=graph.json. Like the other oracles it prints numbers
// and fails only when it measured nothing.
func TestOracleGraphifyRust(t *testing.T) {
	root, graphJSON, ok := strings.Cut(os.Getenv("ALIDADE_GRAPHIFY_RUST"), "=")
	if !ok {
		t.Skip("set ALIDADE_GRAPHIFY_RUST to root=graph.json")
	}
	RustFeature = os.Getenv("ALIDADE_GRAPHIFY_RUST_FEATURE")
	g, rep, err := build.Build(root, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rust == nil || len(rep.Rust.Workspaces) == 0 {
		t.Fatalf("no Rust workspace indexed: %+v", rep.Rust)
	}
	gf, err := ReadGraphifyRust(graphJSON, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("graphify: %d Rust functions, %d placed on a layer node, %d on more than one; %d calls edges between them, %d INFERRED, %d with an end not placed",
		gf.Functions, gf.Placed, gf.Ambiguous, gf.Edges, gf.Inferred, gf.Unplaced)
	totals := map[string]*RustScore{}
	var c graphifyCount
	for _, rel := range rep.Rust.Workspaces {
		c.workspace(t, g, gf, filepath.Join(rep.Root, rel), rep.Root, totals)
	}
	excluded, written, compared := c.excluded, c.written, c.compared
	t.Logf("%d of rustc's functions named by the layer's function on their line", c.renamed)
	if compared == 0 {
		t.Fatal("no library compiled: nothing measured")
	}
	t.Logf("%d libraries compared; %d of rustc's edges left out (to code outside the crate)", compared, excluded)
	t.Logf("of the rest, %d are calls no source text writes as one (a derive's or a macro's, an operator's): the -written scores leave them out", written)
	for _, tool := range []string{"alidade", "graphify", "graphify-extracted", "alidade-written", "graphify-written"} {
		t.Logf("TOTAL %s", totals[tool])
	}
	if dump := os.Getenv("ALIDADE_GRAPHIFY_RUST_DUMP"); dump != "" {
		var b strings.Builder
		for _, tool := range []string{"alidade", "graphify", "alidade-written", "graphify-written"} {
			for _, e := range totals[tool].Wrong {
				b.WriteString(tool + "\twrong\t" + e + "\n")
			}
			for _, e := range totals[tool].Missed {
				b.WriteString(tool + "\tmissed\t" + e + "\n")
			}
		}
		if err := os.WriteFile(dump, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sum(totals map[string]*RustScore, s *RustScore) {
	t := totals[s.Tool]
	if t == nil {
		t = &RustScore{Tool: s.Tool, Lib: RustLib{Package: "all"}}
		totals[s.Tool] = t
	}
	t.Reference += s.Reference
	t.Found += s.Found
	t.FoundBound += s.FoundBound
	t.Edges += s.Edges
	t.Uncovered += s.Uncovered
	t.FoundUncovered += s.FoundUncovered
	t.Right += s.Right
	t.Bound += s.Bound
	t.Dyn += s.Dyn
	for _, e := range s.Wrong {
		t.Wrong = append(t.Wrong, s.Lib.Package+": "+e)
	}
	for _, e := range s.Missed {
		t.Missed = append(t.Missed, s.Lib.Package+": "+e)
	}
}

// graphifyCount is what TestOracleGraphifyRust counts over the libraries.
type graphifyCount struct{ excluded, written, compared, renamed int }

// workspace scores each library of the Cargo workspace at ws.
func (c *graphifyCount) workspace(t *testing.T, g *graph.Graph, gf *GraphifyRust, ws, root string, totals map[string]*RustScore) {
	layerAt := LayerAt(g)
	libs, err := RustLibs(root, ws)
	if err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("ALIDADE_GRAPHIFY_RUST_TARGET")
	if target == "" {
		target = t.TempDir()
	}
	for _, lib := range libs {
		rc, err := ReadRustCalls(ws, target, lib)
		if err != nil {
			t.Logf("%s: not compared: %v", lib.Package, err)
			continue
		}
		c.compared++
		ref := NewRustReference(g, rc, root)
		src := ref.Written()
		c.excluded += ref.Excluded
		c.renamed += ref.Renamed
		c.written += src.Excluded - ref.Excluded
		scores := []*RustScore{
			ref.Score("alidade", ref.LayerCalls(g)),
			ref.Score("graphify", ref.Named(gf.Calls)),
			ref.Score("graphify-extracted", ref.Named(gf.Extracted)),
			src.Score("alidade-written", src.LayerCalls(g)),
			src.Score("graphify-written", src.Named(gf.Calls)),
		}
		for i, at := range []map[string]map[string]bool{layerAt, gf.At, nil, layerAt, gf.At} {
			if at != nil {
				[]*RustReference{ref, ref, ref, src, src}[i].ScoreUncovered(scores[i], root, at)
			}
		}
		for _, s := range scores {
			t.Logf("%s", s)
			sum(totals, s)
		}
	}
}
