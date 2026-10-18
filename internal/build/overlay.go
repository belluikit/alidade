// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/adapter/golang"
	"github.com/belluikit/alidade/internal/adapter/py"
	"github.com/belluikit/alidade/internal/graph"
)

// A worktree's graph is the base's with the worktree's differences laid
// over it, as a union file system lays one directory over another (#25,
// ADR 005 amended): one graph is built in full, for the checkout the
// session started in, and another worktree of the repository reads what
// it shares with that one from it.
//
//   - A layer whose files are byte-identical to the base's — its signature
//     is the base's — takes the base's contributions as they are.
//   - A layer whose files differ is extracted from the worktree, as a build
//     of the worktree would, except the Go layer.
//   - The Go layer loads again only the packages a differing Go file is in
//     (golang.Options.Only), and takes every other package's declarations
//     and edges from the base. The calls and implements edges a package
//     has depend on the whole program — every implementation of an
//     interface, every function value reaching a call — which a load of
//     fewer packages does not see; the base's such edges from a
//     declaration the worktree still has into a package it did not load
//     again are kept, marked INFERRED and carried=base.
//   - What a load of fewer packages cannot tell of a whole module — which
//     configurations -go-configs auto chooses, whether the module loads
//     for one — the layer is extracted whole for, when the worktree may
//     not have the base's (#39, #40).
//
// Measured on #25 against a full build of each worktree, on six govm
// worktrees and two of trendvidia/go: the Go layer missed at most 0.06%
// of the edges from the packages it loaded again and had at most 0.07%
// wrong, all of them carried; on trendvidia/go it took 0.6–6.9 s and
// 0.2–2.2 GB, against 20–23 s and 6.7 GB for the full build.
// TestOverlayIsAWorktreeBuild fails the day the two differ by more than
// the carried edges. One difference is known and pinned rather than
// measured away: a package the worktree did not change keeps the base's
// edges, so a call of its through an interface misses an implementation
// the worktree added (TestOverlayMissesAnImplementationCalledFromUnchangedCode).

// An Overlay says how a worktree's graph was made from the base's.
type Overlay struct {
	// Base is the root of the graph it is laid over.
	Base string
	// Inherited are the layers taken whole from the base, their files
	// being the base's; a schema root's is "proto@<label>".
	Inherited []string
	// Packages are the Go packages loaded again because a file of theirs
	// differs from the base's, Removed those whose directory holds no Go
	// file any more.
	Packages, Removed []string
	// Carried counts the base's calls and implements edges from those
	// packages that were kept, INFERRED.
	Carried int
	// GoWhole says the Go layer was extracted whole rather than laid
	// over the base's: a go.mod, go.sum or go.work differs, the base has
	// no Go layer, or a fact of a whole module the base's layer rests on
	// may not hold for the worktree — auto's choice of configurations,
	// or whether a module loads for one (#39, #40). A package to load
	// again that holds no Go file the host builds is laid over the base
	// too (#35).
	GoWhole bool
}

// Overlay returns a session for another worktree of the repository at
// root, whose graph lays the worktree's differences over this session's.
// Nothing is built yet, and this session must have built its graph
// before the worktree's is built.
func (s *Session) Overlay(root string) (*Session, error) {
	o, err := NewSession(root, s.opts)
	if err != nil {
		return nil, err
	}
	o.base = s
	return o, nil
}

// overBase makes a worktree's layer signatures say where each layer comes
// from, so a layer is extracted or taken again exactly when that changes:
// one taken from the base is taken again when the base's files change,
// the Go layer laid over the base's is laid again when the base's or the
// worktree's Go files change, and any other layer of the worktree's own
// is extracted again only when its own files change — a base's edit
// reruns no extraction of the worktree that does not read it. A layer
// moving between the base's and its own changes its signature too. The
// base's own signatures are as given.
func (s *Session) overBase(own map[string]string) map[string]string {
	if s.base == nil {
		return own
	}
	out := map[string]string{}
	for _, m := range []map[string]string{own, s.base.sigs} {
		for name := range m {
			switch {
			case own[name] == s.base.sigs[name]:
				out[name] = "base " + own[name]
			case name == "go" && !s.goWhole():
				out[name] = own[name] + " over " + s.base.sigs[name]
			default:
				out[name] = "own " + own[name]
			}
		}
	}
	return out
}

