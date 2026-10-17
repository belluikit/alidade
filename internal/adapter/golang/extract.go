// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/trendvidia/caliper/pkg/analysis"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"

	"github.com/belluikit/alidade/internal/graph"
)

// A Report says what Extract found, for the caller's accounting. Counts
// are of what was emitted, each distinct edge once; the core may still
// fold a node reported by both the plain and the test variant of a
// package.
type Report struct {
	// Modules are the paths of the modules loaded, the root's first;
	// every go.mod under the directory is one (#67). Failed names the
	// modules below the root that did not load; each keeps a package
	// node carrying the error, and edges into it from the others land
	// on external package nodes, since nothing of it is in the graph.
	Modules, Failed []string
	// Build is the host's configuration, the one every load starts
	// from, as GOOS/GOARCH: "darwin/arm64". Configs lists every
	// configuration loaded, host first; Options.Configs asks for more.
	// A file no loaded configuration includes contributed nothing;
	// Excluded counts those files across the module's packages, and each
	// package node lists its own in an "excluded" attribute, so a reader
	// can see what the graph is missing. Recovered counts the files a
	// further configuration brought in, and Added the nodes and edges
	// those configurations contributed beyond the host's (#43).
	Build     string
	Configs   []string
	Excluded  int
	Recovered int
	Added     int
	// Packages, Decls and Calls count emitted nodes and calls edges
	// within the module. With ExactTests they and Boundary are the load
	// without tests' counts, so they leave out the test declarations and
	// calls the exact graph does carry; of the load with tests the report
	// keeps only Tests and TestModuleEdges (#133, #142).
	Packages int
	Decls    int
	Calls    int
	// Fields counts the struct field nodes among Decls (#193).
	Fields int
	// Boundary counts calls edges that left the module and were kept at
	// package granularity.
	Boundary int
	// ModuleEdges counts the edges that leave each module for the
	// host configuration, by where they land — another module of the
	// repository, or "" for a package outside it — and by kind, sorted
	// (#76). It is the number a question such as "what in the use case
	// reaches into the engine" wants, read from the tool rather than
	// reconstructed from the output, with the edges to the standard
	// library and third-party modules — which the layer keeps at package
	// granularity — counted apart under "". Only production edges are
	// counted here, with tests loaded or not; TestModuleEdges counts the
	// test edges, so the two sum to the graph's (#142).
	ModuleEdges []ModuleEdge
	// TestModuleEdges counts, the same way, the edges touching a test
	// declaration: how tests reach across modules (#142).
	TestModuleEdges []ModuleEdge
	edges           map[ModuleEdge]int
	testEdges       map[ModuleEdge]int
	// Generated counts declarations that came from generated files.
	Generated int
	// Tests counts the module's Go nodes carrying [graph.Node.Test]
	// under Options.Tests: the declarations of _test.go files, the node
	// holding a package's in-package test files (#128), and the package
	// node of an external test package, which has no file of its own. A
	// package outside the module carries the flag when only a test
	// reaches it; those are not counted, as no other node outside the
	// module is.
	// The core derives the same flag on every edge touching one, so
	// leaving them out gives the graph a build without tests would
	// (#81).
	Tests int
	// Skipped are the go.mod files that name no module (#92).
	Skipped []graph.Skip
	// ExactTests says the production view was built from a load without
	// tests (#133).
	ExactTests bool
	// Outside counts the package-level declarations whose source is not
	// in the tree — cgo's _Cfunc_ and _Ctype_ wrappers, written into the
	// build cache — which are left out with every edge to or from them
	// (#92). A call to C.f is then no edge, as a call into C is none. A
	// declaration positioned outside the tree that is not cgo's — a file
	// whose //line directive names a source elsewhere — is counted too,
	// and its file is named in Skipped, once (#109).
	Outside int
	// FailedPackages names, by package path, the packages of the modules
	// that did not type-check (caliper#38, #95); each has a package node
	// carrying its first error, and the declarations the type checker did
	// resolve marked partial. WithoutCalls names the packages whose calls
	// the call graph could not reach, because a package they import has
	// an error outside a function body. Both hold every configuration's
	// failures, the host's and a further one's, once each.
	FailedPackages, WithoutCalls []string
	// Load, CallGraph and Emit are how long caliper's package load, its
	// SSA and call-graph build, and this adapter's own passes took. ADR
	// 005 rests on the total staying seconds, not minutes, and names
	// five seconds as the point to revisit it; the split says which
	// stage to look at when it does.
	Load, CallGraph, Emit time.Duration
}

// A ModuleEdge is one line of Report.ModuleEdges: the edges of one kind
// from a module into another (To a module path) or into packages outside
// the repository (To "").
type ModuleEdge struct {
	From, To string
	Kind     graph.EdgeKind
	Count    int
}

// moduleEdge counts one emitted edge whose ends are in different modules,
// or whose target is outside the repository: a test edge, one touching a
// test declaration, apart from a production one (#142).
func (r *Report) moduleEdge(from, to string, kind graph.EdgeKind, test bool) {
	if from == to {
		return
	}
	counts := &r.edges
	if test {
		counts = &r.testEdges
	}
	if *counts == nil {
		*counts = map[ModuleEdge]int{}
	}
	(*counts)[ModuleEdge{From: from, To: to, Kind: kind}]++
}

// finish turns the counts into the sorted ModuleEdges and
// TestModuleEdges.
func (r *Report) finish() {
	r.ModuleEdges, r.TestModuleEdges = sortedEdges(r.edges), sortedEdges(r.testEdges)
	r.edges, r.testEdges = nil, nil
}

func sortedEdges(counts map[ModuleEdge]int) []ModuleEdge {
	var out []ModuleEdge
	for k, n := range counts {
		k.Count = n
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b ModuleEdge) int {
		return cmp.Or(strings.Compare(a.From, b.From), strings.Compare(a.To, b.To), strings.Compare(string(a.Kind), string(b.Kind)))
	})
	return out
}

// Extract loads every Go module under dir through caliper and adds their
// nodes and edges to b. Each module is loaded on its own, from its own
// directory, which is how the go tool builds a repository whose modules
// no go.work joins (#67); an edge from one module into another resolves
// through the ID grammar, since a Go ID carries its module path (ADR 002
// rule 5), so a call into a sibling module lands on the sibling's
// declaration whether the type checker reached it by a replace directive
// or by a version. Extract performs the whole analysis in one call and
// holds no state afterwards (ADR 005): the graph is built on demand from
// the working tree.
//
// dir should be inside b's root, since positions are relativized against
// that root; a declaration whose source lies outside it is left out and
// counted (#92).
func Extract(b *graph.Builder, dir string, opts Options) (*Report, error) {
	if !opts.Tests || !opts.ExactTests {
		return extractOnce(b, dir, opts)
	}
	return extractExact(b, dir, opts)
}

// extractExact builds the production graph from a load without tests,
// then a load with them into a graph of its own, and adds to b only what
// that one has beyond the production build: its test nodes, the external
// packages it reaches, and its edges touching a test declaration (#133).
// A production edge only the load with tests has — an interface call that
// reaches a package only tests bring in — is left out, so the production
// view is the build without tests, edge for edge; an external package only
// test edges reach is then a test node by the core's rule.
func extractExact(b *graph.Builder, dir string, opts Options) (*Report, error) {
	prodOpts := opts
	prodOpts.Tests, prodOpts.ExactTests = false, false
	prod, err := extractOnce(b, dir, prodOpts)
	if err != nil {
		return nil, err
	}
	scratch := graph.NewBuilder(b.Minter().Root())
	testOpts := opts
	testOpts.ExactTests = false
	tests, err := extractOnce(scratch, dir, testOpts)
	if err != nil {
		return nil, err
	}
	if err := addTests(b, scratch.Build()); err != nil {
		return nil, err
	}
	return exactReport(prod, tests), nil
}

// exactReport is what -tests-exact reports: the production load's report,
// which describes the production graph as the build without tests does —
// its configurations, module edges, excluded files and failed packages —
// and from the load with tests only what tests added: the test nodes, how
// test edges cross modules, and the inputs it skipped (#142, the owner's
// decision). The time is both loads'.
func exactReport(prod, tests *Report) *Report {
	rep := *prod
	rep.ExactTests = true
	rep.Tests = tests.Tests
	rep.TestModuleEdges = tests.TestModuleEdges
	rep.Skipped = nil
	seen := map[graph.Skip]bool{}
	for _, s := range append(slices.Clone(prod.Skipped), tests.Skipped...) {
		if !seen[s] {
			seen[s] = true
			rep.Skipped = append(rep.Skipped, s)
		}
	}
	rep.Load += tests.Load
	rep.CallGraph += tests.CallGraph
	rep.Emit += tests.Emit
	return &rep
}

// addTests adds to b what the graph built with tests has beyond the
// production build: the edges touching a test declaration, and the nodes
// they need that the production build may lack — test nodes, and the
// external packages they reach. An external package goes over as
// observed, not with the test flag g derived for it, since b derives its
// own from b's edges; and only when an edge copied here reaches it, since
// one only a production edge of the load with tests reached is not in the
// build without tests (#133).
func addTests(b *graph.Builder, g *graph.Graph) error {
	edges, ends := testEdges(g)
	for n := range g.Nodes() {
		external := n.Attrs["external"] == "true"
		if external && !ends[n.ID] || !external && !n.Test {
			continue
		}
		n.Test = n.Test && !external
		// A built position is escaped (#124); the builder escapes it
		// again, so it goes back as the file system names it.
		n.Pos.File = graph.FilePath(n.Pos.File)
		if err := b.AddNode(n); err != nil {
			return err
		}
	}
	for _, e := range edges {
		if err := b.AddEdge(e); err != nil {
			return err
		}
	}
	return nil
}

