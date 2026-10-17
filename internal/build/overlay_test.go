// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/belluikit/alidade/internal/adapter/py"
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

// overlayEqualsFull builds the base's graph and the worktree's over it,
// and checks the worktree's against a full build of it: node for node,
// and edge for edge but for carried edges.
func overlayEqualsFull(t *testing.T, main, wt string) *Report {
	t.Helper()
	g, rep := overlayOf(t, main, wt)
	full, _, err := Build(wt, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nodeLines(g), nodeLines(full); !slices.Equal(got, want) {
		t.Errorf("nodes differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	got, _ := edgeLines(g)
	want, _ := edgeLines(full)
	if !slices.Equal(got, want) {
		t.Errorf("edges differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	return rep
}

// with is overlayRepo with files added or replaced.
func with(files map[string]string) map[string]string {
	out := maps.Clone(overlayRepo)
	maps.Copy(out, files)
	return out
}

// TestOverlayBuildsGoWholeForAPackageTheHostDoesNotBuild: a load naming a
// package whose files only a build tag brings in fails its whole module,
// which a load of every package does not, so a worktree that changed one
// builds its Go layer whole — here a's changes are in the graph too.
func TestOverlayBuildsGoWholeForAPackageTheHostDoesNotBuild(t *testing.T) {
	tools := "//go:build tools\n\npackage tools\n\nimport _ \"example.com/w/a\"\n"
	main, wt := worktreeOf(t, with(map[string]string{"tools/tools.go": tools}), map[string]string{
		"tools/tools.go": tools + "\n// changed\n",
		"a/a.go":         "package a\n\ntype Shape interface{ Area() int }\n\nfunc Only() int { return 1 }\n\nfunc Total(ss []Shape) int { return 0 }\n",
	})
	if rep := overlayEqualsFull(t, main, wt); !rep.Overlay.GoWhole {
		t.Errorf("overlay %+v: want the Go layer built whole", rep.Overlay)
	}
}

// TestOverlayDropsAnExternalPackageNoLongerImported: an external package
// only a package loaded again imported is not kept from the base when the
// worktree no longer imports it; one an unchanged package imports is.
func TestOverlayDropsAnExternalPackageNoLongerImported(t *testing.T) {
	main, wt := worktreeOf(t, with(map[string]string{
		"c/c.go": "package c\n\nimport (\n\t_ \"strings\"\n\n\t\"example.com/w/a\"\n)\n\nfunc Helper() int { return a.Total(nil) }\n",
		"b/x.go": "package b\n\nimport _ \"bytes\"\n",
	}), map[string]string{
		"c/c.go": "package c\n\nimport \"example.com/w/a\"\n\nfunc Helper() int { return a.Total(nil) }\n",
	})
	overlayEqualsFull(t, main, wt)
}

// TestOverlayMissesAnImplementationCalledFromUnchangedCode pins the
// divergence ADR 005's amendment for #25 names: a package the worktree
// did not change has the base's edges, so a call through an interface
// from it does not reach an implementation the worktree added elsewhere.
// A full build of the worktree has the edge. The day the overlay finds
// it, this fails, and the ADR and docs/usage.md should say so.
func TestOverlayMissesAnImplementationCalledFromUnchangedCode(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, map[string]string{
		"b/b.go": overlayRepo["b/b.go"] + "\ntype Tri struct{}\n\nfunc (Tri) Area() int { return 3 }\n\nfunc Use3() int { return a.Total([]a.Shape{Tri{}}) }\n",
	})
	g, _ := overlayOf(t, main, wt)
	full, _, err := Build(wt, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	const tri = "go://example.com/w/a#Total -calls-> go://example.com/w/b#Tri.Area"
	got, _ := edgeLines(g)
	want, _ := edgeLines(full)
	if !slices.Contains(want, tri+" RESOLVED") {
		t.Fatalf("the full build lacks %s: the fixture no longer shows the divergence", tri)
	}
	if slices.Contains(got, tri+" RESOLVED") {
		t.Errorf("the overlay has %s: update ADR 005 and docs/usage.md, the divergence is gone", tri)
	}
	if want = slices.DeleteFunc(want, func(l string) bool { return l == tri+" RESOLVED" }); !slices.Equal(got, want) {
		t.Errorf("edges differ from a full build of the worktree by more than %s:\n%s", tri, diffLines(got, want))
	}
}

// TestOverlayExtractsItsOwnLayerOnlyWhenItsFilesChange: a layer of the
// worktree's own is not extracted again because the base's files for it
// changed, and is taken from the base once its files are the base's.
func TestOverlayExtractsItsOwnLayerOnlyWhenItsFilesChange(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, map[string]string{"README.md": "# w\n\nSee `Helper`.\n"})
	base := newSession(t, main)
	mustSessionBuild(t, base)
	o, err := base.Overlay(wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, rep := mustSessionBuild(t, o); slices.Contains(rep.Overlay.Inherited, "md") {
		t.Fatalf("inherited %v: the worktree's md differs from the base's", rep.Overlay.Inherited)
	}
	write(t, filepath.Join(main, "README.md"), "# w\n\nSee `Use`.\n")
	mustSessionBuild(t, base)
	if _, rep := mustSessionBuild(t, o); slices.Contains(rep.Rebuilt, "md") {
		t.Errorf("rebuilt %v: the worktree's own md layer was extracted again for the base's edit", rep.Rebuilt)
	}
	write(t, filepath.Join(main, "README.md"), "# w\n\nSee `Helper`.\n")
	mustSessionBuild(t, base)
	if _, rep := mustSessionBuild(t, o); !slices.Contains(rep.Overlay.Inherited, "md") {
		t.Errorf("inherited %v: the worktree's md is the base's again", rep.Overlay.Inherited)
	}
}

// TestOverlayWithoutGoModTakesNoGoLayer: a worktree whose go.mod went
// away after its Go layer was taken from the base says it has none.
func TestOverlayWithoutGoModTakesNoGoLayer(t *testing.T) {
	main, wt := worktreeOf(t, overlayRepo, nil)
	base := newSession(t, main)
	mustSessionBuild(t, base)
	o, err := base.Overlay(wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, rep := mustSessionBuild(t, o); !slices.Contains(rep.Overlay.Inherited, "go") {
		t.Fatalf("inherited %v: want go", rep.Overlay.Inherited)
	}
	if err := os.Remove(filepath.Join(wt, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, rep := mustSessionBuild(t, o); rep.Go != nil || slices.Contains(rep.Overlay.Inherited, "go") {
		t.Errorf("without go.mod: Go report %v, inherited %v", rep.Go != nil, rep.Overlay.Inherited)
	}
}

// TestOwnLiveIsNeverTheBases: a worktree's session stops or closes only
// a jedi it started; one its Python layer took from the base is the
// base's.
func TestOwnLiveIsNeverTheBases(t *testing.T) {
	l := &py.Live{}
	prev := &Report{Py: &py.Report{Live: l}}
	s := &Session{from: map[string]string{"py": "base"}}
	if got := s.ownLive(prev); got != nil {
		t.Error("the base's jedi is taken for the worktree's own")
	}
	s.from["py"] = "own"
	if got := s.ownLive(prev); got != l {
		t.Error("the worktree's own jedi is not its own")
	}
}

// TestOverlaySkipsAreAWorktreeBuilds is #35: the files a worktree's Go
// layer skips are a full build's of it. A package loaded again has its
// own skips in place of the base's: here a //line directive naming a
// source outside the tree (#109), gone from one package and added to
// another.
func TestOverlaySkipsAreAWorktreeBuilds(t *testing.T) {
	files := maps.Clone(overlayRepo)
	files["a/gen.go"] = "package a\n\n//line /outside/gen.y:1\nfunc Gen() int { return 1 }\n"
	main, wt := worktreeOf(t, files, map[string]string{
		"a/gen.go": "package a\n\nfunc Gen() int { return 1 }\n",
		"c/gen.go": "package c\n\n//line /outside/gen.y:1\nfunc Gen() int { return 1 }\n",
	})
	_, rep := overlayOf(t, main, wt)
	if rep.Overlay == nil || rep.Overlay.GoWhole {
		t.Fatalf("not laid over the base: %+v", rep.Overlay)
	}
	_, full, err := Build(wt, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Go.Skipped, full.Go.Skipped) || len(full.Go.Skipped) != 1 {
		t.Errorf("skipped %+v, a full build of the worktree %+v", rep.Go.Skipped, full.Go.Skipped)
	}
}
