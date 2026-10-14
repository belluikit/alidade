// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/adapter/golang"
	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
)

// TestRebuildSeesEditsUnderBuildDirs pins #93: "build" and "dist" are the
// TS layer's to skip, not every layer's, so a Go edit under internal/build/
// — alidade's own layout — reruns the Go layer.
func TestRebuildSeesEditsUnderBuildDirs(t *testing.T) {
	for _, dir := range []string{"build", "dist"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
			write(t, filepath.Join(root, "internal", dir, "b.go"), "package b\n\nfunc Foo() {}\n")
			s := newSession(t, root)
			mustSessionBuild(t, s)
			write(t, filepath.Join(root, "internal", dir, "b.go"), "package b\n\nfunc Foo() {}\n\nfunc Bar() {}\n")
			g, rep := mustSessionBuild(t, s)
			if !slices.Contains(rep.Rebuilt, "go") {
				t.Errorf("rebuilt %v kept %v; want go rebuilt", rep.Rebuilt, rep.Kept)
			}
			if _, ok := g.Node(graph.ID("go://example.com/m/internal/" + dir + "#Bar")); !ok {
				t.Error("the new function is not in the graph")
			}
			sameAsFresh(t, root, g)
		})
	}
}

// TestRebuildSeesCrossLayerReads pins #93: the Markdown layer resolves a
// link to code by what exists and by go.mod, so a package appearing under
// a linked directory, or a module renamed, reruns it.
func TestRebuildSeesCrossLayerReads(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(t, filepath.Join(root, "m.go"), "package m\n")
	write(t, filepath.Join(root, "README.md"), "# Hi\n\nSee [the package](pkg/).\n")
	s := newSession(t, root)
	mustSessionBuild(t, s)

	write(t, filepath.Join(root, "pkg", "p.go"), "package pkg\n\nfunc P() {}\n")
	g, rep := mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "md") {
		t.Errorf("new package: rebuilt %v kept %v; want md rebuilt", rep.Rebuilt, rep.Kept)
	}
	if !linked(g, "md://README.md#hi", "go://example.com/m/pkg") {
		t.Error("new package: README does not reach it")
	}
	sameAsFresh(t, root, g)

	write(t, filepath.Join(root, "go.mod"), "module example.com/renamed\n\ngo 1.26\n")
	g, rep = mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "md") {
		t.Errorf("module rename: rebuilt %v kept %v; want md rebuilt", rep.Rebuilt, rep.Kept)
	}
	if !linked(g, "md://README.md#hi", "go://example.com/renamed/pkg") {
		t.Error("module rename: README does not reach the renamed package")
	}
	sameAsFresh(t, root, g)
}

// TestFailedRebuildIsRerun pins #93: a layer dropped by a build that then
// failed is rerun by the next one, even when its files are back to what
// the last good build signed — it is not kept with nothing in it. No
// input fails a layer since #92, so the failure is injected.
func TestFailedRebuildIsRerun(t *testing.T) {
	root := t.TempDir()
	good := "package m\n\nfunc F() {}\n"
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(t, filepath.Join(root, "m.go"), good)
	s := newSession(t, root)
	mustSessionBuild(t, s)

	write(t, filepath.Join(root, "m.go"), "package m\n\nfunc F() {}\n\nfunc G() {}\n")
	t.Cleanup(func() { extractGo = golang.Extract })
	extractGo = func(*graph.Builder, string, golang.Options) (*golang.Report, error) {
		return nil, errors.New("injected")
	}
	_, _, err := s.Build()
	extractGo = golang.Extract
	if err == nil {
		t.Fatal("the injected failure built")
	}
	write(t, filepath.Join(root, "m.go"), good)
	g, rep := mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "go") {
		t.Errorf("rebuilt %v kept %v; want go rerun after its failure", rep.Rebuilt, rep.Kept)
	}
	if _, ok := g.Node("go://example.com/m#F"); !ok {
		t.Error("F is missing: the failed layer was kept empty")
	}
	sameAsFresh(t, root, g)
}

// TestRebuildSeesExtensionlessPaths pins #93: the Markdown layer resolves
// a link by whether its path is on disk, and a path with no extension is
// one it reads (markdown.pathNode's case ""), so a Makefile appearing
// reruns it.
func TestRebuildSeesExtensionlessPaths(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(t, filepath.Join(root, "m.go"), "package m\n")
	write(t, filepath.Join(root, "README.md"), "# Hi\n\nSee [the build file](Makefile).\n")
	s := newSession(t, root)
	g, _ := mustSessionBuild(t, s)
	// Gone from a directory that holds Go: the link is minted as the
	// package it would be and drops, which is the state to leave behind.
	if !dropped(g, "md://README.md#hi", "go://example.com/m/Makefile") {
		t.Fatal("a link to a missing Makefile does not drop as the package it would be")
	}

	write(t, filepath.Join(root, "Makefile"), "all:\n\t@true\n")
	g, rep := mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "md") {
		t.Errorf("rebuilt %v kept %v; want md rebuilt", rep.Rebuilt, rep.Kept)
	}
	if dropped(g, "md://README.md#hi", "go://example.com/m/Makefile") {
		t.Error("the Makefile is there now: the link must no longer drop as a package")
	}
	sameAsFresh(t, root, g)
}

