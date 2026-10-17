// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// TestOneBadInputCostsOneFact pins #92: an input no ID can name, or that
// cannot be read, is left out on its own and recorded with its reason —
// the build goes on, and what sits beside it is in the graph. Each case
// is a tree that failed the whole build before.
func TestOneBadInputCostsOneFact(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		// skip is the expected record: layer, file, line, and a
		// substring of the reason; an empty layer expects none.
		layer, file string
		line        int
		reason      string
		// keep is a node that must be in the graph, and at, when set,
		// the file its position must name.
		keep graph.ID
		at   string
	}{
		{name: "document with a space",
			files: map[string]string{"docs/My Notes.md": "# Notes\n", "README.md": "# Readme\n"},
			layer: "md", file: "docs/My Notes.md", reason: "whitespace", keep: "md://README.md"},
		{name: "link to a name with a space",
			files: map[string]string{"README.md": "# Readme\n\nSee [notes](<Meeting Notes.md>).\n"},
			layer: "md", file: "README.md", line: 3, reason: "whitespace", keep: "md://README.md#readme"},
		{name: "proto file with a space",
			files: map[string]string{"my api.proto": "syntax = \"proto3\";\npackage a;\nmessage M {}\n", "ok.proto": "syntax = \"proto3\";\npackage b;\nmessage O {}\n"},
			layer: "proto", file: "my api.proto", reason: "whitespace", keep: "proto://b.O"},
		{name: "pxf file with a space",
			files: map[string]string{"my conf.pxf": "x = 1\n", "ok.pxf": "y = 2\n"},
			layer: "pxf", file: "my conf.pxf", reason: "whitespace", keep: "pxf://ok.pxf#y"},
		{name: "pxf reference with an empty segment",
			files: map[string]string{"a.pxf": "greeting = \"hello ${user.}\"\n"},
			layer: "pxf", file: "a.pxf", line: 1, reason: "empty segment", keep: "pxf://a.pxf#greeting"},
		{name: "pxf reference on the second line",
			files: map[string]string{"a.pxf": "greeting = \"hi\"\nother = \"hello ${user.}\"\n"},
			layer: "pxf", file: "a.pxf", line: 2, reason: "empty segment", keep: "pxf://a.pxf#greeting"},
		{name: "pxf chain loading outside the root",
			files: map[string]string{"cfg/chain.pxf": "@type chameleon.v1.ChainSpec\nversion = \"1.0.0\"\ndefault_environment = \"dev\"\n" +
				"environments = [ { name = \"dev\" } ]\nsources = {\n  \"default\": { kind = \"fs\" options = { \"root\": \"../../shared\" } }\n}\n" +
				"layers = [ { id = \"base\" path = \"base.pxf\" for_each = ONCE required = true } ]\n"},
			layer: "pxf", file: "cfg/chain.pxf", reason: "outside the repository root", keep: "pxf://cfg/chain.pxf"},
		{name: "sql column no ID can carry",
			files: map[string]string{"s.sql": "CREATE TABLE t (id int, \"rate/sec\" int);\n"},
			layer: "sql", file: "s.sql", line: 1, reason: `contains "/"`, keep: "sql://column/t.id"},
		{name: "sql under a directory with a space",
			files: map[string]string{"q dir/schema.sql": "CREATE TABLE users (id int);\n"},
			keep:  "sql://table/users", at: "q dir/schema.sql"},
		{name: "ts file with a space",
			files: map[string]string{"web/my page.ts": "export const x = 1;\n", "web/ok.ts": "import { x } from \"./my page\";\nexport const y = x;\n"},
			layer: "ts", file: "web/my page.ts", reason: "whitespace", keep: "ts://web/ok.ts"},
		{name: "go module that does not compile",
			files: map[string]string{"go.mod": "module example.com/m\n\ngo 1.26\n", "m.go": "package m\n\nfunc F( {\n", "README.md": "# Readme\n"},
			keep:  "go://example.com/m"},
		{name: "go.mod with no module path",
			files: map[string]string{"go.mod": "module example.com/m\n\ngo 1.26\n", "m.go": "package m\n", "tools/go.mod": "go 1.26\n"},
			layer: "go", file: "tools/go.mod", reason: "declares no module path", keep: "go://example.com/m"},
		{name: "goasm file with a space",
			files: map[string]string{"my t.goasm": "# TEMPLATE Nop\n", "README.md": "# Readme\n"},
			layer: "goasm", file: "my t.goasm", reason: "whitespace", keep: "md://README.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range c.files {
				write(t, filepath.Join(root, name), body)
			}
			g, rep := mustBuild(t, root)
			n, ok := g.Node(c.keep)
			if !ok {
				t.Errorf("graph lacks %s", c.keep)
			} else if c.at != "" && n.Pos.File != c.at {
				t.Errorf("%s at %q, want %q", c.keep, n.Pos.File, c.at)
			}
			if c.layer == "" {
				if s := rep.Skipped(); len(s) > 0 {
					t.Errorf("skipped %+v, want nothing", s)
				}
				return
			}
			wantSkip(t, rep, c.layer, c.file, c.line, c.reason)
		})
	}
}

