// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"bytes"
	"fmt"
	"github.com/trendvidia/alidade/internal/scope"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"

	"github.com/trendvidia/alidade/internal/adapter/ts"
	"github.com/trendvidia/alidade/internal/graph"
)

// Options controls the Markdown layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Changelogs includes fragment directories (changelog.d, .changes).
	Changelogs bool
	// Testdata includes .md files under testdata directories.
	Testdata bool
}

// DefaultOptions returns the layer's defaults: fragments and testdata out.
func DefaultOptions() Options { return Options{} }

// A Mention is a code span in a section, kept for [Link] to resolve
// against the merged graph.
type Mention struct {
	Section graph.ID
	Text    string
	// Fence is a procedure path read out of a fenced block (#158): it
	// resolves only to an RPC, as nothing else in a fence is a mention.
	Fence bool
}

// A Report says what Extract found, and — once the build has run [Link]
// — what the mentions resolved to.
type Report struct {
	// Files counts documents read; Sections, Fences and Links what was
	// emitted; Anchors the links resolved to a section; External the
	// links to URLs, which are counted and not nodes.
	Files, Sections, Fences, Links, Anchors, External int
	// AnchorsUnresolved counts links whose "#anchor" names no heading of
	// the target document by GitHub's slug rules; each is an edge that
	// drops with its reason rather than a link that vanishes (#98).
	// Unkeyed counts links to files of no layer — an image, a YAML file —
	// which have no node to point at.
	AnchorsUnresolved, Unkeyed int
	// Mentions are the code spans found, for Link.
	Mentions []Mention
	// Words are the words of each section — its own text and its heading
	// path — lowercased, for Link's rule on a bare word naming a nested
	// declaration (#167).
	Words map[graph.ID]map[string]bool
	// Resolved, Ambiguous and Unmatched are Link's accounting: mentions
	// that named exactly one node, more than one, and none. Unparented
	// named one, a nested declaration whose parent the section never
	// names (#167).
	Resolved, Ambiguous, Unmatched, Unparented int
	// Changelogs and Testdata count files skipped under those rules.
	Changelogs, Testdata int
	// Skipped are the documents and links left out because no ID can
	// name them, or because they could not be read (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
}

// SkipDirs are the directories the layer never descends into, by name.
// The build reads them too: a file under one is not in the layer's
// signature, and nothing else is left out of it (#93).
var SkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".tmp": true}

// changelogDirs are fragment directories: one paragraph per change.
var changelogDirs = map[string]bool{"changelog.d": true, ".changes": true}

// Extract parses every .md file under root and adds the layer to b. The
// mentions it returns are resolved by [Link] after every layer has
// reported.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("markdown: %w", err)
	}
	m := b.Minter()
	c, err := discover(abs, opts, m)
	if err != nil {
		return nil, fmt.Errorf("markdown: discover under %s: %w", abs, err)
	}
	x := &extractor{b: b, m: m, md: goldmark.New(), modules: map[string]string{}, docs: map[string]*document{},
		report: &Report{Files: len(c.files), Changelogs: c.changelogs, Testdata: c.testdata, Skipped: c.skipped}}
	// Every document is walked before any link is emitted, so a link to
	// another document's heading resolves against that document's
	// anchors (ADR 002, the consequence of rule 8 for #5; #98).
	var walked []*document
	for _, f := range c.files {
		d, err := x.file(f)
		if err != nil {
			return nil, err
		}
		if d != nil {
			walked = append(walked, d)
		}
	}
	for _, d := range walked {
		if err := d.flush(); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

type corpus struct {
	files                []string
	changelogs, testdata int
	skipped              []graph.Skip
}

func discover(root string, opts Options, m *graph.Minter) (*corpus, error) {
	c := &corpus{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			c.skipped = append(c.skipped, m.Skip(p, 0, err))
			return nil
		}
		if d.IsDir() {
			return c.enter(root, p, d.Name(), opts)
		}
		if strings.HasSuffix(d.Name(), ".md") {
			c.files = append(c.files, p)
		}
		return nil
	})
	slices.Sort(c.files)
	return c, err
}