// testEdges lists g's edges touching a test declaration, dropped ones
// included, and the endpoints of those it kept.
func testEdges(g *graph.Graph) ([]graph.Edge, map[graph.ID]bool) {
	var edges []graph.Edge
	ends := map[graph.ID]bool{}
	for e := range g.Edges() {
		if e.Test {
			edges = append(edges, e)
			ends[e.From], ends[e.To] = true, true
		}
	}
	for _, d := range g.Dropped() {
		if d.Edge.Test {
			edges = append(edges, d.Edge)
		}
	}
	return edges, ends
}

func extractOnce(b *graph.Builder, dir string, opts Options) (*Report, error) {
	mods, skipped, err := Modules(dir, opts.Skip)
	if err != nil {
		return nil, err
	}
	if opts.Module != "" {
		mods = slices.DeleteFunc(mods, func(m Module) bool { return m.Path != opts.Module })
	} else if len(mods) == 0 && len(skipped) == 0 {
		return nil, fmt.Errorf("golang: no go.mod under %s", dir)
	}
	mods, skipped = unbuiltGoRepository(b.Minter(), mods, skipped)
	host := hostConfig()
	report := &Report{Build: host.String(), Skipped: skipped}
	if len(mods) == 0 {
		return report, nil
	}
	opts = forTree(mods, opts)
	rec := newRecorder(b)
	loaded, err := loadModules(rec, b.Minter(), mods, opts, report)
	if err != nil {
		return nil, err
	}
	extra, covered, err := configsFor(b.Minter(), loaded, opts, host, report)
	if err != nil {
		return nil, err
	}
	report.Configs, report.Recovered = configNames(host, extra), len(covered)
	modules := graphModules(mods, report, opts)
	rec.theirs = notAsked(opts.Only, modules)
	for _, lm := range loaded {
		if err := hostPass(rec, b.Minter(), lm, modules, opts, covered, report); err != nil {
			return nil, err
		}
		for _, c := range extra {
			if err := extraPass(rec, b.Minter(), lm, modules, opts, c, report); err != nil {
				return nil, err
			}
		}
	}
	report.finish()
	return report, nil
}

// hostPass emits a module's packages as the host's configuration loaded
// them, if it loaded any.
func hostPass(rec *recorder, m *graph.Minter, lm loadedModule, modules []string, opts Options, covered map[string]config, report *Report) error {
	if lm.eng == nil {
		return nil
	}
	x := newExtractor(rec, m, lm.eng, lm.Path, modules, opts, report)
	x.recovered = covered
	return x.pass(lm.pkgs)
}

// graphModules are the module paths an edge may land in at declaration
// granularity: the modules loaded, and under Options.Only also those not
// loaded because none of their packages was asked for, which the base
// graph holds (#25). A module that failed to load is in neither.
func graphModules(mods []Module, report *Report, opts Options) []string {
	out := slices.Clone(report.Modules)
	if opts.Only == nil {
		return out
	}
	for _, mod := range mods {
		if len(onlyIn(opts.Only, mod.Path, mods)) == 0 {
			out = append(out, mod.Path)
		}
	}
	return out
}

// onlyIn lists the import paths of only that belong to module, nil when
// only is nil.
func onlyIn(only []string, module string, mods []Module) []string {
	if only == nil {
		return nil
	}
	paths := make([]string, len(mods))
	for i, m := range mods {
		paths[i] = m.Path
	}
	var out []string
	for _, p := range only {
		if Owner(p, paths) == module {
			out = append(out, p)
		}
	}
	return out
}

// goRepository reports whether the modules are a checkout of the Go
// repository: one of them is std, the standard library.
func goRepository(mods []Module) bool {
	return slices.ContainsFunc(mods, func(m Module) bool { return m.Path == "std" })
}

// forTree adjusts the options to the tree. A checkout of the Go
// repository is its own GOROOT (caliper loads std against it), so export
// data would be compiled by the checkout's compiler, whose version is the
// checkout's and not the go command's — "compile: version go1.27.2 does
// not match go tool version go1.27.1" — and every package compiled failed
// (#217). Its dependencies are all in the tree, and are read from source.
func forTree(mods []Module, opts Options) Options {
	if goRepository(mods) {
		opts.DepsFromExportData = false
	}
	return opts
}

// unbuiltGoRepository leaves out the std and cmd modules of a checkout
// of the Go repository that has not been built, and says so (#219, the
// owner's decision). Without its own compiler (pkg/tool), the go command
// cannot load the checkout as its GOROOT, and caliper loads it against
// the installed toolchain instead: the standard library's non-test files
// overlaid from the checkout, cmd and every test file the installed
// toolchain's own, under the checkout's paths — an edit to cmd/go was
// not in the graph, and nothing said so. misc is an ordinary module.
func unbuiltGoRepository(m *graph.Minter, mods []Module, skipped []graph.Skip) ([]Module, []graph.Skip) {
	i := slices.IndexFunc(mods, func(mod Module) bool { return mod.Path == "std" })
	if i < 0 || compiledToolchain(filepath.Dir(mods[i].Dir)) {
		return mods, skipped
	}
	unbuilt := errors.New("a checkout of the Go repository that is not built: run ./make.bash in src/, and its Go code is read")
	kept := mods[:0:0]
	for _, mod := range mods {
		if mod.Path == "std" || mod.Path == "cmd" {
			skipped = append(skipped, m.Skip(filepath.Join(mod.Dir, "go.mod"), 0, unbuilt))
			continue
		}
		kept = append(kept, mod)
	}
	return kept, skipped
}

// compiledToolchain reports whether a GOROOT holds its compiled tools.
func compiledToolchain(goroot string) bool {
	entries, err := os.ReadDir(filepath.Join(goroot, "pkg", "tool"))
	return err == nil && len(entries) > 0
}

// A loadedModule is one module's caliper engine and its packages. eng is
// nil when Options.Only asks only for packages the host's configuration
// builds none of: they are loaded under the further configurations.
type loadedModule struct {
	Module
	eng  *analysis.Engine
	pkgs []*packages.Package
	// only are the import paths of this module's packages Options.Only
	// asks for, nil when it asks for every package.
	only []string
}

// loadModules loads each module for the host configuration. A module
// that does not load — the root's with a syntax error in it, a tools
// module whose imports are not on the machine — keeps a package node
// carrying the error, the way every other layer keeps a file that failed
// to parse, and the rest of the repository is read without it (#92).
func loadModules(rec *recorder, m *graph.Minter, mods []Module, opts Options, report *Report) ([]loadedModule, error) {
	var out []loadedModule
	for _, mod := range mods {
		only := onlyIn(opts.Only, mod.Path, mods)
		if opts.Only != nil && len(only) == 0 {
			continue // none of its packages asked for (#25)
		}
		lm := loadedModule{Module: mod, only: only}
		host := hostBuilt(mod, only)
		if only != nil && len(host) == 0 {
			report.Modules = append(report.Modules, mod.Path)
			out = append(out, lm)
			continue
		}
		start := time.Now()
		eng, err := load(mod.Dir, opts, nil, host)
		report.Load += time.Since(start)
		if err != nil {
			// The root's module too: a syntax error in a one-package
			// repository costs its Go layer, never the other layers
			// (#92).
			report.Failed = append(report.Failed, mod.Path)
			if err := failedModule(rec, m, mod, err); err != nil {
				return nil, err
			}
			continue
		}
		lm.eng, lm.pkgs = eng, targetPackages(eng.Packages())
		report.Modules = append(report.Modules, mod.Path)
		out = append(out, lm)
	}
	return out, nil
}

// hostBuilt are the packages of only the host's configuration builds a
// Go file of. Naming one it builds none of fails the whole load, so the
// others are loaded for the host and it only under the configurations
// that build it (#35). nil when only is.
func hostBuilt(mod Module, only []string) []string {
	if only == nil {
		return nil
	}
	var out []string
	for _, p := range only {
		if hostBuilds(packageDir(mod, p)) {
			out = append(out, p)
		}
	}
	return out
}

// failedModule emits the package node of a module that did not load: its
// module path, positioned at its go.mod, with the load error on it.
func failedModule(rec *recorder, m *graph.Minter, mod Module, loadErr error) error {
	id, err := m.GoID(mod.Path, "")
	if err != nil {
		return fmt.Errorf("golang: %w", err)
	}
	return rec.AddNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: moduleName(mod.Path),
		Pos:   graph.Position{File: filepath.Join(mod.Dir, "go.mod")},
		Attrs: map[string]string{"error": loadError(loadErr)}})
}

// configsFor chooses the configurations beyond the host's to load, from
// the files the host's load left out — those of its packages, and those
// of directories it never matched (#95) — and records as skipped each
// directory of the latter that no chosen configuration brings in.
func configsFor(m *graph.Minter, loaded []loadedModule, opts Options, host config, report *Report) ([]config, map[string]config, error) {
	var unloaded []string
	for _, lm := range loaded {
		unloaded = append(unloaded, unloadedGoFiles(lm, opts, m.Root())...)
	}
	excluded := append(excludedInModules(loaded, opts.Tests), unloaded...)
	sort.Strings(excluded)
	extra, covered, unread, err := chooseConfigs(opts.Configs, host, excluded, opts.Only != nil)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range slices.Sorted(maps.Keys(unread)) {
		report.Skipped = append(report.Skipped, m.Skip(f, 0, unread[f]))
	}
	report.Skipped = append(report.Skipped, unloadedDirs(m, unloaded, covered, unread)...)
	return extra, covered, nil
}

