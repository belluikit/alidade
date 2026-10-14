// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trendvidia/caliper/pkg/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/trendvidia/alidade/internal/graph"
)

// failedPackages emits the packages of the module the load dropped for
// errors (caliper#38): the package of a repository in the middle of an
// edit, which until now vanished with no record, its importers' edges
// into it dropped as unknown_target or never made (#95).
//
// Each gets its package node carrying the compiler's first error, and the
// declarations the type checker resolved, marked partial: calls into the
// package then land on nodes, and a reader can tell the package is there,
// broken, from one that does not exist. What it cannot have is calls out
// of it, since the SSA program leaves an ill-typed package out.
//
// Report.FailedPackages names them by package path, as
// Report.WithoutCalls does and as a node ID carries: a packages.Package
// ID is not one — with tests it reads "p [p.test]", which no node has and
// which the space-joined go_packages_failed field cannot hold. When the
// failed variant's path already has a node from a variant that loaded —
// the plain package, when only its test variant failed — the
// declarations are not emitted again, but the error still reaches that
// node, so every path named here answers get_node with its error.
func (x *extractor) failedPackages(emitted []*packages.Package) error {
	done := make(map[string]bool, len(emitted))
	for _, p := range emitted {
		done[p.PkgPath] = true
	}
	for _, pkg := range failedTargets(x.eng.Failed()) {
		if !x.inModule(pkg.PkgPath) {
			continue
		}
		x.report.FailedPackages = append(x.report.FailedPackages, pkg.PkgPath)
		if err := x.failedPackage(pkg, done[pkg.PkgPath]); err != nil {
			return err
		}
	}
	return nil
}

// failedPackage emits one failed package. The error goes on the package
// node that [extractor.packageNode] mints rather than on a second
// contribution of its own, so that a further configuration's pass — which
// adds only what the host's did not, and drops a repeat of a node it
// already has (#43) — cannot lose it.
func (x *extractor) failedPackage(pkg *packages.Package, emitted bool) error {
	x.pkgError = loadError(pkg.Errors[0])
	defer func() { x.pkgError = "" }()
	if emitted {
		// The path's node and declarations are already in the graph from
		// the variant that loaded; only the error is new, and node
		// attributes fold.
		id, err := x.m.GoID(pkg.PkgPath, "")
		if err != nil {
			return fmt.Errorf("golang: %w", err)
		}
		return x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: pkg.Name,
			Test: x.testPkg(pkg), Attrs: map[string]string{"error": x.pkgError}})
	}
	if pkg.Types == nil || pkg.TypesInfo == nil {
		_, err := x.packageNode(pkg)
		return err
	}
	x.partial = true
	defer func() { x.partial = false }()
	return x.declarations(pkg)
}

// failedTargets keeps one variant of each failed package, as
// targetPackages does of the loaded ones: the one with the most files.
func failedTargets(all []*packages.Package) []*packages.Package {
	byPath := make(map[string]*packages.Package)
	for _, p := range all {
		if len(p.Errors) == 0 {
			continue
		}
		if have, ok := byPath[p.PkgPath]; !ok || len(p.GoFiles) > len(have.GoFiles) {
			byPath[p.PkgPath] = p
		}
	}
	out := make([]*packages.Package, 0, len(byPath))
	for _, p := range byPath {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
	return out
}

// withoutCalls marks the packages of the module the call graph leaves out
// because one they import, directly or not, has an error outside a
// function body (caliper#38): they loaded and type-checked, and their
// declarations are in the graph, but none of their calls are. The package
// node names the cause, and Report.WithoutCalls lists them, so an empty
// callers list there reads as unknown rather than as none (#95).
func (x *extractor) withoutCalls(skipped []analysis.SkippedPackage) error {
	seen := map[string]bool{}
	slices.SortFunc(skipped, func(a, b analysis.SkippedPackage) int {
		return cmp.Compare(a.Package.ID, b.Package.ID)
	})
	for _, s := range skipped {
		pkg := s.Package
		if !x.inModule(pkg.PkgPath) || seen[pkg.PkgPath] {
			continue
		}
		seen[pkg.PkgPath] = true
		id, err := x.m.GoID(pkg.PkgPath, "")
		if err != nil {
			return err
		}
		cause := "a package it imports"
		if s.Cause != nil {
			cause = s.Cause.PkgPath
		}
		x.report.WithoutCalls = append(x.report.WithoutCalls, pkg.PkgPath)
		if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: pkg.Name, Test: x.testPkg(pkg),
			Attrs: map[string]string{"calls_unavailable": cause}}); err != nil {
			return err
		}
	}
	return nil
}

