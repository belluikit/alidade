// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package py is the Python layer (ADR 008, #237): a file node per .py
// file, a node per class, function, method, module-level variable and
// class attribute, imports between files, and calls resolved by jedi in
// the CPython the binary links (internal/python). A build without the
// python tag has the file nodes, each saying its declarations and calls
// are not in the graph.
package py

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
	"github.com/belluikit/alidade/internal/scope"
)

// Options controls the Python layer.
type Options struct {
	// Skip are the directories left out besides the layer's own:
	// .alidade.pxf's list, vendor and third_party by default.
	Skip *scope.Dirs
	// Testdata includes files under testdata directories; out by default.
	Testdata bool
	// Workers caps the processes that resolve calls; 0 is DefaultWorkers.
	Workers int
	// Background answers at once from the warm-up pass, every call placed
	// by name alone and INFERRED, while jedi reads the tree behind it
	// (#269): [Report.Live] is jedi's progress, and [Refresh] builds the
	// layer again from what it knows. Without it, the build waits for jedi.
	Background bool
	// Reuse is the previous build's background read, stopped: with
	// Background, a file whose answers an edit since cannot have changed
	// keeps jedi's from it, and jedi reads only the rest (#269).
	Reuse *Live
	// Cache is the directory that keeps jedi's answers between sessions
	// (#279): with Background and no Reuse, the answers kept there for the
	// tree are reused as a previous build's would be, and what jedi knows
	// is written back when it is done or [Live.Close] is called. Empty
	// keeps nothing.
	Cache string
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// DefaultWorkers is half the CPUs, at most 8: each worker holds jedi's
// inference of the whole tree, 200-400 MB on agent-config and openpilot,
// and 8 was within 10% of 16 on both (#237).
func DefaultWorkers() int { return min(8, max(1, runtime.NumCPU()/2)) }

// SkipDirs are the directories the layer never enters, by name: version
// control, caches, build output, and the directories every layer skips.
// Installed packages — a virtual environment, whatever its name — are
// left out by every layer, through [scope.Dirs.SkipsDir] (#266).
var SkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".tmp": true,
	"__pycache__": true, ".tox": true, ".nox": true, ".eggs": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true,
	"build": true, "dist": true,
}

// Source reports whether the layer reads a file, by name.
func Source(name string) bool { return strings.HasSuffix(name, ".py") }

// A Report says what Extract found.
type Report struct {
	// Files counts the file nodes, Testdata the files skipped under
	// testdata directories, Unparsed those with a syntax error, whose
	// declarations and calls are not read.
	Files, Testdata, Unparsed int
	// Unanalyzed counts the files whose declarations and calls are not in
	// the graph because the build has no CPython.
	Unanalyzed int
	// Declarations counts the declaration nodes.
	Declarations int
	// Imports counts the file → file edges; ImportsOutside the imports
	// resolved to the stdlib, a package, or a file the layer does not
	// read; ImportsUnresolved those resolved to nothing.
	Imports, ImportsOutside, ImportsUnresolved int
	// Of CallSites, Calls are edges (distinct per caller and callee), and
	// the rest resolve outside the files read (CallsOutside), inside to no
	// declaration with a node (CallsUnplaced: a parameter, a local value),
	// or to nothing (CallsUnresolved).
	CallSites, Calls, CallsOutside, CallsUnplaced, CallsUnresolved int
	// References counts the edges from a declaration to a function it
	// names as a value, not calling it.
	References int
	// Bases counts the implements edges from a class to a class it
	// inherits from (#249); BasesOutside the bases resolved outside the
	// files read (Exception, unittest.TestCase), BasesUnplaced those
	// inside to no class with a node (a parameter, a value), and
	// BasesUnresolved those resolved to nothing.
	Bases, BasesOutside, BasesUnplaced, BasesUnresolved int
	// Workers is how many processes resolved the calls; WorkerErrors the
	// workers that failed, whose files the build then read itself.
	Workers      int
	WorkerErrors []string
	// Skipped are the files no ID can name, could not be read, or do not
	// parse, and the entries the walk could not open (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
	// Pending counts the files whose calls are still the warm-up pass's,
	// INFERRED by name, and Live is jedi reading them in the background:
	// both with Options.Background only.
	Pending int
	Live    *Live
	// Reused counts the files that kept jedi's answers from
	// Options.Reuse.
	Reused int
}

