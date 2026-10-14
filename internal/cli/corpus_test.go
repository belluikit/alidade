// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trendvidia/alidade/internal/adapter/golang"
	"github.com/trendvidia/alidade/internal/build"
	"github.com/trendvidia/alidade/internal/graph"
)

// TestCorpus runs the real repositories named by ALIDADE_CORPUS — roots
// separated by spaces; scripts/corpus.sh names the in-house ones — through
// the build with tests on, and checks what a fixture cannot: that the
// graph agrees with the tree it was built from. Each check is a class of
// miss a consumer found on a real repository before any fixture did
// (#67, #68, #81); a new class gets a check here, not only a fixture.
// Skipped when the variable is unset, so `go test ./...` stays fast and
// hermetic; run it before a release.
func TestCorpus(t *testing.T) {
	roots := strings.Fields(os.Getenv("ALIDADE_CORPUS"))
	if len(roots) == 0 {
		t.Skip("set ALIDADE_CORPUS to repository roots, or run scripts/corpus.sh")
	}
	built := 0
	for _, root := range roots {
		t.Run(filepath.Base(root), func(t *testing.T) {
			if checkRepo(t, root) {
				built++
			}
		})
	}
	// Every repository skipping is a run that checked nothing, which a
	// green result must not hide.
	if built == 0 {
		t.Fatalf("none of the %d repositories built, so no invariant was checked", len(roots))
	}
}

// checkRepo builds one repository and checks every invariant on it. It
// reports whether the repository built, so a run where none did fails.
func checkRepo(t *testing.T, root string) bool {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Fatalf("root %s is not a directory: %v — a repository that is not there is a broken list, not a skip", root, err)
	}
	opts := build.DefaultOptions()
	opts.Go.Tests = true
	start := time.Now()
	g, rep, err := build.Build(root, opts)
	if err != nil {
		t.Skipf("does not build here (the error is the finding, not the skip): %v", err)
		return false
	}
	info := buildInfo(rep)
	st := g.Stats()
	t.Logf("%v: %d nodes %v, %d edges, dropped %v, conflicts %d, go modules %v (failed %v), test nodes %d",
		time.Since(start).Round(time.Millisecond), st.Nodes, st.NodesByScheme, st.Edges, st.DroppedByReason, st.Conflicts, info.GoModules, info.GoModulesFailed, info.GoTestNodes)
	mods, _, err := golang.Modules(rep.Root, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &corpus{t: t, g: g, root: rep.Root, mods: mods, failed: set(info.GoModulesFailed)}
	c.testMarkers()
	c.filesExist()
	c.generatedOnDisk()
	c.linksOnDisk()
	c.deterministic(root, opts)
	return true
}

// deterministic builds the repository a second time and compares the
// graphs byte for byte. Two builds of one tree that differ are a bug
// wherever they come from, and the one this caught was real: an edge
// deduplicated per (from, to) took the test marking of whichever
// declaration the call graph reached first, which is map order, so govm
// produced a different graph run to run. The core derives that fact now,
// and this is what would have said so.
func (c *corpus) deterministic(root string, opts build.Options) {
	var first, second bytes.Buffer
	if err := c.g.WritePXF(&first); err != nil {
		c.t.Fatal(err)
	}
	g, _, err := build.Build(root, opts)
	if err != nil {
		c.t.Fatalf("second build: %v", err)
	}
	if err := g.WritePXF(&second); err != nil {
		c.t.Fatal(err)
	}
	if first.Len() == second.Len() && first.String() == second.String() {
		return
	}
	c.t.Errorf("two builds of an unchanged tree differ: %d bytes then %d", first.Len(), second.Len())
	a, b := strings.Split(first.String(), "\n"), strings.Split(second.String(), "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			c.t.Errorf("  first difference at line %d:\n    %s\n    %s", i+1, a[i], b[i])
			return
		}
	}
}

type corpus struct {
	t      *testing.T
	g      *graph.Graph
	root   string
	mods   []golang.Module
	failed map[string]bool
}

