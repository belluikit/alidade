// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package ts

import (
	"github.com/trendvidia/alidade/internal/adapter/ts/svelte"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

var (
	checkerOnce   sync.Once
	checkerGraph  *graph.Graph
	checkerReport *Report
)

// checkerFixture builds testdata/checker, the type checker's fixture
// (#215): a solution tsconfig over tsconfig.app.json, a file none
// includes, and a Svelte component.
func checkerFixture(t *testing.T) (*graph.Graph, *Report) {
	t.Helper()
	checkerOnce.Do(func() {
		dir, err := filepath.Abs(filepath.Join("testdata", "checker"))
		if err != nil {
			t.Fatal(err)
		}
		b := graph.NewBuilder(dir)
		rep, err := Extract(b, dir, DefaultOptions())
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		checkerGraph, checkerReport = b.Build(), rep
	})
	return checkerGraph, checkerReport
}

// TestCheckerDeclarations pins the declarations that get nodes: module
// level ones and one level of members — a class's, an interface's, a
// type literal's, an object literal's functions — with their kind and
// the lines they span, contained by their file or holder.
func TestCheckerDeclarations(t *testing.T) {
	g, _ := checkerFixture(t)
	for decl, want := range map[string]struct {
		kind      graph.Kind
		line, end string
	}{
		"src/util.ts#helper":          {graph.KindFunc, "1", "3"},
		"src/util.ts#twice":           {graph.KindFunc, "5", "5"},
		"src/util.ts#Store":           {graph.KindType, "7", "16"},
		"src/util.ts#Store.items":     {graph.KindField, "8", "8"},
		"src/util.ts#Store.add":       {graph.KindMethod, "9", "12"},
		"src/util.ts#Store.size":      {graph.KindMethod, "13", "15"}, // a getter
		"src/util.ts#Service.open":    {graph.KindMethod, "19", "19"},
		"src/util.ts#Service.name":    {graph.KindField, "20", "20"},
		"src/util.ts#Handlers.onSave": {graph.KindMethod, "24", "24"}, // a type literal's
		"src/util.ts#api":             {graph.KindConst, "28", "34"},
		"src/util.ts#api.load":        {graph.KindMethod, "29", "31"},
		"src/util.ts#api.save":        {graph.KindMethod, "32", "32"},
		"src/util.ts#default":         {graph.KindFunc, "36", "38"}, // export default function ()
		"src/util.ts#Mode":            {graph.KindEnum, "40", "43"},
		"src/App.tsx#Button":          {graph.KindFunc, "4", "7"},
		"scripts/build.mjs#step":      {graph.KindFunc, "5", "7"}, // in no tsconfig
		// Named functions nested in a body (#235), at any depth, under
		// the objects holding them; of two of one name, the first.
		"src/App.tsx#App.local":                   {graph.KindFunc, "10", "10"},
		"src/nested.ts#Panel.inner":               {graph.KindFunc, "7", "9"},
		"src/nested.ts#Panel.onSave":              {graph.KindFunc, "10", "12"}, // useCallback's
		"src/nested.ts#Panel.handle.save":         {graph.KindMethod, "15", "17"},
		"src/nested.ts#Panel.handle.nested.close": {graph.KindMethod, "19", "19"},
		"src/nested.ts#Panel.twin":                {graph.KindFunc, "24", "24"},
	} {
		n := node(t, g, id(decl))
		if n.Kind != want.kind || strconv.Itoa(n.Pos.Line) != want.line || n.Attrs["end_line"] != want.end {
			t.Errorf("%s: %s at %d to %s, want %s at %s to %s", decl, n.Kind, n.Pos.Line, n.Attrs["end_line"], want.kind, want.line, want.end)
		}
	}
	// An object literal's data property, a parameter's type literal
	// members, a local holding a value — useMemo's, an object's — and an
	// anonymous callback are not declarations of the graph.
	for _, decl := range []string{"src/util.ts#api.version", "src/App.tsx#Button.onClick", "src/nested.ts#Panel.value",
		"src/nested.ts#Panel.handle", "src/nested.ts#Panel.handle.nested"} {
		if _, ok := g.Node(id(decl)); ok {
			t.Errorf("%s is a node", decl)
		}
	}
	if got := node(t, g, id("src/Card.svelte")).Attrs["calls_unavailable"]; (got == "") == !svelte.Available() {
		t.Errorf("Card.svelte calls_unavailable %q: it says so when, and only when, the build cannot translate it", got)
	}
	wantEdge(t, g, id("src/util.ts"), graph.KindContains, id("src/util.ts#Store"), graph.Extracted)
	wantEdge(t, g, id("src/util.ts#Store"), graph.KindContains, id("src/util.ts#Store.add"), graph.Extracted)
	wantEdge(t, g, id("src/util.ts#api"), graph.KindContains, id("src/util.ts#api.save"), graph.Extracted)
	wantEdge(t, g, id("src/nested.ts#Panel"), graph.KindContains, id("src/nested.ts#Panel.handle.save"), graph.Extracted)
}

// TestCheckerCalls pins every calls edge of the fixture: through an
// alias re-export (help is helper), a default import, an interface
// method, new, JSX, a call at module level (from the file), a call in a
// function no tsconfig includes, and calls from and to nested functions.
// A call to a parameter's member or into the standard library is no
// edge, and one in an anonymous callback is its enclosing function's.
func TestCheckerCalls(t *testing.T) {
	g, _ := checkerFixture(t)
	var got []string
	for e := range g.Edges() {
		if e.Kind == graph.KindCalls {
			if e.Derivation != graph.Resolved {
				t.Errorf("%s -> %s: %s, want RESOLVED", e.From, e.To, e.Derivation)
			}
			got = append(got, strings.TrimPrefix(string(e.From), "ts://")+" -> "+strings.TrimPrefix(string(e.To), "ts://"))
		}
	}
	slices.Sort(got)
	want := []string{
		"scripts/build.mjs#run -> scripts/build.mjs#step",
		"src/App.tsx -> src/App.tsx#App",
		"src/App.tsx#App -> src/App.tsx#Button",
		"src/App.tsx#App -> src/util.ts#Service.open",
		"src/App.tsx#App -> src/util.ts#Store",
		"src/App.tsx#App -> src/util.ts#Store.add",
		"src/App.tsx#App -> src/util.ts#api.load",
		"src/App.tsx#App -> src/util.ts#default",
		"src/App.tsx#App -> src/util.ts#helper",
		"src/App.tsx#App -> src/App.tsx#App.local",
		"src/App.tsx#App.local -> src/util.ts#twice",
		"src/util.ts#Store.add -> src/util.ts#helper",
		"src/util.ts#api.load -> src/util.ts#helper",
		"src/util.ts#api.save -> src/util.ts#twice",
		"src/util.ts#default -> src/util.ts#Store",
		"src/util.ts#twice -> src/util.ts#helper",
		// Through interfaces (#229): the call to the interface member,
		// and one to each declared implementation — an inherited one,
		// BaseImpl.close, and through a base interface, Closer.
		"src/App.tsx#App -> src/util.ts#FileService.open",
		"src/App.tsx#persist -> src/util.ts#BaseImpl.close",
		"src/App.tsx#persist -> src/util.ts#Closer.close",
		"src/App.tsx#persist -> src/util.ts#DiskStore.save",
		"src/App.tsx#persist -> src/util.ts#Saver.save",
		"src/util.ts#DiskStore.save -> src/util.ts#helper",
		"src/util.ts#FileService.open -> src/util.ts#helper",
		// Nested functions (#235): useMemo's and forEach's callbacks are
		// Panel's, and both twins are one node.
		"src/nested.ts#Panel -> src/nested.ts#Panel.twin",
		"src/nested.ts#Panel -> src/nested.ts#useMemo",
		"src/nested.ts#Panel -> src/util.ts#helper",
		"src/nested.ts#Panel.handle.nested.close -> src/util.ts#helper",
		"src/nested.ts#Panel.handle.save -> src/nested.ts#Panel.onSave",
		"src/nested.ts#Panel.inner -> src/util.ts#helper",
		"src/nested.ts#Panel.onSave -> src/nested.ts#Panel.inner",
		"src/nested.ts#Panel.onSave -> src/nested.ts#useCallback",
		"src/nested.ts#Panel.twin -> src/nested.ts#Panel.inner",
		"src/nested.ts#Panel.twin -> src/util.ts#helper",
	}
	slices.Sort(want)
	if svelte.Available() {
		// Components (#221): a call in a component's script, from the
		// function holding it; one in its markup, a handler's included,
		// from the component; a component used in markup, to it.
		want = append(want,
			"src/Card.svelte -> src/Card.svelte#label",
			"src/Card.svelte#badge -> src/Card.svelte#badge.inner",
			"src/Card.svelte#badge.inner -> src/util.ts#helper",
			"src/Card.svelte -> src/util.ts#helper",
			"src/Card.svelte#label -> src/util.ts#helper",
			"src/Page.svelte -> src/Card.svelte",
		)
		slices.Sort(want)
	}
	if !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCheckerReport pins the pass's counts: the solution tsconfig stands
// for tsconfig.app.json, so only build.mjs is left to the inferred
// project; every call site is an edge, external, unplaced or unresolved.
func TestCheckerReport(t *testing.T) {
	_, rep := checkerFixture(t)
	got := [...]int{rep.Programs, rep.Inferred, rep.ConfigErrors, rep.Checked, rep.Unchecked, rep.Declarations,
		rep.CallSites, rep.Calls, rep.CallsExternal, rep.CallsUnplaced, rep.CallsUnresolved, rep.Implements, rep.Dispatch, rep.Extends}
	// 20 sites: 7 in util.ts, 12 in App.tsx, 1 in build.mjs; 2 external
	// (push, map), 2 unplaced (local, props.onClick), 16 to a node,
	// twice's two calls of helper one edge. Both components unchecked.
	//
	// The interfaces (#229) add 12 declarations — Closer, Saver, BaseImpl,
	// DiskStore, FileService, their 6 members and persist — 4 sites
	// (two helper calls, s.save(), s.close()) and their 4 edges; 3
	// implements edges (FileService Service, DiskStore Saver and Closer)
	// and 3 dispatch edges, which Calls does not count.
	//
	// Nested functions (#235): local is a declaration, and its call
	// placed, an edge. nested.ts adds a file, 8 declarations (Panel, the
	// two declared hooks, inner, onSave, save, close, the twins as one),
	// 13 sites, 1 external (forEach) and 10 edges.
	//
	// Base classes (#249): Oops is a declaration, and DiskStore's extends
	// BaseImpl an edge; Oops extends Error, outside, gives none.
	want := [...]int{2, 1, 0, 5, 2, 43, 37, 30, 3, 1, 0, 3, 3, 1}
	if svelte.Available() {
		// Translated, both are checked: 4 more sites — label's helper,
		// label() and the handler's helper in Card's markup, <Card /> in
		// Page's — 4 more edges, and label a declaration. badge and the
		// inner function it nests (#235) add 2 declarations, 2 sites and
		// 2 edges.
		want = [...]int{2, 1, 0, 7, 0, 46, 43, 36, 3, 1, 0, 3, 3, 1}
	}
	if got != want {
		t.Errorf("Programs, Inferred, ConfigErrors, Checked, Unchecked, Declarations, CallSites, Calls, External, Unplaced, Unresolved, Implements, Dispatch, Extends = %v, want %v", got, want)
	}
}

// TestCheckerComponentDeclarations: with V8, a component's declaration is
// positioned in the component through the translation's source map —
// function label() at 4:12, to line 6 — and contained by it (#221).
func TestCheckerComponentDeclarations(t *testing.T) {
	if !svelte.Available() {
		t.Skip("built without the v8 tag: components are not translated")
	}
	g, _ := checkerFixture(t)
	n := node(t, g, id("src/Card.svelte#label"))
	if n.Kind != graph.KindFunc || n.Pos.File != "src/Card.svelte" || n.Pos.Line != 4 || n.Pos.Col != 12 || n.Attrs["end_line"] != "6" {
		t.Errorf("Card.svelte#label = %s at %s:%d:%d to %s, want func at src/Card.svelte:4:12 to 6", n.Kind, n.Pos.File, n.Pos.Line, n.Pos.Col, n.Attrs["end_line"])
	}
	wantEdge(t, g, id("src/Card.svelte"), graph.KindContains, id("src/Card.svelte#label"), graph.Extracted)
	// A function nested in a script-level one is under it (#235).
	if n := node(t, g, id("src/Card.svelte#badge.inner")); n.Kind != graph.KindFunc || n.Pos.Line != 9 {
		t.Errorf("Card.svelte#badge.inner = %s at line %d, want func at 9", n.Kind, n.Pos.Line)
	}
	wantEdge(t, g, id("src/Card.svelte#badge"), graph.KindContains, id("src/Card.svelte#badge.inner"), graph.Extracted)
}

// TestCheckerImplements pins #229's edges: implements from a class to
// each interface its clause names and the interfaces those extend, and
// the dispatch calls marked via=interface, the direct ones not. A class
// implements the class it extends too (#249), and none outside the
// sources (Oops extends Error).
func TestCheckerImplements(t *testing.T) {
	g, _ := checkerFixture(t)
	wantEdge(t, g, id("src/util.ts#DiskStore"), graph.KindImplements, id("src/util.ts#BaseImpl"), graph.Resolved)
	for e := range g.Edges() {
		if e.From == id("src/util.ts#Oops") && e.Kind == graph.KindImplements {
			t.Errorf("Oops implements %s: Error is outside the sources", e.To)
		}
	}
	wantEdge(t, g, id("src/util.ts#FileService"), graph.KindImplements, id("src/util.ts#Service"), graph.Resolved)
	wantEdge(t, g, id("src/util.ts#DiskStore"), graph.KindImplements, id("src/util.ts#Saver"), graph.Resolved)
	wantEdge(t, g, id("src/util.ts#DiskStore"), graph.KindImplements, id("src/util.ts#Closer"), graph.Resolved)
	if _, ok := edge(g, id("src/util.ts#BaseImpl"), graph.KindImplements, id("src/util.ts#Closer")); ok {
		t.Error("BaseImpl implements Closer: it has its member, but declares no implements — structural is not the rule")
	}
	for from, to := range map[string]string{
		"src/App.tsx#App":     "src/util.ts#FileService.open",
		"src/App.tsx#persist": "src/util.ts#BaseImpl.close",
	} {
		e, ok := edge(g, id(from), graph.KindCalls, id(to))
		if !ok || e.Attrs["via"] != "interface" {
			t.Errorf("%s -> %s: %+v, %v; want a calls edge via=interface", from, to, e, ok)
		}
	}
	if e, _ := edge(g, id("src/App.tsx#persist"), graph.KindCalls, id("src/util.ts#Saver.save")); e.Attrs["via"] != "" {
		t.Errorf("the call to the interface member itself is marked %q", e.Attrs["via"])
	}
}

// TestSvelteKitAppNeverSynced pins #232 (the owner's decision): an app
// whose tsconfig extends a missing .svelte-kit/tsconfig.json is checked
// under the config SvelteKit would generate — $lib and kit.alias resolve,
// so both calls are edges — the synthesis is a skipped input saying so,
// and nothing is written to the tree.
func TestSvelteKitAppNeverSynced(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "sveltekit"))
	if err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	wantEdge(t, g, id("app/src/routes/page.ts#load"), graph.KindCalls, id("app/src/lib/util.ts#helper"), graph.Resolved)
	wantEdge(t, g, id("app/src/routes/page.ts#load"), graph.KindCalls, id("gen/api.ts#generated"), graph.Resolved)
	if rep.Synthesized != 1 || rep.ConfigErrors != 0 {
		t.Errorf("Synthesized %d, ConfigErrors %d; want 1 and 0", rep.Synthesized, rep.ConfigErrors)
	}
	said := false
	for _, s := range rep.Skipped {
		said = said || s.File == "app/.svelte-kit/tsconfig.json" && strings.Contains(s.Reason, "synthesized")
	}
	if !said {
		t.Errorf("the synthesis is not recorded: %+v", rep.Skipped)
	}
	if _, err := os.Stat(filepath.Join(dir, "app", ".svelte-kit")); !os.IsNotExist(err) {
		t.Errorf("the synthesis wrote to the tree: %v", err)
	}
}
