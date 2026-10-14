// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"errors"
	"fmt"
	"github.com/belluikit/alidade/internal/scope"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/belluikit/alidade/internal/graph"
)

// A Module is one go.mod under a root: the directory it sits in, absolute,
// and the module path it declares.
type Module struct {
	Dir, Path string
}

// skipDirs are the directories no module is looked for in: what ADR 003
// keeps out of the corpus (.git, node_modules, vendor) and what the go
// tool's own package patterns never descend into (testdata; names
// beginning with "." or "_" are skipped in [Modules] too).
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true}

// Modules lists the Go modules under root: every go.mod, at root or
// below it, is a module of its own, and a repository that builds
// per module — a nested module with no go.work joining it to the root's,
// which the go tool loads only from its own directory — is read the same
// way (#67). The root's module, when there is one, comes first; the rest
// follow in directory order.
//
// A go.mod that declares no module path, or cannot be read, names no
// module; it is returned in skipped, repo-relative, and passed over
// (#92).
func Modules(root string, skip *scope.Dirs) (mods []Module, skipped []graph.Skip, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil && p == root:
			return err
		case err != nil:
			// A directory the walk cannot open is passed over: the
			// layers that read its files record it (#92), and a module
			// in it could not be loaded either.
			return nil
		case d.IsDir():
			if p != root && (skipModuleDir(d.Name()) || skip.SkipsDir(root, p)) {
				return filepath.SkipDir
			}
			return nil
		case d.Name() != "go.mod":
			return nil
		}
		mp, err := modulePathOf(p)
		if err != nil {
			rel, _ := filepath.Rel(root, p)
			skipped = append(skipped, graph.Skip{File: filepath.ToSlash(rel), Reason: graph.SkipReason(err)})
			return nil
		}
		mods = append(mods, Module{Dir: filepath.Dir(p), Path: mp})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("golang: modules under %s: %w", root, err)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Dir < mods[j].Dir })
	return mods, skipped, nil
}

// skipModuleDir reports whether the module walk leaves a directory out:
// the go tool's own exclusions and the dependency directories.
func skipModuleDir(name string) bool {
	return skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// modulePathOf reads the module path a go.mod declares.
func modulePathOf(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	if mp := modfile.ModulePath(data); mp != "" {
		return mp, nil
	}
	return "", errors.New("declares no module path")
}

// Owner returns which of the graph's module paths a package path belongs
// to — the longest one it sits under, since a nested module's path
// extends its parent's — or "" for a package of no module in the graph.
//
// The standard library is the one module whose packages are not under
// its path: the module is std, its packages bytes and net/http. In a
// checkout of the Go repository, which loads std as a module of the tree,
// std owns a standard-library path no other module of the graph does —
// one whose first element has no dot, the go tool's own rule — so its
// packages are the tree's rather than external (#217). C, cgo's
// pseudo-package, is no package of any module.
func Owner(pkgPath string, modules []string) string {
	best := ""
	for _, m := range modules {
		if (pkgPath == m || strings.HasPrefix(pkgPath, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" && pkgPath != "C" && standardPath(pkgPath) && slices.Contains(modules, "std") {
		return "std"
	}
	return best
}

// owner is Owner, for the layer's own callers.
func owner(pkgPath string, modules []string) string { return Owner(pkgPath, modules) }

// standardPath reports whether an import path is the standard library's
// by the go tool's rule: its first element has no dot.
func standardPath(pkgPath string) bool {
	first, _, _ := strings.Cut(pkgPath, "/")
	return first != "" && !strings.Contains(first, ".")
}

var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// moduleName is the name the package node of a module that did not load
// carries: the last element of its path, skipping a major-version suffix,
// which is what its root package would most likely be called.
func moduleName(modPath string) string {
	name := path.Base(modPath)
	if majorSuffix.MatchString(name) {
		name = path.Base(path.Dir(modPath))
	}
	return name
}

// loadError renders a load error on one line for a node attribute: the
// go tool's "# package" headers go, and the lines that remain — the
// compiler's own messages — are joined. A packages.Error carries the
// same text with go/packages' "-: " for "no position" in front of it, so
// that prefix goes first and the header behind it is still recognized;
// otherwise a failed package's error would read "-: # p [p.test]; …"
// rather than the compiler's message (#95).
func loadError(err error) string {
	var lines []string
	for _, l := range strings.Split(err.Error(), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-: "))
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "; ")
}
