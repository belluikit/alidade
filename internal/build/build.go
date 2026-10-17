// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package build is the pipeline of ADR 001: extract each layer, merge into
// one graph. It knows which adapters exist and nothing about what they
// find, and it is the only place a consumer needs to import to get a
// graph of a repository.
package build

import (
	"cmp"
	"fmt"
	"github.com/belluikit/alidade/internal/scope"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/belluikit/alidade/internal/adapter/goasm"
	"github.com/belluikit/alidade/internal/adapter/golang"
	"github.com/belluikit/alidade/internal/adapter/markdown"
	"github.com/belluikit/alidade/internal/adapter/proto"
	"github.com/belluikit/alidade/internal/adapter/pxf"
	"github.com/belluikit/alidade/internal/adapter/py"
	"github.com/belluikit/alidade/internal/adapter/sql"
	"github.com/belluikit/alidade/internal/adapter/ts"
	"github.com/belluikit/alidade/internal/graph"
)

// Options selects what each layer extracts. Layers not yet built have no
// entry here; a new adapter adds one.
type Options struct {
	Go       golang.Options
	Proto    proto.Options
	SQL      sql.Options
	PXF      pxf.Options
	Goasm    goasm.Options
	TS       ts.Options
	Py       py.Options
	Markdown markdown.Options
	// SchemaRoots are further repositories whose .proto layer joins the
	// graph (ADR 002 rule 12), for a repository whose generated code
	// comes from schemas kept elsewhere (#47). Each is labelled by its
	// directory name: its file nodes are proto://file/@label/…, its
	// messages proto://pkg.Message like anyone's, and its Go types are
	// not linked, since they are not in the graph.
	SchemaRoots []string
}

// DefaultOptions is every layer's own default.
func DefaultOptions() Options {
	return Options{Go: golang.DefaultOptions(), Proto: proto.DefaultOptions(), SQL: sql.DefaultOptions(),
		PXF: pxf.DefaultOptions(), Goasm: goasm.DefaultOptions(), TS: ts.DefaultOptions(), Py: py.DefaultOptions(),
		Markdown: markdown.DefaultOptions()}
}

// A Report says what the build did: which layers ran, what each found,
// and how long the whole thing took, since ADR 005 rests on that number.
type Report struct {
	Root     string
	Go       *golang.Report
	Proto    *proto.Report
	SQL      *sql.Report
	PXF      *pxf.Report
	Goasm    *goasm.Report
	TS       *ts.Report
	Py       *py.Report
	Markdown *markdown.Report
	// Schema reports each schema root's .proto layer, in the order given.
	Schema []SchemaReport
	// Rebuilt and Kept say, for a build after the first, which layers
	// ran because their files changed and which kept their previous
	// contributions (#56); a schema root is "proto@<label>". Both empty
	// on a first build. Signature is how long deciding took.
	Rebuilt, Kept []string
	// Overlay says how a worktree's graph was made from the base's (#25);
	// nil for the base.
	Overlay   *Overlay
	Signature time.Duration
	Elapsed   time.Duration
}

// A SchemaReport is one schema root's contribution.
type SchemaReport struct {
	Root, Label string
	Proto       *proto.Report
}

// Skipped gathers what every layer left out because no ID could name it
// or it could not be read (#92), sorted by file and line. Each is tagged
// with its layer; an entry several layers left out for one reason — a
// directory none of their walks could open — is one, naming each of them
// once.
func (r *Report) Skipped() []graph.Skip {
	var out []graph.Skip
	add := func(layer string, ss []graph.Skip) {
		for _, s := range ss {
			s.Layer = layer
			out = append(out, s)
		}
	}
	if r.Go != nil {
		add("go", r.Go.Skipped)
	}
	if r.Proto != nil {
		add("proto", r.Proto.Skipped)
	}
	if r.SQL != nil {
		add("sql", r.SQL.Skipped)
	}
	if r.PXF != nil {
		add("pxf", r.PXF.Skipped)
	}
	if r.Goasm != nil {
		add("goasm", r.Goasm.Skipped)
	}
	if r.TS != nil {
		add("ts", r.TS.Skipped)
	}
	if r.Py != nil {
		add("py", r.Py.Skipped)
	}
	if r.Markdown != nil {
		add("md", r.Markdown.Skipped)
	}
	for _, sr := range r.Schema {
		add("proto@"+sr.Label, sr.Proto.Skipped)
	}
	slices.SortStableFunc(out, func(a, b graph.Skip) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Reason, b.Reason))
	})
	return mergeSkips(out)
}