// TestOneLayerIsNamedOnce pins the two things a single bad input in a
// single fixture cannot show: one layer reporting the same reason twice
// names itself once, and two references on two lines are two records.
func TestOneLayerIsNamedOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.pxf"), "greeting = \"${user.} and ${user.}\"\nother = \"${user.}\"\n")
	_, rep := mustBuild(t, root)
	got := rep.Skipped()
	if len(got) != 2 {
		t.Fatalf("skipped %+v, want one record per line", got)
	}
	for i, want := range []int{1, 2} {
		if got[i].Layer != "pxf" {
			t.Errorf("skip %d names layers %q, want %q: one layer, named once", i, got[i].Layer, "pxf")
		}
		if got[i].Line != want {
			t.Errorf("skip %d at line %d, want %d", i, got[i].Line, want)
		}
	}
}

// TestASkipIsNotALayer pins that a layer which read no file is not one
// of the layers that ran, however many skips it recorded: a directory
// every walk fails to open must not claim a .proto or .sql layer for a
// repository that has neither, on the first build or a rebuild ([layer],
// #61, #92).
func TestASkipIsNotALayer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "# Readme\n")
	locked := filepath.Join(root, "pgdata")
	write(t, filepath.Join(locked, "PG_VERSION"), "17\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"build", "rebuild"} {
		_, rep, err := s.Build()
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		if got := rep.Layers(); !slices.Equal(got, []string{"md"}) {
			t.Errorf("%s: layers %v, want only md: no other layer read a file", phase, got)
		}
		if phase == "rebuild" && !slices.Equal(rep.Kept, []string{"md"}) {
			t.Errorf("rebuild: layers_kept %v, want only md", rep.Kept)
		}
		// The skip itself survives being no layer, and still names
		// every walk that hit the directory.
		sk := wantSkip(t, rep, "proto", "pgdata", 0, "open: permission denied")
		for _, layer := range []string{"sql", "pxf", "goasm", "ts", "md"} {
			if !strings.Contains(" "+sk.Layer+" ", " "+layer+" ") {
				t.Errorf("%s: pgdata skip names layers %q, want %s among them", phase, sk.Layer, layer)
			}
		}
	}
}

// TestSkipReasonIsMachineIndependent: a reason reaches both surfaces and
// the PXF the graph command writes, so it carries the operation and the
// cause, never the absolute path of this machine.
func TestSkipReasonIsMachineIndependent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26\n")
	write(t, filepath.Join(root, "m.go"), "package m\n")
	gomod := filepath.Join(root, "tools", "go.mod")
	write(t, gomod, "module example.com/m/tools\n\ngo 1.26\n")
	if err := os.Chmod(gomod, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gomod, 0o644) })

	_, rep := mustBuild(t, root)
	s := wantSkip(t, rep, "go", "tools/go.mod", 0, "open: permission denied")
	if strings.Contains(s.Reason, root) || strings.Contains(s.Reason, "/") {
		t.Errorf("reason %q names a path; the file is %q and the reason is the operation and its cause", s.Reason, s.File)
	}
	if _, ok := mustGraphNode(t, root); !ok {
		t.Error("graph lacks the root module's package")
	}
}

func mustGraphNode(t *testing.T, root string) (graph.Node, bool) {
	t.Helper()
	g, _, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return g.Node("go://example.com/m")
}

