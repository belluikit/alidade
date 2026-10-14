// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"fmt"
	"github.com/belluikit/alidade/internal/scope"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/belluikit/alidade/internal/graph"
)

// Options controls the frontend layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Testdata includes files under testdata directories; out by default.
	Testdata bool
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// A Report says what Extract found.
type Report struct {
	// Files counts every file node; Components those that are .svelte;
	// Generated those protoc-gen-es wrote.
	Files, Components, Generated int
	// Exports counts the export nodes of generated files.
	Exports int
	// Imports counts resolved file → file edges; External file → package;
	// Unresolved the specifiers esbuild could not resolve to either;
	// Assets those it resolved to a file the layer does not read (a
	// stylesheet, JSON, an image), which give no edge.
	Imports, External, Unresolved, Assets int
	// References counts importing file → generated export edges;
	// Unreadable the import clauses of a generated file that bind no
	// names (namespace and default imports), which keep only the
	// file-level edge.
	References, Unreadable int
	// Generates counts .proto → generated edges; ProtoUnmatched the
	// generated files whose source .proto was not found once under root.
	Generates, ProtoUnmatched int
	// Outside counts imports esbuild resolved to a file outside the root,
	// which no node of this graph can be (#99).
	Outside int
	// Testdata counts files skipped under testdata directories.
	Testdata int
	// The type checker's pass (#215). Programs counts the projects it
	// built, Inferred the files no tsconfig includes, which one project
	// inferred for them read, and ConfigErrors the tsconfig files it
	// could not parse. Checked counts the files walked, Unchecked the
	// Svelte components, which the compiler does not read, and
	// Declarations the declaration nodes. Of CallSites — calls, news and
	// JSX elements — Calls are edges (distinct per caller), and the rest
	// resolve outside the tree's sources (CallsExternal), to a
	// declaration with no node (CallsUnplaced: a local function, a
	// parameter), or to nothing (CallsUnresolved). Unnamed counts
	// declarations whose name no ID can carry.
	Programs, Inferred, ConfigErrors          int
	Checked, Unchecked, Declarations, Unnamed int
	// Synthesized counts the SvelteKit configs the layer synthesized for
	// apps that were never synced (#232).
	Synthesized int
	// Implements counts the class → interface edges, Dispatch the calls
	// edges from a call to an interface member to an implementation of it
	// (#229), and Extends the class → class edges of extends clauses,
	// implements edges too (#249).
	Implements, Dispatch, Extends int
	// Translated counts the Svelte components the checker read, as
	// svelte2tsx translates them (#221); Unchecked those it did not.
	Translated                      int
	CallSites, Calls, CallsExternal int
	CallsUnplaced, CallsUnresolved  int
	// Skipped are the files no ID can name or that could not be read,
	// and the entries the walk could not open (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
}

// Extract reads every frontend file under root, resolves its imports
// with esbuild, and adds the layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("ts: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real // esbuild reports resolved paths; the walk must agree
	}
	d, err := discover(abs, !opts.Testdata, opts.Skip)
	if err != nil {
		return nil, fmt.Errorf("ts: discover under %s: %w", abs, err)
	}
	x := &extractor{b: b, m: b.Minter(), root: abs, report: &Report{Testdata: d.testdata},
		pkgs: map[string]bool{}, generated: map[string]*genFile{}}
	for _, u := range d.unread {
		x.skip(u.path, u.err)
	}
	if len(d.files) == 0 {
		x.report.Elapsed = time.Since(start)
		return x.report, nil
	}
	files, err := x.read(d)
	if err != nil {
		return nil, err
	}
	resolveByApp(abs, d, files)
	for _, f := range files {
		if err := x.file(f, d); err != nil {
			return nil, err
		}
	}
	for _, f := range files {
		if err := x.imports(f); err != nil {
			return nil, err
		}
	}
	if err := x.check(d, files); err != nil {
		return nil, err
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	root   string
	report *Report
	// pkgs records external package nodes already emitted.
	pkgs map[string]bool
	// generated maps a generated file's repo-relative path to what it
	// states, for the references edges of its importers.
	generated map[string]*genFile
}