func (c *corpus) enter(root, p, name string, opts Options) error {
	if p == root {
		return nil
	}
	switch {
	case SkipDirs[name], opts.Skip.SkipsDir(root, p):
		return filepath.SkipDir
	case !opts.Changelogs && changelogDirs[name]:
		c.changelogs += countMD(p)
		return filepath.SkipDir
	case !opts.Testdata && name == "testdata":
		c.testdata += countMD(p)
		return filepath.SkipDir
	}
	return nil
}

func countMD(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			n++
		}
		return nil
	})
	return n
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	md     goldmark.Markdown
	report *Report
	// modules caches, per repo-relative directory, the Go module path
	// the nearest go.mod at or above it declares, "" for none; see
	// goPackage.
	modules map[string]string
	// docs holds every walked document by repo-relative path, for
	// resolving a link to another document's heading.
	docs map[string]*document
}

// file parses one document and emits its nodes; its links are emitted by
// flush once every document is walked. A document no ID can name, or
// that cannot be read, is skipped and recorded rather than failing the
// layer (#92), and returns nil.
func (x *extractor) file(p string) (*document, error) {
	// rel is the path as the file system names it: links resolve against
	// its directory, and each ID or position minted from it escapes and
	// labels it once (#124). RelPath says whether an ID can carry it.
	_, err := x.m.RelPath(p)
	var rel string
	if err == nil {
		rel, err = x.m.Rel(p)
	}
	if err != nil {
		x.report.Skipped = append(x.report.Skipped, x.m.Skip(p, 0, err))
		return nil, nil
	}
	src, err := os.ReadFile(p)
	if err != nil {
		x.report.Skipped = append(x.report.Skipped, x.m.Skip(p, 0, err))
		return nil, nil
	}
	body, keys, title, offset := frontMatter(src)
	docID, err := x.m.MarkdownID(rel)
	if err != nil {
		x.report.Skipped = append(x.report.Skipped, x.m.Skip(p, 0, err))
		return nil, nil
	}
	d := &document{x: x, rel: rel, docID: docID, src: body, lineBase: offset, seen: map[string]int{},
		used: map[graph.ID]bool{}, anchors: map[string]graph.ID{}, slugs: map[string]int{}, fences: map[graph.ID]int{},
		base: map[string]string{}, family: map[string]int{}}
	root := x.md.Parser().Parse(text.NewReader(body))
	d.reserved = d.headingPaths(root)
	d.walk(root)
	d.recordWords()
	x.docs[rel] = d
	attrs := map[string]string{}
	if len(keys) > 0 {
		attrs["front_matter"] = strings.Join(keys, " ")
	}
	if title == "" {
		title = d.title
	}
	if title != "" {
		attrs["title"] = title
	}
	if err := x.b.AddNode(graph.Node{ID: docID, Kind: graph.KindDocument, Name: filepath.Base(rel), Pos: graph.Position{File: rel}, Attrs: attrs}); err != nil {
		return nil, err
	}
	return d, nil
}