// goWhole reports whether a worktree's Go layer differing from the base's
// is extracted whole without trying to lay it over the base's: the base
// has no Go layer, or the module files differ. overlayGo may still find
// it must be extracted whole (#39, #40); that layer is signed as over the
// base all the same, since whether it is rests on the base's report.
func (s *Session) goWhole() bool {
	return s.base.rep.Go == nil || moduleFilesDiffer(s.base.goFiles, s.goFilesNow())
}

// inherits reports whether a layer's files are the base's.
func (s *Session) inherits(name string) bool {
	return s.base != nil && s.own[name] == s.base.sigs[name]
}

// adopt takes the base's contributions to a layer.
func (s *Session) adopt(lb *graph.Builder, name string) {
	nodes, edges := s.base.b.Contributions(name)
	lb.Adopt(nodes, edges)
	s.from[name] = "base"
}

// baseReport is the base's last report, or an empty one for a session
// that is not a worktree's.
func (s *Session) baseReport() *Report {
	if s.base == nil || s.base.rep == nil {
		return &Report{}
	}
	return s.base.rep
}

// inherit wraps a layer's extraction: a worktree's layer whose files are
// the base's takes the base's contributions and report instead.
func inherit[R any](s *Session, name string, baseRep *R, extract func(*graph.Builder) (*R, error)) func(*graph.Builder) (*R, error) {
	return func(lb *graph.Builder) (*R, error) {
		if s.inherits(name) {
			s.adopt(lb, name)
			if baseRep == nil {
				return new(R), nil // the base read none of its files
			}
			return baseRep, nil
		}
		s.from[name] = "own"
		return extract(lb)
	}
}

// goLayer is the Go layer's extraction: the whole layer for the base, and
// for a worktree the base's, the base's with the differing packages laid
// over it, or the whole layer when goWhole says so.
func (s *Session) goLayer(root string, opts golang.Options) func(*graph.Builder) (*golang.Report, error) {
	return func(lb *graph.Builder) (*golang.Report, error) {
		s.overlaid = nil
		switch {
		case s.inherits("go"):
			s.adopt(lb, "go")
			return s.base.rep.Go, nil
		case s.base == nil || s.goWhole():
			s.from["go"] = "own"
			return extractGo(lb, root, opts)
		}
		s.from["go"] = "over"
		rep, whole, err := s.overlayGo(lb, root, opts)
		if err != nil || !whole {
			return rep, err
		}
		s.from["go"] = "own"
		return extractGo(lb, root, opts)
	}
}

// goFilesNow are the Go layer's file hashes of the build under way; see
// Session.signatures.
func (s *Session) goFilesNow() map[string]string { return s.pendingGo }

// moduleFilesDiffer reports whether a file of the Go layer that is not Go
// source — go.mod, go.sum, go.work — differs between two trees: which
// modules there are, and which versions they require, is then not the
// base's, and no package of it can be taken from the base.
func moduleFilesDiffer(base, wt map[string]string) bool {
	for _, m := range []map[string]string{base, wt} {
		for rel := range m {
			if !strings.HasSuffix(rel, ".go") && base[rel] != wt[rel] {
				return true
			}
		}
	}
	return false
}