// skip records that a file was left out.
func (x *extractor) skip(file string, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, 0, err))
}

// A source is one frontend file: its paths, its import clauses, and what
// esbuild resolved each to.
type source struct {
	abs, rel string
	gen      *genFile
	clauses  []importClause
}

// read loads every discovered file and scans its import clauses. A
// component contributes its <script> blocks. A file no ID can name, or
// that cannot be read, is skipped and recorded (#92); an import of it
// then names no node and gives no edge.
func (x *extractor) read(d *discovery) ([]*source, error) {
	out := make([]*source, 0, len(d.files))
	for _, abs := range d.files {
		// rel is the path as the file system names it, a map key and
		// the input of every ID and position minted for the file, each
		// of which escapes and labels it once (#124).
		rel, err := x.m.Rel(abs)
		if err == nil {
			_, err = x.m.TSID(rel, "")
		}
		if err != nil {
			x.skip(abs, err)
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			x.skip(abs, err)
			continue
		}
		s := &source{abs: abs, rel: rel, gen: parseGenerated(src)}
		if strings.HasSuffix(abs, ".svelte") {
			src = []byte(scriptBlocks(src))
		}
		s.clauses = importClauses(src)
		out = append(out, s)
	}
	return out, nil
}

// resolve asks esbuild's resolver where every clause's specifier leads,
// under the repository's own rules — tsconfig paths, extension-less and
// index imports, SvelteKit's aliases — and records the answer on the
// clause. One Build with an empty entry hosts the calls: the resolver is
// reachable only from inside a plugin. Nothing is parsed or bundled, so
// a type-only import, which the bundler would erase, resolves like any
// other: to this graph it is a dependency all the same.
// resolveByApp resolves each app's files under that app's aliases, and a
// file of no app under none; esbuild takes one alias table per build, so
// each is a build of its own (#99).
func resolveByApp(root string, d *discovery, files []*source) {
	byApp := map[*app][]*source{}
	for _, f := range files {
		a := d.appOf(f.abs)
		byApp[a] = append(byApp[a], f)
	}
	for a, fs := range byApp {
		var aliases map[string]string
		if a != nil {
			aliases = a.aliases
		}
		resolve(root, aliases, fs)
	}
}

