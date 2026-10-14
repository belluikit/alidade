// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"bytes"
	"context"
	"fmt"
	"github.com/trendvidia/alidade/internal/scope"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/trendvidia/alidade/internal/graph"
)

// search_text (#178) is a search of the working tree's text that
// answers what the graph cannot — a string literal, a log message, a
// comment — and gives each hit the declaration containing it, as a node
// the traversal tools take. It is the one analysis alidade performs of
// its own (ADR 000, amended 2026-10-08): it stays outside the graph, and
// matches bytes rather than parsing a format.

// textSkipDirs are the directory names no layer reads: the ones in every
// layer's SkipDirs, and testdata, which every layer leaves out by
// default. A name only some layers skip is searched (#275): dist and
// build are the frontend's build output, and internal/build is a Go
// package. Installed packages and .alidade.pxf's list are left out by
// [scope.Dirs.SkipsDir], as every layer leaves them out.
var textSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".tmp": true, "testdata": true,
}

// SearchSkips reports whether search_text leaves a directory of this
// name out: a search there is one only grep can make (#208).
func SearchSkips(name string) bool { return textSkipDirs[name] }

// maxTextFile is the largest file searched; a bigger one is data, not
// source.
const maxTextFile = 2 << 20

// registerTextSearch registers search_text.
func (s *Server) registerTextSearch() {
	s.addTool(mcpsdk.NewTool("search_text",
		mcpsdk.WithDescription("Search the files' text — a string literal, a log message, a comment — for what the graph does not hold. Each hit names the declaration containing it as a node for the traversal tools; guess marks one placed by position alone. A cut answer gives next: pass it as after to continue; total counts every hit."),
		mcpsdk.WithString("pattern", mcpsdk.Required(), mcpsdk.Description("The text to find; a regular expression with regex.")),
		mcpsdk.WithBoolean("regex", mcpsdk.DefaultBool(false), mcpsdk.Description("Read pattern as a Go regular expression.")),
		mcpsdk.WithString("path", mcpsdk.Description("Only files whose repo-relative path starts with this.")),
		mcpsdk.WithBoolean("files_only", mcpsdk.DefaultBool(false), mcpsdk.Description("List the matching files with their hit counts, not the hits.")),
		mcpsdk.WithBoolean("exclude_tests", mcpsdk.DefaultBool(false), mcpsdk.Description("Leave out test files: _test.go, *.test.*, *.spec.*.")),
		mcpsdk.WithString("after", mcpsdk.Description("Continue after this file:line, the next of a cut answer.")),
		withBudget(),
		mcpsdk.WithReadOnlyHintAnnotation(true),
	), s.handleSearchText)
}

// A textHit is one matching line.
type textHit struct {
	At    string `json:"at"`
	Text  string `json:"text"`
	In    any    `json:"in,omitempty"`
	Guess bool   `json:"guess,omitempty"`
}

type textResult struct {
	Hits    []textHit   `json:"hits,omitempty"`
	Matches []fileCount `json:"matches,omitempty"`
	Nodes   []nodeOut   `json:"nodes,omitempty"`
	// Files counts the files searched; Total the hits and Matched the
	// files with one, all of them, on the page or not.
	Files     int    `json:"files"`
	Total     int    `json:"total"`
	Matched   int    `json:"matched"`
	Truncated bool   `json:"truncated,omitempty"`
	Next      string `json:"next,omitempty"`
	Tokens    int    `json:"tokens"`
}

// A fileCount is a matching file and its hits, for files_only.
type fileCount struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

func (s *Server) handleSearchText(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, info, err := s.graph(ctx)
	if err != nil {
		return nil, err
	}
	match, err := textMatcher(req.GetString("pattern", ""), req.GetBool("regex", false))
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	budget, err := budgetOf(req)
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	skip, err := scope.Load(info.Root)
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	ts := &textSearch{g: g, root: info.Root, byFile: nodesByFile(g), index: map[graph.ID]int{}, match: match,
		excludeTests: req.GetBool("exclude_tests", false), skip: skip}
	afterFile, afterLine := splitAt(req.GetString("after", ""))
	c := &textPage{ts: ts, budget: budget, used: envelope, filesOnly: req.GetBool("files_only", false), matched: map[string]int{}}
	visit := func(rel string, line int, text string, src []byte) bool {
		if rel < afterFile || rel == afterFile && line <= afterLine {
			return true
		}
		return c.add(rel, line, text, src)
	}
	if err := ts.walk(req.GetString("path", ""), visit); err != nil {
		return nil, err
	}
	c.finish()
	c.r.Files, c.r.Tokens = ts.files, c.used
	return jsonResult(c.r), nil
}

