// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package rust is the Rust layer (ADR 009, #72): a file node per .rs file
// and, from the SCIP index the user's own rust-analyzer writes for each
// Cargo workspace, a node per item, contained by what holds it, and the
// calls and references between functions. A machine without cargo,
// rust-analyzer or the standard library's source has the file nodes, each
// saying its declarations and calls are not read and why.
package rust

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/scope"
)

// Options controls the Rust layer.
type Options struct {
	// Skip are the directories left out besides the layer's own:
	// .alidade.pxf's list, vendor and third_party by default.
	Skip *scope.Dirs
	// Testdata includes files under testdata directories; out by default.
	Testdata bool
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// SkipDirs are the directories the layer never enters, by name: version
// control, Cargo's build output, and the directories every layer skips.
var SkipDirs = map[string]bool{
	".git": true, "target": true, "node_modules": true, "vendor": true, ".tmp": true,
}

// Source reports whether the layer reads a file, by name.
func Source(name string) bool { return strings.HasSuffix(name, ".rs") }

// A Report says what Extract found.
type Report struct {
	// Files counts the file nodes, Testdata the files skipped under
	// testdata directories.
	Files, Testdata int
	// Workspaces are the Cargo workspaces indexed, by repo-relative root
	// ("." for the root itself).
	Workspaces []string
	// Engine is the rust-analyzer that indexed them, as its index names it.
	Engine string
	// Unanalyzed counts the files whose declarations and calls are not in
	// the graph: Unavailable says why when the toolchain is missing, and
	// otherwise no crate rust-analyzer indexed includes the file (a script
	// outside every workspace, a compile-fail case a test feeds rustc).
	Unanalyzed  int
	Unavailable string
	// Declarations counts the declaration nodes; MacroWritten the nodes of
	// the methods a derive or a macro writes, which the workspace calls
	// (Square[Clone].clone()), among them.
	Declarations, MacroWritten int
	// Of CallSites, the calls inside a function body to a function, Calls
	// are edges (distinct per caller and callee); the rest call a function
	// outside the workspace (CallsOutside: the standard library, a
	// dependency), one Cargo's build generated (CallsGenerated: not in the
	// index, ADR 009), or one of several declarations sharing a symbol
	// none of which is in the calling file (CallsUnplaced).
	CallSites, Calls, CallsOutside, CallsGenerated, CallsUnplaced int
	// References counts the edges from a function to a function it names
	// as a value, not calling it.
	References int
	// GeneratedFiles counts the files Cargo's build wrote that the
	// workspace includes, and Generated the nodes of their items, at each
	// include! (ADR 002 rule 16). Generates counts the edges from a .proto
	// message or enum to the struct or enum prost-build wrote for it;
	// GeneratesConflicting the ones left out because the code states
	// other names for itself.
	GeneratedFiles, Generated, Generates, GeneratesConflicting int
	// Implements counts the edges from a type to a workspace trait it
	// implements; ImplementsUnplaced the implementations with no edge: of
	// a type with no node (a type parameter, a slice, a type outside the
	// workspace), or by a name two workspace traits share.
	Implements, ImplementsUnplaced int
	// BuildScriptsFailed counts the packages whose build script failed,
	// each reported in Skipped with what it printed (#85): what it would
	// have written has no nodes, and calls into it count as
	// CallsGenerated.
	BuildScriptsFailed int
	// ServerColumns is how rust-analyzer's server counted columns, "utf-8"
	// or "utf-16"; "" when no server was asked.
	ServerColumns string
	// Skipped are the files no ID can name or could not be read, the
	// items whose name no ID can hold, the workspaces that could not be
	// indexed, and the entries the walk could not open (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
}

// Extract reads every Rust file under root and adds the layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("rust: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real // rust-analyzer reports resolved paths; the walk must agree
	}
	x := &extractor{b: b, m: b.Minter(), root: abs, report: &Report{}, emitted: map[graph.ID]bool{},
		docs: map[string]*doc{}, external: map[string]bool{}}
	files, manifests, err := x.discover(opts)
	if err != nil {
		return nil, fmt.Errorf("rust: discover under %s: %w", abs, err)
	}
	if len(files) == 0 {
		x.report.Elapsed = time.Since(start)
		return x.report, nil
	}
	if err := x.read(files, manifests); err != nil {
		return nil, err
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

// read has the workspaces indexed, when the toolchain is there, and adds
// the files, their items, the methods macros write, the calls and
// references, and the implementations.
func (x *extractor) read(files, manifests []string) error {
	x.reading = map[string]bool{}
	for _, f := range files {
		x.reading[f] = true
	}
	if len(manifests) > 0 {
		if x.report.Unavailable = toolchainMissing(x.root); x.report.Unavailable == "" {
			x.index(manifests)
		}
	}
	if err := x.files(files); err != nil {
		return err
	}
	defer x.closeServers()
	for _, step := range []func() error{x.declarations, x.generated, x.macroWritten, x.edges, x.implementations, x.buildScripts} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	root   string
	report *Report
	// reading are the files the layer has nodes for, by repo-relative
	// path; docs the index's documents of them.
	reading map[string]bool
	docs    map[string]*doc
	// external are the symbols an index lists without a definition: the
	// items Cargo's build generated (ADR 009).
	external map[string]bool
	emitted  map[graph.ID]bool
	// bySym maps a symbol to the declarations defining it: one per symbol
	// but in a package with several crate roots, whose tests, benches and
	// build script share its symbols (ADR 002 rule 16).
	bySym map[string][]*def
	// funcs are each document's functions with a node, what a body is;
	// defsOf all its items with a node.
	funcs, defsOf map[*doc][]*def
	// indexed are the roots of the workspaces rust-analyzer indexed, and
	// manifests their packages' Cargo.toml files, by root; servers its
	// servers on them, by root, once a step has asked.
	indexed   []string
	manifests map[string][]string
	servers   map[string]*lsp
}

// discover walks the tree for the files the layer reads and the Cargo
// manifests that say which workspaces to index.
func (x *extractor) discover(opts Options) (files, manifests []string, err error) {
	err = filepath.WalkDir(x.root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if path == x.root {
				return err
			}
			x.skip(path, 0, err)
			return nil
		}
		if e.IsDir() {
			return x.enter(path, e.Name(), opts)
		}
		if e.Name() == "Cargo.toml" {
			manifests = append(manifests, path)
			return nil
		}
		if !Source(e.Name()) {
			return nil
		}
		rel, err := x.m.Rel(path)
		if err == nil {
			_, err = x.m.RustID(rel)
		}
		if err != nil {
			x.skip(path, 0, err)
			return nil
		}
		files = append(files, rel)
		return nil
	})
	slices.Sort(files)
	slices.Sort(manifests)
	return files, manifests, err
}

// enter decides whether the walk descends into a directory.
func (x *extractor) enter(path, name string, opts Options) error {
	if path == x.root {
		return nil
	}
	switch {
	case SkipDirs[name], opts.Skip.SkipsDir(x.root, path):
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

func (x *extractor) skip(file string, line int, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, line, err))
}

// files adds a node per file; one the index did not cover says why its
// declarations and calls are not read.
func (x *extractor) files(files []string) error {
	for _, rel := range files {
		id, err := x.m.RustID(rel)
		if err != nil {
			return fmt.Errorf("rust: %w", err)
		}
		x.report.Files++
		attrs := map[string]string{}
		if x.docs[rel] == nil {
			x.report.Unanalyzed++
			why := "no crate rust-analyzer indexed includes the file: it is outside every Cargo workspace, or no target or module declaration names it"
			if x.report.Unavailable != "" {
				why = x.report.Unavailable
			}
			attrs["calls_unavailable"] = "its declarations and calls are not read: " + why
		}
		if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindModule, Name: filepath.Base(rel), Pos: graph.Position{File: rel}, Attrs: attrs}); err != nil {
			return err
		}
	}
	return nil
}
