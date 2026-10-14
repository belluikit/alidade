// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkipDirs are the directories the layer never descends into, by name:
// dependencies and build caches carry copies of schemas that are not the
// repository's own. The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".tmp": true}

// A corpus is what discovery found: the .proto files to compile, and the
// include roots their import paths are relative to.
type corpus struct {
	// skip are the directories left out besides SkipDirs.
	skip  *scope.Dirs
	files []string // absolute, sorted
	roots []string // absolute, deepest first, the repository root last
	// inferred are the roots no buf configuration declares, inferred from
	// import statements for a subtree that has none.
	inferred map[string]bool
	// testdata counts .proto files left out under testdata directories.
	testdata int
	// unread are the entries the walk could not open, passed over.
	unread []unread
}

type unread struct {
	path string
	err  error
}

// discover walks root for .proto files and buf configuration. Include
// roots come from three places, in the order buf itself would honour
// them: every module of every buf.yaml in the tree, every directory of a
// buf.work.yaml, and — for schema trees with no buf configuration at all,
// which test fixtures usually are — inference from the files' own import
// statements: a file importing "x/y.proto" makes the ancestor directory
// under which x/y.proto exists a root.
func discover(root string, skipTestdata bool, skip *scope.Dirs) (*corpus, error) {
	c := &corpus{skip: skip}
	bufFiles, err := c.walk(root, skipTestdata)
	if err != nil {
		return nil, err
	}
	slices.Sort(c.files)
	c.roots, c.inferred = assembleRoots(root, bufFiles, c.files)
	return c, nil
}

// walk collects the .proto files and buf configuration files under root.
func (c *corpus) walk(root string, skipTestdata bool) ([]string, error) {
	var bufFiles []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			c.unread = append(c.unread, unread{path, err})
			return nil
		}
		if d.IsDir() {
			return c.enter(root, path, d.Name(), skipTestdata)
		}
		switch {
		case strings.HasSuffix(d.Name(), ".proto"):
			c.files = append(c.files, path)
		case d.Name() == "buf.yaml" || d.Name() == "buf.work.yaml":
			bufFiles = append(bufFiles, path)
		}
		return nil
	})
	return bufFiles, err
}

// enter decides whether the walk descends into a directory.
func (c *corpus) enter(root, path, name string, skipTestdata bool) error {
	if path == root {
		return nil
	}
	if SkipDirs[name] || c.skip.SkipsDir(root, path) {
		return filepath.SkipDir
	}
	if skipTestdata && name == "testdata" {
		c.testdata += countProtos(path)
		return filepath.SkipDir
	}
	return nil
}

// assembleRoots merges the declared and inferred include roots, deepest
// first (so a file under a nested module is addressed relative to the
// nested one), ties by name, the repository root last.
func assembleRoots(root string, bufFiles, files []string) ([]string, map[string]bool) {
	set := map[string]bool{}
	for _, bf := range bufFiles {
		for _, r := range bufRoots(bf) {
			set[r] = true
		}
	}
	inferred := map[string]bool{}
	for _, r := range inferRoots(root, files) {
		if !set[r] {
			inferred[r] = true
		}
		set[r] = true
	}
	delete(set, root)
	roots := make([]string, 0, len(set)+1)
	for r := range set {
		roots = append(roots, r)
	}
	slices.Sort(roots)
	slices.SortStableFunc(roots, func(a, b string) int { return len(b) - len(a) })
	return append(roots, root), inferred
}

// countProtos counts the .proto files under a directory left out of the
// corpus, so the report can say what was skipped.
func countProtos(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".proto") {
			n++
		}
		return nil
	})
	return n
}

// bufRoots returns the include roots a buf.yaml or buf.work.yaml declares:
// v2 modules relative to the file's directory, a v1 buf.yaml's own
// directory, a buf.work.yaml's directories.
func bufRoots(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(file)
	var cfg struct {
		Modules []struct {
			Path string `yaml:"path"`
		} `yaml:"modules"`
		Directories []string `yaml:"directories"`
	}
	if yaml.Unmarshal(b, &cfg) != nil {
		return nil
	}
	var roots []string
	for _, m := range cfg.Modules {
		if m.Path != "" {
			roots = append(roots, filepath.Join(dir, filepath.FromSlash(m.Path)))
		}
	}
	for _, d := range cfg.Directories {
		roots = append(roots, filepath.Join(dir, filepath.FromSlash(d)))
	}
	if len(roots) == 0 && filepath.Base(file) == "buf.yaml" {
		roots = append(roots, dir)
	}
	return roots
}

var importRE = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"`)

// inferRoots finds include roots from import statements: for each import
// of each file, the nearest ancestor directory of the file under which
// the imported path exists. Reading the import lines is discovery, not
// parsing — the compiler still decides what the file means.
func inferRoots(root string, files []string) []string {
	var roots []string
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range importRE.FindAllSubmatch(b, -1) {
			imp := filepath.FromSlash(string(m[1]))
			for dir := filepath.Dir(f); strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
				if isFile(filepath.Join(dir, imp)) {
					if !seen[dir] {
						seen[dir] = true
						roots = append(roots, dir)
					}
					break
				}
				if dir == root {
					break
				}
			}
		}
	}
	return roots
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// importPath returns the path a file is imported by: relative to the
// deepest include root containing it, forward-slashed.
// rootsFor orders the include roots for compiling one file: the root it
// lies in first — the deepest that holds it — then the others, deepest
// first. Two modules may hold the same import path (steward keeps a copy
// of a schema in a second module); searched deepest first for every
// file, the shallower module's copy compiled as the deeper one's, and
// its declarations were never read (#97). A file sees its own module
// first, as buf compiles a module.
//
// A file inside a buf module does not search the roots inferred for other
// subtrees: buf compiles a module against its workspace and its
// dependencies, not against a directory no configuration names. Searched,
// they made protocompile's module files import its copies of the
// well-known types under wellknownimports/fs, where buf takes its own
// (the proto oracle, #148). A file outside every module keeps them: that
// is what they were inferred for.
func rootsForFile(roots []string, inferred map[string]bool, file string) []string {
	ordered := rootsFor(roots, file)
	if len(ordered) == 0 || inferred[ordered[0]] || ordered[0] == roots[len(roots)-1] {
		return ordered
	}
	if rel, err := filepath.Rel(ordered[0], file); err != nil || strings.HasPrefix(rel, "..") {
		return ordered // under no root but the repository's
	}
	return slices.DeleteFunc(ordered, func(r string) bool { return inferred[r] })
}

func rootsFor(roots []string, file string) []string {
	for i, r := range roots {
		if rel, err := filepath.Rel(r, file); err == nil && !strings.HasPrefix(rel, "..") {
			out := append([]string{r}, roots[:i]...)
			return append(out, roots[i+1:]...)
		}
	}
	return roots
}

func importPath(roots []string, file string) string {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, file); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Base(file))
}

// locate returns the file on disk an import path names, searching the
// include roots in order, or "" if none has it.
func locate(roots []string, imp string) string {
	for _, r := range roots {
		if p := filepath.Join(r, filepath.FromSlash(imp)); isFile(p) {
			return p
		}
	}
	return ""
}
