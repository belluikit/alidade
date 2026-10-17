// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package golang

// SPIKE (#25), not for merge: dumps the Go layer's nodes and edges for a
// full load of a tree, or for a load of only some of its packages, so a
// layered graph (base + per-worktree overlay) can be measured against a
// full build of the worktree.
//
//	SPIKE_DIR=tree SPIKE_OUT=file [SPIKE_PKGS=import/path,...] go test -run TestSpikeDump

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/scope"
)

func TestSpikeDump(t *testing.T) {
	dir, out := os.Getenv("SPIKE_DIR"), os.Getenv("SPIKE_OUT")
	if dir == "" || out == "" {
		t.Skip("spike: SPIKE_DIR and SPIKE_OUT unset")
	}
	skip, err := scope.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Skip = skip
	if os.Getenv("SPIKE_DEPS") == "source" {
		opts.DepsFromExportData = false
	}
	if pk := os.Getenv("SPIKE_PKGS"); pk != "" {
		pats := patternsFor(t, dir, strings.Split(pk, ","))
		opts.patterns = &pats
		t.Logf("patterns %v", pats)
	}
	b := graph.NewBuilder(dir).Layer("go")
	start := time.Now()
	rep, err := Extract(b, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	g := b.Build()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "T\t%d\t%d\t%d\n", elapsed.Milliseconds(), rep.Load.Milliseconds(), rep.CallGraph.Milliseconds())
	var lines []string
	for n := range g.Nodes() {
		lines = append(lines, fmt.Sprintf("N\t%s\t%s", n.ID, n.Attrs["external"]))
	}
	for e := range g.Edges() {
		lines = append(lines, fmt.Sprintf("E\t%s\t%s\t%s", e.From, e.To, e.Kind))
	}
	// A subset load is an overlay: an edge into a package it did not load
	// lands on the base's node, so the edge is kept for the splice, which
	// drops it only if the base has no such node either.
	for _, d := range g.Dropped() {
		if opts.patterns != nil && d.Reason == graph.DropUnknownTarget {
			lines = append(lines, fmt.Sprintf("E\t%s\t%s\t%s", d.Edge.From, d.Edge.To, d.Edge.Kind))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Logf("%s: %d lines, %v (load %v, call graph %v), modules %v", dir, len(lines), elapsed, rep.Load, rep.CallGraph, rep.Modules)
}

// patternsFor maps import paths to each module's directory and the
// import paths caliper loads there.
func patternsFor(t *testing.T, dir string, paths []string) map[string][]string {
	mods, _, err := Modules(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, ip := range paths {
		best := -1
		for i, m := range mods {
			if (ip == m.Path || strings.HasPrefix(ip, m.Path+"/")) && (best < 0 || len(m.Path) > len(mods[best].Path)) {
				best = i
			}
		}
		if best < 0 { // the standard library has no path prefix
			best = slices.IndexFunc(mods, func(m Module) bool { return m.Path == "std" })
		}
		if best < 0 {
			t.Fatalf("no module holds %s", ip)
		}
		m := mods[best]
		rel := ip
		if m.Path != "std" {
			rel = strings.TrimPrefix(strings.TrimPrefix(ip, m.Path), "/")
		}
		if _, err := os.Stat(filepath.Join(m.Dir, rel)); err != nil {
			continue // deleted in this tree
		}
		// The import path, not ./rel: caliper loads a toolchain checkout
		// with no directory (GOROOT names it), where ./rel resolves
		// against the process's directory and matches nothing.
		out[m.Dir] = append(out[m.Dir], ip)
	}
	return out
}