// excludedInModules lists the Go files the host's load left out across
// every loaded module.
func excludedInModules(loaded []loadedModule, tests bool) []string {
	var out []string
	for _, lm := range loaded {
		out = append(out, excludedIn(lm.pkgs, lm.Path, tests)...)
	}
	return out
}

// load runs caliper over the module for one configuration: the host's
// when env is nil, otherwise the GOOS and GOARCH env carries.
// patterns, when not empty, are the import paths to load in place of
// every package of the module: import paths, not ./dir, since caliper
// loads a checkout of the Go repository from no directory, GOROOT
// naming it, and ./dir would match nothing there (#25).
func load(dir string, opts Options, env, patterns []string) (*analysis.Engine, error) {
	eng, err := analysis.NewEngineWithOptions(dir, analysis.EngineOptions{
		// Never inherited from analysis.DefaultOptions: both flags are
		// alidade's decision (ADR 004). SkipGenerated is off so the AST
		// side agrees with the SSA side, which ignores it anyway.
		SkipGenerated: false,
		SkipCgo:       true,
		Tests:         opts.Tests,
		Env:           env,

		DepsFromExportData: opts.DepsFromExportData,
	}, patterns...)
	if err != nil {
		return nil, fmt.Errorf("golang: load %s: %w", dir, err)
	}
	return eng, nil
}

// extraPass loads one module for one further configuration and adds
// what the host's pass did not, marked with the configuration. A module
// that does not load for the configuration at all — every package of it
// ill-typed there, a dependency the platform has no build of — costs that
// configuration and is reported skipped, never the host's graph, which
// has already been read: one bad input must cost one fact (#92), and a
// layer that returns an error is dropped whole (#93).
func extraPass(rec *recorder, m *graph.Minter, mod loadedModule, modules []string, opts Options, c config, report *Report) error {
	start := time.Now()
	eng, err := load(mod.Dir, opts, []string{"GOOS=" + c.goos, "GOARCH=" + c.goarch}, mod.only)
	report.Load += time.Since(start)
	if err != nil {
		report.Skipped = append(report.Skipped, m.Skip(filepath.Join(mod.Dir, "go.mod"), 0,
			fmt.Errorf("the module does not load for %s: %s", c, loadError(err))))
		return nil
	}
	o := newOverlay(rec, c.String())
	x := newExtractor(o, m, eng, mod.Path, modules, opts, &Report{})
	if err := x.pass(targetPackages(eng.Packages())); err != nil {
		return err
	}
	report.CallGraph += x.report.CallGraph
	report.Emit += x.report.Emit
	report.Added += o.nodes + o.edges
	report.FailedPackages = mergePaths(report.FailedPackages, o.onlyAdded(m, x.report.FailedPackages))
	report.WithoutCalls = mergePaths(report.WithoutCalls, o.onlyAdded(m, x.report.WithoutCalls))
	return nil
}

// mergePaths adds the package paths out does not already hold and keeps
// it sorted: a package the host's configuration never loaded, and a
// further one found broken, is a fact of that pass, and the report is one
// report whichever pass found it (#95).
func mergePaths(out, add []string) []string {
	for _, p := range add {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func newExtractor(s sink, m *graph.Minter, eng *analysis.Engine, module string, modules []string, opts Options, report *Report) *extractor {
	return &extractor{
		callGraph:    func() (*analysis.InterProceduralContext, error) { return callGraphOf(eng) },
		b:            s,
		m:            m,
		eng:          eng,
		fset:         eng.Fset(),
		module:       module,
		modules:      modules,
		opts:         opts,
		ext:          make(map[string]bool),
		tests:        make(map[string]graph.ID),
		testIDs:      make(map[graph.ID]bool),
		namedOutside: make(map[string]bool),
		inits:        make(map[graph.ID]*initDecls),
		called:       make(map[[2]graph.ID]bool),
		funcs:        make(map[*types.Func]funcInfo),
		varSpans:     make(map[*types.Package][]varSpan),
		report:       report,
	}
}

// pass emits one configuration's packages: declarations, implements,
// and the call graph.
func (x *extractor) pass(pkgs []*packages.Package) error {
	loaded := time.Now()
	for _, pkg := range pkgs {
		if !x.inModule(pkg.PkgPath) {
			continue
		}
		if err := x.declarations(pkg); err != nil {
			return err
		}
	}
	if err := x.failedPackages(pkgs); err != nil {
		return err
	}
	if err := x.implements(pkgs); err != nil {
		return err
	}
	emitted := time.Now()
	ipc, failure, err := x.interProcedural()
	if err != nil {
		return fmt.Errorf("golang: call graph for %s: %w", x.module, err)
	}
	built := time.Now()
	if failure != "" {
		if err := x.callGraphFailed(pkgs, failure); err != nil {
			return err
		}
	} else if err := x.withoutCalls(ipc.Skipped); err != nil {
		return err
	} else if err := x.calls(ipc, newCallGraph(ipc)); err != nil {
		return err
	}
	if err := x.flushInits(); err != nil {
		return err
	}
	x.report.CallGraph += built.Sub(emitted)
	x.report.Emit += emitted.Sub(loaded) + time.Since(built)
	return nil
}

// callGraphOf builds a module's call graph: caliper's, which a test
// replaces with one that panics.
var callGraphOf = (*analysis.Engine).InterProcedural

// interProcedural builds the module's call graph, and recovers a panic
// inside it as a failure to report rather than a build that dies: x/tools
// panicked walking a method with type parameters of its own (#216), and
// the next construct it does not know would do the same. The call graph is
// one whole-program build per module, so a failure costs the module's
// calls; its declarations, imports and implements stay.
func (x *extractor) interProcedural() (ipc *analysis.InterProceduralContext, failure string, err error) {
	defer func() {
		if r := recover(); r != nil {
			ipc, failure, err = nil, fmt.Sprint(r), nil
		}
	}()
	ipc, err = x.callGraph()
	return ipc, failure, err
}

// callGraphFailed marks every package of the module as one whose calls
// are not in the graph, with the call graph's failure as the cause, as
// withoutCalls marks a package whose dependency is broken: an empty
// callers list there then reads as unknown rather than as none.
func (x *extractor) callGraphFailed(pkgs []*packages.Package, failure string) error {
	cause := "the call graph could not be built: " + failure
	for _, pkg := range pkgs {
		if !x.inModule(pkg.PkgPath) || slices.Contains(x.report.WithoutCalls, pkg.PkgPath) {
			continue
		}
		id, err := x.m.GoID(pkg.PkgPath, "")
		if err != nil {
			return err
		}
		x.report.WithoutCalls = append(x.report.WithoutCalls, pkg.PkgPath)
		if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: pkg.Name, Test: x.testPkg(pkg),
			Attrs: map[string]string{"calls_unavailable": cause}}); err != nil {
			return err
		}
	}
	return nil
}

// hostConfig is the configuration go/packages loads for by default:
// GOOS and GOARCH from the environment when set, the host's otherwise.
func hostConfig() config {
	goos, goarch := os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return config{goos, goarch}
}

