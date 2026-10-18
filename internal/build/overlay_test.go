// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
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
	return overlayWith(t, main, wt, DefaultOptions())
}

func overlayWith(t *testing.T, main, wt string, opts Options) (*graph.Graph, *Report) {
	t.Helper()
	base, err := NewSession(main, opts)
	if err != nil {
		t.Fatal(err)
	}
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
		out = append(out, fmt.Sprintf("%s %s:%d", n.ID, n.Pos.File, n.Pos.Line)+buildOf(n.Attrs))
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
		all = append(all, fmt.Sprintf("%s %v", line, d)+buildOf(e.Attrs))
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
	_, rep := overlayEqualsFullWith(t, main, wt, DefaultOptions())
	return rep
}

func overlayEqualsFullWith(t *testing.T, main, wt string, opts Options) (*graph.Graph, *Report) {
	t.Helper()
	g, rep := overlayWith(t, main, wt, opts)
	full, frep, err := Build(wt, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSkips(rep.Go.Skipped, frep.Go.Skipped) {
		t.Errorf("skipped %+v, a full build of the worktree %+v", rep.Go.Skipped, frep.Go.Skipped)
	}
	if got, want := nodeLines(g), nodeLines(full); !slices.Equal(got, want) {
		t.Errorf("nodes differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	got, _ := edgeLines(g)
	want, _ := edgeLines(full)
	if !slices.Equal(got, want) {
		t.Errorf("edges differ from a full build of the worktree:\n%s", diffLines(got, want))
	}
	return g, rep
}

// with is overlayRepo with files added or replaced.
func with(files map[string]string) map[string]string {
	out := maps.Clone(overlayRepo)
	maps.Copy(out, files)
	return out
}

// TestOverlayOfAPackageTheHostDoesNotBuild is #35, the owner's decision:
// a load naming a package whose files only a build tag brings in fails
// its whole module, so the host's load leaves it out. The worktree's
// graph is still laid over the base's, and equals a full build of it,
// a's changes and the tag-only package's skip both.
func TestOverlayOfAPackageTheHostDoesNotBuild(t *testing.T) {
	tools := "//go:build tools\n\npackage tools\n\nimport _ \"example.com/w/a\"\n"
	main, wt := worktreeOf(t, with(map[string]string{"tools/tools.go": tools}), map[string]string{
		"tools/tools.go": tools + "\n// changed\n",
		"a/a.go":         overlayRepo["a/a.go"] + "\nfunc Only() int { return 1 }\n",
	})
	rep := overlayEqualsFull(t, main, wt)
	if o := rep.Overlay; o.GoWhole || !slices.Equal(o.Packages, []string{"example.com/w/a", "example.com/w/tools"}) {
		t.Errorf("overlay %+v: want a and tools laid over the base", o)
	}
}

// TestOverlayOfAnotherPlatformsPackage is #35 for a package only another
// platform builds: alone among the worktree's changes, so the host loads
// nothing of its module, and beside one it does build. With -go-configs
// auto it is read under the configuration a full build reads it under,
// and without, it is skipped as a full build skips it.
func TestOverlayOfAnotherPlatformsPackage(t *testing.T) {
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	file := "plat/plat_" + other + ".go"
	files := with(map[string]string{file: "package plat\n\nfunc P() int { return 1 }\n"})
	changes := map[string]map[string]string{
		"alone":  {file: "package plat\n\nfunc P() int { return 2 }\n\nfunc Q() int { return P() }\n"},
		"beside": {file: "package plat\n\nfunc Q() int { return 2 }\n", "c/c.go": overlayRepo["c/c.go"] + "\nfunc More() int { return 0 }\n"},
	}
	if other == "windows" {
		// The base's build reads plat under windows/arm64, which its
		// greedy choice takes for y as well; plat alone would be read
		// under windows/amd64, the first candidate building it.
		files["y/y_windows_arm64.go"] = "package y\n\nfunc Y() int { return 1 }\n"
	}
	for name, change := range changes {
		for _, configs := range []string{"", "auto"} {
			t.Run(name+"/"+configs, func(t *testing.T) {
				main, wt := worktreeOf(t, files, change)
				opts := DefaultOptions()
				opts.Go.Configs = configs
				g, rep := overlayEqualsFullWith(t, main, wt, opts)
				if o := rep.Overlay; o.GoWhole || !slices.Contains(o.Packages, "example.com/w/plat") {
					t.Errorf("overlay %+v: want plat laid over the base", o)
				}
				_, read := g.Node("go://example.com/w/plat#Q")
				skipped := slices.ContainsFunc(rep.Go.Skipped, func(s graph.Skip) bool { return s.File == "plat" })
				if read != (configs == "auto") || skipped == read {
					t.Errorf("plat read %v, skipped %v: want it read under auto and skipped otherwise", read, skipped)
				}
			})
		}
	}
}

// TestOverlayBuildsWholeWhenAutoWouldChooseOtherwise pins #39: under
// -go-configs auto the base's choice of configurations is made from the
// whole module, and a worktree whose files would choose differently —
// another configuration, the same ones in another order, one fewer — is
// built whole, as only that reads its unchanged packages under its own
// choice. A worktree whose files choose the same is still laid over the
// base. The fixtures name Windows configurations only, which no host CI
// runs on builds.
func TestOverlayBuildsWholeWhenAutoWouldChooseOtherwise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures' configurations are the host's")
	}
	file := func(pkg, name string) string { return "package " + pkg + "\n\nfunc " + name + "() int { return 1 }\n" }
	// windows/amd64 brings in three files, v among them, and is chosen
	// first; windows/arm64 then brings in w's one.
	ordered := with(map[string]string{
		"u/u.go":                file("u", "U"),
		"u/v.go":                "//go:build windows\n\n" + file("u", "V"),
		"l/l1_windows_amd64.go": file("l", "L1"),
		"l/l2_windows_amd64.go": file("l", "L2"),
		"w/w.go":                file("w", "W0"),
		"w/w_windows_arm64.go":  file("w", "W"),
	})
	tests := DefaultOptions()
	tests.Go.Tests, tests.Go.ExactTests = true, true
	for _, tt := range []struct {
		name   string
		files  map[string]string
		change map[string]string
		opts   Options
		whole  bool
	}{
		{"another configuration", with(nil), map[string]string{"plat/plat_windows.go": file("plat", "Q")}, DefaultOptions(), true},
		// w's two more files make windows/arm64 bring in four, v among
		// them: it is chosen first, and v is credited to it.
		{"another order", ordered, map[string]string{"w/w2_windows_arm64.go": file("w", "W2"), "w/w3_windows_arm64.go": file("w", "W3")}, DefaultOptions(), true},
		{"one fewer", with(map[string]string{"plat/p.go": file("plat", "P"), "plat/plat_windows.go": file("plat", "Q")}),
			map[string]string{"plat/plat_windows.go": "//go:build ignore\n\npackage plat\n"}, DefaultOptions(), true},
		// The load without tests chooses windows/arm64 alone; c's file
		// makes it windows/amd64 first, while the load with tests, whose
		// five test files already chose windows/amd64 first, is unmoved.
		{"another choice without tests", with(map[string]string{
			"u/u.go": file("u", "U"), "b2/b_windows_arm64.go": file("b2", "B"),
			"u/t1_windows_amd64_test.go": file("u", "T1"), "u/t2_windows_amd64_test.go": file("u", "T2"),
			"u/t3_windows_amd64_test.go": file("u", "T3"), "u/t4_windows_amd64_test.go": file("u", "T4"),
		}), map[string]string{"c/c_windows_amd64.go": file("c", "CW")}, tests, true},
		{"the same choice", ordered, map[string]string{"l/l3_windows_amd64.go": file("l", "L3")}, DefaultOptions(), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			main, wt := worktreeOf(t, tt.files, tt.change)
			opts := tt.opts
			opts.Go.Configs = "auto"
			_, rep := overlayEqualsFullWith(t, main, wt, opts)
			if rep.Overlay.GoWhole != tt.whole {
				t.Errorf("overlay %+v: want GoWhole %v", rep.Overlay, tt.whole)
			}
			_, full, err := Build(wt, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(rep.Go.Configs, full.Go.Configs) || !slices.Equal(rep.Go.HostExcluded, full.Go.HostExcluded) {
				t.Errorf("configs %v excluded %v, a full build's %v %v", rep.Go.Configs, rep.Go.HostExcluded, full.Go.Configs, full.Go.HostExcluded)
			}
		})
	}
}

// TestOverlayBuildsWholeWhenAModuleDoesNotLoad pins #40: whether a module
// loads for a configuration is a fact of all its packages, as caliper
// refuses a load only when none of them type-checks, so a load of the
// packages that differ cannot tell it. A worktree changing a module the
// base's build could not load for a configuration is built whole, and so
// is one whose load of its differing packages fails, for the host or a
// further configuration, where a whole module's would not. A worktree
// changing another module is still laid over the base.
func TestOverlayBuildsWholeWhenAModuleDoesNotLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures' configuration is the host's")
	}
	cgo := func(pkg, name string) string {
		return "package " + pkg + "\n\nimport \"C\"\n\nfunc " + name + "() int { return 2 }\n"
	}
	plain := func(pkg, name string) string { return "package " + pkg + "\n\nfunc " + name + "() int { return 2 }\n" }
	// m2 does not load for windows/amd64: each of its packages imports C
	// there, which the load does not build.
	m2 := with(map[string]string{
		"m2/go.mod":         "module example.com/m2\n\ngo 1.26\n",
		"m2/p/p.go":         plain("p", "P"),
		"m2/p/p_windows.go": cgo("p", "PW"),
		"m2/r/r.go":         plain("r", "R"),
		"m2/r/r_windows.go": cgo("r", "RW"),
	})
	// The root module loads for windows/amd64, though p alone does not.
	lone := with(map[string]string{"p/p.go": plain("p", "P"), "p/p_windows.go": cgo("p", "PW")})
	for _, tt := range []struct {
		name          string
		files, change map[string]string
		configs       string
		whole         bool
	}{
		{"fixed whole", m2, map[string]string{"m2/p/p_windows.go": plain("p", "PW"), "m2/r/r_windows.go": plain("r", "RW")}, "auto", true},
		// p, unchanged, is then read for windows/amd64.
		{"fixed in part", m2, map[string]string{"m2/r/r_windows.go": plain("r", "RW")}, "auto", true},
		{"still failing", m2, map[string]string{"m2/r/r.go": plain("r", "R") + "\nfunc R2() int { return 1 }\n"}, "auto", true},
		{"another module", m2, map[string]string{"c/c.go": overlayRepo["c/c.go"] + "\nfunc More() int { return 0 }\n"}, "auto", false},
		{"alone for a further configuration", lone, map[string]string{"p/p.go": plain("p", "P") + "\nfunc P2() int { return 1 }\n"}, "auto", true},
		{"alone for the host", with(nil), map[string]string{"c/c.go": overlayRepo["c/c.go"] + "\nfunc Y() int { return nope }\n"}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			main, wt := worktreeOf(t, tt.files, tt.change)
			opts := DefaultOptions()
			opts.Go.Configs = tt.configs
			_, rep := overlayEqualsFullWith(t, main, wt, opts)
			if rep.Overlay.GoWhole != tt.whole {
				t.Errorf("overlay %+v: want GoWhole %v", rep.Overlay, tt.whole)
			}
		})
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
// another. A directory below a package loaded again keeps the base's
// skip: a/tagged, which only a build tag brings in, as src/runtime/asan
// is below src/runtime in the Go repository.
func TestOverlaySkipsAreAWorktreeBuilds(t *testing.T) {
	files := maps.Clone(overlayRepo)
	files["a/gen.go"] = "package a\n\n//line /outside/gen.y:1\nfunc Gen() int { return 1 }\n"
	files["a/tagged/t.go"] = "//go:build tools\n\npackage tagged\n"
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
	if !sameSkips(rep.Go.Skipped, full.Go.Skipped) || len(full.Go.Skipped) != 2 {
		t.Errorf("skipped %+v, a full build of the worktree %+v", rep.Go.Skipped, full.Go.Skipped)
	}
}

