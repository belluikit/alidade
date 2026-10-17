// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/scope"
	"github.com/belluikit/alidade/internal/worktree"
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
		mcpsdk.WithArray("exclude", mcpsdk.WithStringItems(), mcpsdk.Description("Leave out files whose repo-relative path starts with any of these: [\"api/\", \"docs/old\"].")),
		mcpsdk.WithBoolean("files_only", mcpsdk.DefaultBool(false), mcpsdk.Description("List the matching files with their hit counts, not the hits. Cut, a first page also counts hits per directory, two levels below path (dirs), to choose path or exclude.")),
		mcpsdk.WithBoolean("exclude_tests", mcpsdk.DefaultBool(false), mcpsdk.Description("Leave out test files: _test.go, *.test.*, *.spec.*.")),
		mcpsdk.WithString("after", mcpsdk.Description("Continue after this file:line, the next of a cut answer.")),
		mcpsdk.WithString("root", mcpsdk.Description("Search this git worktree of the repository instead of the checkout the graph is built from: its top directory, absolute or relative to the repository. Its hits name no declaration.")),
		mcpsdk.WithString("ref", mcpsdk.Description("Search the files of this commit instead of the files on disk: a branch, tag, SHA or expression git takes (origin/main, HEAD~2), resolved in root's checkout. Its hits name no declaration.")),
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
	// Root is the worktree searched, when it is not the graph's; Ref
	// the commit searched, when it is not the files on disk.
	Root    string      `json:"root,omitempty"`
	Ref     string      `json:"ref,omitempty"`
	Hits    []textHit   `json:"hits,omitempty"`
	Matches []fileCount `json:"matches,omitempty"`
	// Dirs rolls a cut files_only answer's hits up by directory, two
	// levels below path, most hits first: every matching file's, listed
	// or not, so where the noisy directories are shows before any paging.
	Dirs []dirCount `json:"dirs,omitempty"`
	// DirsOmitted counts the directories dirs had no room for.
	DirsOmitted int       `json:"dirs_omitted,omitempty"`
	Nodes       []nodeOut `json:"nodes,omitempty"`
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
	budget, err := budgetOf(req)
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	ts, err := s.textSearch(ctx, req)
	if err != nil {
		var bad badParam
		if errors.As(err, &bad) {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return nil, err
	}
	afterFile, afterLine := splitAt(req.GetString("after", ""))
	ts.after = afterFile
	c := &textPage{ts: ts, budget: budget, used: envelope, filesOnly: req.GetBool("files_only", false), matched: map[string]int{}}
	visit := func(rel string, line int, text string, src []byte) bool {
		if rel < afterFile || rel == afterFile && line <= afterLine {
			return true
		}
		return c.add(rel, line, text, src)
	}
	if err := ts.walk(visit); err != nil {
		return nil, err
	}
	c.finish()
	c.r.Files, c.r.Tokens = ts.files, c.used
	if ts.other {
		c.r.Root = ts.root
	}
	c.r.Ref = ts.ref
	return jsonResult(c.r), nil
}

// A badParam is an error in a call's parameters, answered as a tool
// error rather than a failure of the server.
type badParam struct{ error }

// textSearch reads a search_text call: what to match, in which tree, and
// which of its files. The graph is built only for the checkout it is
// built from; another worktree is searched without it.
func (s *Server) textSearch(ctx context.Context, req mcpsdk.CallToolRequest) (*textSearch, error) {
	match, err := textMatcher(req.GetString("pattern", ""), req.GetBool("regex", false))
	if err != nil {
		return nil, badParam{err}
	}
	exclude, err := stringsParam(req, "exclude")
	if err != nil {
		return nil, badParam{err}
	}
	ts := &textSearch{byFile: map[string][]graph.Node{}, index: map[graph.ID]int{}, match: match,
		prefix: req.GetString("path", ""), exclude: exclude, excludeTests: req.GetBool("exclude_tests", false)}
	if err := s.placeSearch(ctx, ts, req.GetString("root", ""), req.GetString("ref", "")); err != nil {
		return nil, err
	}
	if !ts.other && ts.ref == "" {
		g, _, err := s.graph(ctx)
		if err != nil {
			return nil, err
		}
		ts.byFile = nodesByFile(g)
	}
	if ts.skip, err = scope.Load(ts.root); err != nil {
		return nil, badParam{err}
	}
	return ts, nil
}

// placeSearch settles which tree a search reads: root's worktree, and
// ref's commit in it.
func (s *Server) placeSearch(ctx context.Context, ts *textSearch, root, ref string) error {
	served, err := s.servedRoot(ctx)
	if err != nil {
		return err
	}
	if ts.root, ts.other, err = treeParam(root, served); err != nil {
		return badParam{err}
	}
	if ref != "" {
		if ts.ref, err = refParam(ts.root, ref); err != nil {
			return badParam{err}
		}
	}
	return nil
}