// mergeSkips folds the records of one file, line and reason — sorted, so
// they are adjacent — into one naming every layer that reported it,
// each layer once: one layer can report one reason twice, two
// references to the same unnameable path in one line.
func mergeSkips(sorted []graph.Skip) []graph.Skip {
	var merged []graph.Skip
	for _, s := range sorted {
		n := len(merged)
		if n == 0 || merged[n-1].File != s.File || merged[n-1].Line != s.Line || merged[n-1].Reason != s.Reason {
			merged = append(merged, s)
			continue
		}
		if !slices.Contains(strings.Fields(merged[n-1].Layer), s.Layer) {
			merged[n-1].Layer += " " + s.Layer
		}
	}
	return merged
}

// Layers names the layers that ran, in a fixed order: the ones that read
// a file of their own. A report kept only to carry what its layer
// skipped — a directory none of the walks could open, in a tree with
// none of that layer's files — read nothing, and naming it would claim a
// .proto or .sql layer for a repository that has neither ([layer], #61).
func (r *Report) Layers() []string {
	var out []string
	for _, l := range []struct {
		name string
		ran  bool
	}{
		{"go", r.Go != nil && len(r.Go.Modules)+len(r.Go.Failed) > 0},
		{"proto", (r.Proto != nil && r.Proto.Files > 0) || len(r.Schema) > 0},
		{"sql", r.SQL != nil && r.SQL.Files > 0},
		{"pxf", r.PXF != nil && r.PXF.Files > 0},
		{"goasm", r.Goasm != nil && r.Goasm.Files > 0},
		{"ts", r.TS != nil && r.TS.Files > 0},
		{"py", r.Py != nil && r.Py.Files > 0},
		{"md", r.Markdown != nil && r.Markdown.Files > 0},
	} {
		if l.ran {
			out = append(out, l.name)
		}
	}
	return out
}

// Build extracts every available layer of the repository at root and
// merges the result: one build, nothing kept. See [Session] for rebuilds
// that keep untouched layers.
func Build(root string, opts Options) (*graph.Graph, *Report, error) {
	s, err := NewSession(root, opts)
	if err != nil {
		return nil, nil, err
	}
	return s.Build()
}

// A Session builds one repository's graph and can build it again. Every
// layer's contributions are tagged in the Builder; on a rebuild, a layer
// whose files' contents are unchanged keeps them and does not run, a
// layer whose files changed is dropped and rerun, and the whole is
// folded again (#56). Nothing leaves the process: a Session lives as long
// as the server holds it, and a kept layer is one whose inputs are
// byte-identical to the build that produced it, which is not the
// staleness ADR 005 rejects.
//
// One step follows every merge: the Markdown layer's mentions — code
// spans naming a declaration — are resolved against the merged graph by
// [markdown.Link], which adds documents edges, and the graph is built
// again. It reruns on every build, since it reads the whole graph.
//
// A layer that finds nothing to do is not an error: the Go layer runs
// when a go.mod is found under root — every one found is a module of the
// graph (#67) — and a repository without one gets no Go layer; the
// .proto, SQL, .pxf, goasm and frontend layers run when the tree holds a
// file of theirs. A root that is not a directory is an
// error, since an empty graph for a mistyped path would read as an empty
// repository.
type Session struct {
	root string
	opts Options
	b    *graph.Builder
	sigs map[string]string
	rep  *Report
	// stale names the layers a failed build dropped: whatever they
	// contributed before failing is not their output, so the next build
	// reruns them whatever their signature says (#93).
	stale map[string]bool
	// labels are the schema-root labels registered with the Builder,
	// which refuses one registered twice.
	labels map[string]bool
	// skip are the directories this build leaves out, read from the
	// root's .alidade.pxf at its start (ADR 003, amended for #215).
	skip *scope.Dirs
	// goFiles are the content hashes of the files the Go layer read, by
	// repo-relative path, as of the last build: what a worktree's graph
	// compares with to find the Go packages that differ (#25).
	goFiles map[string]string
	// gen counts the builds this session has made, so a worktree's graph
	// over it knows when the base it took layers from has moved. A
	// refresh does not count: a worktree's Python layer taken from the
	// base follows jedi through Pending, as the base's does.
	gen int
	// base is the session a worktree's session lays its differences
	// over (#25), nil for the base itself; own are the worktree's own
	// layer signatures in the build under way, and from says, per layer,
	// whether its contributions are the base's ("base") or its own.
	base *Session
	own  map[string]string
	from map[string]string
	// pendingGo are the Go layer's file hashes of the build under way,
	// and overlaid what its Go layer last laid over the base's.
	pendingGo map[string]string
	overlaid  *Overlay
	// pyAt is the version of jedi's answers (#269) the Python layer was
	// last made from, read before it was: a build that keeps the layer
	// folds in what jedi has learnt since, which no signature shows (#34).
	pyAt int64
}