// frontMatter splits a leading YAML block off the source and returns the
// rest, the block's top-level keys sorted, its title if it has one, and
// how many lines the block took, so positions in the rest stay right.
// The block opens with a "---" line and closes with the next one, either
// with Windows line endings and the closing one with or without a final
// newline; anything else is no front matter, and goldmark would otherwise
// read the closing "---" as a setext heading of the block's text (#98).
func frontMatter(src []byte) (body []byte, keys []string, title string, lines int) {
	open, ok := fenceLine(src, 0)
	if !ok {
		return src, nil, "", 0
	}
	for i := open; i < len(src); {
		end, ok := fenceLine(src, i)
		if ok && i > open {
			keys, title, ok = yamlKeys(src[open:i])
			if !ok {
				return src, nil, "", 0
			}
			return src[end:], keys, title, bytes.Count(src[:end], []byte("\n"))
		}
		nl := bytes.IndexByte(src[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	return src, nil, "", 0
}

// fenceLine reports whether the line at i is "---", and where the next
// line starts.
func fenceLine(src []byte, i int) (next int, ok bool) {
	line := src[i:]
	if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
		line, next = line[:nl], i+nl+1
	} else {
		next = len(src)
	}
	return next, string(bytes.TrimRight(line, "\r")) == "---"
}

// yamlKeys parses a front matter block for its top-level keys, sorted,
// and its title.
func yamlKeys(block []byte) (keys []string, title string, ok bool) {
	var m map[string]any
	if yaml.Unmarshal(block, &m) != nil {
		return nil, "", false
	}
	for k, v := range m {
		keys = append(keys, k)
		if t, isStr := v.(string); k == "title" && isStr {
			title = t
		}
	}
	sort.Strings(keys)
	return keys, title, true
}

// A document walks one parsed file.
type document struct {
	x        *extractor
	rel      string
	docID    graph.ID
	src      []byte
	lineBase int
	title    string
	// stack is the open sections, outermost first, with their levels.
	stack []section
	// seen counts section paths, for the collision suffix; reserved is
	// every path a heading of the document has before any suffix, and
	// used every ID given out, so a suffixed ID is never one a real
	// heading holds (#98).
	seen     map[string]int
	reserved map[graph.ID]bool
	used     map[graph.ID]bool
	// anchors maps each heading's anchor as GitHub renders it to its
	// section, and slugs counts them for GitHub's own duplicate suffix;
	// a "#anchor" link resolves through it (#98).
	anchors map[string]graph.ID
	slugs   map[string]int
	// base maps each anchor to the slug it was numbered from, and family
	// counts the headings of each slug: an anchor whose family has more
	// than one is positional.
	base    map[string]string
	family  map[string]int
	pending []pendingLink
	// fences counts the fenced blocks of each section, which number
	// within it, so a block added in one section renumbers no other
	// (ADR 002 rule 1; #98).
	fences map[graph.ID]int
	// starts are the sections in order, each with the byte offset its
	// heading starts at, for its words (#167).
	starts []sectionStart
}

type section struct {
	level    int
	id       graph.ID
	headings []string // heading texts, for MarkdownID
}

type pendingLink struct {
	from   graph.ID
	dest   string
	line   int
	anchor string
}

// owner is the innermost open section, or the document.
func (d *document) owner() graph.ID {
	if len(d.stack) == 0 {
		return d.docID
	}
	return d.stack[len(d.stack)-1].id
}

func (d *document) walk(root ast.Node) {
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			d.heading(n)
		case *ast.FencedCodeBlock:
			d.fence(n)
			d.fenceProcedures(n)
		case *ast.Link:
			d.link(string(n.Destination), lineOf(d.src, n, d.lineBase))
		case *ast.AutoLink:
			d.link(string(n.URL(d.src)), lineOf(d.src, n, d.lineBase))
		case *ast.CodeSpan:
			d.x.report.Mentions = append(d.x.report.Mentions, Mention{Section: d.owner(), Text: inlineText(n, d.src)})
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
}

// heading opens a section: it closes every open section of the same or
// deeper level, then mints its ID from the enclosing headings.
func (d *document) heading(h *ast.Heading) {
	textOf := inlineText(h, d.src)
	if h.Level == 1 && d.title == "" {
		d.title = textOf
	}
	for len(d.stack) > 0 && d.stack[len(d.stack)-1].level >= h.Level {
		d.stack = d.stack[:len(d.stack)-1]
	}
	var parentHeadings []string
	if len(d.stack) > 0 {
		parentHeadings = d.stack[len(d.stack)-1].headings
	}
	headings := append(slices.Clone(parentHeadings), textOf)
	id, err := d.x.m.MarkdownID(d.rel, headings...)
	if err != nil {
		return // a heading that slugs to nothing has no identity
	}
	id = d.collision(id)
	owner := d.owner()
	d.stack = append(d.stack, section{level: h.Level, id: id, headings: headings})
	if h.Lines().Len() > 0 {
		d.starts = append(d.starts, sectionStart{id: id, at: h.Lines().At(0).Start, headings: headings})
	}
	d.x.report.Sections++
	anchor, base := githubAnchor(d.slugs, textOf)
	d.anchors[anchor], d.base[anchor] = id, base
	d.family[base]++
	_ = d.x.b.AddNode(graph.Node{ID: id, Kind: graph.KindSection, Name: textOf,
		Pos:   graph.Position{File: d.rel, Line: lineOf(d.src, h, d.lineBase), Col: 1},
		Attrs: map[string]string{"level": strconv.Itoa(h.Level)}})
	_ = d.x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// A sectionStart is where a section's heading begins.
type sectionStart struct {
	id       graph.ID
	at       int
	headings []string
}

// wordRE is a word as recordWords keeps it: letters, digits, underscore.
var wordRE = regexp.MustCompile(`[A-Za-z0-9_]+`)

// recordWords keeps each section's words: the text from its heading to
// the next heading, and every heading of its path. Text before the
// first heading is the document's.
func (d *document) recordWords() {
	if d.x.report.Words == nil {
		d.x.report.Words = map[graph.ID]map[string]bool{}
	}
	add := func(id graph.ID, texts ...string) {
		set := d.x.report.Words[id]
		if set == nil {
			set = map[string]bool{}
			d.x.report.Words[id] = set
		}
		for _, t := range texts {
			for _, w := range wordRE.FindAllString(t, -1) {
				set[strings.ToLower(w)] = true
			}
		}
	}
	end := len(d.src)
	if len(d.starts) > 0 {
		end = d.starts[0].at
	}
	add(d.docID, string(d.src[:end]))
	for i, st := range d.starts {
		end := len(d.src)
		if i+1 < len(d.starts) {
			end = d.starts[i+1].at
		}
		add(st.id, append(slices.Clone(st.headings), string(d.src[st.at:end]))...)
	}
}

// fence emits a fenced code block under the open section.
func (d *document) fence(f *ast.FencedCodeBlock) {
	d.x.report.Fences++
	owner := d.owner()
	d.fences[owner]++
	name := "@fence"
	if n := d.fences[owner]; n > 1 {
		name += strconv.Itoa(n)
	}
	var id graph.ID
	if owner == d.docID {
		id = graph.ID(string(d.docID) + "#" + name)
	} else {
		id = graph.ID(string(owner) + "." + name)
	}
	attrs := map[string]string{"fence": "true", "lines": strconv.Itoa(f.Lines().Len())}
	if lang := string(f.Language(d.src)); lang != "" {
		attrs["lang"] = lang
	}
	_ = d.x.b.AddNode(graph.Node{ID: id, Kind: graph.KindBlock, Name: name,
		Pos: graph.Position{File: d.rel, Line: lineOf(d.src, f, d.lineBase), Col: 1}, Attrs: attrs})
	_ = d.x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// fenceProcedures records each RPC procedure path in a fenced block — a
// Connect or gRPC route, "POST /api/pkg.v1.Service/Method" — as a mention
// (#158, the owner's decision): an HTTP example names the RPC it calls.
// Nothing else in a fence is read; sample code is not prose about code.
func (d *document) fenceProcedures(f *ast.FencedCodeBlock) {
	lines := f.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		for _, p := range procedures(string(seg.Value(d.src))) {
			d.x.report.Mentions = append(d.x.report.Mentions, Mention{Section: d.owner(), Text: p, Fence: true})
		}
	}
}

// link records a link for resolution once the document's anchors are
// known. A URL is counted and goes no further, and so is a root-absolute
// path: "/concepts/x/" is a route of the site the document is published
// on, not a path in the repository.
func (d *document) link(dest string, line int) {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") || strings.HasPrefix(dest, "/") {
		d.x.report.External++
		return
	}
	target, anchor, _ := strings.Cut(dest, "#")
	// "?plain=1" and the like select a view of the file, not another file;
	// "%20" is the path's own space (#98).
	target, _, _ = strings.Cut(target, "?")
	if u, err := url.PathUnescape(target); err == nil {
		target = u
	}
	if u, err := url.PathUnescape(anchor); err == nil {
		anchor = u
	}
	d.pending = append(d.pending, pendingLink{from: d.owner(), dest: target, line: line, anchor: anchor})
}

// flush emits the link edges: to a section of this document for a bare
// anchor, to the file node a relative path names otherwise.
func (d *document) flush() error {
	for _, l := range d.pending {
		to, attrs, err := d.linkTarget(l)
		if err != nil {
			d.x.report.Skipped = append(d.x.report.Skipped, d.x.m.Skip(d.rel, l.line, err))
			continue
		}
		if to == "" {
			continue
		}
		d.x.report.Links++
		if err := d.x.b.AddEdge(graph.Edge{From: l.from, To: to, Kind: graph.KindReferences, Derivation: graph.Extracted, Attrs: attrs}); err != nil {
			return err
		}
	}
	return nil
}

// linkTarget resolves a link to a node ID, or "" for one that names
// nothing this layer can key.
func (d *document) linkTarget(l pendingLink) (graph.ID, map[string]string, error) {
	attrs := map[string]string{"link": l.dest}
	if l.anchor != "" {
		attrs["anchor"] = l.anchor
	}
	if l.dest == "" {
		// "#anchor": a heading of this document.
		attrs["link"] = "#" + l.anchor
		return d.x.anchored(d, l.anchor), attrs, nil
	}
	target := path.Clean(path.Join(path.Dir(d.rel), l.dest))
	if strings.HasPrefix(target, "../") || target == ".." {
		d.x.report.External++ // out of the repository, as a URL is
		return "", nil, nil
	}
	if to, ok := d.x.docs[target]; ok && l.anchor != "" {
		// "other.md#heading": that document's section (ADR 002, rule 8's
		// consequence for #5).
		return d.x.anchored(to, l.anchor), attrs, nil
	}
	id, err := d.x.pathNode(target)
	if err != nil {
		return "", nil, fmt.Errorf("markdown: %w", err)
	}
	return id, attrs, nil
}

// anchored resolves an anchor in a walked document and counts the
// outcome.
func (x *extractor) anchored(d *document, anchor string) graph.ID {
	id, ok := d.anchorTarget(anchor)
	if ok {
		x.report.Anchors++
	} else {
		x.report.AnchorsUnresolved++
	}
	return id
}

// fileNodes mints the node of a file of a format with file nodes (ADR 002
// rule 7), by extension.
var fileNodes = map[string]func(*graph.Minter, string) (graph.ID, error){
	".md":    func(m *graph.Minter, rel string) (graph.ID, error) { return m.MarkdownID(rel) },
	".proto": func(m *graph.Minter, rel string) (graph.ID, error) { return m.ProtoFileID(rel) },
	".pxf":   func(m *graph.Minter, rel string) (graph.ID, error) { return m.PXFID(rel, "") },
	".goasm": func(m *graph.Minter, rel string) (graph.ID, error) { return m.GoasmID(rel, "") },
	".py":    func(m *graph.Minter, rel string) (graph.ID, error) { return m.PyID(rel) },
}

// pathNode returns the node a repo-relative path names, or "" for one
// nothing in the graph can key. A file of a format with file nodes (ADR
// 002 rule 7) is that file's node; a Go file or a directory is the Go
// package there, by the module path of the nearest go.mod above it
// (#68). A path that is gone is minted too, as the package it would be,
// so the link drops with reason unknown_target and the raw link in attrs,
// the same as a link to a missing document — when it is gone from a
// place that holds Go, a package directory or a module root. A path
// missing from a directory of documents is a route of the site they
// are published on, not a package that moved, and is left alone; so is a
// directory that exists and holds no Go files.
func (x *extractor) pathNode(rel string) (graph.ID, error) {
	if mint, ok := fileNodes[path.Ext(rel)]; ok {
		return mint(x.m, rel)
	}
	switch path.Ext(rel) {
	case ".go":
		if x.exists(rel) {
			return x.goPackage(path.Dir(rel))
		}
	case "":
		if x.exists(rel) {
			if x.holdsGo(rel) {
				return x.goPackage(rel)
			}
			return "", nil
		}
	default:
		if ts.Source(rel) {
			return x.m.TSID(rel, "")
		}
		x.report.Unkeyed++ // a file of no layer: an image, a YAML file
		return "", nil
	}
	if x.holdsGo(x.nearestExisting(rel)) {
		return x.goPackage(rel) // gone from a Go directory: the package it would be
	}
	return "", nil
}

// exists reports whether a repo-relative path is on disk.
func (x *extractor) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(x.m.Root(), filepath.FromSlash(rel)))
	return err == nil
}