// treeParam reads root: the worktree to search, and whether it is
// another than the served one, the graph's. A worktree is a checkout of
// the same repository, the main one or one `git worktree add` made
// (#23); any other directory is refused.
func treeParam(root, served string) (string, bool, error) {
	if root == "" {
		return served, false, nil
	}
	repo := worktree.Top(served)
	if repo == "" {
		return "", false, fmt.Errorf("root: %s is in no git repository, so it has no other worktree", served)
	}
	p := root
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p)
	}
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false, fmt.Errorf("root: %v", err)
	}
	if real, err := filepath.EvalSymlinks(served); err == nil && real == p {
		return served, false, nil
	}
	if worktree.Top(p) != p {
		return "", false, fmt.Errorf("root: %s is not the top directory of a checkout; give the worktree's top, and path for a directory in it", root)
	}
	if !worktree.Same(p, repo) {
		return "", false, fmt.Errorf("root: %s is not a worktree of the repository at %s; `git worktree list` names its worktrees", root, served)
	}
	return p, true, nil
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

// finish lays out a files_only answer and the totals. When the files
// will not all fit on a first page, the answer leads with where they are
// (#26); a page continued with after lists files only, since the first
// already said where they are.
func (c *textPage) finish() {
	c.r.Matched = len(c.matched)
	if !c.filesOnly {
		return
	}
	costs := make([]int, len(c.order))
	for i, f := range c.order {
		costs[i] = estimate(fileCount{File: f, Count: c.matched[f]})
	}
	n := c.fitting(costs)
	if n < len(costs) && c.ts.after == "" {
		c.rollUp(costs[0])
		n = c.fitting(costs)
	}
	if n == 0 && len(costs) > 0 {
		// A first file bigger than the whole budget is listed alone, so
		// the answer has a next and can be continued (#32).
		n = 1
	}
	for i, f := range c.order[:n] {
		c.used += costs[i]
		c.r.Matches = append(c.r.Matches, fileCount{File: f, Count: c.matched[f]})
	}
	if n < len(costs) {
		c.r.Truncated = true
		if n > 0 {
			c.r.Next = c.order[n-1] + ":999999999"
		}
	}
}

// fitting is how many of the matching files, in order, fit what is left
// of the budget; costs are their estimates.
func (c *textPage) fitting(costs []int) int {
	used := c.used
	for i, cost := range costs {
		if used+cost > c.budget {
			return i
		}
		used += cost
	}
	return len(costs)
}

// A dirCount is a directory and the hits of the matching files under it,
// for a cut files_only answer.
type dirCount struct {
	Dir     string `json:"dir"`
	Count   int    `json:"count"`
	Matched int    `json:"matched"`
}

// rollupDepth is how many path segments below path's directory a
// directory of dirs keeps, the owner's decision on #32: src/cmd, not src:
// on trendvidia/go, src/cmd held 1,277 of 2,780 TODOs, and src alone would
// have named every one of them.
const rollupDepth = 2

// rollUp adds dirs: the matching files' hits by directory, most first,
// as many as fit half the budget, so files still get the rest. The half
// is of what the first file leaves, so the rollup never takes the room
// a page needs to list one file and continue (first is its cost).
func (c *textPage) rollUp(first int) {
	base := strings.Count(c.ts.prefix, "/")
	by := map[string]*dirCount{}
	var dirs []*dirCount
	for _, f := range c.order {
		d := rollupDir(f, base)
		if by[d] == nil {
			by[d] = &dirCount{Dir: d}
			dirs = append(dirs, by[d])
		}
		by[d].Count += c.matched[f]
		by[d].Matched++
	}
	slices.SortStableFunc(dirs, func(a, b *dirCount) int { return b.Count - a.Count })
	// The keys are the answer's too, not only the entries.
	frame := estimate(struct {
		Dirs    []dirCount `json:"dirs"`
		Omitted int        `json:"dirs_omitted"`
	}{Omitted: len(dirs)})
	room := (c.budget - c.used - frame - first) / 2
	c.used += frame
	for i, d := range dirs {
		cost := estimate(*d)
		if cost > room {
			c.r.DirsOmitted = len(dirs) - i
			return
		}
		room -= cost
		c.used += cost
		c.r.Dirs = append(c.r.Dirs, *d)
	}
}

// rollupDir is a file's directory, cut to rollupDepth segments past the
// base segments path names; "." for a file at the root.
func rollupDir(rel string, base int) string {
	parts := strings.Split(rel, "/")
	parts = parts[:len(parts)-1]
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts[:min(len(parts), base+rollupDepth)], "/")
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
	// A first hit bigger than the whole budget is kept alone, so the
	// answer has a next and can be continued (#32).
	alone := len(c.r.Hits) == 0 && !c.r.Truncated
	if c.r.Truncated || c.used+cost > c.budget && !alone {
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
	root string
	// other says root is a worktree other than the graph's, and ref
	// that the search reads a commit rather than the files on disk:
	// either way, its hits name no declaration.
	other  bool
	ref    string
	byFile map[string][]graph.Node
	index  map[graph.ID]int
	files  int
	match  func([]byte) bool
	// prefix is the path a file's must start with, and exclude the ones
	// it must not; excludeTests leaves test files out.
	prefix       string
	exclude      []string
	excludeTests bool
	// after is the file a cut answer ended in: the files before it are
	// not read again to continue it.
	after  string
	goDecl map[string][]declRange
	// skip are the directories the build leaves out (.alidade.pxf).
	skip *scope.Dirs
}