// unloadedGoFiles lists the Go files of a module in directories no
// package of the load came from, loaded or failed: a directory whose
// every file the host's build constraints exclude, which the ./...
// pattern never matches, so neither caliper's packages nor its failed
// ones name it (caliper#38's own limit). Those files are what
// -go-configs auto exists to recover, and without it they are counted
// as excluded rather than missing silently (#95). Nested modules,
// testdata, vendor and the directories the go tool ignores are not the
// module's; test files count only with tests.
func unloadedGoFiles(lm loadedModule, opts Options, root string) []string {
	tests := opts.Tests
	loaded := loadedDirs(lm.eng)
	var out []string
	_ = filepath.WalkDir(lm.Dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir() && p != lm.Dir && (skipModuleDir(d.Name()) || hasGoMod(p) || opts.Skip.SkipsDir(root, p)):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, ".go") || loaded[filepath.Dir(p)]:
			return nil
		case tests || !strings.HasSuffix(p, "_test.go"):
			out = append(out, p)
		}
		return nil
	})
	return out
}

// loadedDirs is the set of directories some package of the load, loaded
// or failed, came from.
func loadedDirs(eng *analysis.Engine) map[string]bool {
	out := map[string]bool{}
	for _, p := range append(eng.Packages(), eng.Failed()...) {
		for _, f := range append(append([]string{}, p.GoFiles...), p.IgnoredFiles...) {
			out[filepath.Dir(f)] = true
		}
	}
	return out
}

func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// unloadedDirs records, once per directory, the directories of unloaded
// files that no further configuration brought in: a package the host
// cannot build, which no node can say is missing since there is none.
// The reason says whether another platform would build it — what
// -go-configs auto loads — or only a build tag would, a custom one or
// "ignore", which no configuration alidade loads sets.
//
// A directory one chosen configuration does bring in is not skipped,
// however many of its other files stay excluded: its package is in the
// graph, and those files are named in the package node's "excluded"
// attribute. Neither is a file no constraint could be read from, which
// is already skipped on its own and whose constraint is unknown rather
// than exclusive. The reason names no GOOS/GOARCH: a reason reaches the
// build's output, where something machine-specific makes two builds of
// one tree disagree (see graph.SkipReason), and go_build already says
// which platform this build read.
func unloadedDirs(m *graph.Minter, unloaded []string, covered map[string]config, unread map[string]error) []graph.Skip {
	byDir := map[string][]string{}
	recovered := map[string]bool{}
	seen := map[string]bool{}
	var dirs []string
	for _, f := range unloaded {
		d := filepath.Dir(f)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
		if _, ok := covered[f]; ok {
			recovered[d] = true
			continue
		}
		if _, ok := unread[f]; ok {
			continue
		}
		byDir[d] = append(byDir[d], f)
	}
	out := make([]graph.Skip, 0, len(dirs))
	for _, d := range dirs {
		if recovered[d] || len(byDir[d]) == 0 {
			continue
		}
		reason := "build constraints exclude every Go file for the host platform, and every other platform's too: only a build tag includes them"
		if anyPlatformBuilds(byDir[d]) {
			reason = "build constraints exclude every Go file for the host platform; -go-configs auto would load another platform's"
		}
		out = append(out, m.Skip(d, 0, errors.New(reason)))
	}
	return out
}

// anyPlatformBuilds reports whether some candidate configuration
// includes one of the files.
func anyPlatformBuilds(files []string) bool {
	for _, f := range files {
		fc, err := readConstraint(f)
		if err != nil {
			continue
		}
		for _, c := range candidates {
			if fc.satisfied(c) {
				return true
			}
		}
	}
	return false
}