// TestFailedBuildAfterTheGoLayerWentAwayIsRerun pins #93 case 3 on the
// drop no signature decides: a go.mod that went away takes the Go layer
// with it, and a build that fails after that must leave the layer stale
// like any other drop, or the next build keeps it with nothing in it.
func TestFailedBuildAfterTheGoLayerWentAwayIsRerun(t *testing.T) {
	root, schemas := t.TempDir(), t.TempDir()
	x, y := filepath.Join(schemas, "x"), filepath.Join(schemas, "y")
	link := filepath.Join(schemas, "link")
	mkdirs(t, x, y)
	relink(t, y, link)
	gomod, mod := filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n"
	write(t, gomod, mod)
	write(t, filepath.Join(root, "m.go"), "package m\n\nfunc F() {}\n")
	opts := DefaultOptions()
	opts.SchemaRoots = []string{x, link}
	s, err := NewSession(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	mustSessionBuild(t, s)

	// The Go layer goes away, and the build fails after it was dropped:
	// the second schema root now resolves to the first's label.
	if err := os.Remove(gomod); err != nil {
		t.Fatal(err)
	}
	relink(t, x, link)
	if _, _, err := s.Build(); err == nil {
		t.Fatal("two schema roots under one label built")
	}

	// Both are back to what the last good build signed.
	write(t, gomod, mod)
	relink(t, y, link)
	g, rep := mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "go") {
		t.Errorf("rebuilt %v kept %v; want go rerun after the failed build dropped it", rep.Rebuilt, rep.Kept)
	}
	if _, ok := g.Node("go://example.com/m#F"); !ok {
		t.Error("F is missing: the dropped layer was kept empty")
	}
	sameAsFresh(t, root, g)
}

// TestSchemaRootRegisteredOnce pins #93 case 4, through Build rather than
// the helper: a build that failed after registering a schema root's label
// leaves the Session able to build, and two roots under one label are
// refused by the build itself.
func TestSchemaRootRegisteredOnce(t *testing.T) {
	root, schemas := t.TempDir(), t.TempDir()
	x, y := filepath.Join(schemas, "x"), filepath.Join(schemas, "y")
	link := filepath.Join(schemas, "link")
	mkdirs(t, x, y)
	relink(t, x, link) // both roots are "x": the first build fails
	opts := DefaultOptions()
	opts.SchemaRoots = []string{x, link}
	s, err := NewSession(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Build()
	if err == nil {
		t.Fatal("two schema roots under one label built")
	}
	if !strings.Contains(err.Error(), `label "x" used twice`) {
		t.Errorf("first build failed with %v; want the duplicate label refused", err)
	}

	// The label "x" was registered before that failure; registering it
	// again would refuse every later build.
	relink(t, y, link)
	if _, _, err := s.Build(); err != nil {
		t.Fatalf("build after the failed first one: %v", err)
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// relink points link at target, replacing it.
func relink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func newSession(t *testing.T, root string) *Session {
	t.Helper()
	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustSessionBuild(t *testing.T, s *Session) (*graph.Graph, *Report) {
	t.Helper()
	g, rep, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	return g, rep
}

// dropped reports whether an edge from from to to was minted and could
// not be resolved — a Markdown link to a path that is not there.
func dropped(g *graph.Graph, from, to string) bool {
	for _, d := range g.Dropped() {
		if d.Edge.From == graph.ID(from) && d.Edge.To == graph.ID(to) {
			return true
		}
	}
	return false
}

func linked(g *graph.Graph, from, to string) bool {
	for _, e := range g.Out(graph.ID(from)) {
		if e.To == graph.ID(to) {
			return true
		}
	}
	return false
}

// TestRebuildPython pins the Python layer's place in a rebuild (#237): a
// .py edit reruns it and keeps the Go layer, and a Markdown link to a
// Python file reaches the file's node. Built with the python tag, the new
// function is in the graph with its call; without it, the file is.
func TestRebuildPython(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(t, filepath.Join(root, "m.go"), "package m\n")
	write(t, filepath.Join(root, "tool.py"), "def f():\n    pass\n")
	write(t, filepath.Join(root, "README.md"), "# Hi\n\nSee [the tool](tool.py).\n")
	s := newSession(t, root)
	g, rep := mustSessionBuild(t, s)
	if !slices.Contains(rep.Layers(), "py") || rep.Py.Files != 1 {
		t.Fatalf("layers %v; want py with one file", rep.Layers())
	}
	if !linked(g, "md://README.md#hi", "py://tool.py") {
		t.Error("README does not reach tool.py")
	}

	write(t, filepath.Join(root, "tool.py"), "def f():\n    pass\n\n\ndef g():\n    f()\n")
	g, rep = mustSessionBuild(t, s)
	if !slices.Contains(rep.Rebuilt, "py") || !slices.Contains(rep.Kept, "go") {
		t.Errorf("rebuilt %v kept %v; want py rebuilt and go kept", rep.Rebuilt, rep.Kept)
	}
	if python.Available() && !linked(g, "py://tool.py#g", "py://tool.py#f") {
		t.Error("g's call of f is not in the graph")
	}
	sameAsFresh(t, root, g)
}