// excludedIn lists the Go files the load left out across the module's
// packages, absolute; test files only when tests are being loaded.
func excludedIn(pkgs []*packages.Package, module string, tests bool) []string {
	var out []string
	for _, pkg := range pkgs {
		if owner(pkg.PkgPath, []string{module}) != module {
			continue
		}
		for _, f := range pkg.IgnoredFiles {
			if strings.HasSuffix(f, ".go") && (tests || !strings.HasSuffix(f, "_test.go")) {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// targetPackages dedupes the load by import path. With Tests on, go/packages
// returns a package twice — plain, and the variant with its _test.go files
// — and the variant is a superset, so the plain one is dropped rather than
// emitted twice and folded.
func targetPackages(all []*packages.Package) []*packages.Package {
	byPath := make(map[string]*packages.Package)
	for _, p := range all {
		if p.Types == nil {
			continue
		}
		if have, ok := byPath[p.PkgPath]; !ok || len(p.GoFiles) > len(have.GoFiles) {
			byPath[p.PkgPath] = p
		}
	}
	out := make([]*packages.Package, 0, len(byPath))
	for _, p := range byPath {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
	return out
}

type extractor struct {
	// callGraph builds the module's call graph (callGraphOf).
	callGraph func() (*analysis.InterProceduralContext, error)
	b         sink
	m         *graph.Minter
	eng       *analysis.Engine
	fset      *token.FileSet
	// module is the path of the module this pass emits declarations for;
	// modules are the paths of every module in the graph, which is where
	// an edge may land at declaration granularity.
	module  string
	modules []string
	opts    Options
	// ext records external package nodes already emitted, so a boundary
	// call and an import to the same package share one node.
	ext map[string]bool
	// tests maps a package path to its in-package test files' node, for
	// a package that has them and is not a test package itself (#128).
	tests map[string]graph.ID
	// testIDs are the nodes this pass knows to be test ones — emitted
	// with Test, or a function from a test file — so a calls edge's
	// module count can say whether it is a test edge (#142).
	testIDs map[graph.ID]bool
	// namedOutside are the files outsideSkip has named (#109).
	namedOutside map[string]bool
	// inits collects each package's init declarations until the end of
	// the pass, when one node is emitted for them; see initNode.
	inits map[graph.ID]*initDecls
	// called records the calls edges already emitted. The core folds
	// duplicates anyway, but the SSA call graph reports one edge per
	// call site per SSA function, and on a large module with tests that
	// is over a million edges folding to a quarter of that; dropping
	// the repeats here is cheaper than handing them over.
	called map[[2]graph.ID]bool
	// funcs caches what the calls pass asks about a declaration — its
	// ID and whether it is generated — since the call graph visits a
	// popular callee once per caller, and a position lookup and a mint
	// per visit is what the emit stage's time went on.
	funcs map[*types.Func]funcInfo
	// varSpans caches, per package, where its variables' initializers
	// are, for attributing the calls they make.
	varSpans map[*types.Package][]varSpan
	// partial is set while the declarations of a package that did not
	// type-check are emitted, each then marked partial, and pkgError to
	// that package's first load error, which its package node carries.
	partial  bool
	pkgError string
	report   *Report
	// recovered maps an excluded file to the configuration that brings
	// it in, so a package node lists only what no configuration loads.
	recovered map[string]config
}

type funcInfo struct {
	id   graph.ID
	test bool
}

// funcInfo returns the cached identity of a function or method.
func (x *extractor) funcInfo(f *types.Func) (funcInfo, error) {
	if fi, ok := x.funcs[f]; ok {
		return fi, nil
	}
	id, err := x.funcID(f)
	if err != nil {
		return funcInfo{}, err
	}
	fi := funcInfo{id: id, test: x.testFile(f.Pos())}
	// An init of an in-package test file is left out: a package's inits
	// share one ID (ADR 002 rule 1 forbids a positional one), so marking
	// it would make the package's production inits' edges test ones
	// (#142). The fold gives such a call the test files' node as its
	// From instead, and packageTests has marked that (#128).
	if _, folds := x.testInit(f); fi.test && !folds {
		x.testIDs[id] = true
	}
	x.funcs[f] = fi
	return fi, nil
}

// inModule reports whether an import path belongs to the module this pass
// emits, and inGraph whether it belongs to any module of the graph — the
// module owning a path being the one whose path it sits under, longest
// first, since a nested module's path extends its parent's. The external
// test package of a package (pkg_test) is a path under the module and
// counts; the synthetic test binary go/packages adds under Tests
// (pkg.test) declares nothing of the module's own and does not.
func (x *extractor) inModule(path string) bool {
	return !strings.HasSuffix(path, ".test") && owner(path, x.modules) == x.module
}

func (x *extractor) inGraph(path string) bool {
	return !strings.HasSuffix(path, ".test") && owner(path, x.modules) != ""
}

// testFile reports whether a position lies in a _test.go file, which is
// the go tool's own definition of a test file rather than a convention
// read off a name (#81).
func (x *extractor) testFile(pos token.Pos) bool {
	return pos.IsValid() && strings.HasSuffix(x.fset.Position(pos).Filename, "_test.go")
}

// testPkg reports whether a package is a test package (ADR 004 rule 4).
// An external test package is the go tool's: a package named with the
// "_test" suffix whose every file is a _test.go file. The import path's
// suffix alone is not it — a production package in a directory named
// "e2e_test/" is not a test (#95). A package with no production files is
// one too, whatever its name (#113, the owner's decision): a build
// without tests has no such package.
func (x *extractor) testPkg(pkg *packages.Package) bool {
	return externalTestPkg(pkg) || testOnlyDir(pkg.Dir)
}

func externalTestPkg(pkg *packages.Package) bool {
	if !strings.HasSuffix(pkg.Name, "_test") || len(pkg.GoFiles) == 0 {
		return false
	}
	for _, f := range pkg.GoFiles {
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
	}
	return true
}

// testOnlyDir reports whether a package directory holds Go files and every
// one is a _test.go file. The files are counted on disk, not in the build:
// a package whose only production file is foo_linux.go is a production
// package on every host, and a //go:build ignore file counts as
// production, which leaves its package unmarked (#113).
func testOnlyDir(dir string) bool {
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	tests := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !strings.HasSuffix(name, "_test.go") {
			return false
		}
		tests = true
	}
	return tests
}

// generated reports whether a position lies in a generated file. Anything
// without a position is not generated: it is synthetic, which is a
// different thing and is handled by declOf.
func (x *extractor) generated(pos token.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	return x.eng.IsGeneratedFile(x.fset.Position(pos).Filename)
}

// outside reports whether a declaration's source lies outside the tree,
// where no position of the graph can point: cgo's generated wrappers.
func (x *extractor) outside(pos token.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	_, err := x.m.PosPath(x.fset.Position(pos).Filename)
	return err != nil
}

// outsideSkip names, once per file, a file of the tree whose declarations
// were left out for being positioned outside it — a //line directive
// naming a source elsewhere, as goyacc's output does (#109). The file
// named is the one read, so a reader finds the directive. A declaration
// whose file is itself outside the tree is not the repository's: cgo's
// wrappers, written into the build cache by the hundred, stay a count.
func (x *extractor) outsideSkip(pos token.Pos) {
	file := x.fset.PositionFor(pos, false).Filename
	if x.namedOutside[file] {
		return
	}
	x.namedOutside[file] = true
	if _, err := x.m.PosPath(file); err != nil {
		return
	}
	at := x.fset.Position(pos).Filename
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, 0, fmt.Errorf("declarations positioned outside the tree, at %s, are left out", at)))
}

// position renders a declaration's position for navigation. The file is
// absolute here and relativized by the core.
func (x *extractor) position(pos token.Pos) graph.Position {
	if !pos.IsValid() {
		return graph.Position{}
	}
	p := x.fset.Position(pos)
	return graph.Position{File: p.Filename, Line: p.Line, Col: p.Column}
}

// packageNode emits the node for a package in the module. Files the
// build configuration excluded are named on it, sorted, so their absence
// from the graph is visible where a reader would look for them.
func (x *extractor) packageNode(pkg *packages.Package) (graph.ID, error) {
	id, err := x.m.GoID(pkg.PkgPath, "")
	if err != nil {
		return "", fmt.Errorf("golang: %w", err)
	}
	x.report.Packages++
	attrs := map[string]string{}
	if excluded := x.excludedFiles(pkg); len(excluded) > 0 {
		x.report.Excluded += len(excluded)
		attrs["excluded"] = strings.Join(excluded, " ")
	}
	if x.pkgError != "" {
		attrs["error"] = x.pkgError
	}
	if len(attrs) == 0 {
		attrs = nil
	}
	test := x.testPkg(pkg)
	if test {
		x.testIDs[id] = true
		// An external test package (pkg_test) has no file of its own, so
		// it is counted here rather than in declNode. Report.Tests is of
		// the module's nodes carrying the flag, so a caller can reconcile
		// it with the graph instead of finding an off-by-one (#90).
		x.report.Tests++
	}
	return id, x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: pkg.Name, Test: test, Attrs: attrs})
}

// excludedFiles lists the base names of a package's Go files that no
// loaded configuration includes: the host's build constraints left them
// out ("//go:build ignore" among them) and no further configuration
// recovers them. Test files count only when tests are being loaded.
func (x *extractor) excludedFiles(pkg *packages.Package) []string {
	out := make([]string, 0, len(pkg.IgnoredFiles))
	for _, f := range pkg.IgnoredFiles {
		if _, ok := x.recovered[f]; ok || !strings.HasSuffix(f, ".go") || !x.opts.Tests && strings.HasSuffix(f, "_test.go") {
			continue
		}
		out = append(out, filepath.Base(f))
	}
	sort.Strings(out)
	return out
}

// externalPackage emits, once, the node a boundary edge lands on: a package
// outside the module, with no position and no contents. It is marked so a
// consumer can tell it from a package the graph actually knows about.
func (x *extractor) externalPackage(path, name string) (graph.ID, error) {
	id, err := x.m.GoID(path, "")
	if err != nil {
		return "", fmt.Errorf("golang: %w", err)
	}
	if x.ext[path] {
		return id, nil
	}
	x.ext[path] = true
	return id, x.b.AddNode(graph.Node{
		ID: id, Kind: graph.KindPackage, Name: name,
		Attrs: map[string]string{"external": "true"},
	})
}

// declarations emits every package-level declaration of one package, its
// methods, and the edges that are visible from declarations alone:
// contains, imports and references — to the types a declaration
// mentions, and from a function to the constants and variables it uses.
func (x *extractor) declarations(pkg *packages.Package) error {
	pkgID, err := x.packageNode(pkg)
	if err != nil {
		return err
	}
	testID, err := x.packageTests(pkg, pkgID)
	if err != nil {
		return err
	}
	if err := x.imports(pkgID, testID, pkg); err != nil {
		return err
	}
	literal := structLiterals(pkg)
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if x.outside(obj.Pos()) {
			x.report.Outside++
			x.outsideSkip(obj.Pos())
			continue
		}
		gen := x.generated(obj.Pos())
		if gen {
			x.report.Generated++
		}
		id, kind, ok := x.identify(obj)
		if !ok {
			continue
		}
		if err := x.declNode(id, kind, obj, pkgID, gen); err != nil {
			return err
		}
		if tn, ok := obj.(*types.TypeName); ok {
			if err := x.methods(id, tn); err != nil {
				return err
			}
			if err := x.fields(id, tn, literal); err != nil {
				return err
			}
		}
	}
	return x.uses(pkg)
}

