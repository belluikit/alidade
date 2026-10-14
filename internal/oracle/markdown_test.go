// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// The committed sample reads: every repository pinned to a full commit,
// every edge labelled, and every wrong one saying why.
func TestMarkdownSampleLoads(t *testing.T) {
	for file, want := range map[string][2]int{"markdown-sample.pxf": {10, 1}, "markdown-bareword-sample.pxf": {4, 1}} {
		edges, wrong := checkSample(t, file)
		if edges != want[0] || wrong != want[1] {
			t.Errorf("%s: %d edges, %d wrong; want %d and %d", file, edges, wrong, want[0], want[1])
		}
	}
}

func checkSample(t *testing.T, file string) (edges, wrong int) {
	t.Helper()
	samples, err := LoadSample("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range samples {
		if len(s.Commit) != 40 {
			t.Errorf("%s: commit %q", name, s.Commit)
		}
		for _, l := range s.Edges {
			edges++
			if l.From == "" || l.To == "" || l.Mention == "" {
				t.Errorf("%s: incomplete %+v", name, l)
			}
			if !l.Right {
				wrong++
				if l.Why == "" {
					t.Errorf("%s: wrong edge %s with no why", name, l.To)
				}
			}
		}
	}
	return edges, wrong
}

// CompareSample sorts each labelled edge by whether the graph still has
// it: a right one kept, a wrong one kept, a right one gone (a
// regression) and a wrong one gone (a fix).
func TestCompareSample(t *testing.T) {
	b := graph.NewBuilder(t.TempDir())
	for _, n := range []graph.Node{
		{ID: "md://a.md#s", Kind: graph.KindSection, Name: "s"},
		{ID: "go://m/p#Kept", Kind: graph.KindFunc, Name: "Kept"},
		{ID: "go://m/p#Wrong", Kind: graph.KindFunc, Name: "Wrong"},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, to := range []graph.ID{"go://m/p#Kept", "go://m/p#Wrong"} {
		if err := b.AddEdge(graph.Edge{From: "md://a.md#s", To: to, Kind: graph.KindDocuments, Derivation: graph.Inferred}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Sample{Edges: []Labelled{
		{From: "md://a.md#s", To: "go://m/p#Kept", Mention: "Kept", Right: true},
		{From: "md://a.md#s", To: "go://m/p#Wrong", Mention: "Wrong", Why: "a word"},
		{From: "md://a.md#s", To: "go://m/p#Lost", Mention: "Lost", Right: true},
		{From: "md://a.md#s", To: "go://m/p#Fixed", Mention: "Fixed", Why: "a word"},
	}}
	r := CompareSample(b.Build(), s)
	if r.Right != 1 || r.Wrong != 1 || len(r.RightGone) != 1 || len(r.WrongGone) != 1 || len(r.WrongKept) != 1 {
		t.Errorf("result %+v", r)
	}
}
