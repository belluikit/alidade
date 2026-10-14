// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package goasm

import (
	"bufio"
	"fmt"
	"github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/trendvidia/goasm/gen"
	"github.com/trendvidia/goasm/template"

	"github.com/trendvidia/alidade/internal/graph"
)

// Options controls the goasm layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Testdata includes .goasm files under testdata directories; out by
	// default, as for the other layers.
	Testdata bool
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// A Report says what Extract found.
type Report struct {
	// Files is how many .goasm files were read; Failed how many the
	// parser rejected (each keeps a node carrying the error).
	Files, Failed int
	// Templates counts template nodes.
	Templates int
	// Directives counts "//go:generate" lines that run goasm with an
	// -in and an -out; each declares which package one template file
	// generates into.
	Directives int
	// Linked counts templates given a generates edge; Unlinked those of
	// a file no directive names, or of a repository with no go.mod.
	Linked, Unlinked int
	// Testdata counts .goasm files skipped under testdata directories.
	Testdata int
	// Skipped are the files no ID can name or that could not be read,
	// and the entries the walk could not open (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
}

// receiver is the type goasm generates every emitter method on. It is
// fixed in goasm's generator rather than exported from it, and the
// fixture's generated file pins it: a change there fails the test.
const receiver = "Emitter"

// SkipDirs are the directories the layer never descends into, by name.
// The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".tmp": true}

// Extract parses every .goasm file under root and adds the layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("goasm: %w", err)
	}
	files, goFiles, testdata, unread, err := discover(abs, !opts.Testdata, opts.Skip)
	if err != nil {
		return nil, fmt.Errorf("goasm: discover under %s: %w", abs, err)
	}
	x := &extractor{b: b, m: b.Minter(), root: abs, report: &Report{Files: len(files), Testdata: testdata}}
	for _, u := range unread {
		x.skip(u.path, 0, u.err)
	}
	if len(files) > 0 {
		x.targets, x.report.Directives, unread = directives(abs, goFiles)
		for _, u := range unread {
			x.skip(u.path, 0, u.err)
		}
	}
	for _, f := range files {
		if err := x.file(f); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

// discover lists every .goasm file under root, sorted, every .go file
// (where a directive may be), and counts .goasm files skipped under
// testdata. An entry the walk cannot open is returned in unread and
// passed over.
func discover(root string, skipTestdata bool, skip *scope.Dirs) (files, goFiles []string, testdata int, unread []unreadPath, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			unread = append(unread, unreadPath{path, err})
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if SkipDirs[d.Name()] || skip.SkipsDir(root, path) {
				return filepath.SkipDir
			}
			if skipTestdata && d.Name() == "testdata" {
				testdata += countGoasm(path)
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(d.Name(), ".goasm"):
			files = append(files, path)
		case strings.HasSuffix(d.Name(), ".go"):
			goFiles = append(goFiles, path)
		}
		return nil
	})
	slices.Sort(files)
	slices.Sort(goFiles)
	return files, goFiles, testdata, unread, err
}

// An unreadPath is an entry the walk could not open, or a file that could
// not be read.
type unreadPath struct {
	path string
	err  error
}

func countGoasm(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".goasm") {
			n++
		}
		return nil
	})
	return n
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	root   string
	report *Report
	// targets maps a template file's absolute path to the package and
	// architecture a directive says it generates into.
	targets map[string]target
}

// skip records that something in file, at line, was left out.
func (x *extractor) skip(file string, line int, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, line, err))
}

// file parses one template file and emits it. A file no ID can name, or
// that cannot be read, is skipped and recorded (#92).
func (x *extractor) file(path string) error {
	id, err := x.m.GoasmID(path, "")
	if err != nil {
		x.skip(path, 0, err)
		return nil
	}
	tgt, linked := x.targets[path]
	attrs := map[string]string{}
	if linked {
		attrs["package"] = tgt.pkg
		if tgt.arch != "" {
			attrs["arch"] = tgt.arch
		}
	}
	templates, aliases, perr := template.ParseFile(path)
	if perr != nil {
		x.report.Failed++
		attrs["error"] = firstLine(perr.Error())
		return x.b.AddNode(graph.Node{ID: id, Kind: graph.KindGoasmFile, Name: filepath.Base(path),
			Pos: graph.Position{File: path}, Attrs: attrs})
	}
	if s := aliasList(aliases); s != "" {
		attrs["aliases"] = s
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindGoasmFile, Name: filepath.Base(path),
		Pos: graph.Position{File: path}, Attrs: attrs}); err != nil {
		return err
	}
	lines, err := templateLines(path)
	if err != nil {
		x.skip(path, 0, err)
		return nil
	}
	for _, t := range templates {
		if err := x.template(id, path, t, lines[t.Name], tgt, linked); err != nil {
			return err
		}
	}
	return nil
}

// template emits one template: its node, the contains edge from its
// file, and the generates edge to its emitter when the file has a target.
func (x *extractor) template(file graph.ID, path string, t template.Template, line int, tgt target, linked bool) error {
	id, err := x.m.GoasmID(path, t.Name)
	if err != nil {
		x.skip(path, line, err)
		return nil
	}
	x.report.Templates++
	attrs := map[string]string{"instructions": strconv.Itoa(len(t.Instrs))}
	if s := paramList(t.Params); s != "" {
		attrs["params"] = s
	}
	if s := paramList(t.Returns); s != "" {
		attrs["returns"] = s
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindTemplate, Name: t.Name,
		Pos: graph.Position{File: path, Line: line}, Attrs: attrs}); err != nil {
		return err
	}
	if err := x.b.AddEdge(graph.Edge{From: file, To: id, Kind: graph.KindContains, Derivation: graph.Extracted}); err != nil {
		return err
	}
	if !linked {
		x.report.Unlinked++
		return nil
	}
	to, err := x.m.GoID(tgt.pkg, receiver+"."+gen.MethodName(t))
	if err != nil {
		x.skip(path, line, err)
		return nil
	}
	x.report.Linked++
	edge := map[string]string{"via": "goasm"}
	if tgt.arch != "" {
		edge["arch"] = tgt.arch // none for a directive that names no architecture of its own
	}
	return x.b.AddEdge(graph.Edge{From: id, To: to, Kind: graph.KindGenerates, Derivation: graph.Declared, Attrs: edge})
}

// templateLines maps each template name to the line its "# TEMPLATE"
// marker is on, for the node's position. goasm's parser reports no
// positions; this is a scan for the marker, not a second parse.
func templateLines(path string) (map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lines := map[string]int{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "# TEMPLATE ")
		if !ok {
			continue
		}
		name := rest
		if i := strings.IndexByte(rest, '('); i >= 0 {
			name = rest[:i]
		}
		name = strings.TrimSpace(name)
		if _, seen := lines[name]; !seen {
			lines[name] = n
		}
	}
	return lines, sc.Err()
}

// paramList renders parameters the way the template declares them.
func paramList(ps []template.Parameter) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.Name+" "+p.Type)
	}
	return strings.Join(parts, ", ")
}

// aliasList renders "# ALIAS" bindings as "NAME=REG" pairs, sorted.
func aliasList(aliases map[string]string) string {
	parts := make([]string, 0, len(aliases))
	for k, v := range aliases {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