// A textPage is search_text's answer under construction, kept to its
// budget as the traversal tools' pages are.
type textPage struct {
	ts           *textSearch
	budget, used int
	r            textResult
	filesOnly    bool
	// matched counts each file's hits, in walk order through order.
	matched map[string]int
	order   []string
	last    string // the last hit on the page, for next
}

// finish lays out a files_only answer and the totals.
func (c *textPage) finish() {
	c.r.Matched = len(c.matched)
	if !c.filesOnly {
		return
	}
	for _, f := range c.order {
		fc := fileCount{File: f, Count: c.matched[f]}
		if cost := estimate(fc); !c.r.Truncated && c.used+cost <= c.budget {
			c.used += cost
			c.r.Matches = append(c.r.Matches, fc)
			c.r.Next = ""
			continue
		}
		if !c.r.Truncated {
			c.r.Truncated = true
			if n := len(c.r.Matches); n > 0 {
				c.r.Next = c.r.Matches[n-1].File + ":999999999"
			}
		}
	}
}

// splitAt reads an after value, "file:line".
func splitAt(at string) (string, int) {
	i := strings.LastIndexByte(at, ':')
	if i < 0 {
		return at, 0
	}
	n, err := strconv.Atoi(at[i+1:])
	if err != nil {
		return at, 0
	}
	return at[:i], n
}

// add adds one hit, with the declaration holding it when that is not on
// the page yet, or counts it omitted once the budget is spent.
func (c *textPage) add(rel string, line int, text string, src []byte) bool {
	c.r.Total++
	if _, seen := c.matched[rel]; !seen {
		c.order = append(c.order, rel)
	}
	c.matched[rel]++
	if c.filesOnly {
		return true
	}
	hit := textHit{At: fmt.Sprintf("%s:%d", rel, line), Text: clip(strings.TrimSpace(text), 200)}
	var newNode *nodeOut
	if n, guess, ok := c.ts.containing(rel, line, src); ok {
		hit.Guess = guess
		if i, seen := c.ts.index[n.ID]; seen {
			hit.In = i
		} else {
			hit.In = len(c.r.Nodes)
			o := compact(renderNode(n))
			newNode = &o
		}
	}
	cost := estimate(hit)
	if newNode != nil {
		cost += estimate(*newNode)
	}
	if c.r.Truncated || c.used+cost > c.budget {
		if !c.r.Truncated {
			c.r.Truncated, c.r.Next = true, c.last
		}
		return true
	}
	c.used += cost
	c.last = hit.At
	if newNode != nil {
		c.ts.index[graph.ID(newNode.ID)] = len(c.r.Nodes)
		c.r.Nodes = append(c.r.Nodes, *newNode)
	}
	c.r.Hits = append(c.r.Hits, hit)
	return true
}

// budgetOf reads max_tokens within the bounds every tool keeps.
func budgetOf(req mcpsdk.CallToolRequest) (int, error) {
	b := int(req.GetFloat("max_tokens", defaultBudget))
	if b < minBudget || b > maxBudget {
		return 0, fmt.Errorf("max_tokens must be between %d and %d", minBudget, maxBudget)
	}
	return b, nil
}