// TestUnreadableEntriesAreSkipped: a dangling symbolic link and a directory the walk cannot open are passed over and
// recorded, by every layer that would have read them, as one entry.
func TestUnreadableEntriesAreSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "# Readme\n")
	if err := os.Symlink("nowhere", filepath.Join(root, "notes.md")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "pgdata")
	write(t, filepath.Join(locked, "PG_VERSION"), "17\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	g, rep := mustBuild(t, root)
	if _, ok := g.Node("md://README.md"); !ok {
		t.Error("graph lacks md://README.md")
	}
	wantSkip(t, rep, "md", "notes.md", 0, "open: no such file")
	s := wantSkip(t, rep, "md", "pgdata", 0, "open: permission denied")
	for _, layer := range []string{"proto", "sql", "pxf", "goasm", "ts"} {
		if !strings.Contains(" "+s.Layer+" ", " "+layer+" ") {
			t.Errorf("pgdata skip names layers %q, want %s among them", s.Layer, layer)
		}
	}
}

// TestCgoPackageBuilds: cgo's generated declarations are positioned in
// the build cache, outside the tree; they are left out and counted, and
// the package's own declarations and calls stay.
func TestCgoPackageBuilds(t *testing.T) {
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skip("cgo is not enabled")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/cg\n\ngo 1.26\n")
	write(t, filepath.Join(root, "c.go"), "package c\n\n// #include <stdlib.h>\nimport \"C\"\n\nfunc F() { _ = C.CString(\"x\") }\n\nfunc G() { F() }\n")
	g, rep := mustBuild(t, root)
	if rep.Go.Outside == 0 {
		t.Error("Outside = 0, want cgo's wrappers counted")
	}
	// The wrappers stay a count: cgo writes them by the hundred, and
	// naming its files would bury a real skip (#109).
	for _, s := range rep.Skipped() {
		if strings.Contains(" "+s.Layer+" ", " go ") {
			t.Errorf("a cgo package reports a skip: %+v", s)
		}
	}
	for n := range g.Nodes() {
		if strings.Contains(string(n.ID), "#_Cfunc_") || strings.Contains(string(n.ID), "#_Ctype_") {
			t.Errorf("cgo wrapper %s reached the graph", n.ID)
		}
	}
	var called bool
	for _, e := range g.Out("go://example.com/cg#G") {
		called = called || e.Kind == graph.KindCalls && e.To == "go://example.com/cg#F"
	}
	if !called {
		t.Error("G -> F calls edge missing")
	}
	if len(g.Dropped()) > 0 {
		t.Errorf("dropped %+v, want none: no edge points at a wrapper", g.Dropped())
	}
}

func mustBuild(t *testing.T, root string) (*graph.Graph, *Report) {
	t.Helper()
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatalf("build failed, want the bad input skipped: %v", err)
	}
	return g, rep
}

// wantSkip finds the record for file at line whose layers include layer
// and whose reason contains reason.
// TestOutsideTreeDeclarationsAreNamed pins #109: a declaration positioned
// outside the tree that cgo did not write — here a //line directive naming
// a grammar elsewhere, as goyacc's output does — is left out and counted
// like a cgo wrapper, and its file is named once in the skipped inputs, so
// a package reduced to nothing is not indistinguishable from cgo noise.
func TestOutsideTreeDeclarationsAreNamed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/ln\n\ngo 1.26\n")
	write(t, filepath.Join(root, "p", "gen.go"), "package p\n\nfunc Kept() {}\n\n//line /elsewhere/gen.y:10\nfunc Gen() {}\n\nfunc Gen2() {}\n")
	g, rep := mustBuild(t, root)
	if rep.Go.Outside != 2 {
		t.Errorf("Outside = %d, want 2 (Gen and Gen2)", rep.Go.Outside)
	}
	wantSkip(t, rep, "go", "p/gen.go", 0, "declarations positioned outside the tree, at /elsewhere/gen.y, are left out")
	n := 0
	for _, s := range rep.Skipped() {
		if s.File == "p/gen.go" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("p/gen.go is named %d times, want once", n)
	}
	if _, ok := g.Node("go://example.com/ln/p#Kept"); !ok {
		t.Error("Kept, before the directive, is missing")
	}
}

