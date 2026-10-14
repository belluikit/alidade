// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
)

// all lists a graph's nodes and edges, derivation included, to compare two
// graphs whole.
func all(g *graph.Graph) []string {
	var out []string
	for n := range g.Nodes() {
		out = append(out, "node "+string(n.ID)+" "+string(n.Kind))
	}
	for e := range g.Edges() {
		out = append(out, "edge "+string(e.From)+" -> "+string(e.To)+" "+string(e.Kind)+" "+string(e.Derivation))
	}
	slices.Sort(out)
	return out
}

// holdJedi makes the background's jedi wait until the returned channel is
// closed, and puts it back when the test ends.
func holdJedi(t *testing.T) chan struct{} {
	release := make(chan struct{})
	real := analyzeEach
	t.Cleanup(func() { analyzeEach = real })
	analyzeEach = func(ctx context.Context, root string, files []python.File, workers int, each func(string, *python.Result)) (*python.Result, error) {
		<-release
		return real(ctx, root, files, workers, each)
	}
	return release
}

// callsBy counts a graph's calls edges INFERRED and RESOLVED.
func callsBy(g *graph.Graph) (inferred, resolved int) {
	for e := range g.Edges() {
		if e.Kind != graph.KindCalls {
			continue
		}
		switch e.Derivation {
		case graph.Inferred:
			inferred++
		case graph.Resolved:
			resolved++
		}
	}
	return inferred, resolved
}

// checkWarm checks the layer before jedi has read a file: every file
// pending, and every call INFERRED.
func checkWarm(t *testing.T, b *graph.Builder, rep *Report) {
	t.Helper()
	if rep.Live == nil || rep.Pending != 7 {
		t.Fatalf("Live %v, Pending %d; want jedi running and all 7 files pending", rep.Live != nil, rep.Pending)
	}
	if inferred, resolved := callsBy(b.Build()); inferred == 0 || resolved != 0 {
		t.Errorf("before jedi: %d calls INFERRED, %d RESOLVED; want some and none", inferred, resolved)
	}
	if done, total, finished := rep.Live.Progress(); done != 0 || total != 7 || finished {
		t.Errorf("progress before jedi %d of %d, done %v; want 0 of 7, not done", done, total, finished)
	}
}

// TestBackground (#269): the layer answers at once from the warm-up pass,
// each call it places INFERRED, while jedi reads the tree; refreshed once
// jedi is done, it is the layer a build that waits for jedi gives.
func TestBackground(t *testing.T) {
	if !python.Available() {
		t.Skip("built without the python tag")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "proj"))
	if err != nil {
		t.Fatal(err)
	}
	release := holdJedi(t)

	b := graph.NewBuilder(dir)
	opts := DefaultOptions()
	opts.Background = true
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	checkWarm(t, b, rep)

	close(release)
	rep.Live.Wait()
	rb := graph.NewBuilder(dir)
	again, err := Refresh(rb, rep.Live)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pending != 0 {
		t.Errorf("Pending %d after jedi, want 0", again.Pending)
	}
	waited, _ := extractFixture(t)
	if got, want := all(rb.Build()), all(waited); !slices.Equal(got, want) {
		t.Errorf("refreshed after jedi, the layer differs from a build that waited:\n%v\n%v", got, want)
	}
}