// NewSession checks root and prepares a Session; nothing is built yet.
func NewSession(root string, opts Options) (*Session, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("build: %s is not a directory", abs)
	}
	// The root the IDs are relativized against must be the path the
	// tools report: go/packages and esbuild resolve symbolic links (a
	// macOS temporary directory is one), and a path outside the root
	// mints nothing.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return &Session{root: abs, opts: opts, b: graph.NewBuilder(abs), sigs: map[string]string{},
		stale: map[string]bool{}, labels: map[string]bool{}, from: map[string]string{}}, nil
}

// Build builds the graph, or on a later call rebuilds the layers whose
// files changed.
func (s *Session) Build() (*graph.Graph, *Report, error) {
	start := time.Now()
	skip, err := scope.Load(s.root)
	if err != nil {
		return nil, nil, fmt.Errorf("build: %w", err)
	}
	s.skip = skip
	if s.base != nil && s.base.rep == nil {
		return nil, nil, fmt.Errorf("build: the graph of %s is not built, and a worktree's graph is laid over it", s.base.root)
	}
	sigs, goFiles, err := s.signatures()
	if err != nil {
		return nil, nil, fmt.Errorf("build: signatures: %w", err)
	}
	s.own, sigs = sigs, s.overBase(sigs)
	s.pendingGo = goFiles
	rep := &Report{Root: s.root, Signature: time.Since(start)}
	first := s.rep == nil
	prev := s.rep
	if first {
		prev = &Report{}
	}
	var dropped []string
	// drop removes a layer's contributions and records that it did, so a
	// build that fails afterwards leaves every layer it emptied stale.
	// Every signed layer goes through here; the one drop that went
	// around it was kept, empty, by the next build (#93).
	drop := func(name string) {
		s.b.Drop(name)
		dropped = append(dropped, name)
	}
	changed := func(name string) bool {
		if !first && !s.stale[name] && sigs[name] == s.sigs[name] {
			return false
		}
		if !first {
			rep.Rebuilt = append(rep.Rebuilt, name)
		}
		drop(name)
		return true
	}
	if err := s.layers(rep, prev, changed, drop); err != nil {
		for _, name := range dropped {
			s.stale[name] = true
		}
		return nil, nil, err
	}
	clear(s.stale)
	if !first {
		rep.Rebuilt = presentOnly(rep.Rebuilt, rep, prev)
		rep.Kept = kept(rep)
	}
	s.sigs, s.goFiles = sigs, goFiles
	if s.base != nil {
		rep.Overlay = s.overlayReport(rep)
	}
	g := s.fold(rep)
	rep.Elapsed = time.Since(start)
	s.rep = rep
	s.gen++
	return g, rep, nil
}

// fold builds the graph from every layer's contributions, then runs the
// passes that read the whole of it.
func (s *Session) fold(rep *Report) *graph.Graph {
	s.b.Drop("pxf-schema")
	s.b.Drop("md-link")
	g := s.b.Build()
	if rep.PXF != nil {
		// Before the Markdown linker, which reads these edges to tell a
		// schema field from the .pxf fields that set it (#190).
		pxf.LinkSchema(s.b.Layer("pxf-schema"), g, rep.PXF)
		g = s.b.Build()
	}
	if rep.Markdown != nil {
		markdown.Link(s.b.Layer("md-link"), g, rep.Markdown)
		g = s.b.Build()
	}
	return g
}

