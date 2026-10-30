// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto

import (
	"context"
	"errors"
	"fmt"
	"github.com/belluikit/alidade/internal/scope"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trendvidia/protocompile"
	_ "github.com/trendvidia/protocompile/experimentalcompile" // wires Compiler.Compile
	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/reporter"
	"github.com/trendvidia/protos"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/belluikit/alidade/internal/graph"
)

// Options controls the .proto layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *scope.Dirs
	// Testdata includes .proto files under testdata directories. They
	// are out by default, as they are for the Go layer (the Go toolchain
	// never loads testdata): a fixture tree usually repeats declarations
	// the repository already has, and every repeat is a merge conflict.
	Testdata bool
	// NoGoLink leaves out the generates edges to the Go types
	// protoc-gen-go names. A schema root (ADR 002 rule 12) sets it: its
	// generated Go lives in its own repository, not in the graph, and
	// an edge to a node that is absent by construction would only be
	// dropped and counted.
	NoGoLink bool
}

// DefaultOptions returns the layer's defaults: testdata out.
func DefaultOptions() Options { return Options{} }

// A Report says what Extract found.
type Report struct {
	// Files is how many .proto files were discovered; Failed how many of
	// them the compiler rejected. A failed file has a node and nothing
	// else.
	Files, Failed int
	// Decls counts emitted declarations, files excluded.
	Decls int
	// Generates counts message and enum nodes given a generates edge to
	// a Go type.
	Generates int
	// External counts files imported that are not in the repository.
	External int
	// Testdata counts .proto files skipped under testdata directories.
	Testdata int
	// NamingErrors counts files whose Go type names protoc-gen-go
	// refused to derive; each carries the reason in go_names_error.
	NamingErrors int
	// Skipped are the files and references left out because no ID can
	// name them, and the directories the walk could not open (#92).
	Skipped []graph.Skip
	// Elapsed is the whole extraction.
	Elapsed time.Duration
}

// Extract compiles every .proto file under root and adds the schema layer
// to b. Each file is compiled on its own, with its imports resolved
// through the include roots, so one file's error or one fixture's
// duplicate symbols leave the others whole.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("proto: %w", err)
	}
	c, err := discover(abs, !opts.Testdata, opts.Skip)
	if err != nil {
		return nil, fmt.Errorf("proto: discover under %s: %w", abs, err)
	}
	x := &extractor{opts: opts,
		b: b, m: b.Minter(), root: abs, roots: c.roots, inferred: c.inferred, gen: loadGenConfigs(abs),
		ext: make(map[string]bool), report: &Report{Files: len(c.files), Testdata: c.testdata},
	}
	for _, s := range c.unread {
		x.report.Skipped = append(x.report.Skipped, x.m.Skip(s.path, 0, s.err))
	}
	for _, f := range c.files {
		if err := x.file(f); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

type extractor struct {
	b     *graph.Builder
	m     *graph.Minter
	opts  Options
	root  string
	roots []string
	// inferred are the roots no buf configuration declares (discover).
	inferred map[string]bool
	gen      []genConfig
	ext      map[string]bool
	report   *Report
	// rel is the file being emitted, for [extractor.skip]; cur is the
	// include roots in the order that file resolves through — its own
	// root first (see rootsForFile).
	rel string
	cur []string
}

// skip records that something in the current file was left out.
func (x *extractor) skip(line int, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(x.rel, line, err))
}

// goImportPath returns the Go import path for a file: what buf's managed
// mode assigns when a buf.gen.yaml generates it — managed mode replaces a
// written option (bufgen.go) — else its go_package option, else "".
func (x *extractor) goImportPath(fd protoreflect.FileDescriptor) string {
	if p := locate(x.cur, fd.Path()); p != "" {
		if rel, err := x.m.Rel(p); err == nil {
			if managed := goImportPathFor(x.gen, rel, fd.Path()); managed != "" {
				return managed
			}
		}
	}
	return goPackage(fd)
}