// TestReuse (#269): after an edit, a file keeps jedi's answers from the
// build before unless something jedi read for them changed, and the layer
// jedi then finishes is the one a fresh build gives. Each case edits a
// copy of the fixture, with dyn.py added, whose parameter jedi would infer
// by searching the project for calls of measure; and each runs twice, with
// the build before in memory and kept on disk between sessions (#279).
func TestReuse(t *testing.T) {
	if !python.Available() {
		t.Skip("built without the python tag")
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string) // before the first build
		edit  func(t *testing.T, dir string)
		rerun []string // the files whose answers are not kept
	}{
		{"nothing depends on it", nil, func(t *testing.T, dir string) { prependLine(t, dir, "app.py") },
			[]string{"app.py"}},
		{"a file parsed for it", nil, func(t *testing.T, dir string) { prependLine(t, dir, "lib/helperlib.py") },
			[]string{"lib/helperlib.py", "scripts/tool.py"}},
		{"a file the layer does not read", nil, func(t *testing.T, dir string) {
			// vendored.vend becomes a function the layer has.
			p := "env/lib/site-packages/vendored/__init__.py"
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, p, string(b)+"\nfrom pkg.shapes import helper as vend\n")
		}, []string{"uses_env.py"}},
		{"a file gone", nil, func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "lib", "helperlib.py")); err != nil {
				t.Fatal(err)
			}
		}, []string{"scripts/tool.py"}},
		{"a module added under a name looked up", nil, func(t *testing.T, dir string) {
			writeFile(t, dir, "scripts/helperlib.py", "def go():\n    return 2\n")
		}, []string{"scripts/helperlib.py", "scripts/tool.py"}},
		{"a file added to a directory looked up", nil, func(t *testing.T, dir string) {
			// app.py looks up pkg, which finds what it found before (#281).
			writeFile(t, dir, "pkg/extra.py", "def more():\n    return 3\n")
		}, []string{"pkg/extra.py"}},
		{"a file added in a new directory looked up", func(t *testing.T, dir string) {
			// The lookup of fresh finds nothing, so jedi never asks for sub
			// or mod: only the new directory's name ties wants.py to the file.
			writeFile(t, dir, "wants.py", "import fresh.sub.mod\n\nfresh.sub.mod.go()\n")
		}, func(t *testing.T, dir string) {
			writeFile(t, dir, "fresh/sub/mod.py", "def go():\n    return 1\n")
		}, []string{"fresh/sub/mod.py", "wants.py"}},
		{"a package marker added", func(t *testing.T, dir string) {
			// lib/ is on user.py's sys.path while it is no package.
			writeFile(t, dir, "lib/sub/user.py", "import helperlib\n\nhelperlib.go()\n")
		}, func(t *testing.T, dir string) { writeFile(t, dir, "lib/__init__.py", "") },
			[]string{"lib/__init__.py", "lib/helperlib.py", "lib/sub/user.py"}},
		{"a name searched for only inside a word", nil, func(t *testing.T, dir string) {
			writeFile(t, dir, "later.py", "measured = 1\n")
		}, []string{"later.py"}},
		{"a caller added in another file", nil, func(t *testing.T, dir string) {
			// jedi does not search other files for a parameter's callers
			// (#265), so measure's answers cannot change.
			writeFile(t, dir, "later.py", "from dyn import measure\nfrom pkg.shapes import Square\n\nmeasure(Square(1))\n")
		}, []string{"later.py"}},
	} {
		t.Run(c.name, func(t *testing.T) { checkReuse(t, c.setup, c.edit, c.rerun, false) })
		t.Run(c.name+" (from disk)", func(t *testing.T) { checkReuse(t, c.setup, c.edit, c.rerun, true) })
	}
}

// checkReuse builds a copy of the fixture with dyn.py added, edits it,
// and builds it again from the first build, or from what the first build
// kept on disk: rerun are the files jedi must read again, and the layer
// it finishes must be a fresh build's.
func checkReuse(t *testing.T, setup, edit func(*testing.T, string), rerun []string, disk bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "proj"))); err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	writeFile(t, dir, "dyn.py", "def measure(shape):\n    return shape.area()\n")
	if setup != nil {
		setup(t, dir)
	}
	opts := DefaultOptions()
	opts.Background = true
	if disk {
		opts.Cache = t.TempDir()
	}
	first, err := Extract(graph.NewBuilder(dir), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	first.Live.Wait()

	edit(t, dir)
	release := holdJedi(t)
	first.Live.Stop()
	if !disk {
		opts.Reuse = first.Live
	}
	rep, err := Extract(graph.NewBuilder(dir), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, pending, _ := rep.Live.snapshot()
	if got := slices.Sorted(maps.Keys(pending)); !slices.Equal(got, rerun) {
		t.Errorf("rerun %v, want %v", got, rerun)
	}
	if rep.Reused != rep.Files-len(rerun) {
		t.Errorf("Reused %d of %d files, want all but %d", rep.Reused, rep.Files, len(rerun))
	}

	close(release)
	checkFresh(t, dir, rep.Live)
}

// checkFresh waits for l's jedi and checks the layer it finishes is the
// one a fresh build of dir gives.
func checkFresh(t *testing.T, dir string, l *Live) {
	t.Helper()
	l.Wait()
	rb := graph.NewBuilder(dir)
	if _, err := Refresh(rb, l); err != nil {
		t.Fatal(err)
	}
	fb := graph.NewBuilder(dir)
	if _, err := Extract(fb, dir, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if got, want := all(rb.Build()), all(fb.Build()); !slices.Equal(got, want) {
		t.Errorf("after reuse, the layer differs from a fresh build:\n%v\n%v", got, want)
	}
}

func writeFile(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// prependLine moves every declaration of a file down a line, and so the
// target of every call into it.
func prependLine(t *testing.T, dir, rel string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, rel, "\n"+string(b))
}