func set(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// testMarkers: with tests on, a Go node's Test says exactly whether its
// file is a _test.go, and every edge touching such a node is a test edge
// (#81). A package node carries no file and is checked by externalTest.
// The edge half is the core's derivation, which this holds to the same
// rule from the outside.
func (c *corpus) testMarkers() {
	test := map[graph.ID]bool{}
	decls := c.declFiles()
	bad := 0
	for n := range c.g.Nodes() {
		if s, _ := n.ID.Scheme(); s != graph.SchemeGo || n.Attrs["external"] == "true" {
			continue
		}
		marked := n.Test
		test[n.ID] = marked
		want := marked
		if n.Pos.File != "" {
			want = strings.HasSuffix(n.Pos.File, "_test.go")
		} else if n.Kind == graph.KindPackage {
			files := decls[n.ID]
			if len(files) == 0 {
				// A directory of *_test files only also has a package
				// of its own name that declares nothing: its files are
				// the external test package's.
				files = decls[n.ID+"_test"]
			}
			want = externalTest(n, decls[n.ID]) || c.testOnlyOnDisk(files)
		}
		if want != marked && c.count(&bad) {
			c.t.Errorf("test marker: %s at %s is marked %v", n.ID, n.Pos.File, marked)
		}
	}
	c.externalMarkers(test)
	bad = 0
	for e := range c.g.Edges() {
		if want := test[e.From] || test[e.To]; want != e.Test && c.count(&bad) {
			c.t.Errorf("test marker: edge %s -%s-> %s: Test = %v, want %v", e.From, e.Kind, e.To, e.Test, want)
		}
	}
}

// externalMarkers re-derives an external Go package's test marker (#128,
// #133): test when it has an edge from a Go declaration and every such
// edge leaves a test one; an edge from another layer does not count. It
// records the marker in test, since an edge into a test package is a test
// edge, a document's included.
func (c *corpus) externalMarkers(test map[graph.ID]bool) {
	bad := 0
	for n := range c.g.Nodes() {
		if s, _ := n.ID.Scheme(); s != graph.SchemeGo || n.Attrs["external"] != "true" {
			continue
		}
		code, prod := 0, false
		for _, e := range c.g.In(n.ID) {
			if s, _ := e.From.Scheme(); s == graph.SchemeGo {
				code++
				prod = prod || !test[e.From]
			}
		}
		test[n.ID] = n.Test
		if want := code > 0 && !prod; want != n.Test && c.count(&bad) {
			c.t.Errorf("test marker: external %s is marked %v, want %v", n.ID, n.Test, want)
		}
	}
}

// declFiles lists, per package node, the file of each declaration it
// contains. It is what a package node has instead of a file of its own:
// the external test package's declarations are the _test.go files of the
// directory beside it, which no import path shows.
func (c *corpus) declFiles() map[graph.ID][]string {
	file := map[graph.ID]string{}
	for n := range c.g.Nodes() {
		file[n.ID] = n.Pos.File
	}
	out := map[graph.ID][]string{}
	for e := range c.g.Edges() {
		if e.Kind == graph.KindContains {
			out[e.From] = append(out[e.From], file[e.To])
		}
	}
	return out
}

// externalTest re-derives the go tool's definition of an external test
// package: a package named with the "_test" suffix, declaring something,
// and declaring it only in _test.go files. The import path's suffix is
// not the rule — a production package in a directory named "e2e_test/"
// has that suffix and its own name (#95), and this check read the suffix
// until the adapter stopped. Neither is "every file is a test": a
// package of test files whose name is its own has a file to carry the
// flag, which is what ADR 004 rule 4 marks the package instead of. The
// one shape this cannot see is a *_test package whose non-test file
// declares nothing at all; it would read as marked here and is not.
// A package with no production files is the other test package (#113),
// which testOnlyOnDisk re-derives from the directory.
func externalTest(n graph.Node, declFiles []string) bool {
	if !strings.HasSuffix(n.Name, "_test") || len(declFiles) == 0 {
		return false
	}
	for _, f := range declFiles {
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
	}
	return true
}

// testOnlyOnDisk re-derives #113's test package from the tree rather than
// the loader: the directory its declarations are in holds Go files, and
// every one is a _test.go file. A package declaring nothing is looked for
// through its external test package's declarations, the only files its
// directory has; with neither, it reads as unmarked.
func (c *corpus) testOnlyOnDisk(declFiles []string) bool {
	if len(declFiles) == 0 || declFiles[0] == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(c.root, filepath.FromSlash(path.Dir(declFiles[0]))))
	if err != nil {
		return false
	}
	tests := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if !strings.HasSuffix(e.Name(), "_test.go") {
			return false
		}
		tests = true
	}
	return tests
}