// compile compiles one file, returning its descriptor, or the first error
// the compiler reported if it did not compile.
func (x *extractor) compile(file string) (linker.File, error) {
	var mu sync.Mutex
	var first error
	c := protocompile.Compiler{
		// The repository first, so its own copy of a shared schema wins;
		// then the schemas pxf build bundles, which a repository imports
		// and does not hold (#59); then the standard imports.
		Resolver: protocompile.WithStandardImports(protocompile.CompositeResolver{
			&protocompile.SourceResolver{ImportPaths: x.cur},
			&protocompile.SourceResolver{Accessor: func(p string) (io.ReadCloser, error) { return protos.Files.Open(p) }},
		}),
		SourceInfoMode: protocompile.SourceInfoStandard,
		Reporter: reporter.NewReporter(func(err reporter.ErrorWithPos) error {
			mu.Lock()
			defer mu.Unlock()
			if first == nil {
				first = err
			}
			return nil // keep going, so the descriptor of what did compile survives
		}, nil),
	}
	imp := importPath(x.cur, file)
	res, err := c.Compile(context.Background(), imp)
	if err != nil && !errors.Is(err, reporter.ErrInvalidSource) {
		return nil, err
	}
	if len(res) == 0 || res[0] == nil {
		if first == nil {
			first = errors.New("did not compile")
		}
		return nil, first
	}
	if first != nil {
		// A descriptor came back alongside errors: the file linked but
		// something in it is wrong. Treat it as failed rather than
		// emit declarations from a schema the compiler rejects.
		return nil, first
	}
	return res[0], nil
}

// file emits one .proto file: its node, then everything in it if it
// compiled.
//
// A file no ID can name is skipped and recorded (#92).
func (x *extractor) file(path string) error {
	x.rel = path
	x.cur = rootsForFile(x.roots, x.inferred, path)
	id, err := x.m.ProtoFileID(path)
	if err != nil {
		x.skip(0, err)
		return nil
	}
	fd, cerr := x.compile(path)
	if cerr != nil {
		x.report.Failed++
		return x.b.AddNode(graph.Node{
			ID: id, Kind: graph.KindProtoFile, Name: filepath.Base(path), Pos: graph.Position{File: path},
			Attrs: map[string]string{"error": firstLine(cerr.Error())},
		})
	}
	attrs := map[string]string{"package": string(fd.Package())}
	if gp := x.goImportPath(fd); gp != "" {
		attrs["go_package"] = gp
	}
	names, nerr := goNames(fd, x.goImportPath)
	if nerr != nil {
		// The file compiles but protoc-gen-go would refuse it, so no
		// Go types are named; say why on the node rather than emit
		// nothing silently.
		attrs["go_names_error"] = firstLine(nerr.Error())
		x.report.NamingErrors++
	}
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindProtoFile, Name: filepath.Base(path), Pos: graph.Position{File: path}, Attrs: attrs}); err != nil {
		return err
	}
	if err := x.imports(id, fd); err != nil {
		return err
	}
	w := &walker{x: x, rel: path, fd: fd, names: names}
	return w.file(id)
}

// imports emits an imports edge per import, to the file in the repository
// that answers it or to an external file node if none does.
func (x *extractor) imports(from graph.ID, fd protoreflect.FileDescriptor) error {
	imps := fd.Imports()
	for i := range imps.Len() {
		to, err := x.fileNode(imps.Get(i).Path())
		if err != nil {
			x.skip(0, err)
			continue
		}
		if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImports, Derivation: graph.Extracted}); err != nil {
			return err
		}
	}
	return nil
}

// fileNode returns the node ID for an import path: the repository file
// it resolves to, or an external file node emitted once.
func (x *extractor) fileNode(imp string) (graph.ID, error) {
	if p := locate(x.cur, imp); p != "" {
		if _, err := x.m.RelPath(p); err == nil {
			id, err := x.m.ProtoFileID(p)
			if err != nil {
				return "", fmt.Errorf("proto: %w", err)
			}
			return id, nil
		}
	}
	id, err := x.m.ProtoFileID(imp)
	if err != nil {
		return "", fmt.Errorf("proto: %w", err)
	}
	if !x.ext[imp] {
		x.ext[imp] = true
		x.report.External++
		if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindProtoFile, Name: filepath.Base(imp), Attrs: map[string]string{"external": "true"}}); err != nil {
			return "", err
		}
	}
	return id, nil
}

// goPackage returns the go_package option's import path, or "".
func goPackage(fd protoreflect.FileDescriptor) string {
	return string(goImportPath(fd))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