func resolve(root string, aliases map[string]string, files []*source) {
	const entry = "alidade:entry"
	plugin := api.Plugin{Name: "alidade-resolve", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^` + entry + `$`}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: entry, Namespace: "alidade"}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "alidade"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			for _, f := range files {
				dir := filepath.Dir(f.abs)
				for i := range f.clauses {
					c := &f.clauses[i]
					r := pb.Resolve(c.spec, api.ResolveOptions{ResolveDir: dir, Importer: f.abs, Kind: c.resolveKind()})
					switch {
					case len(r.Errors) > 0:
						c.err = r.Errors[0].Text
					case r.External:
						c.external = true
					default:
						c.path = r.Path
					}
				}
			}
			empty := ""
			return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
		})
	}}
	api.Build(api.BuildOptions{
		EntryPoints:   []string{entry},
		Bundle:        true,
		Packages:      api.PackagesExternal,
		Write:         false,
		AbsWorkingDir: root,
		Alias:         aliases,
		LogLevel:      api.LogLevelSilent,
		Plugins:       []api.Plugin{plugin},
	})
}

// file emits one file node, and for a generated file its exports.
func (x *extractor) file(s *source, d *discovery) error {
	id, err := x.m.TSID(s.rel, "")
	if err != nil {
		return fmt.Errorf("ts: %w", err)
	}
	kind := graph.KindModule
	attrs := map[string]string{}
	if strings.HasSuffix(s.rel, ".svelte") {
		kind = graph.KindComponent
		x.report.Components++
		// A component the checker does not read says so (#215, #221);
		// one it reads loses nothing here.
	}
	x.report.Files++
	if s.gen != nil {
		x.report.Generated++
		attrs["generator"] = "protoc-gen-es"
		attrs["proto"] = s.gen.protoFile
		x.generated[s.rel] = s.gen
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: kind, Name: filepath.Base(s.rel), Pos: graph.Position{File: s.rel},
		Generated: s.gen != nil, Attrs: attrs}); err != nil {
		return err
	}
	if s.gen == nil {
		return nil
	}
	return x.generatedFile(id, s.rel, s.gen, d)
}

// imports emits the edges of one file's clauses: file → file where
// esbuild resolved the specifier inside the root, file → package where
// it is external, and for a generated target the references its clause
// names.
func (x *extractor) imports(s *source) error {
	from, err := x.m.TSID(s.rel, "")
	if err != nil {
		return fmt.Errorf("ts: %w", err)
	}
	for _, c := range s.clauses {
		switch {
		case c.err != "":
			x.report.Unresolved++
		case c.external:
			if err := x.external(from, c); err != nil {
				return err
			}
		default:
			if err := x.resolved(from, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// targetPath is a resolved import's repo-relative path, or false when it
// can be no node: a name no ID can carry, recorded when its file was
// skipped, or a file outside the root, counted here (#99).
func (x *extractor) targetPath(abs string) (string, bool) {
	if _, err := x.m.RelPath(abs); err == nil {
		rel, _ := x.m.Rel(abs)
		return rel, true
	}
	if _, perr := x.m.PosPath(abs); perr != nil {
		x.report.Outside++
	}
	return "", false
}

// resolved emits the file → file edge of a clause, and its references
// when the target is generated.
func (x *extractor) resolved(from graph.ID, c importClause) error {
	trel, ok := x.targetPath(c.path)
	if !ok {
		return nil
	}
	if !Source(trel) {
		x.report.Assets++
		return nil
	}
	to, err := x.m.TSID(trel, "")
	if err != nil {
		return nil // likewise: the target itself is recorded by [extractor.read]
	}
	x.report.Imports++
	if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImports, Derivation: graph.Resolved,
		Attrs: map[string]string{"kind": c.kind}}); err != nil {
		return err
	}
	if x.generated[trel] == nil || c.kind == kindDynamic {
		return nil
	}
	if len(c.names) == 0 {
		x.report.Unreadable++
		return nil
	}
	for _, n := range c.names {
		ref, err := x.m.TSID(trel, n)
		if err != nil {
			continue
		}
		x.report.References++
		if err := x.b.AddEdge(graph.Edge{From: from, To: ref, Kind: graph.KindReferences, Derivation: graph.Extracted}); err != nil {
			return err
		}
	}
	return nil
}

// external emits, once, the package node a bare import lands on, and the
// edge to it.
func (x *extractor) external(from graph.ID, c importClause) error {
	name := packageName(c.spec)
	if name == "" {
		return nil
	}
	to, err := x.m.TSPackageID(name)
	if err != nil {
		return nil // a specifier the grammar cannot hold is not a node
	}
	if !x.pkgs[name] {
		x.pkgs[name] = true
		if err := x.b.AddNode(graph.Node{ID: to, Kind: graph.KindPackage, Name: name, Attrs: map[string]string{"external": "true"}}); err != nil {
			return err
		}
	}
	x.report.External++
	return x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImports, Derivation: graph.Extracted,
		Attrs: map[string]string{"kind": c.kind}})
}

// packageName reduces a bare specifier to its package: "@scope/name" or
// "name", the subpath dropped; "node:fs" and SvelteKit's "$app/…" keep
// their first segment.
func packageName(spec string) string {
	if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return ""
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