// walk calls visit for every matching line of every text file under the
// root, in the string order of their paths, which a cut answer's next
// relies on to continue: "a-c/x" comes before "a/x", as git orders a
// tree, though a directory walk enters a/ first.
func (ts *textSearch) walk(visit func(rel string, line int, text string, src []byte) bool) error {
	var err error
	if ts.ref != "" {
		err = ts.walkRef(visit)
	} else {
		err = ts.walkDir(ts.root, visit)
	}
	if err == filepath.SkipAll {
		return nil
	}
	return err
}

// walkDir walks one directory, its entries in path order: a directory
// sorts as its name with a slash after it.
func (ts *textSearch) walkDir(dir string, visit func(rel string, line int, text string, src []byte) bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	key := func(d fs.DirEntry) string {
		if d.IsDir() {
			return d.Name() + "/"
		}
		return d.Name()
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(key(a), key(b)) })
	for _, d := range entries {
		p := filepath.Join(dir, d.Name())
		if d.IsDir() {
			if !ts.entered(p) {
				continue
			}
			if err := ts.walkDir(p, visit); err != nil {
				return err
			}
			continue
		}
		if err := ts.searchFile(p, d, visit); err != nil {
			return err
		}
	}
	return nil
}

// entered reports whether the walk enters a directory: one no layer
// skips, not excluded, and able to hold a file the call asks for.
func (ts *textSearch) entered(p string) bool {
	rel := ts.rel(p) + "/"
	return !textSkipDirs[filepath.Base(p)] && ts.mayHold(rel) && !ts.excluded(rel) && !ts.skip.SkipsDir(ts.root, p)
}

// searchFile calls visit for every matching line of one file.
func (ts *textSearch) searchFile(p string, d fs.DirEntry, visit func(rel string, line int, text string, src []byte) bool) error {
	rel, src, ok := ts.read(p, d)
	if !ok {
		return nil
	}
	return ts.searchSrc(rel, src, visit)
}

// searchSrc calls visit for every matching line of one file's contents.
func (ts *textSearch) searchSrc(rel string, src []byte, visit func(rel string, line int, text string, src []byte) bool) error {
	ts.files++
	for i, line := range bytes.Split(src, []byte("\n")) {
		if ts.match(line) && !visit(rel, i+1, string(line), src) {
			return filepath.SkipAll
		}
	}
	return nil
}

// rel is a path's repo-relative form, with forward slashes.
func (ts *textSearch) rel(p string) string {
	rel, _ := filepath.Rel(ts.root, p)
	return filepath.ToSlash(rel)
}

// excluded reports whether a repo-relative path starts with one of
// exclude's prefixes.
func (ts *textSearch) excluded(rel string) bool {
	for _, x := range ts.exclude {
		if x != "" && strings.HasPrefix(rel, x) {
			return true
		}
	}
	return false
}

// read returns a file's repo-relative path and contents when it is one
// to search: under prefix, not excluded, not too large, and text.
func (ts *textSearch) read(p string, d fs.DirEntry) (string, []byte, bool) {
	rel := ts.rel(p)
	if !ts.wanted(rel) {
		return "", nil, false
	}
	if info, err := d.Info(); err != nil || info.Size() > maxTextFile {
		return "", nil, false
	}
	src, err := os.ReadFile(p)
	if err != nil || binary(src) {
		return "", nil, false
	}
	return rel, src, true
}

// wanted reports whether a repo-relative file is one the call asks for:
// under prefix, not excluded, not a test when tests are left out, and
// not before the file a cut answer ended in.
func (ts *textSearch) wanted(rel string) bool {
	return strings.HasPrefix(rel, ts.prefix) && rel >= ts.after && !ts.excluded(rel) && (!ts.excludeTests || !testFile(rel))
}

// mayHold reports whether a directory, repo-relative with a slash after
// it, can hold a file the call asks for: one under prefix, or holding
// it, and not wholly before after. Every path that starts with dir sorts
// before after exactly when dir does and after does not start with dir.
func (ts *textSearch) mayHold(dir string) bool {
	underPrefix := strings.HasPrefix(dir, ts.prefix) || strings.HasPrefix(ts.prefix, dir)
	before := dir < ts.after && !strings.HasPrefix(ts.after, dir)
	return underPrefix && !before
}

// binary reports whether a file's contents are not text: a NUL byte in
// its first 8000, as git decides.
func binary(src []byte) bool {
	return bytes.IndexByte(src[:min(len(src), 8000)], 0) >= 0
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