func wantSkip(t *testing.T, rep *Report, layer, file string, line int, reason string) graph.Skip {
	t.Helper()
	for _, s := range rep.Skipped() {
		if s.File == file && s.Line == line && strings.Contains(" "+s.Layer+" ", " "+layer+" ") && strings.Contains(s.Reason, reason) {
			return s
		}
	}
	t.Errorf("no %s skip of %s:%d (%q); skipped %+v", layer, file, line, reason, rep.Skipped())
	return graph.Skip{}
}

// TestSkippedDirectories pins the owner's rule for #215: vendor and
// third_party are left out by every layer and the fingerprint by default;
// a submodule, a repository nested in the tree, is read like any other
// directory; and .alidade.pxf's skip list replaces the default.
func TestSkippedDirectories(t *testing.T) {
	root := skippedTree(t)
	read := func(t *testing.T, want map[string]bool) { t.Helper(); wantRead(t, root, want) }
	read(t, map[string]bool{"third_party/dep": false, "vendored/clone": true, "generated": true})

	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "third_party/dep/doc.md"), "# Changed\n")
	if after, _ := s.Fingerprint(); after != before {
		t.Error("an edit under third_party changed the fingerprint")
	}

	if _, _, err := s.Build(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".alidade.pxf"), "skip = [\"generated\"]\n")
	read(t, map[string]bool{"third_party/dep": true, "vendored/clone": true, "generated": false})
	// A session rebuilds every layer when the list changes.
	_, rep, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Rebuilt, "md") || !slices.Contains(rep.Rebuilt, "go") {
		t.Errorf("rebuilt %v after .alidade.pxf changed, want every layer", rep.Rebuilt)
	}

	write(t, filepath.Join(root, ".alidade.pxf"), "skip = generated\n")
	if _, _, err := Build(root, DefaultOptions()); err == nil || !strings.Contains(err.Error(), ".alidade.pxf") {
		t.Errorf("a skip list that does not read: err %v, want one naming .alidade.pxf", err)
	}
}

// skippedTree writes TestSkippedDirectories' tree: every layer's files in
// third_party/dep, in vendored/clone, a submodule, and in generated.
func skippedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "# Readme\n")
	for _, dir := range []string{"third_party/dep", "vendored/clone", "generated"} {
		d := filepath.Join(root, dir)
		write(t, filepath.Join(d, "go.mod"), "module example.com/"+filepath.Base(dir)+"\n\ngo 1.26\n")
		write(t, filepath.Join(d, "x.go"), "package x\n\nfunc X() {}\n")
		write(t, filepath.Join(d, "doc.md"), "# Doc\n")
		write(t, filepath.Join(d, "a.ts"), "export function a() {}\n")
		write(t, filepath.Join(d, "a.proto"), "syntax = \"proto3\";\npackage p"+filepath.Base(dir)+";\nmessage M {}\n")
		write(t, filepath.Join(d, "q.sql"), "CREATE TABLE t (id int);\n")
		write(t, filepath.Join(d, "c.pxf"), "a = 1\n")
	}
	// vendored/clone is a submodule: its .git is a file.
	write(t, filepath.Join(root, "vendored/clone/.git"), "gitdir: ../../.git/modules/clone\n")
	return root
}

// wantRead builds root and checks, for each directory, whether any node
// is positioned in it.
func wantRead(t *testing.T, root string, want map[string]bool) {
	t.Helper()
	g, _ := mustBuild(t, root)
	got := map[string]bool{}
	for n := range g.Nodes() {
		for dir := range want {
			if strings.HasPrefix(n.Pos.File, dir+"/") {
				got[dir] = true
			}
		}
	}
	for dir, w := range want {
		if got[dir] != w {
			t.Errorf("%s read: %v, want %v", dir, got[dir], w)
		}
	}
}