// Pending says what a layer still being completed in the background
// (#269) has left: a version that changes whenever more is known, and
// what an answer should say about it, "" once nothing is pending. Python
// is the one such layer, with py.Options.Background.
func (s *Session) Pending() (int64, string) {
	if s.rep == nil || s.rep.Py == nil || s.rep.Py.Live == nil {
		return 0, ""
	}
	l := s.rep.Py.Live
	v := l.Version()
	resolved, total, done := l.Progress()
	if done {
		return v, ""
	}
	return v, fmt.Sprintf("jedi has resolved the calls of %d of %d Python files; calls from the rest are INFERRED by name until it reaches them", resolved, total)
}

// Close ends the session: a layer still completing in the background
// stops, and keeps what it has for the next session (#279).
func (s *Session) Close() {
	if s.from["py"] == "base" {
		return // the base's to close
	}
	if s.rep != nil && s.rep.Py != nil && s.rep.Py.Live != nil {
		s.rep.Py.Live.Close()
	}
}

// Refresh builds the graph again from what the layers completing in the
// background know now, without rerunning any layer: the Python layer is
// rebuilt from jedi's answers so far and the warm-up pass's for the rest.
func (s *Session) Refresh() (*graph.Graph, *Report, error) {
	if s.rep == nil {
		return s.Build()
	}
	start := time.Now()
	rep := *s.rep
	if s.from["py"] == "base" {
		if err := s.refreshFromBase(&rep); err != nil {
			return nil, nil, err
		}
	} else if rep.Py != nil && rep.Py.Live != nil {
		if err := s.refreshPy(&rep); err != nil {
			return nil, nil, err
		}
	}
	g := s.fold(&rep)
	rep.Rebuilt, rep.Kept = nil, nil
	rep.Elapsed = time.Since(start)
	s.rep = &rep
	return g, &rep, nil
}

// pyCurrent brings the Python layer the build has to what jedi knows
// now: the base's, for a worktree whose Python files are the base's, or
// the session's own when it was kept (#34). live is the session's own
// previous jedi.
func (s *Session) pyCurrent(rep, prev *Report, live *py.Live) error {
	switch {
	case s.from["py"] == "base":
		if live != nil {
			live.Close() // the worktree's own jedi, its files now the base's
		}
		if s.base.pyBehind(s.base.rep) {
			if _, _, err := s.base.Refresh(); err != nil {
				return err
			}
		}
		s.pyFromBase(rep)
	case rep.Py != prev.Py:
		s.pyAt = 0 // a new jedi, its version counted from 0
	case s.pyBehind(rep):
		return s.refreshPy(rep)
	}
	return nil
}

// pyBehind says whether jedi has read more since rep's Python layer was
// made (#34).
func (s *Session) pyBehind(rep *Report) bool {
	return rep != nil && rep.Py != nil && rep.Py.Live != nil && rep.Py.Live.Version() != s.pyAt
}

// refreshPy makes rep's Python layer again from what jedi knows now.
func (s *Session) refreshPy(rep *Report) error {
	l := rep.Py.Live
	v := l.Version()
	s.b.Drop("py")
	r, err := py.Refresh(s.b.Layer("py"), l)
	if err != nil {
		s.stale["py"] = true
		return fmt.Errorf("build: %w", err)
	}
	rep.Py, s.pyAt = r, v
	return nil
}

// withSkip gives every layer the build's list of directories left out.
func withSkip(opts Options, skip *scope.Dirs) Options {
	opts.Go.Skip, opts.Proto.Skip, opts.SQL.Skip, opts.PXF.Skip = skip, skip, skip, skip
	opts.Goasm.Skip, opts.TS.Skip, opts.Py.Skip, opts.Markdown.Skip = skip, skip, skip, skip
	return opts
}

// presentOnly keeps the names of layers present in either report: a
// layer whose signature bucket changed but that had no files before or
// after ran and found nothing, and is not a rebuild worth naming (#61).
func presentOnly(names []string, rep, prev *Report) []string {
	present := map[string]bool{}
	for _, r := range []*Report{rep, prev} {
		for _, n := range r.Layers() {
			if n != "proto" || r.Proto != nil {
				present[n] = true
			}
		}
		for _, sr := range r.Schema {
			present["proto@"+sr.Label] = true
		}
	}
	var out []string
	for _, n := range names {
		if present[n] {
			out = append(out, n)
		}
	}
	return out
}

