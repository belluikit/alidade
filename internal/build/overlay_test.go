// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// overlayRepo is a module of three packages: a declares an interface and
// calls through it, b implements it, and c calls into a. a's call through
// the interface reaches b, which a load of a alone does not see.
var overlayRepo = map[string]string{
	"go.mod":    "module example.com/w\n\ngo 1.26\n",
	"a/a.go":    "package a\n\ntype Shape interface{ Area() int }\n\nfunc Total(ss []Shape) int {\n\tn := 0\n\tfor _, s := range ss {\n\t\tn += s.Area()\n\t}\n\treturn n\n}\n",
	"b/b.go":    "package b\n\nimport \"example.com/w/a\"\n\ntype Sq struct{}\n\nfunc (Sq) Area() int { return 4 }\n\nfunc Use() int { return a.Total([]a.Shape{Sq{}}) }\n",
	"c/c.go":    "package c\n\nimport \"example.com/w/a\"\n\nfunc Helper() int { return a.Total(nil) }\n",
	"README.md": "# w\n\nSee `Total`.\n",
}

// worktreeOf makes a repository of files and a worktree of it, and
// commits change in the worktree.
func worktreeOf(t *testing.T, files, change map[string]string) (main, wt string) {
	t.Helper()
	base := gittest.TempDir(t)
	main, wt = filepath.Join(base, "main"), filepath.Join(base, "wt")
	gittest.Init(t, main, files)
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "wt", wt)
	gittest.Commit(t, wt, change, "change")
	return main, wt
}

// overlayOf builds the base's graph, then the worktree's laid over it.
func overlayOf(t *testing.T, main, wt string) (*graph.Graph, *Report) {
	t.Helper()
	base := newSession(t, main)
	mustSessionBuild(t, base)
	o, err := base.Overlay(wt)
	if err != nil {
		t.Fatal(err)
	}
	return mustSessionBuild(t, o)
}

// TestOverlayIsAWorktreeBuild pins ADR 005's amendment for #25: a
// worktree's graph laid over the base's is a full build of the worktree,
// node for node — each at the worktree's position — and edge for edge,
// but for the whole-program edges it carries from the base, which say so.
// Here a.Total's call through Shape reaches b.Sq.Area only by way of b,
// which the overlay does not load again.
func TestOverlayIsAWorktreeBuild(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, map[string]string{
		"a/a.go": "package a\n\n// Shape has an area.\ntype Shape interface{ Area() int }\n\n// Only is the worktree's.\nfunc Only() int { return Total(nil) }\n\nfunc Total(ss []Shape) int {\n\tn := 0\n\tfor _, s := range ss {\n\t\tn += s.Area()\n\t}\n\treturn n\n}\n",
	})
	g, rep := overlayOf(t, main, wt)
	full, _, err := Build(wt, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nodeLines(g), nodeLines(full); !slices.Equal(got, want) {
		t.Errorf("nodes differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	got, carriedOnes := edgeLines(g)
	want, _ := edgeLines(full)
	if !slices.Equal(got, want) {
		t.Errorf("edges differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	if !slices.Contains(carriedOnes, "go://example.com/w/a#Total -calls-> go://example.com/w/b#Sq.Area") {
		t.Errorf("a.Total's call through Shape is not carried from the base: carried %v", carriedOnes)
	}
	only, ok := g.Node("go://example.com/w/a#Only")
	if !ok || only.Pos.File != "a/a.go" || only.Pos.Line != 7 {
		t.Errorf("the worktree's own declaration: %+v, want a/a.go:7", only)
	}
	o := rep.Overlay
	if o == nil || o.Base != main || !slices.Equal(o.Packages, []string{"example.com/w/a"}) || o.Carried == 0 || o.GoWhole {
		t.Fatalf("overlay %+v", o)
	}
	if !slices.Contains(o.Inherited, "md") || slices.Contains(o.Inherited, "go") {
		t.Errorf("inherited %v: want md, not go", o.Inherited)
	}
}

// TestOverlayDropsARemovedPackage: a package the worktree deleted has no
// node left, and an edge into it from code the worktree kept is the
// base's no more — c, which called into a, is the worktree's too here.
func TestOverlayDropsARemovedPackage(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, nil)
	gittest.Git(t, wt, "rm", "-q", "c/c.go")
	gittest.Commit(t, wt, nil, "remove c")
	g, rep := overlayOf(t, main, wt)
	if _, ok := g.Node("go://example.com/w/c"); ok {
		t.Error("the removed package is still in the graph")
	}
	if !slices.Equal(rep.Overlay.Removed, []string{"example.com/w/c"}) || len(rep.Overlay.Packages) != 0 {
		t.Errorf("overlay %+v", rep.Overlay)
	}
	full, _, err := Build(wt, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := edgeLines(g)
	want, _ := edgeLines(full)
	if !slices.Equal(got, want) {
		t.Errorf("edges differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
}

// TestOverlayBuildsGoWholeWhenModulesDiffer: a worktree whose go.mod
// differs may require other versions, so no package of it is the base's.
func TestOverlayBuildsGoWholeWhenModulesDiffer(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, map[string]string{"go.mod": "module example.com/w\n\ngo 1.26.0\n"})
	_, rep := overlayOf(t, main, wt)
	if !rep.Overlay.GoWhole || len(rep.Overlay.Packages) != 0 {
		t.Errorf("overlay %+v: want the Go layer built whole", rep.Overlay)
	}
}

// TestOverlayFollowsTheBase: a worktree's graph made again after the
// base's files changed takes the base's new contributions.
func TestOverlayFollowsTheBase(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, nil)
	base := newSession(t, main)
	mustSessionBuild(t, base)
	o, err := base.Overlay(wt)
	if err != nil {
		t.Fatal(err)
	}
	mustSessionBuild(t, o)
	fp, err := o.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(main, "README.md"), "# w\n\nSee `Helper`.\n")
	mustSessionBuild(t, base)
	if fp2, _ := o.Fingerprint(); fp2 == fp {
		t.Error("the worktree's fingerprint did not move with the base's graph")
	}
	_, rep := mustSessionBuild(t, o)
	if !slices.Contains(rep.Rebuilt, "md") {
		t.Errorf("rebuilt %v: the worktree's md layer, taken from the base, is not taken again", rep.Rebuilt)
	}
}

func nodeLines(g *graph.Graph) []string {
	var out []string
	for n := range g.Nodes() {
		out = append(out, fmt.Sprintf("%s %s:%d", n.ID, n.Pos.File, n.Pos.Line))
	}
	sort.Strings(out)
	return out
}

// edgeLines lists a graph's edges with their derivation, a carried one's
// as its full build would have it, and the carried ones apart.
func edgeLines(g *graph.Graph) (all, carried []string) {
	for e := range g.Edges() {
		line := fmt.Sprintf("%s -%s-> %s", e.From, e.Kind, e.To)
		d := e.Derivation
		if e.Attrs["carried"] == "base" {
			carried = append(carried, line)
			d = graph.Resolved
		}
		all = append(all, fmt.Sprintf("%s %v", line, d))
	}
	sort.Strings(all)
	sort.Strings(carried)
	return all, carried
}

func diffLines(got, want []string) string {
	out := ""
	for _, l := range got {
		if !slices.Contains(want, l) {
			out += "+ " + l + "\n"
		}
	}
	for _, l := range want {
		if !slices.Contains(got, l) {
			out += "- " + l + "\n"
		}
	}
	return out
}
