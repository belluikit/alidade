// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"maps"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/adapter/golang"
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
//
// Measured on #25 against a full build of each worktree, on six govm
// worktrees and two of trendvidia/go: the Go layer missed at most 0.06%
// of the edges from the packages it loaded again and had at most 0.07%
// wrong, all of them carried; on trendvidia/go it took 0.6–6.9 s and
// 0.2–2.2 GB, against 20–23 s and 6.7 GB for the full build.
// TestOverlayIsAWorktreeBuild fails the day the two differ by more than
// the carried edges.

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
	// over the base's: a go.mod, go.sum or go.work differs, or the base
	// has no Go layer.
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

// Generation counts the graphs this session has made: a build or a
// refresh. A worktree's graph over it is out of date when it moves.
func (s *Session) Generation() int { return s.gen }

// overBase makes a worktree's layer signatures stand for its files and
// the base's together, so a layer taken from the base is taken again
// when the base's files change, and one of its own is extracted again
// when the base it was laid over changes; the base's own are as given.
func (s *Session) overBase(own map[string]string) map[string]string {
	if s.base == nil {
		return own
	}
	out := map[string]string{}
	for _, m := range []map[string]string{own, s.base.sigs} {
		for name := range m {
			out[name] = own[name] + " over " + s.base.sigs[name]
		}
	}
	return out
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
// over it, or the whole layer when the module files differ.
func (s *Session) goLayer(root string, opts golang.Options) func(*graph.Builder) (*golang.Report, error) {
	return func(lb *graph.Builder) (*golang.Report, error) {
		s.overlaid = nil
		switch {
		case s.inherits("go"):
			s.adopt(lb, "go")
			return s.base.rep.Go, nil
		case s.base == nil || s.base.rep.Go == nil || moduleFilesDiffer(s.base.goFiles, s.goFilesNow()):
			s.from["go"] = "own"
			return extractGo(lb, root, opts)
		}
		s.from["go"] = "over"
		return s.overlayGo(lb, root, opts)
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
	var out []string
	for _, m := range []map[string]string{base, wt} {
		for rel := range m {
			if strings.HasSuffix(rel, ".go") && base[rel] != wt[rel] && !slices.Contains(out, rel) {
				out = append(out, rel)
			}
		}
	}
	slices.Sort(out)
	return out
}

// overlayGo lays the worktree's differing Go packages over the base's Go
// layer: the base's contributions for every other package, the packages
// loaded again, and the base's whole-program edges from them carried.
func (s *Session) overlayGo(lb *graph.Builder, root string, opts golang.Options) (*golang.Report, error) {
	mods, _, err := golang.Modules(root, opts.Skip)
	if err != nil {
		return nil, err
	}
	present, gone := golang.PackagesOf(root, mods, opts.Skip, differingGo(s.base.goFiles, s.goFilesNow()))
	owned := ownedBy(append(slices.Clone(present), gone...))

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
		case !owned(e.To) && (e.Kind == graph.KindCalls || e.Kind == graph.KindImplements):
			carry = append(carry, e)
		}
	}
	lb.Adopt(keptNodes, keptEdges)

	sub := &golang.Report{}
	var subNodes []graph.Node
	var subEdges []graph.Edge
	if len(present) > 0 {
		scratch := graph.NewBuilder(root)
		o := opts
		o.Only = present
		if sub, err = extractGo(scratch, root, o); err != nil {
			return nil, err
		}
		subNodes, subEdges = scratch.Contributions("")
		lb.Adopt(subNodes, subEdges)
	}
	carried := carried(carry, subNodes, subEdges)
	lb.Adopt(nil, carried)

	s.overlaid = &Overlay{Packages: present, Removed: gone, Carried: len(carried)}
	return overlaidGoReport(s.base.rep.Go, sub, owned), nil
}

// ownedBy reports whether a Go ID is of one of the packages, or of the
// external test package of one.
func ownedBy(pkgs []string) func(graph.ID) bool {
	set := map[string]bool{}
	for _, p := range pkgs {
		set[p], set[p+"_test"] = true, true
	}
	return func(id graph.ID) bool {
		rest, ok := strings.CutPrefix(string(id), "go://")
		if !ok {
			return false
		}
		pkg, _, _ := strings.Cut(rest, "#")
		return set[pkg]
	}
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

// refreshFromBase takes the base's Python contributions again, the base
// having folded in what jedi has learnt since (#269): the worktree's
// Python files are the base's, so jedi's answers for the base are its.
func (s *Session) refreshFromBase() error {
	if _, _, err := s.base.Refresh(); err != nil {
		return err
	}
	s.b.Drop("py")
	s.adopt(s.b.Layer("py"), "py")
	return nil
}