// differingGo lists the Go files whose content differs between two
// trees, or that only one has.
func differingGo(base, wt map[string]string) []string {
	differ := map[string]bool{}
	for _, m := range []map[string]string{base, wt} {
		for rel := range m {
			if strings.HasSuffix(rel, ".go") && base[rel] != wt[rel] {
				differ[rel] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(differ))
}

// overlayGo lays the worktree's differing Go packages over the base's Go
// layer: the base's contributions for every other package, the packages
// loaded again, and the base's whole-program edges from them carried.
// It adds nothing and reports whole when a fact of a whole module the
// base's graph rests on may not hold for the worktree (#39, #40); the
// layer is then extracted whole.
func (s *Session) overlayGo(lb *graph.Builder, root string, opts golang.Options) (rep *golang.Report, whole bool, err error) {
	mods, _, err := golang.Modules(root, opts.Skip)
	if err != nil {
		return nil, false, err
	}
	differ := differingGo(s.base.goFiles, s.goFilesNow())
	present, gone := golang.PackagesOf(root, mods, opts.Skip, differ)
	paths := make([]string, len(mods))
	for i, m := range mods {
		paths[i] = m.Path
	}
	base := s.base.rep.Go
	if !modulesLoaded(base, paths, append(slices.Clone(present), gone...)) {
		return nil, true, nil
	}
	sub := &golang.Report{}
	var subNodes []graph.Node
	var subEdges []graph.Edge
	if len(present) > 0 {
		scratch := graph.NewBuilder(root)
		if sub, err = extractGo(scratch, root, golang.Reload(opts, base, present)); err != nil {
			return nil, false, err
		}
		if !modulesLoaded(sub, paths, present) {
			return nil, true, nil
		}
		subNodes, subEdges = scratch.Contributions("")
	}
	if !s.stillLoad(sub, paths, append(slices.Clone(present), gone...)) {
		return nil, true, nil
	}
	excluded := hostExcluded(base.HostExcluded, sub.HostExcluded, differ)
	if opts.Configs == "auto" && !golang.SameAutoChoice(base, root, excluded) {
		return nil, true, nil
	}

	owned := ownedBy(append(slices.Clone(present), gone...))
	carried := s.layOver(lb, owned, sub, subNodes, subEdges)
	s.overlaid = &Overlay{Packages: present, Removed: gone, Carried: carried}
	rep = overlaidGoReport(base, sub, owned)
	rep.Skipped = overlaidSkips(base.Skipped, sub.Skipped, differ, s.base.root, s.root)
	rep.HostExcluded = excluded
	return rep, false, nil
}

// layOver adds the base's Go contributions outside the packages loaded
// again, the load's own, and the base's whole-program edges from those
// packages it did not find, carried; it returns how many were carried.
// Only an edge a full build could have is carried; see carries.
func (s *Session) layOver(lb *graph.Builder, owned func(graph.ID) bool, sub *golang.Report, subNodes []graph.Node, subEdges []graph.Edge) int {
	nodes, edges := s.base.b.Contributions("go")
	keptNodes := nodes[:0:0]
	for _, n := range nodes {
		if !owned(n.ID) {
			keptNodes = append(keptNodes, n)
		}
	}
	var keptEdges, carry []graph.Edge
	for _, e := range edges {
		switch {
		case !owned(e.From):
			keptEdges = append(keptEdges, e)
		case !owned(e.To) && carries(sub, e):
			carry = append(carry, e)
		}
	}
	lb.Adopt(nil, keptEdges)
	lb.Adopt(subNodes, subEdges)
	carried := carried(carry, subNodes, subEdges)
	lb.Adopt(reached(keptNodes, keptEdges, carried), carried)
	return len(carried)
}

// carries reports whether a whole-program edge of the base, from a
// package loaded again, is one a full build of the worktree could have,
// as the load of the packages found them: a call only from a package its
// call graph reached, and an implements edge only from one that
// type-checked. A package the load found broken has neither, and one a
// broken import cut off from the call graph keeps its implements edges,
// as in a full build. Which packages those are is a fact of each
// package's variants, not of its path: one whose test variant alone
// failed is in FailedPackages, and its plain variant still has calls
// (#41).
func carries(sub *golang.Report, e graph.Edge) bool {
	typed, called := sub.Reached(goPackage(e.From))
	switch e.Kind {
	case graph.KindCalls:
		return called
	case graph.KindImplements:
		return typed
	}
	return false
}

// modulesLoaded reports whether every module holding one of the packages,
// of the module paths given, loaded as rep found them: for the host and
// each further configuration, with tests and without. Whether a module
// loads is a fact of all its packages — caliper refuses a load only when
// none of them type-checks — so a load of some of them can neither
// confirm the base's failure nor stand for a whole module's, and only a
// whole build can tell (#40).
func modulesLoaded(rep *golang.Report, paths, pkgs []string) bool {
	for _, p := range pkgs {
		if !rep.Loaded(golang.Owner(p, paths)) {
			return false
		}
	}
	return true
}

// stillLoad reports whether each module holding one of the packages
// changed still loads as the base's did, by golang.StillLoads: the
// packages the change cannot reach are the base's that import none of
// them, at any depth (#50).
func (s *Session) stillLoad(sub *golang.Report, paths, changed []string) bool {
	reached := importersOf(s.base.b, changed)
	unreached := func(p string) bool { return !reached[p] }
	seen := map[string]bool{}
	for _, p := range changed {
		m := golang.Owner(p, paths)
		if seen[m] {
			continue
		}
		seen[m] = true
		if !golang.StillLoads(s.base.rep.Go, sub, m, paths, unreached) {
			return false
		}
	}
	return true
}

// importersOf are the packages of the base's Go layer that are, or
// import at any depth, one of pkgs or its external test package.
func importersOf(b *graph.Builder, pkgs []string) map[string]bool {
	_, edges := b.Contributions("go")
	by := map[string][]string{}
	for _, e := range edges {
		if e.Kind == graph.KindImports {
			by[goPackage(e.To)] = append(by[goPackage(e.To)], goPackage(e.From))
		}
	}
	out := map[string]bool{}
	var queue []string
	for _, p := range pkgs {
		queue = append(queue, p, p+"_test")
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if out[p] {
			continue
		}
		out[p] = true
		queue = append(queue, by[p]...)
	}
	return out
}

// hostExcluded are the worktree's files its host's load leaves out: the
// base's outside the packages loaded again, which are the directories of
// the differing files, and the load's own in their place (#39).
func hostExcluded(base, sub, differ []string) []string {
	dirs := dirsOf(differ)
	out := slices.Clone(sub)
	for _, f := range base {
		if !dirs[path.Dir(f)] {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// reached drops the base's external packages no edge kept from the base
// reaches: the extractor emits one only where a boundary edge lands, so
// one only a package loaded again imported is the worktree's to emit, if
// it still imports it.
func reached(nodes []graph.Node, kept, carried []graph.Edge) []graph.Node {
	ends := map[graph.ID]bool{}
	for _, es := range [][]graph.Edge{kept, carried} {
		for _, e := range es {
			ends[e.From], ends[e.To] = true, true
		}
	}
	out := nodes[:0:0]
	for _, n := range nodes {
		if n.Attrs["external"] != "true" || ends[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// ownedBy reports whether a Go ID is of one of the packages, or of the
// external test package of one.
func ownedBy(pkgs []string) func(graph.ID) bool {
	set := map[string]bool{}
	for _, p := range pkgs {
		set[p], set[p+"_test"] = true, true
	}
	return func(id graph.ID) bool {
		pkg := goPackage(id)
		return pkg != "" && set[pkg]
	}
}

// goPackage is the import path of the package a Go ID is of, "" for an
// ID of another layer.
func goPackage(id graph.ID) string {
	rest, ok := strings.CutPrefix(string(id), "go://")
	if !ok {
		return ""
	}
	pkg, _, _ := strings.Cut(rest, "#")
	return pkg
}

// carried keeps the base's whole-program edges from a declaration the
// packages loaded again still have, when their load did not find the
// edge itself, marked INFERRED and carried=base: such an edge depends on
// code the load did not read.
func carried(base []graph.Edge, nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	has := map[graph.ID]bool{}
	for _, n := range nodes {
		has[n.ID] = true
	}
	type key struct {
		from, to graph.ID
		kind     graph.EdgeKind
	}
	found := map[key]bool{}
	for _, e := range edges {
		found[key{e.From, e.To, e.Kind}] = true
	}
	var out []graph.Edge
	for _, e := range base {
		k := key{e.From, e.To, e.Kind}
		if !has[e.From] || found[k] {
			continue
		}
		found[k] = true
		e.Derivation = graph.Inferred
		e.Attrs = maps.Clone(e.Attrs)
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		e.Attrs["carried"] = "base"
		out = append(out, e)
	}
	return out
}

// overlaidGoReport is the base's Go report with the packages loaded
// again in place of the base's: their failures, and the time the load
// took. The counts stay the base's: they describe the modules, which a
// worktree with the same module files shares.
func overlaidGoReport(base, sub *golang.Report, owned func(graph.ID) bool) *golang.Report {
	rep := *base
	notOwned := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if !owned(graph.ID("go://" + p)) {
				out = append(out, p)
			}
		}
		return out
	}
	rep.FailedPackages = merge(notOwned(base.FailedPackages), sub.FailedPackages)
	rep.WithoutCalls = merge(notOwned(base.WithoutCalls), sub.WithoutCalls)
	rep.Load, rep.CallGraph, rep.Emit = sub.Load, sub.CallGraph, sub.Emit
	return &rep
}

// overlaidSkips are the base's skipped files and directories outside the
// packages loaded again, which are the directories of the differing
// files, and the load's in their place (#35). A skipped directory is a
// package's own, so one below a package loaded again stays; a go.mod is
// the module's, and stays unless the load skips it too. A reason naming
// a path of the base's names the worktree's instead, as a build of it
// would.
func overlaidSkips(base, sub []graph.Skip, differ []string, baseRoot, root string) []graph.Skip {
	dirs := dirsOf(differ)
	out := slices.Clone(sub)
	for _, sk := range base {
		reloaded := dirs[sk.File] || strings.HasSuffix(sk.File, ".go") && dirs[path.Dir(sk.File)]
		sk.Reason = renameRoot(sk.Reason, baseRoot, root)
		if !reloaded && !slices.Contains(out, sk) {
			out = append(out, sk)
		}
	}
	slices.SortStableFunc(out, func(a, b graph.Skip) int { return strings.Compare(a.File, b.File) })
	return out
}

// dirsOf are the directories of the differing files: those of the
// packages loaded again.
func dirsOf(differ []string) map[string]bool {
	dirs := map[string]bool{}
	for _, f := range differ {
		dirs[path.Dir(f)] = true
	}
	return dirs
}

// renameRoot names root wherever s names baseRoot or a path below it:
// the root module's load error names its directory with nothing below it
// ("load /x/go: ..."). A longer name, /x/go-old beside /x/go, is not it.
func renameRoot(s, baseRoot, root string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, baseRoot)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(baseRoot)
		if end < len(s) && namesOn(s[end]) {
			b.WriteString(s[:end])
		} else {
			b.WriteString(s[:i] + root)
		}
		s = s[end:]
	}
}

// namesOn reports whether a byte after a path continues its last element.
func namesOn(c byte) bool {
	return c == '.' || c == '_' || c == '-' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func merge(a, b []string) []string {
	out := slices.Clone(a)
	for _, p := range b {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// overlayReport says how the worktree's graph was made: which layers are
// the base's, and what the Go layer loaded again.
func (s *Session) overlayReport(rep *Report) *Overlay {
	o := &Overlay{Base: s.base.root}
	switch s.from["go"] {
	case "over":
		if s.overlaid != nil {
			o.Packages, o.Removed, o.Carried = s.overlaid.Packages, s.overlaid.Removed, s.overlaid.Carried
		}
	case "own":
		o.GoWhole = rep.Go != nil
	}
	for _, name := range slices.Sorted(maps.Keys(s.from)) {
		if s.from[name] == "base" {
			o.Inherited = append(o.Inherited, name)
		}
	}
	return o
}

// refreshFromBase folds what jedi has learnt since (#269) into the base's
// Python layer, whose files the worktree's are, and takes it again.
func (s *Session) refreshFromBase(rep *Report) error {
	if err := s.base.catchUpPy(); err != nil {
		return err
	}
	s.pyFromBase(rep)
	return nil
}

// pyFromBase takes the base's Python contributions and report as they
// are now. A build whose Python files are unchanged takes them again
// too: the base may have folded jedi's answers in since, which changes
// no signature.
func (s *Session) pyFromBase(rep *Report) {
	s.b.Drop("py")
	s.adopt(s.b.Layer("py"), "py")
	rep.Py = s.base.rep.Py
}

// ownLive is the jedi of the session's own previous Python layer, nil
// when there is none or that layer was the base's: a worktree's session
// stops or closes only a jedi it started, never the base's.
func (s *Session) ownLive(prev *Report) *py.Live {
	if s.from["py"] == "base" || prev.Py == nil {
		return nil
	}
	return prev.Py.Live
}