// nearestExisting returns the closest ancestor of a repo-relative path
// that is on disk, "." at worst.
func (x *extractor) nearestExisting(rel string) string {
	for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
		if x.exists(d) {
			return d
		}
	}
	return "."
}

// holdsGo reports whether a repo-relative directory is a place Go lives:
// it has .go files of its own or a go.mod.
func (x *extractor) holdsGo(dir string) bool {
	abs := filepath.Join(x.m.Root(), filepath.FromSlash(dir))
	if hasGoFiles(abs) {
		return true
	}
	_, err := os.Stat(filepath.Join(abs, "go.mod"))
	return err == nil
}

// goPackage mints the ID of the Go package in a repo-relative directory:
// the module path of the nearest go.mod at or above it, extended by the
// directory's path under that module. A directory under no go.mod is
// "" — a repository without Go has no package to point at.
func (x *extractor) goPackage(dir string) (graph.ID, error) {
	for d := dir; ; d = path.Dir(d) {
		mod, err := x.modulePath(d)
		if err != nil {
			return "", err
		}
		if mod != "" {
			pkg := mod
			if rel := strings.TrimPrefix(strings.TrimPrefix(dir, d), "/"); rel != "" {
				pkg += "/" + rel
			}
			return x.m.GoID(pkg, "")
		}
		if d == "." || d == "/" {
			return "", nil
		}
	}
}

// modulePath returns the module path a go.mod in one repo-relative
// directory declares, "" when the directory has none, cached.
func (x *extractor) modulePath(dir string) (string, error) {
	if mod, ok := x.modules[dir]; ok {
		return mod, nil
	}
	mod := ""
	data, err := os.ReadFile(filepath.Join(x.m.Root(), filepath.FromSlash(dir), "go.mod"))
	if err == nil {
		mod = modfile.ModulePath(data)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	x.modules[dir] = mod
	return mod, nil
}

// hasGoFiles reports whether a directory holds a .go file of its own.
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// inlineText concatenates the text of a node's inline children.
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// lineOf returns the 1-based line of a node: its first line for a block,
// the enclosing block's for an inline node.
func lineOf(src []byte, n ast.Node, base int) int {
	for n != nil {
		if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
			return base + 1 + bytes.Count(src[:n.Lines().At(0).Start], []byte("\n"))
		}
		if h, ok := n.(*ast.Heading); ok && h.Lines().Len() == 0 {
			// A setext heading's line is its text's.
			break
		}
		n = n.Parent()
	}
	return 0
}