// TestInstalledPackagesAreLeftOut pins #266: installed packages are left
// out by every layer and the fingerprint, not only by the Python layer —
// a virtual environment by its pyvenv.cfg, whatever its name, and .venv,
// venv and site-packages by name — and .alidade.pxf cannot bring them
// back. On openpilot the Markdown layer read 156 documents of a uv
// environment's site-packages.
func TestInstalledPackagesAreLeftOut(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "# Readme\n")
	dirs := []string{"env/lib/python3.12/site-packages/dep", "venv/dep", ".venv/dep", "conda/lib/site-packages/dep", "mine"}
	for _, dir := range dirs {
		d := filepath.Join(root, dir)
		write(t, filepath.Join(d, "go.mod"), "module example.com/"+strings.ReplaceAll(dir, "/", "_")+"\n\ngo 1.26\n")
		write(t, filepath.Join(d, "x.go"), "package x\n\nfunc X() {}\n")
		write(t, filepath.Join(d, "doc.md"), "# Doc\n")
		write(t, filepath.Join(d, "a.ts"), "export function a() {}\n")
		write(t, filepath.Join(d, "a.proto"), "syntax = \"proto3\";\npackage p"+strings.NewReplacer("/", "", ".", "", "-", "").Replace(dir)+";\nmessage M {}\n")
		write(t, filepath.Join(d, "q.sql"), "CREATE TABLE t (id int);\n")
		write(t, filepath.Join(d, "c.pxf"), "a = 1\n")
		write(t, filepath.Join(d, "m.py"), "def f():\n    pass\n")
	}
	write(t, filepath.Join(root, "env/pyvenv.cfg"), "home = /usr/bin\n")
	want := map[string]bool{"env": false, "venv": false, ".venv": false, "conda": false, "mine": true}
	wantRead(t, root, want)
	wantLayers(t, root, "mine", []string{"go", "md", "proto", "pxf", "py", "sql", "ts"})

	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "env/lib/python3.12/site-packages/dep/doc.md"), "# Changed\n")
	if after, _ := s.Fingerprint(); after != before {
		t.Error("an edit in a virtual environment changed the fingerprint")
	}

	write(t, filepath.Join(root, ".alidade.pxf"), "skip = []\n")
	wantRead(t, root, want)
}

// wantLayers checks that every layer in want has a node positioned under
// dir: the fixture's directories are read by each layer, so leaving one
// out is the rule's doing.
func wantLayers(t *testing.T, root, dir string, want []string) {
	t.Helper()
	g, _ := mustBuild(t, root)
	got := map[string]bool{}
	for n := range g.Nodes() {
		if strings.HasPrefix(n.Pos.File, dir+"/") {
			got[strings.SplitN(string(n.ID), "://", 2)[0]] = true
		}
	}
	for _, l := range want {
		if !got[l] {
			t.Errorf("no %s node under %s: layers %v", l, dir, got)
		}
	}
}

// TestNestedWorktreeIsLeftOut pins #24: a `git worktree` of the
// repository nested inside it — under .claude/worktrees, or any other
// directory — is read by no layer and not by the fingerprint, so its Go
// module is not loaded a second time; another repository nested the same
// way, as a submodule is, is still read (TestSkippedDirectories).
func TestNestedWorktreeIsLeftOut(t *testing.T) {
	root := gittest.TempDir(t)
	files := map[string]string{"go.mod": "module example.com/m\n\ngo 1.26\n", "a/a.go": "package a\n\nfunc F() {}\n",
		"docs/x.md": "# X\n", "web/a.ts": "export function a() {}\n", "p/a.proto": "syntax = \"proto3\";\npackage p;\nmessage M {}\n"}
	gittest.Init(t, root, files)
	gittest.Git(t, root, "worktree", "add", "-q", "-b", "f", filepath.Join(root, ".claude/worktrees/f"))
	gittest.Git(t, root, "worktree", "add", "-q", "-b", "g", filepath.Join(root, "wts/g"))
	gittest.Init(t, filepath.Join(root, "nested"), map[string]string{"doc.md": "# Nested\n"})
	g, rep := mustBuild(t, root)
	for n := range g.Nodes() {
		if strings.HasPrefix(n.Pos.File, ".claude/") || strings.HasPrefix(n.Pos.File, "wts/") || strings.HasPrefix(string(n.ID), "md://.claude") || strings.HasPrefix(string(n.ID), "md://wts") {
			t.Errorf("%s, of a nested worktree, is in the graph", n.ID)
		}
	}
	if rep.Go == nil || !slices.Equal(rep.Go.Modules, []string{"example.com/m"}) {
		t.Errorf("Go modules %v, want example.com/m once", rep.Go)
	}
	if _, ok := g.Node("md://nested/doc.md#nested"); !ok {
		t.Error("a nested repository of its own, as a submodule is, was not read")
	}
	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "wts/g/docs/x.md"), "# Changed in the worktree\n")
	if after, _ := s.Fingerprint(); after != before {
		t.Error("an edit in a nested worktree changed the fingerprint")
	}
}
