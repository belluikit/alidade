// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// moduleView renders the module's own part of a Go layer graph — every
// node that is not an external package, and every edge whose target is
// not one — as sorted lines, for comparing two loads.
func moduleView(g *graph.Graph) []string {
	external := map[graph.ID]bool{}
	for n := range g.Nodes() {
		if n.Attrs["external"] == "true" {
			external[n.ID] = true
		}
	}
	var lines []string
	for n := range g.Nodes() {
		if external[n.ID] {
			continue
		}
		lines = append(lines, "node "+string(n.ID)+" "+string(n.Kind))
		for _, e := range g.Out(n.ID) {
			if !external[e.To] {
				lines = append(lines, "edge "+string(e.From)+" "+string(e.Kind)+" "+string(e.To)+" "+e.Derivation.String())
			}
		}
	}
	sort.Strings(lines)
	return lines
}

func extractWith(t *testing.T, root string, export bool) (*graph.Graph, *Report) {
	t.Helper()
	opts := DefaultOptions()
	opts.DepsFromExportData = export
	b := graph.NewBuilder(root)
	rep, err := Extract(b, root, opts)
	if err != nil {
		t.Fatalf("DepsFromExportData=%v: %v", export, err)
	}
	return b.Build(), rep
}

// TestExportDataKeepsTheModuleGraph pins #58's claim: with dependencies'
// types from export data, everything the layer says about the module's
// own code — nodes, contains, calls, implements, imports, references —
// is identical to a source load. Only the boundary may differ.
func TestExportDataKeepsTheModuleGraph(t *testing.T) {
	root := fixtureDir(t)
	src, srcRep := extractWith(t, root, false)
	exp, expRep := extractWith(t, root, true)
	a, b := moduleView(src), moduleView(exp)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("module graph differs:\nsource load %d lines, export data %d lines", len(a), len(b))
		for i := range a {
			if i >= len(b) || a[i] != b[i] {
				t.Errorf("first difference at %d:\n  %s\n  %s", i, a[i], at(b, i))
				break
			}
		}
	}
	if srcRep.Decls != expRep.Decls || srcRep.Calls != expRep.Calls || srcRep.Packages != expRep.Packages {
		t.Errorf("reports differ: %+v vs %+v", srcRep, expRep)
	}
	if len(exp.Dropped()) != 0 {
		t.Errorf("export data load dropped %d edges", len(exp.Dropped()))
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<end>"
}

// TestExportDataOnRealRepository is the same pin over ALIDADE_REPO, with
// the numbers logged; skipped when unset.
func TestExportDataOnRealRepository(t *testing.T) {
	root := os.Getenv("ALIDADE_REPO")
	if root == "" {
		t.Skip("ALIDADE_REPO not set")
	}
	src, srcRep := extractWith(t, root, false)
	exp, expRep := extractWith(t, root, true)
	t.Logf("source:      load %s callgraph %s emit %s boundary %d", srcRep.Load, srcRep.CallGraph, srcRep.Emit, srcRep.Boundary)
	t.Logf("export data: load %s callgraph %s emit %s boundary %d", expRep.Load, expRep.CallGraph, expRep.Emit, expRep.Boundary)
	if a, b := moduleView(src), moduleView(exp); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("module graph differs: %d vs %d lines", len(a), len(b))
	}
}