// imports emits an imports edge from a package to each package it imports,
// an external package node standing in for one outside the graph. An
// import that only the package's in-package test files make is an edge
// from its test files' node instead (#128): the package imports it in no
// build without tests.
func (x *extractor) imports(pkgID, testID graph.ID, pkg *packages.Package) error {
	testOnly := x.testOnlyImports(pkg, testID)
	for _, imp := range pkg.Types.Imports() {
		var to graph.ID
		var err error
		if x.inGraph(imp.Path()) {
			if to, err = x.m.GoID(imp.Path(), ""); err != nil {
				return fmt.Errorf("golang: %w", err)
			}
		} else if to, err = x.externalPackage(imp.Path(), imp.Name()); err != nil {
			return err
		}
		from := pkgID
		if testOnly[imp.Path()] {
			from = testID
		}
		x.report.moduleEdge(x.module, owner(imp.Path(), x.modules), graph.KindImports, from == testID || x.testPkg(pkg))
		if err := x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImports, Derivation: graph.Extracted}); err != nil {
			return err
		}
	}
	return nil
}

// testOnlyImports lists the import paths of a package that its in-package
// test files make and its production files do not, when it has a test
// files' node to hang them on.
func (x *extractor) testOnlyImports(pkg *packages.Package, testID graph.ID) map[string]bool {
	if testID == "" {
		return nil
	}
	prod, test := map[string]bool{}, map[string]bool{}
	for _, f := range pkg.Syntax {
		set := prod
		if x.testFile(f.Pos()) {
			set = test
		}
		for _, spec := range f.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil {
				set[path] = true
			}
		}
	}
	for path := range prod {
		delete(test, path)
	}
	return test
}

// packageTests emits the node of a package's in-package test files —
// go://<pkg>#(test), kind package_tests — and the contains edge from the
// package, when the package has such files and is not a test package
// itself, whose package node is a test node already (#128). It returns
// the node's ID, or "" for none. The node is positioned at the first test
// file in name order.
func (x *extractor) packageTests(pkg *packages.Package, pkgID graph.ID) (graph.ID, error) {
	if x.testPkg(pkg) {
		return "", nil
	}
	var first string
	for _, f := range pkg.Syntax {
		if !x.testFile(f.Pos()) {
			continue
		}
		if name := x.fset.Position(f.Pos()).Filename; first == "" || name < first {
			first = name
		}
	}
	if first == "" {
		return "", nil
	}
	id, err := x.m.GoTestID(pkg.PkgPath)
	if err != nil {
		return "", fmt.Errorf("golang: %w", err)
	}
	x.tests[pkg.PkgPath] = id
	x.testIDs[id] = true
	x.report.Tests++
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindPackageTests, Name: pkg.Name, Pos: graph.Position{File: first, Line: 1, Col: 1}, Test: true}); err != nil {
		return "", err
	}
	return id, x.b.AddEdge(graph.Edge{From: pkgID, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}

// declNode emits one declaration node, the contains edge from its owner, and
// its references. owner is the package for a top-level declaration and the
// type for a method.
func (x *extractor) declNode(id graph.ID, kind graph.Kind, obj types.Object, owner graph.ID, gen bool) error {
	x.report.Decls++
	test := x.testFile(obj.Pos())
	if test {
		x.report.Tests++
	}
	n := graph.Node{ID: id, Kind: kind, Name: obj.Name(), Pos: x.position(obj.Pos()), Generated: gen, Test: test, Attrs: map[string]string{}}
	if test {
		x.testIDs[id] = true
	}
	if !obj.Exported() {
		n.Attrs["exported"] = "false"
	}
	if x.partial {
		// From a package that did not type-check: what the checker
		// resolved, and no calls out of it (#95).
		n.Attrs["partial"] = "true"
	}
	if err := x.b.AddNode(n); err != nil {
		return err
	}
	if err := x.b.AddEdge(graph.Edge{From: owner, To: id, Kind: graph.KindContains, Derivation: graph.Extracted}); err != nil {
		return err
	}
	return x.references(id, obj)
}

// methods emits the methods declared on a named type — concrete methods of a
// struct or other named type, and the explicit methods of an interface —
// each contained by the type.
func (x *extractor) methods(typeID graph.ID, tn *types.TypeName) error {
	for _, m := range methodsOf(tn) {
		gen := x.generated(m.Pos())
		if gen {
			x.report.Generated++
		}
		id, err := x.m.GoID(tn.Pkg().Path(), tn.Name()+"."+m.Name())
		if err != nil {
			return fmt.Errorf("golang: %w", err)
		}
		if err := x.declNode(id, graph.KindMethod, m, typeID, gen); err != nil {
			return err
		}
	}
	return nil
}

// fields emits the fields of a named struct type, each contained by the
// type and keyed go://pkg#Type.field (ADR 002 rule 14, #193): Go forbids
// a field and a method of one name on one type, so the key is free. A
// field's references are the named types its type mentions, as a
// declaration's are. An alias declares no fields; a blank field names
// nothing; the fields of an anonymous struct nested in a field have no
// declaration path of their own and are left out.
func (x *extractor) fields(typeID graph.ID, tn *types.TypeName, literal map[string]bool) error {
	if tn.IsAlias() || !literal[tn.Name()] {
		// type X Y over a struct Y has Y's fields, declared at Y: they
		// are Y's nodes, not X's.
		return nil
	}
	st, ok := tn.Type().Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i := range st.NumFields() {
		f := st.Field(i)
		if f.Name() == "_" || x.outside(f.Pos()) {
			continue
		}
		gen := x.generated(f.Pos())
		if gen {
			x.report.Generated++
		}
		id, err := x.m.GoID(tn.Pkg().Path(), tn.Name()+"."+f.Name())
		if err != nil {
			return fmt.Errorf("golang: %w", err)
		}
		x.report.Fields++
		if err := x.declNode(id, graph.KindField, f, typeID, gen); err != nil {
			return err
		}
	}
	return nil
}

// structLiterals names a package's types declared by a struct type
// literal, type T struct{...}, the ones whose fields are their own.
func structLiterals(pkg *packages.Package) map[string]bool {
	out := map[string]bool{}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok && !ts.Assign.IsValid() {
					if _, isStruct := ts.Type.(*ast.StructType); isStruct {
						out[ts.Name.Name] = true
					}
				}
			}
		}
	}
	return out
}

// methodsOf lists the methods declared on a named type: the concrete
// methods of a struct or other named type, or the explicit methods of an
// interface. An alias declares none.
func methodsOf(tn *types.TypeName) []*types.Func {
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return nil
	}
	var ms []*types.Func
	if iface, ok := named.Underlying().(*types.Interface); ok {
		for i := range iface.NumExplicitMethods() {
			ms = append(ms, iface.ExplicitMethod(i))
		}
		return ms
	}
	for i := range named.NumMethods() {
		ms = append(ms, named.Method(i))
	}
	return ms
}

// identify mints the ID and kind of a package-level object. Objects the
// graph has no node kind for — labels, builtins, the package itself — are
// reported as not ok.
func (x *extractor) identify(obj types.Object) (graph.ID, graph.Kind, bool) {
	var kind graph.Kind
	switch obj.(type) {
	case *types.TypeName:
		kind = graph.KindType
	case *types.Func:
		kind = graph.KindFunc
	case *types.Var:
		kind = graph.KindVar
	case *types.Const:
		kind = graph.KindConst
	default:
		return "", "", false
	}
	id, err := x.m.GoID(obj.Pkg().Path(), obj.Name())
	if err != nil {
		return "", "", false
	}
	return id, kind, true
}

// references emits a references edge from a declaration to every named type
// of the graph that its declared type mentions: parameters and results of
// a function, fields of a struct, methods of an interface, the element of
// a slice or map. A type does not reference itself, and a method does not
// reference its receiver — that is what contains says.
func (x *extractor) references(from graph.ID, obj types.Object) error {
	self, isType := obj.(*types.TypeName)
	v, isVar := obj.(*types.Var)
	seen := make(map[*types.TypeName]bool)
	var err error
	walkTypes(obj.Type(), isType, !isVar || !v.IsField(), func(tn *types.TypeName) {
		if err != nil || seen[tn] || tn == self || tn.Pkg() == nil || !x.inGraph(tn.Pkg().Path()) || x.outside(tn.Pos()) {
			return
		}
		seen[tn] = true
		var to graph.ID
		if to, err = x.m.GoID(tn.Pkg().Path(), tn.Name()); err != nil {
			err = fmt.Errorf("golang: %w", err)
			return
		}
		x.report.moduleEdge(x.module, owner(tn.Pkg().Path(), x.modules), graph.KindReferences, x.testFile(obj.Pos()) || x.testFile(tn.Pos()))
		err = x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindReferences, Derivation: graph.Resolved})
	})
	return err
}

// implements emits an implements edge from every concrete named type of the
// module to every non-empty interface it satisfies, by pointer receiver if
// not by value, and from the concrete types of the graph's other modules
// to the module's interfaces. It is structural satisfaction as the type
// checker computes it (RESOLVED), which is the claim a name-matching tool
// cannot make at all. A pair with neither side in the module is the other
// module's own pass to emit.
//
// Generic types and interfaces are skipped: satisfaction is only defined
// for an instantiation, and the graph keys on the declaration.
func (x *extractor) implements(pkgs []*packages.Package) error {
	concrete, ifaces := x.namedTypes(pkgs)
	for _, c := range concrete {
		for _, i := range ifaces {
			if !x.inModule(c.loaded.Pkg().Path()) && !x.inModule(i.loaded.Pkg().Path()) || !c.satisfies(i) {
				continue
			}
			if err := x.implementsEdge(c.loaded, i.loaded); err != nil {
				return err
			}
		}
	}
	return nil
}