// Extract reads every Python file under root and adds the layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("py: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real // jedi reports resolved paths; the walk must agree
	}
	x := &extractor{b: b, m: b.Minter(), root: abs, report: &Report{}, emitted: map[graph.ID]bool{}}
	files, err := x.discover(opts)
	if err != nil {
		return nil, fmt.Errorf("py: discover under %s: %w", abs, err)
	}
	for _, f := range files {
		if err := x.file(f.Rel); err != nil {
			return nil, err
		}
	}
	switch {
	case len(files) == 0 || !python.Available():
	case opts.Background:
		if err := x.background(files, opts); err != nil {
			return nil, err
		}
	default:
		if err := x.analyze(files, opts); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

type extractor struct {
	b       *graph.Builder
	m       *graph.Minter
	root    string
	report  *Report
	emitted map[graph.ID]bool
	// read is the files read, by repo-relative path; inferred those whose
	// answers are the warm-up pass's, by name alone (#269).
	read, inferred map[string]bool
	// decls maps a file, a line and a name to the declaration there, for
	// a call's target.
	decls map[declKey]graph.ID
}

type declKey struct {
	file string
	line int
	name string
}

// discover walks the tree for the files the layer reads.
func (x *extractor) discover(opts Options) ([]python.File, error) {
	var out []python.File
	err := filepath.WalkDir(x.root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if path == x.root {
				return err
			}
			x.skip(path, err)
			return nil
		}
		if e.IsDir() {
			return x.enter(path, e.Name(), opts)
		}
		if !Source(e.Name()) {
			return nil
		}
		rel, err := x.m.Rel(path)
		if err == nil {
			_, err = x.m.PyID(rel)
		}
		if err != nil {
			x.skip(path, err)
			return nil
		}
		info, err := e.Info()
		if err != nil {
			x.skip(path, err)
			return nil
		}
		out = append(out, python.File{Rel: rel, Size: info.Size()})
		return nil
	})
	slices.SortFunc(out, func(a, b python.File) int { return strings.Compare(a.Rel, b.Rel) })
	return out, err
}

// enter decides whether the walk descends into a directory.
func (x *extractor) enter(path, name string, opts Options) error {
	if path == x.root {
		return nil
	}
	switch {
	case SkipDirs[name], strings.HasSuffix(name, ".egg-info"), opts.Skip.SkipsDir(x.root, path):
		return filepath.SkipDir
	case !opts.Testdata && name == "testdata":
		x.report.Testdata += count(path)
		return filepath.SkipDir
	}
	return nil
}

func count(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && Source(e.Name()) {
			n++
		}
		return nil
	})
	return n
}

func (x *extractor) skip(file string, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, 0, err))
}

// file adds a file's node.
func (x *extractor) file(rel string) error {
	id, err := x.m.PyID(rel)
	if err != nil {
		return fmt.Errorf("py: %w", err)
	}
	x.report.Files++
	attrs := map[string]string{}
	if !python.Available() {
		x.report.Unanalyzed++
		attrs["calls_unavailable"] = "its declarations and calls are not read: " + python.ErrUnavailable.Error()
	}
	return x.b.AddNode(graph.Node{ID: id, Kind: graph.KindModule, Name: filepath.Base(rel), Pos: graph.Position{File: rel}, Attrs: attrs})
}

// analyzeFiles is python.Analyze; a variable for the tests, which make a
// worker fail.
var analyzeFiles = python.Analyze

// analyze adds what jedi found: declarations, imports and calls.
func (x *extractor) analyze(files []python.File, opts Options) error {
	workers := opts.Workers
	if workers == 0 {
		workers = DefaultWorkers()
	}
	r, err := analyzeFiles(x.root, files, workers)
	if err != nil {
		return fmt.Errorf("py: %w", err)
	}
	return x.emit(files, r, nil)
}

// emit adds a result's declarations and edges; an edge from a file in
// inferred is the warm-up pass's, INFERRED, and the rest are jedi's.
func (x *extractor) emit(files []python.File, r *python.Result, inferred map[string]bool) error {
	x.inferred = inferred
	x.report.Workers, x.report.WorkerErrors = r.Workers, r.WorkerErrors
	// The graph is the same either way, so a failed worker shows only as a
	// slower build (on agent-config, 70 s for 14): the skipped entries are
	// where a reader learns why.
	failed := map[string]int{}
	for _, e := range r.WorkerErrors {
		failed[e]++
	}
	for _, e := range slices.Sorted(maps.Keys(failed)) {
		x.skip(x.root, fmt.Errorf("%d of %d Python workers failed and their files were read in this process instead, more slowly: %s", failed[e], r.Workers, e))
	}
	x.read = map[string]bool{}
	for _, f := range files {
		x.read[f.Rel] = true
	}
	for _, rel := range slices.Sorted(maps.Keys(r.Skipped)) {
		x.report.Unparsed++
		x.skip(filepath.Join(x.root, filepath.FromSlash(rel)), errors.New("does not parse: "+r.Skipped[rel]+"; its declarations and calls are not read"))
	}
	if err := x.declarations(r.Decls); err != nil {
		return err
	}
	if err := x.imports(r); err != nil {
		return err
	}
	if err := x.calls(r); err != nil {
		return err
	}
	if err := x.references(r); err != nil {
		return err
	}
	return x.bases(r)
}
