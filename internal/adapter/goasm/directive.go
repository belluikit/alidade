// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package goasm

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// A target is what a "//go:generate" directive declares for one template
// file: the package its generated emitter lands in, and the architecture.
type target struct {
	pkg  string
	arch string
}

// directives reads every "//go:generate" line in goFiles that runs goasm
// with an -in and an -out, and maps each -in file to its target. The
// first directive naming a file wins. With no go.mod at root there is no
// package to name, so nothing is linked.
//
// A Go file that cannot be read is returned in skipped and passed over.
func directives(root string, goFiles []string) (targets map[string]target, n int, skipped []unreadPath) {
	mods := moduleFinder{root: root, seen: map[string]module{}}
	targets = map[string]target{}
	for _, f := range goFiles {
		ds, err := scanDirectives(f)
		if err != nil {
			skipped = append(skipped, unreadPath{f, err})
			continue
		}
		dir := filepath.Dir(f)
		for _, d := range ds {
			n++
			out := filepath.Dir(filepath.Join(dir, d.out))
			mod := mods.of(out)
			if mod.path == "" {
				continue
			}
			pkg := goPackage(mod.dir, mod.path, out)
			if pkg == "" {
				continue
			}
			in := filepath.Clean(filepath.Join(dir, d.in))
			if _, seen := targets[in]; !seen {
				targets[in] = target{pkg: pkg, arch: d.arch}
			}
		}
	}
	return targets, n, skipped
}

type directive struct {
	arch, in, out string
}

// scanDirectives returns the goasm directives of one Go file: a
// "//go:generate" line whose command names goasm and carries -in and
// -out. Flags are read as "-in x" or "-in=x"; the command in front of
// them is not interpreted, so a pinned "go run …/cmd/goasm@v1" and a
// relative "../goasm/goasm" read the same.
func scanDirectives(path string) ([]directive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []directive
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "//go:generate ")
		if !ok || !strings.Contains(rest, "goasm") {
			continue
		}
		d := parseDirective(generateArgs(rest), filepath.Base(path))
		if d.in != "" && d.out != "" {
			out = append(out, d)
		}
	}
	return out, sc.Err()
}

// parseDirective reads goasm's flags from a directive's words. go
// generate expands $GOFILE to the file's name; $GOARCH is the
// architecture of whoever runs it, which no reading of the tree knows, so
// an -arch of it states none (#99).
func parseDirective(fields []string, gofile string) directive {
	var d directive
	for i := 0; i < len(fields); i++ {
		name, val, joined := strings.Cut(fields[i], "=")
		if !joined && i+1 < len(fields) {
			val = fields[i+1]
		}
		switch name {
		case "-arch", "--arch":
			d.arch = val
		case "-in", "--in":
			d.in = val
		case "-out", "--out":
			d.out = val
		default:
			continue
		}
		if !joined {
			i++
		}
	}
	expand := func(v string) string {
		return os.Expand(v, func(name string) string {
			if name == "GOFILE" {
				return gofile
			}
			return "$" + name
		})
	}
	d.in, d.out = expand(d.in), expand(d.out)
	if strings.Contains(d.arch, "$") {
		d.arch = ""
	}
	return d
}

// generateArgs splits a directive's command line into words as go
// generate does: on spaces, with a double-quoted word taken whole and
// unquoted by Go's rules (#99).
func generateArgs(line string) []string {
	var out []string
	for line = strings.TrimSpace(line); line != ""; line = strings.TrimSpace(line) {
		if line[0] == '"' {
			if q, err := strconv.QuotedPrefix(line); err == nil {
				if w, err := strconv.Unquote(q); err == nil {
					out = append(out, w)
					line = line[len(q):]
					continue
				}
			}
		}
		end := strings.IndexAny(line, " \t")
		if end < 0 {
			end = len(line)
		}
		out = append(out, line[:end])
		line = line[end:]
	}
	return out
}

// A module is a go.mod's directory and the module path it declares.
type module struct{ dir, path string }

// moduleFinder answers, per directory, the module the nearest go.mod at
// or above it declares, up to the scan root: a repository whose Go lives
// below the root, as the Markdown layer already reads it (#68, #99).
type moduleFinder struct {
	root string
	seen map[string]module
}

func (f moduleFinder) of(dir string) module {
	if m, ok := f.seen[dir]; ok {
		return m
	}
	var m module
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		m = module{dir: dir, path: modfile.ModulePath(b)}
	} else if parent := filepath.Dir(dir); dir != f.root && parent != dir && strings.HasPrefix(parent, f.root) {
		m = f.of(parent)
	}
	f.seen[dir] = m
	return m
}

// goPackage returns the import path of a directory of the module: the
// module path plus the directory's path within it.
func goPackage(root, module, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	if rel == "." {
		return module
	}
	return module + "/" + filepath.ToSlash(rel)
}