// sameSkips reports whether two builds skipped the same files, in
// whatever order: a full build lists them as its passes met them, a
// worktree's laid over the base's by file.
func sameSkips(a, b []graph.Skip) bool {
	by := func(x, y graph.Skip) int {
		return cmp.Or(strings.Compare(x.File, y.File), strings.Compare(x.Reason, y.Reason))
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, by)
	slices.SortFunc(b, by)
	return slices.Equal(a, b)
}

// buildOf renders the configuration a node or edge was read under beyond
// the host's, "" for the host's.
func buildOf(attrs map[string]string) string {
	if c := attrs["build"]; c != "" {
		return " build=" + c
	}
	return ""
}

// TestOverlaidSkipsNameTheWorktree: a skip kept from the base whose
// reason names a path of the base's, as a module's load failure does,
// names the worktree's, as a full build of it would (#35).
func TestOverlaidSkipsNameTheWorktree(t *testing.T) {
	base, wt := filepath.Join("/x", "go"), filepath.Join("/x", "go-wt")
	at := func(root string) string { return filepath.Join(root, "misc", "gmp.go") }
	got := overlaidSkips([]graph.Skip{{File: "misc/go.mod", Reason: "load " + at(base) + ": no C"}}, nil, nil, base, wt)
	if len(got) != 1 || got[0].Reason != "load "+at(wt)+": no C" {
		t.Errorf("skips %+v, want the reason to name %s", got, at(wt))
	}
	// The root module's load error names the root itself, with nothing
	// below it; a sibling whose name only starts with the root's is not it.
	got = overlaidSkips([]graph.Skip{{File: "go.mod", Reason: "load " + base + ": no C; " + base + "-old"}}, nil, nil, base, wt)
	if want := "load " + wt + ": no C; " + base + "-old"; len(got) != 1 || got[0].Reason != want {
		t.Errorf("skips %+v, want the reason %q", got, want)
	}
}

// TestHostExcludedIsTheWorktrees: a directory loaded again gives its own
// list in place of the base's, so a file it no longer excludes, or no
// longer has, is not the base's to keep (#39).
func TestHostExcludedIsTheWorktrees(t *testing.T) {
	got := hostExcluded([]string{"a/gone_windows.go", "a/kept_windows.go", "b/y_windows.go"}, []string{"a/kept_windows.go", "a/new_windows.go"}, []string{"a/gone_windows.go", "a/new_windows.go"})
	if want := []string{"a/kept_windows.go", "a/new_windows.go", "b/y_windows.go"}; !slices.Equal(got, want) {
		t.Errorf("hostExcluded = %q, want %q", got, want)
	}
}