// namedTypes splits the non-generic named types of the module's packages,
// and of the graph's other modules' packages they import, into the
// concrete ones and the non-empty interfaces, the two sides of implements.
func (x *extractor) namedTypes(pkgs []*packages.Package) (concrete, ifaces []namedType) {
	plain := x.plainPackages()
	for _, pkg := range x.withImported(pkgs) {
		if !x.inGraph(pkg.PkgPath) {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() || x.outside(tn.Pos()) {
				continue
			}
			nt := namedType{loaded: tn, production: x.productionType(tn, plain[pkg.PkgPath])}
			switch classify(tn) {
			case "concrete":
				concrete = append(concrete, nt)
			case "interface":
				ifaces = append(ifaces, nt)
			}
		}
	}
	return concrete, ifaces
}

// plainPackages maps each package path to its plain variant, the one
// built without test files, when the load has it.
func (x *extractor) plainPackages() map[string]*packages.Package {
	out := map[string]*packages.Package{}
	for _, p := range x.eng.Packages() {
		if p.ID == p.PkgPath && p.Types != nil {
			out[p.PkgPath] = p
		}
	}
	return out
}

// A namedType is a type the implements pass compares, in the two type
// universes a load with tests has. With tests the emitted variant of a
// package is its test variant, a universe apart from the plain package
// the rest of the module imports: a type there whose methods name the
// package's own types satisfies an interface of the plain universe in
// source and not to the type checker. loaded is the type as the emitted
// variant declares it; production is the plain package's, for a type of
// a production file when the plain package was loaded, and nil otherwise.
type namedType struct {
	loaded, production *types.TypeName
}

// satisfies reports whether c satisfies i in either universe: the plain
// one, where production code meets it (#95), or the loaded one, the only
// one a type declared in a test file is in. A load without tests has one
// universe — every production type is its own plain type — so the pair
// is only checked twice when it differs, and the O(concrete × ifaces)
// loop does not pay for the second check where there is nothing to find.
func (c namedType) satisfies(i namedType) bool {
	twoUniverses := c.production != c.loaded || i.production != i.loaded
	if twoUniverses && c.production != nil && i.production != nil && satisfies(c.production, i.production) {
		return true
	}
	return satisfies(c.loaded, i.loaded)
}

// productionType returns a type name as the plain package declares it,
// or nil for a type of a test file or of a load without a plain variant.
func (x *extractor) productionType(tn *types.TypeName, plain *packages.Package) *types.TypeName {
	if plain == nil || x.testFile(tn.Pos()) {
		return nil
	}
	if plain.Types == tn.Pkg() {
		return tn
	}
	p, _ := plain.Types.Scope().Lookup(tn.Name()).(*types.TypeName)
	return p
}

// withImported returns pkgs followed by the packages of the graph's other
// modules they import, directly or through those, each once and in path
// order. Their types come from whatever the load type-checked them from —
// source or export data — and either carries a complete method set.
func (x *extractor) withImported(pkgs []*packages.Package) []*packages.Package {
	seen := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		seen[p.PkgPath] = true
	}
	var imported []*packages.Package
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		for _, imp := range p.Imports {
			if seen[imp.PkgPath] || imp.Types == nil || !x.inGraph(imp.PkgPath) {
				continue
			}
			seen[imp.PkgPath] = true
			imported = append(imported, imp)
			walk(imp)
		}
	}
	for _, p := range pkgs {
		walk(p)
	}
	slices.SortFunc(imported, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
	return append(slices.Clone(pkgs), imported...)
}

// classify says which side of implements a named type is on: "concrete",
// "interface" (non-empty), or "" for a generic type or an empty interface,
// which take no part.
func classify(tn *types.TypeName) string {
	named, ok := tn.Type().(*types.Named)
	if !ok || named.TypeParams().Len() > 0 {
		return ""
	}
	if iface, ok := named.Underlying().(*types.Interface); ok {
		if iface.NumMethods() > 0 {
			return "interface"
		}
		return ""
	}
	return "concrete"
}

// satisfies reports whether a concrete type implements an interface, by
// value or by pointer receiver.
func satisfies(c, i *types.TypeName) bool {
	it := i.Type().Underlying().(*types.Interface)
	return types.Implements(c.Type(), it) || types.Implements(types.NewPointer(c.Type()), it)
}

func (x *extractor) implementsEdge(c, i *types.TypeName) error {
	from, err := x.m.GoID(c.Pkg().Path(), c.Name())
	if err != nil {
		return fmt.Errorf("golang: %w", err)
	}
	to, err := x.m.GoID(i.Pkg().Path(), i.Name())
	if err != nil {
		return fmt.Errorf("golang: %w", err)
	}
	x.report.moduleEdge(owner(c.Pkg().Path(), x.modules), owner(i.Pkg().Path(), x.modules), graph.KindImplements, x.testFile(c.Pos()) || x.testFile(i.Pos()))
	return x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindImplements, Derivation: graph.Resolved})
}

// calls emits a calls edge for every edge of the SSA call graph whose caller
// is a declaration of the module. A callee in the graph is an edge to its
// node; a callee outside it is an edge to the callee's package node,
// marked boundary=package. A callee with no declaration at all — a
// synthetic function the SSA builder made up — is nothing to point at, and
// the edge is not emitted.
//
// The functions are those ssautil.AllFunctions visits plus every function
// and method the module declares (declared). AllFunctions starts from
// what a program can run, so it leaves out a generic function or a
// generic type's method nothing in the module instantiates, and a method
// of an unexported type nothing calls or converts to an interface: their
// calls were missing, and the oracle found them (#150).
func (x *extractor) calls(ipc *analysis.InterProceduralContext, cg *callGraph) error {
	all := ipc.AllFunctions()
	for fn := range x.declared(ipc.Program) {
		all[fn] = true
	}
	fns := make([]*ssa.Function, 0, len(all))
	for fn := range all {
		fns = append(fns, fn)
	}
	// Order is for determinism of the report, not the graph: the core
	// folds edges canonically whatever order they arrive in.
	slices.SortFunc(fns, func(a, b *ssa.Function) int { return strings.Compare(a.String(), b.String()) })

	for _, fn := range fns {
		if err := x.callsFrom(cg, fn); err != nil {
			return err
		}
	}
	return nil
}

// declared returns every function and method the module's packages
// declare, generic ones as their origin, with the closures inside them.
func (x *extractor) declared(prog *ssa.Program) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	var visit func(fn *ssa.Function)
	visit = func(fn *ssa.Function) {
		if fn == nil || out[fn] {
			return
		}
		out[fn] = true
		for _, anon := range fn.AnonFuncs {
			visit(anon)
		}
	}
	for _, pkg := range prog.AllPackages() {
		if pkg.Pkg == nil || !x.inModule(pkg.Pkg.Path()) {
			continue
		}
		for _, m := range pkg.Members {
			switch m := m.(type) {
			case *ssa.Function:
				visit(m)
			case *ssa.Type:
				for _, f := range declaredMethods(m.Type()) {
					visit(prog.FuncValue(f))
				}
			}
		}
	}
	return out
}

// declaredMethods lists the methods declared on a named type, by value
// or pointer receiver; an interface's have no body.
func declaredMethods(t types.Type) []*types.Func {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || types.IsInterface(named) {
		return nil
	}
	out := make([]*types.Func, 0, named.NumMethods())
	for i := range named.NumMethods() {
		out = append(out, named.Method(i))
	}
	return out
}

// moduleDecl returns the declaration an SSA function belongs to when it
// is one this pass emits: of the module, and with its source in the tree.
func (x *extractor) moduleDecl(fn *ssa.Function) *types.Func {
	d := declOf(fn)
	if d == nil || !x.inModule(d.Pkg().Path()) || x.outside(d.Pos()) {
		return nil
	}
	return d
}

// callsFrom emits the calls edges of one SSA function's declaration.
func (x *extractor) callsFrom(cg *callGraph, fn *ssa.Function) error {
	caller := x.moduleDecl(fn)
	if caller == nil {
		return x.initializerCalls(cg, fn)
	}
	if interfaceMethod(caller) {
		// An interface method has no body, so every SSA function that
		// declares itself one is the builder's: a method value's bound
		// closure, a method expression's thunk, the promotion wrapper of
		// a type embedding the interface. Their one instruction is the
		// wrapper's own dispatch, and attributing it to the method makes
		// the interface method call itself, or call an implementation it
		// cannot name — the self-call TestSyntheticFunctionsFoldIntoDeclarations
		// forbids, and a wrong edge where ADR 002 asks for none.
		return nil
	}
	ci, err := x.funcInfo(caller)
	if err != nil {
		return err
	}
	from := ci.id
	if id, ok := x.testInit(caller); ok {
		// An init of an in-package test file folds into the test files'
		// node, not the package's init (#128).
		from = id
	} else if err := x.initNode(from, caller); err != nil {
		return err
	}
	if err := x.calleeEdges(cg, fn, caller, from); err != nil {
		return err
	}
	return x.interfaceCalls(from, fn)
}