// kept lists the layers present in a report that were not rebuilt: the
// ones whose previous contributions stand.
func kept(rep *Report) []string {
	rebuilt := map[string]bool{}
	for _, n := range rep.Rebuilt {
		rebuilt[n] = true
	}
	var out []string
	for _, n := range rep.Layers() {
		if n == "proto" && rep.Proto == nil {
			continue // present through a schema root only
		}
		if !rebuilt[n] {
			out = append(out, n)
		}
	}
	for _, sr := range rep.Schema {
		if n := "proto@" + sr.Label; !rebuilt[n] {
			out = append(out, n)
		}
	}
	return out
}

// signatures computes the layer signatures of the root and of each
// schema root.
func (s *Session) signatures() (map[string]string, map[string]string, error) {
	sigs, goFiles, err := signaturesAndGoFiles(s.root, s.skip)
	if err != nil {
		return nil, nil, err
	}
	for _, root := range s.opts.SchemaRoots {
		abs, label, err := schemaRoot(root)
		if err != nil {
			return nil, nil, err
		}
		skip, err := scope.Load(abs)
		if err != nil {
			return nil, nil, err
		}
		ss, err := signatures(abs, skip)
		if err != nil {
			return nil, nil, err
		}
		sigs["proto@"+label] = ss["proto"]
	}
	return sigs, goFiles, nil
}