// filesExist: every positioned node names a file that is in the tree.
func (c *corpus) filesExist() {
	bad := 0
	for n := range c.g.Nodes() {
		if n.Pos.File == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.root, filepath.FromSlash(n.Pos.File))); err != nil && c.count(&bad) {
			c.t.Errorf("position: %s names %s, which is not in the tree", n.ID, n.Pos.File)
		}
	}
}

// generatedOnDisk is #67's signal: a dropped edge into a Go package whose
// directory is in the tree and holds Go files, while the graph has no
// node for the package at all, means a module was not loaded. A package
// the graph has whose declaration is missing is stale generated code,
// logged and not failed.
func (c *corpus) generatedOnDisk() {
	bad, stale := 0, 0
	for _, d := range c.g.Dropped() {
		to := string(d.Edge.To)
		if !strings.HasPrefix(to, "go://") {
			continue
		}
		pkgPath, _, _ := strings.Cut(strings.TrimPrefix(to, "go://"), "#")
		dir, mod := c.packageDir(pkgPath)
		if dir == "" || !hasGo(dir) {
			continue
		}
		if _, ok := c.g.Node(graph.ID("go://" + pkgPath)); ok {
			stale++
			continue
		}
		if c.failed[mod] {
			continue
		}
		if c.count(&bad) {
			c.t.Errorf("dropped %s -%s-> %s: package %s is on disk at %s with Go files and the graph has no node for it", d.Edge.From, d.Edge.Kind, to, pkgPath, dir)
		}
	}
	if stale > 0 {
		c.t.Logf("%d dropped edges name a declaration missing from a package the graph has (stale generated code, not a graph gap)", stale)
	}
}

// linksOnDisk is #68's signal from the other side: a Markdown link that
// dropped although its target path is in the tree and holds Go.
func (c *corpus) linksOnDisk() {
	bad := 0
	for _, d := range c.g.Dropped() {
		if !strings.HasPrefix(string(d.Edge.From), "md://") || d.Edge.Attrs["link"] == "" {
			continue
		}
		from, _, _ := strings.Cut(strings.TrimPrefix(string(d.Edge.From), "md://"), "#")
		target := path.Clean(path.Join(path.Dir(from), d.Edge.Attrs["link"]))
		abs := filepath.Join(c.root, filepath.FromSlash(target))
		st, err := os.Stat(abs)
		if err != nil {
			continue // gone: the drop is right
		}
		if !st.IsDir() {
			abs = filepath.Dir(abs)
		}
		if !hasGo(abs) {
			continue
		}
		if _, mod := c.packageDir(c.pkgPathOf(abs)); c.failed[mod] {
			continue
		}
		if c.count(&bad) {
			c.t.Errorf("dropped %s -> %s: link %q is on disk and holds Go", d.Edge.From, d.Edge.To, d.Edge.Attrs["link"])
		}
	}
}

// packageDir maps a Go package path to its directory under the root and
// the module owning it, "" when no module of the tree does.
func (c *corpus) packageDir(pkgPath string) (dir, mod string) {
	best := golang.Module{}
	for _, m := range c.mods {
		if (pkgPath == m.Path || strings.HasPrefix(pkgPath, m.Path+"/")) && len(m.Path) > len(best.Path) {
			best = m
		}
	}
	if best.Path == "" {
		return "", ""
	}
	return filepath.Join(best.Dir, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(pkgPath, best.Path), "/"))), best.Path
}

// pkgPathOf is packageDir the other way round, for a directory.
func (c *corpus) pkgPathOf(dir string) string {
	best := golang.Module{}
	for _, m := range c.mods {
		if (dir == m.Dir || strings.HasPrefix(dir, m.Dir+string(filepath.Separator))) && len(m.Dir) > len(best.Dir) {
			best = m
		}
	}
	if best.Path == "" {
		return ""
	}
	rel, _ := filepath.Rel(best.Dir, dir)
	if rel == "." {
		return best.Path
	}
	return best.Path + "/" + filepath.ToSlash(rel)
}

func hasGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// count reports whether a violation should be printed: the first ten of
// a class are, the rest are summed at the end.
func (c *corpus) count(n *int) bool {
	*n++
	if *n == 11 {
		c.t.Errorf("... more of the same, stopped printing")
	}
	return *n <= 10
}