// calleeEdges emits an edge to each callee the call graph gives fn.
func (x *extractor) calleeEdges(cg *callGraph, fn *ssa.Function, caller *types.Func, from graph.ID) error {
	for _, cfn := range cg.callees(fn) {
		callee := declOf(cfn)
		if callee == nil || callee == caller && fn != cfn && !closureRecursion(fn, cfn) && !genericRecursion(fn, cfn) {
			// Two SSA functions of one declaration calling each
			// other — a pointer-receiver wrapper calling the value
			// method, a function calling its own closure, an
			// instantiation calling its origin — is the declaration's
			// own plumbing, not a call.
			continue
		}
		if err := x.callEdge(from, callee); err != nil {
			return err
		}
	}
	return nil
}

// genericRecursion reports whether a call from fn to cfn, two SSA
// functions of one declaration, is a generic function's body calling an
// instantiation of itself: the recursion of Walk[T] in Walk's own body,
// which only the origin carries (#150). The reverse, an instantiation
// calling its origin, is the builder's plumbing.
func genericRecursion(fn, cfn *ssa.Function) bool {
	return fn.Origin() == nil && cfn.Origin() == fn
}

// closureRecursion reports whether a call from fn to cfn, two SSA
// functions of one declaration, is the declaration calling itself from a
// closure inside it: real recursion, which a closure's folding into its
// declaration must not hide (#95).
func closureRecursion(fn, cfn *ssa.Function) bool {
	return fn.Parent() != nil && cfn.Parent() == nil && cfn.Synthetic == ""
}

// invokes calls visit for each call through an interface in fn, with the
// interface method the type checker resolved the call to and the position
// of the call site. The call graph's edges go to the implementations it
// finds; this one is to the method the source names, and the only one
// when the program has no implementation (#95). A method of an anonymous
// interface, or of one declared inside a function, has no declaration to
// point at.
func (x *extractor) invokes(fn *ssa.Function, visit func(*types.Func, token.Pos) error) error {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(ssa.CallInstruction)
			if !ok || !call.Common().IsInvoke() {
				continue
			}
			m := call.Common().Method
			if m.Pkg() == nil || !x.inGraph(m.Pkg().Path()) || !packageLevel(receiverType(m)) {
				continue
			}
			if err := visit(m, call.Pos()); err != nil {
				return err
			}
		}
	}
	return nil
}

// interfaceCalls emits an edge from one declaration to every interface
// method it calls through.
func (x *extractor) interfaceCalls(from graph.ID, fn *ssa.Function) error {
	return x.invokes(fn, func(m *types.Func, _ token.Pos) error { return x.callEdge(from, m) })
}

// interfaceMethod reports whether a function is a method declared on an
// interface, named or anonymous. Such a method has no body: every SSA
// function that gives it as its declaration is one the builder made to
// dispatch through it.
func interfaceMethod(f *types.Func) bool {
	sig, ok := f.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	_, ok = types.Unalias(t).Underlying().(*types.Interface)
	return ok
}

// packageLevel reports whether a type name is declared at package level,
// where the graph has a node for it; a type declared inside a function
// has none.
func packageLevel(tn *types.TypeName) bool {
	return tn != nil && tn.Pkg() != nil && tn.Parent() == tn.Pkg().Scope()
}

// initializerCalls emits the calls a package-level variable's initializer
// makes, directly or in a closure assigned to it (#95). They belong to
// the package's synthetic initializer, which has no declaration; each is
// an edge from the variable whose initializer holds the call site, or
// from the package for a blank variable.
func (x *extractor) initializerCalls(cg *callGraph, fn *ssa.Function) error {
	outer := fn
	for outer.Parent() != nil {
		outer = outer.Parent()
	}
	if outer.Synthetic != "package initializer" || outer.Pkg == nil || !x.inModule(outer.Pkg.Pkg.Path()) {
		return nil
	}
	tpkg := outer.Pkg.Pkg
	for _, c := range cg.out(fn) {
		callee := declOf(c.callee)
		if callee == nil || c.site == nil {
			continue
		}
		if err := x.initializerEdge(tpkg, c.site.Pos(), callee); err != nil {
			return err
		}
	}
	// An initializer calling through an interface names the method as
	// much as a function body does, and reaches it the same way: the
	// call graph has the implementations, this has the method itself.
	return x.invokes(fn, func(m *types.Func, pos token.Pos) error {
		return x.initializerEdge(tpkg, pos, m)
	})
}

// initializerEdge emits an edge to callee from every node whose
// initializer holds pos.
func (x *extractor) initializerEdge(tpkg *types.Package, pos token.Pos, callee *types.Func) error {
	for _, from := range x.initializedAt(tpkg, pos) {
		if err := x.callEdge(from, callee); err != nil {
			return err
		}
	}
	return nil
}

// initializedAt returns the nodes whose initializer holds a position of a
// package: the variable a value expression initializes — each of them,
// when one call initializes several — or the package for a blank one.
// A position in no initializer, the synthetic call of an init function,
// gives nothing.
func (x *extractor) initializedAt(pkg *types.Package, pos token.Pos) []graph.ID {
	if !pos.IsValid() {
		return nil
	}
	spans, ok := x.varSpans[pkg]
	if !ok {
		spans = x.initializerSpans(pkg)
		x.varSpans[pkg] = spans
	}
	for _, sp := range spans {
		if sp.pos <= pos && pos < sp.end {
			return sp.ids
		}
	}
	return nil
}

// A varSpan is the source range of one initializer and what it
// initializes.
type varSpan struct {
	pos, end token.Pos
	ids      []graph.ID
}

// initializerSpans lists the initializer expressions of a package's
// package-level variables, from its syntax.
func (x *extractor) initializerSpans(tpkg *types.Package) []varSpan {
	var out []varSpan
	for _, pkg := range x.eng.Packages() {
		if pkg.Types != tpkg {
			continue
		}
		for _, f := range pkg.Syntax {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, sp := range gd.Specs {
					out = append(out, x.valueSpans(pkg, sp.(*ast.ValueSpec))...)
				}
			}
		}
	}
	return out
}

// valueSpans maps one var spec's values to the variables they initialize:
// value i to name i when there is one value per name, else every value to
// every name. A name this build leaves out drops out of its span, which
// stays rather than falling through to the next variable's.
func (x *extractor) valueSpans(pkg *packages.Package, vs *ast.ValueSpec) []varSpan {
	ids := make([]graph.ID, len(vs.Names))
	for i, n := range vs.Names {
		ids[i] = x.varID(pkg, n)
	}
	if len(vs.Values) == len(vs.Names) {
		out := make([]varSpan, len(vs.Values))
		for i, v := range vs.Values {
			out[i] = varSpan{v.Pos(), v.End(), named(ids[i : i+1])}
		}
		return out
	}
	if len(vs.Values) == 0 {
		return nil
	}
	return []varSpan{{vs.Values[0].Pos(), vs.Values[len(vs.Values)-1].End(), slices.Compact(named(ids))}}
}

