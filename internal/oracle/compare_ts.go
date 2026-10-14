// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"path"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// TSResult compares the frontend layer with TypeScript's resolution and
// protoc-gen-es's embedded descriptors. Counts are [oracle, graph has].
// An import TypeScript left unresolved is not compared on either side:
// the graph's edge for that specifier is counted apart, not as extra.
type TSResult struct {
	Files, Unresolved, OutOfScope, PBFiles int
	// Pairs is how many distinct (file, target) pairs Imports' specifiers
	// come to; Named the imports of a module named, not a file, by kind.
	Pairs                                           int
	Named                                           map[string]int
	UnresolvedImports                               []string
	Imports, Generates                              [2]int
	MissingImports, ExtraImports, UncomparedImports []string
	MissingGenerates, ExtraGenerates                []string
}

// CompareTS compares a graph with the frontend oracle.
func CompareTS(g *graph.Graph, t *TS) *TSResult {
	r := &TSResult{Files: len(t.Files), Unresolved: len(t.Unresolved), OutOfScope: len(t.OutOfScope), PBFiles: t.PBFiles,
		Named: t.Named, UnresolvedImports: t.Unresolved}
	pairs := map[[2]graph.ID]bool{}
	for _, e := range t.Imports {
		pairs[e] = true
	}
	r.Pairs = len(pairs)
	r.Imports, r.MissingImports = edgeRecall(g, t.Imports, graph.KindImports)
	r.extraImports(g, t)
	r.generates(g, t)
	sortAll(&r.ExtraImports, &r.UncomparedImports, &r.MissingGenerates, &r.ExtraGenerates)
	return r
}

// extraImports lists the graph's imports from the files the oracle read
// that TypeScript did not resolve to the same place, setting apart those
// a specifier TypeScript left unresolved accounts for.
func (r *TSResult) extraImports(g *graph.Graph, t *TS) {
	want := map[[2]graph.ID]bool{}
	for _, e := range t.Imports {
		want[e] = true
	}
	unresolved := map[graph.ID][]string{}
	for _, u := range t.Unresolved {
		file, spec, _ := strings.Cut(u, ": ")
		id := graph.ID("ts://" + file)
		unresolved[id] = append(unresolved[id], spec)
	}
	for e := range g.Edges() {
		if e.Kind != graph.KindImports || !t.Files[e.From] || want[[2]graph.ID{e.From, e.To}] {
			continue
		}
		label := string(e.From) + " -> " + string(e.To)
		if accountedFor(e.To, unresolved[e.From]) {
			r.UncomparedImports = append(r.UncomparedImports, label)
		} else {
			r.ExtraImports = append(r.ExtraImports, label)
		}
	}
}

// accountedFor reports whether an import edge's target is what one of
// the file's unresolved specifiers names: its package, or a file whose
// path ends as the specifier does.
func accountedFor(to graph.ID, specs []string) bool {
	target := strings.TrimPrefix(string(to), "ts://")
	pkg, isPkg := strings.CutPrefix(target, "@pkg/")
	for _, spec := range specs {
		if isPkg && packageName(spec) == pkg {
			return true
		}
		if !isPkg && (strings.HasSuffix(target, strings.TrimLeft(spec, "./")) || path.Base(target) == path.Base(spec)) {
			return true // relative or through an alias ($lib/X.svelte)
		}
	}
	return false
}

func (r *TSResult) generates(g *graph.Graph, t *TS) {
	have := map[[2]graph.ID]bool{}
	pbExports := map[string]bool{}
	note := func(e graph.Edge) {
		if e.Kind == graph.KindGenerates && strings.HasPrefix(string(e.To), "ts://") {
			have[[2]graph.ID{e.From, e.To}] = true
		}
	}
	for e := range g.Edges() {
		note(e)
	}
	for _, d := range g.Dropped() {
		note(d.Edge)
	}
	want := map[[2]graph.ID]bool{}
	for _, e := range t.Generates {
		want[e] = true
		file, _, _ := strings.Cut(string(e[1]), "#")
		pbExports[file] = true
		r.Generates[0]++
		if have[e] {
			r.Generates[1]++
		} else {
			r.MissingGenerates = append(r.MissingGenerates, string(e[0])+" -> "+string(e[1]))
		}
	}
	for e := range have {
		file, _, _ := strings.Cut(string(e[1]), "#")
		if pbExports[file] && !want[e] {
			r.ExtraGenerates = append(r.ExtraGenerates, string(e[0])+" -> "+string(e[1]))
		}
	}
}

func (r *TSResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ts files %d, imports TypeScript left unresolved %d, imports of files the layer does not read %d, _pb.ts files %d\n", r.Files, r.Unresolved, r.OutOfScope, r.PBFiles)
	fmt.Fprintf(&b, "ts imports %d / %d (%d distinct file pairs; Node builtins %d, ambient modules %d), generates %d / %d\n",
		r.Imports[1], r.Imports[0], r.Pairs, r.Named["builtin"], r.Named["ambient"], r.Generates[1], r.Generates[0])
	fmt.Fprintf(&b, "ts extra imports %d (not compared %d), extra generates %d\n", len(r.ExtraImports), len(r.UncomparedImports), len(r.ExtraGenerates))
	for _, l := range []struct {
		name  string
		items []string
	}{{"unresolved import", r.UnresolvedImports}, {"missing import", r.MissingImports}, {"extra import", r.ExtraImports}, {"missing generates", r.MissingGenerates}, {"extra generates", r.ExtraGenerates}} {
		for i, s := range l.items {
			if i == 10 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-10)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