// textMatcher returns a line matcher: the pattern as literal text, or as
// a regular expression.
func textMatcher(pattern string, regex bool) (func([]byte) bool, error) {
	if pattern == "" {
		return nil, fmt.Errorf("pattern is empty")
	}
	if !regex {
		p := []byte(pattern)
		return func(line []byte) bool { return bytes.Contains(line, p) }, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern: %v", err)
	}
	return re.Match, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type textSearch struct {
	g      *graph.Graph
	root   string
	byFile map[string][]graph.Node
	index  map[graph.ID]int
	files  int
	match  func([]byte) bool
	// excludeTests leaves test files out of the search.
	excludeTests bool
	goDecl       map[string][]declRange
	// skip are the directories the build leaves out (.alidade.pxf).
	skip *scope.Dirs
}

// walk calls visit for every matching line of every text file under the
// root, in path order.
func (ts *textSearch) walk(prefix string, visit func(rel string, line int, text string, src []byte) bool) error {
	return filepath.WalkDir(ts.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != ts.root && (textSkipDirs[d.Name()] || ts.skip.SkipsDir(ts.root, p)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, src, ok := ts.read(p, d, prefix)
		if !ok {
			return nil
		}
		ts.files++
		for i, line := range bytes.Split(src, []byte("\n")) {
			if ts.match(line) && !visit(rel, i+1, string(line), src) {
				return filepath.SkipAll
			}
		}
		return nil
	})
}

// read returns a file's repo-relative path and contents when it is one
// to search: under prefix, not too large, and text.
func (ts *textSearch) read(p string, d fs.DirEntry, prefix string) (string, []byte, bool) {
	rel, _ := filepath.Rel(ts.root, p)
	rel = filepath.ToSlash(rel)
	if prefix != "" && !strings.HasPrefix(rel, prefix) || ts.excludeTests && testFile(rel) {
		return "", nil, false
	}
	if info, err := d.Info(); err != nil || info.Size() > maxTextFile {
		return "", nil, false
	}
	src, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(src[:min(len(src), 8000)], 0) >= 0 {
		return "", nil, false // unreadable, or binary
	}
	return rel, src, true
}

// testFile reports whether a file is a test: a Go _test.go, or a
// frontend *.test.* or *.spec.* file.
func testFile(rel string) bool {
	base := rel[strings.LastIndexByte(rel, '/')+1:]
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

// nodesByFile indexes the graph's positioned declarations by file, in
// line order. A file or package node, positioned at line 0, is not a
// declaration a line is inside.
func nodesByFile(g *graph.Graph) map[string][]graph.Node {
	out := map[string][]graph.Node{}
	for n := range g.Nodes() {
		if n.Pos.File != "" && n.Pos.Line > 0 {
			out[n.Pos.File] = append(out[n.Pos.File], n)
		}
	}
	for _, ns := range out {
		slices.SortFunc(ns, func(a, b graph.Node) int { return a.Pos.Line - b.Pos.Line })
	}
	return out
}

// A declRange is a Go declaration's lines.
type declRange struct{ from, to int }

// containing is the declaration a line is in. In a Go file it is exact:
// the innermost function, method or type or value spec whose lines hold
// it, read with go/parser, and the graph's node declared within those
// lines. In a file whose declarations carry their last line — the
// frontend layer's — it is exact too, the innermost declaration spanning
// it. Elsewhere it is the nearest declaration above the line, a guess.
func (ts *textSearch) containing(rel string, line int, src []byte) (graph.Node, bool, bool) {
	nodes := ts.byFile[rel]
	if len(nodes) == 0 {
		return graph.Node{}, false, false
	}
	if strings.HasSuffix(rel, ".go") {
		r, ok := ts.goRange(rel, line, src)
		if !ok {
			return graph.Node{}, false, false
		}
		for _, n := range nodes {
			if n.Pos.Line >= r.from && n.Pos.Line <= r.to {
				return n, false, true
			}
		}
		return graph.Node{}, false, false
	}
	if n, spans, found := spanned(nodes, line); spans {
		return n, false, found
	}
	var best graph.Node
	found := false
	for _, n := range nodes {
		if n.Pos.Line > line {
			break
		}
		best, found = n, true
	}
	return best, true, found
}

// spanned is the innermost declaration whose lines hold a line, for a
// file whose declarations say where they end: the frontend layer's, read
// by the type checker (#212, #215). Exact, as Go's containment is: a line
// at module level is in no declaration. A file none of whose declarations
// spans lines — a Svelte component, a generated file's exports — has no
// answer here, and the caller places the hit by position.
func spanned(nodes []graph.Node, line int) (graph.Node, bool, bool) {
	var best graph.Node
	spans, found, size := false, false, 0
	for _, n := range nodes {
		end, err := strconv.Atoi(n.Attrs["end_line"])
		if err != nil {
			continue
		}
		spans = true
		if n.Pos.Line <= line && line <= end && (!found || end-n.Pos.Line < size) {
			best, found, size = n, true, end-n.Pos.Line
		}
	}
	return best, spans, found
}

// goRange is the innermost top-level declaration of a Go file holding a
// line: a function or method, or one spec of a type, var or const group.
func (ts *textSearch) goRange(rel string, line int, src []byte) (declRange, bool) {
	if ts.goDecl == nil {
		ts.goDecl = map[string][]declRange{}
	}
	ranges, ok := ts.goDecl[rel]
	if !ok {
		ranges = goDecls(src)
		ts.goDecl[rel] = ranges
	}
	best, found := declRange{}, false
	for _, r := range ranges {
		if line >= r.from && line <= r.to && (!found || r.to-r.from < best.to-best.from) {
			best, found = r, true
		}
	}
	return best, found
}

func goDecls(src []byte) []declRange {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	lines := func(a, b token.Pos) declRange { return declRange{fset.Position(a).Line, fset.Position(b).Line} }
	var out []declRange
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			from := d.Pos()
			if d.Doc != nil {
				from = d.Doc.Pos()
			}
			out = append(out, lines(from, d.End()))
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				out = append(out, lines(sp.Pos(), sp.End()))
			}
		}
	}
	return out
}