// named drops the places held by variables with no node.
func named(ids []graph.ID) []graph.ID {
	out := make([]graph.ID, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// varID is the node a package-level variable's initializer calls are
// edges from: the variable's own, or the package's for a blank one, and
// the empty ID for a variable whose source is outside the tree, which has
// no node. An edge from a node that is not there is a dropped edge, and
// ADR 002 rule 3 spends those on misses, not on declarations the build
// meant to omit.
func (x *extractor) varID(pkg *packages.Package, name *ast.Ident) graph.ID {
	if x.outside(name.Pos()) {
		return ""
	}
	if name.Name != "_" {
		id, _, ok := x.identify(pkg.TypesInfo.Defs[name])
		if !ok {
			return ""
		}
		return id
	}
	if id, ok := x.tests[pkg.PkgPath]; ok && x.testFile(name.Pos()) {
		// A blank variable of an in-package test file belongs to the
		// test files' node, as their imports do (#128).
		return id
	}
	id, err := x.m.GoID(pkg.PkgPath, "")
	if err != nil {
		return ""
	}
	return id
}

// callEdge emits one calls edge from a declaration of the module, once per
// (caller, callee) pair. A callee in the graph is an edge to its node; a
// callee outside it is an edge to its package node, marked
// boundary=package; a callee in the test binary's own package is neither
// and is skipped.
func (x *extractor) callEdge(from graph.ID, callee *types.Func) error {
	var e graph.Edge
	switch cpath := callee.Pkg().Path(); {
	case strings.HasSuffix(cpath, ".test"):
		return nil
	case x.inGraph(cpath) && x.outside(callee.Pos()):
		return nil
	case x.inGraph(cpath):
		fi, err := x.funcInfo(callee)
		if err != nil {
			return err
		}
		to := fi.id
		if x.called[[2]graph.ID{from, to}] {
			return nil
		}
		if err := x.initNode(to, callee); err != nil {
			return err
		}
		e = graph.Edge{From: from, To: to, Kind: graph.KindCalls, Derivation: graph.Resolved}
		x.report.Calls++
		x.report.moduleEdge(x.module, owner(cpath, x.modules), graph.KindCalls, x.testIDs[from] || fi.test)
	default:
		to, err := x.externalPackage(cpath, callee.Pkg().Name())
		if err != nil {
			return err
		}
		if x.called[[2]graph.ID{from, to}] {
			return nil
		}
		e = graph.Edge{From: from, To: to, Kind: graph.KindCalls, Derivation: graph.Resolved,
			Attrs: map[string]string{"boundary": "package"}}
		x.report.Boundary++
		x.report.moduleEdge(x.module, "", graph.KindCalls, x.testIDs[from])
	}
	x.called[[2]graph.ID{e.From, e.To}] = true
	return x.b.AddEdge(e)
}

// initDecls collects the init declarations of one package, which share
// one node; see initNode.
type initDecls struct {
	pkgPath string
	// seen holds one key per init declaration, by source position: with
	// tests loaded the same file is compiled into both the plain package
	// and the test variant, so the call graph reaches each declaration
	// twice and counting visits would say five where the source says
	// three.
	seen map[string]bool
	// pos is the position the node takes and test whether every init so
	// far is a test one. A package with a production init and a test one
	// has a production init node — the declaration is there in a build
	// without tests — positioned at the first production init in source
	// order; a package whose inits are all in test files has a test node
	// positioned at the first of them.
	pos       token.Pos
	test, gen bool
}

// add records one more init declaration, ignoring a declaration already
// seen at the same position.
func (d *initDecls) add(f *types.Func, x *extractor) {
	p := x.fset.Position(f.Pos())
	key := p.Filename + ":" + strconv.Itoa(p.Line)
	if d.seen[key] {
		return
	}
	test := x.testFile(f.Pos())
	first := len(d.seen) == 0
	// A production init displaces a test one, and within one kind the
	// earliest in source order wins.
	if first || d.test && !test || d.test == test && x.before(f.Pos(), d.pos) {
		d.pos, d.gen = f.Pos(), x.generated(f.Pos())
	}
	d.seen[key] = true
	d.test = (first || d.test) && test
}

// before reports whether one position precedes another in source order:
// by file, then by line.
func (x *extractor) before(a, b token.Pos) bool {
	pa, pb := x.fset.Position(a), x.fset.Position(b)
	if pa.Filename != pb.Filename {
		return pa.Filename < pb.Filename
	}
	return pa.Line < pb.Line
}

// testInit reports whether f is an init declared in an in-package test
// file of a package with a test files' node, and that node: the init is
// the test files', which carry what they do at package level (#128).
// Folding it into the package's init made a call only the test init
// makes a production edge, the cost ADR 004 rule 4 recorded.
func (x *extractor) testInit(f *types.Func) (graph.ID, bool) {
	if f.Name() != "init" || receiverType(f) != nil || !x.testFile(f.Pos()) {
		return "", false
	}
	id, ok := x.tests[f.Pkg().Path()]
	return id, ok
}

// initNode records a declared init function the first time the call graph
// reaches it. init is the one declaration Go keeps out of the package
// scope, so the declarations pass never sees it; and a package may declare
// several, which share one ID (there is no declaration-anchored way to tell
// them apart, and ADR 002 forbids a positional one), so one node is emitted
// for them by [extractor.flushInits] at the end of the pass, when how many
// there were — and whether any of them is production — is known. A package
// with more than one carries their number in an "inits" attribute.
func (x *extractor) initNode(id graph.ID, f *types.Func) error {
	if f.Name() != "init" || receiverType(f) != nil {
		return nil
	}
	d, ok := x.inits[id]
	if !ok {
		d = &initDecls{pkgPath: f.Pkg().Path(), seen: map[string]bool{}}
		x.inits[id] = d
	}
	d.add(f, x)
	return nil
}

// flushInits emits one node per package that declares init, and the
// contains edge from the package, once every init of the pass is known.
func (x *extractor) flushInits() error {
	for _, id := range slices.Sorted(maps.Keys(x.inits)) {
		d := x.inits[id]
		x.report.Decls++
		if d.test {
			x.report.Tests++
		}
		pkgID, err := x.m.GoID(d.pkgPath, "")
		if err != nil {
			return fmt.Errorf("golang: %w", err)
		}
		attrs := map[string]string{"exported": "false"}
		if len(d.seen) > 1 {
			attrs["inits"] = strconv.Itoa(len(d.seen))
		}
		n := graph.Node{ID: id, Kind: graph.KindFunc, Name: "init", Pos: x.position(d.pos),
			Generated: d.gen, Test: d.test, Attrs: attrs}
		if err := x.b.AddNode(n); err != nil {
			return err
		}
		if err := x.b.AddEdge(graph.Edge{From: pkgID, To: id, Kind: graph.KindContains, Derivation: graph.Extracted}); err != nil {
			return err
		}
	}
	return nil
}

// funcID mints the ID of a function or method from its type-checker
// object, which is the declaration the SSA function was built from.
func (x *extractor) funcID(f *types.Func) (graph.ID, error) {
	decl := f.Name()
	if recv := receiverType(f); recv != nil {
		decl = recv.Name() + "." + f.Name()
	}
	id, err := x.m.GoID(f.Pkg().Path(), decl)
	if err != nil {
		return "", fmt.Errorf("golang: %w", err)
	}
	return id, nil
}

// declOf maps an SSA function to the declaration that owns it: a closure
// to its outermost enclosing function, an instantiation to its generic
// origin, a wrapper to the method it wraps. The result is nil for a
// function with no declaration — the SSA builder's own synthetic
// functions, and the method of an anonymous interface, which a method
// value's wrapper names — and for a function from a package with no path.
//
// Closures fold into their declaration because a closure's own name
// (Parent$1) is positional: adding a closure above it renumbers it, which
// rule 1 of ADR 002 forbids in an identity.
func declOf(fn *ssa.Function) *types.Func {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	f, ok := fn.Object().(*types.Func)
	if !ok || f.Pkg() == nil || anonymousMethod(f) {
		return nil
	}
	return f.Origin()
}

// anonymousMethod reports whether f is a method of an unnamed type — an
// anonymous interface's, interface{ Close() } — which no declaration
// carries. Keyed by its bare name, a call to it through a method value
// was an edge to whatever the package declared under that name, or a
// drop when nothing was (#129); interface calls skip it the same way
// (invokes), and so did callers since #95.
func anonymousMethod(f *types.Func) bool {
	sig, ok := f.Type().(*types.Signature)
	return ok && sig.Recv() != nil && receiverType(f) == nil
}

// receiverType returns the named type a method is declared on, through a
// pointer and back to the generic origin, or nil for a function.
func receiverType(f *types.Func) *types.TypeName {
	sig, ok := f.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return nil
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	// A method declared through an alias receiver is the aliased type's
	// method — the one methods() emits under the type's name (#95).
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Origin().Obj()
	}
	// An interface method's receiver is the interface itself, which is
	// a Named; an anonymous interface's methods have no declaration to
	// key on.
	return nil
}

// walkTypes calls visit for every named type reachable from t through
// composite types, signatures, struct fields and interface methods, without
// descending into a named type's own definition — that is its own node's
// business. definition says t is a type declaration's own type, in which
// case the walk starts from what it is defined as rather than from the
// name, which would only visit itself. With constraints false a type
// parameter is not followed to its constraint: a struct field that uses T
// does not write T's constraint — its type's declaration does, and that
// type's node references it (#193).
func walkTypes(t types.Type, definition, constraints bool, visit func(*types.TypeName)) {
	seen := make(map[types.Type]bool)
	var walk func(t types.Type)
	walk = func(t types.Type) {
		if _, isParam := t.(*types.TypeParam); t == nil || seen[t] || isParam && !constraints {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Named:
			visit(t.Origin().Obj())
		case *types.Alias:
			visit(t.Obj())
		}
		for _, c := range children(t) {
			walk(c)
		}
	}
	if definition {
		switch t := t.(type) {
		case *types.Named:
			walk(t.Underlying())
			for _, c := range typeParams(t.TypeParams()) {
				walk(c)
			}
			return
		case *types.Alias:
			walk(types.Unalias(t))
			return
		}
	}
	walk(t)
}

// children lists the types a type is built from: elements, keys, type
// arguments, parameters and results, fields, methods and embeddings. A
// named type's own definition is not among them — it is its own node's
// business — only its type arguments.
func children(t types.Type) []types.Type {
	switch t := t.(type) {
	case *types.Named:
		return tupleTypes(t.TypeArgs().Len(), func(i int) types.Type { return t.TypeArgs().At(i) })
	case *types.Map:
		return []types.Type{t.Key(), t.Elem()}
	case interface{ Elem() types.Type }: // pointer, slice, array, channel
		return []types.Type{t.Elem()}
	case *types.Tuple:
		return tupleTypes(t.Len(), func(i int) types.Type { return t.At(i).Type() })
	case *types.Signature:
		return append([]types.Type{t.Params(), t.Results()}, typeParams(t.TypeParams())...)
	case *types.TypeParam:
		return []types.Type{t.Constraint()}
	case *types.Union:
		return tupleTypes(t.Len(), func(i int) types.Type { return t.Term(i).Type() })
	case *types.Struct:
		return tupleTypes(t.NumFields(), func(i int) types.Type { return t.Field(i).Type() })
	case *types.Interface:
		out := tupleTypes(t.NumExplicitMethods(), func(i int) types.Type { return t.ExplicitMethod(i).Type() })
		return append(out, tupleTypes(t.NumEmbeddeds(), t.EmbeddedType)...)
	}
	return nil
}

// typeParams lists the type parameters of a declaration, whose
// constraints are types it names (#95).
func typeParams(l *types.TypeParamList) []types.Type {
	return tupleTypes(l.Len(), func(i int) types.Type { return l.At(i) })
}

func tupleTypes(n int, at func(int) types.Type) []types.Type {
	out := make([]types.Type, 0, n)
	for i := range n {
		out = append(out, at(i))
	}
	return out
}