// layers runs each layer whose signature changed and copies the report
// of each kept one from the previous build. drop is for the one layer
// that goes away without a signature deciding it.
func (s *Session) layers(rep, prev *Report, changed func(string) bool, drop func(string)) error {
	b, abs, opts := s.b, s.root, withSkip(s.opts, s.skip)
	var err error
	if mods, skipped, err := golang.Modules(abs, s.skip); err != nil {
		return fmt.Errorf("build: %w", err)
	} else if len(mods) == 0 && len(skipped) == 0 {
		// A go.mod that went away takes the Go layer with it. The drop
		// is recorded like any other, so a build that fails after it
		// does not leave the layer looking unchanged (#93).
		drop("go")
		delete(s.from, "go")
	} else if rep.Go, err = rerun(b, "go", prev.Go, changed, s.goLayer(abs, opts.Go), nil); err != nil {
		return err
	}
	if rep.Proto, err = rerun(b, "proto", prev.Proto, changed, inherit(s, "proto", s.baseReport().Proto, func(lb *graph.Builder) (*proto.Report, error) { return proto.Extract(lb, abs, opts.Proto) }), func(r *proto.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	if rep.SQL, err = rerun(b, "sql", prev.SQL, changed, inherit(s, "sql", s.baseReport().SQL, func(lb *graph.Builder) (*sql.Report, error) { return sql.Extract(lb, abs, opts.SQL) }), func(r *sql.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	if rep.PXF, err = rerun(b, "pxf", prev.PXF, changed, inherit(s, "pxf", s.baseReport().PXF, func(lb *graph.Builder) (*pxf.Report, error) { return pxf.Extract(lb, abs, opts.PXF) }), func(r *pxf.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	if rep.Goasm, err = rerun(b, "goasm", prev.Goasm, changed, inherit(s, "goasm", s.baseReport().Goasm, func(lb *graph.Builder) (*goasm.Report, error) { return goasm.Extract(lb, abs, opts.Goasm) }), func(r *goasm.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	if rep.TS, err = rerun(b, "ts", prev.TS, changed, inherit(s, "ts", s.baseReport().TS, func(lb *graph.Builder) (*ts.Report, error) { return ts.Extract(lb, abs, opts.TS) }), func(r *ts.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	live := s.ownLive(prev)
	if rep.Py, err = rerun(b, "py", prev.Py, changed, inherit(s, "py", s.baseReport().Py, func(lb *graph.Builder) (*py.Report, error) {
		po := opts.Py
		if live != nil {
			live.Stop() // jedi reading the files as they were
			po.Reuse = live
		}
		return py.Extract(lb, abs, po)
	}), func(r *py.Report) int { return r.Files + len(r.Skipped) }); err != nil {
		return err
	}
	if err := s.pyCurrent(rep, prev, live); err != nil {
		return err
	}
	if rep.Schema, err = s.schemaRoots(prev, changed); err != nil {
		return err
	}
	rep.Markdown, err = rerun(b, "md", prev.Markdown, changed, inherit(s, "md", s.baseReport().Markdown, func(lb *graph.Builder) (*markdown.Report, error) { return markdown.Extract(lb, abs, opts.Markdown) }), func(r *markdown.Report) int { return r.Files + len(r.Skipped) })
	return err
}

// rerun runs one layer through its tagged Builder when changed says its
// files changed, and keeps the previous report otherwise. A layer whose
// report says it read no file is reported as absent, as before; files
// is nil for the Go layer, which is present whenever go.mod is.
func rerun[R any](b *graph.Builder, name string, prev *R, changed func(string) bool, extract func(*graph.Builder) (*R, error), files func(*R) int) (*R, error) {
	if !changed(name) {
		return prev, nil
	}
	if files == nil {
		r, err := extract(b.Layer(name))
		if err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
		return r, nil
	}
	return layer(func() (*R, error) { return extract(b.Layer(name)) }, files)
}

// layer runs a file-driven layer and keeps its report when it read a
// file or skipped an input, dropping it when the tree had neither, so
// nothing a layer recorded is lost. Reading a file is what makes it one
// of [Report.Layers]: a report holding skips alone is carried, not
// named.
func layer[R any](extract func() (*R, error), files func(*R) int) (*R, error) {
	rep, err := extract()
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if files(rep) == 0 {
		return nil, nil
	}
	return rep, nil
}

// extractGo is the Go layer's extraction, a variable so a test can make
// it fail: since #92 no input does, and a failed layer must still never
// be kept (#93).
var extractGo = golang.Extract

// schemaRoot resolves and labels one schema root: an absolute, symlink-
// resolved directory, labelled by its directory name.
func schemaRoot(root string) (abs, label string, err error) {
	abs, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("build: schema root: %w", err)
	}
	if st, err := os.Stat(abs); err != nil {
		return "", "", fmt.Errorf("build: schema root: %w", err)
	} else if !st.IsDir() {
		return "", "", fmt.Errorf("build: schema root %s is not a directory", abs)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs, filepath.Base(abs), nil
}

// schemaRoots runs the .proto layer of each schema root through a
// secondary builder, so its messages are the nodes the primary root's
// generated code names and its files are labelled apart (ADR 002 rule
// 12). Each root is its own layer, "proto@<label>", kept or rerun on its
// own signature; a label used twice is refused by the Builder on the
// first build, and on a rebuild the roots are the Session's, unchanged.
func (s *Session) schemaRoots(prev *Report, changed func(string) bool) ([]SchemaReport, error) {
	var out []SchemaReport
	seen := map[string]bool{}
	for i, root := range s.opts.SchemaRoots {
		abs, label, err := schemaRoot(root)
		if err != nil {
			return nil, err
		}
		if seen[label] {
			return nil, fmt.Errorf("build: secondary root label %q used twice", label)
		}
		seen[label] = true
		name := "proto@" + label
		if !changed(name) {
			out = append(out, prev.Schema[i])
			continue
		}
		if s.inherits(name) {
			s.adopt(s.b.Layer(name), name)
			out = append(out, s.base.rep.Schema[i])
			continue
		}
		s.from[name] = "own"
		sb, err := s.secondary(name, label, abs)
		if err != nil {
			return nil, err
		}
		po := s.opts.Proto
		po.NoGoLink = true
		if po.Skip, err = scope.Load(abs); err != nil {
			return nil, fmt.Errorf("build: schema root %s: %w", abs, err)
		}
		pr, err := proto.Extract(sb, abs, po)
		if err != nil {
			return nil, fmt.Errorf("build: schema root %s: %w", abs, err)
		}
		out = append(out, SchemaReport{Root: abs, Label: label, Proto: pr})
	}
	return out, nil
}

// secondary returns the layer-tagged secondary Builder for a schema
// root, registering its label once: on the first build that reaches it,
// which is not the first build when that one failed earlier (#93).
func (s *Session) secondary(name, label, abs string) (*graph.Builder, error) {
	if s.labels[label] {
		m, err := s.b.Minter().Secondary(label, abs)
		if err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
		return s.b.Layer(name).WithMinter(m), nil
	}
	sb, err := s.b.Layer(name).Secondary(label, abs)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	s.labels[label] = true
	return sb, nil
}
