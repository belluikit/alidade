// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

var (
	once    sync.Once
	fixture *graph.Graph
	report  *Report
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func extract(t *testing.T) (*graph.Graph, *Report) {
	t.Helper()
	once.Do(func() {
		dir := fixtureDir(t)
		b := graph.NewBuilder(dir)
		rep, err := Extract(b, dir, DefaultOptions())
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		fixture, report = b.Build(), rep
	})
	return fixture, report
}

func id(s string) graph.ID { return graph.ID("pxf://" + s) }

func hasEdge(g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) (graph.Edge, bool) {
	for _, e := range g.Out(from) {
		if e.Kind == kind && e.To == to {
			return e, true
		}
	}
	return graph.Edge{}, false
}

func wantEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID, d graph.Derivation) graph.Edge {
	t.Helper()
	e, ok := hasEdge(g, from, kind, to)
	if !ok {
		t.Errorf("missing edge %s -%s-> %s", from, kind, to)
	} else if e.Derivation != d {
		t.Errorf("edge %s -%s-> %s: derivation %s, want %s", from, kind, to, e.Derivation, d)
	}
	return e
}

func wantNode(t *testing.T, g *graph.Graph, id graph.ID, kind graph.Kind) graph.Node {
	t.Helper()
	n, ok := g.Node(id)
	if !ok {
		t.Errorf("missing node %s", id)
	} else if n.Kind != kind {
		t.Errorf("node %s: kind %s, want %s", id, n.Kind, kind)
	}
	return n
}

// dropped returns the targets of dropped edges leaving from, by kind.
func dropped(g *graph.Graph, from graph.ID, kind graph.EdgeKind) []string {
	var out []string
	for _, d := range g.Dropped() {
		if d.Edge.From == from && d.Edge.Kind == kind {
			out = append(out, string(d.Edge.To))
		}
	}
	return out
}

func TestReport(t *testing.T) {
	_, rep := extract(t)
	want := Report{Files: 8, Failed: 1, Typed: 6, Blocks: 25, Fields: 56, Datasets: 1, Refs: 2, ContextRefs: 4,
		UnresolvedRefs: 1, Chains: 1, Layers: 7, Testdata: 1}
	got := *rep
	got.Elapsed = 0
	if !reflect.DeepEqual(got, want) {
		t.Errorf("report = %+v\n    want %+v", got, want)
	}
}

func TestFilesAndTypes(t *testing.T) {
	g, _ := extract(t)
	f := wantNode(t, g, id("config/app.pxf"), graph.KindPXFFile)
	if f.Attrs["type"] != "example.v1.Config" || f.Pos.File != "config/app.pxf" {
		t.Errorf("app.pxf = %+v", f)
	}
	// The @type edge is emitted whether or not the schema is in the
	// corpus; here it is not, so it drops with its reason.
	if got := dropped(g, id("config/app.pxf"), graph.KindReferences); len(got) != 1 || got[0] != "proto://example.v1.Config" {
		t.Errorf("@type edge = %v", got)
	}
	b := wantNode(t, g, id("broken.pxf"), graph.KindPXFFile)
	if !strings.Contains(b.Attrs["error"], "newline in string literal") || len(g.Out(b.ID)) != 0 {
		t.Errorf("broken.pxf = %+v with %d edges", b, len(g.Out(b.ID)))
	}
	if _, ok := g.Node(id("testdata/copy.pxf")); ok {
		t.Error("testdata file present by default")
	}
}

func TestBlocksFieldsAndPaths(t *testing.T) {
	g, _ := extract(t)
	wantEdge(t, g, id("config/app.pxf"), graph.KindContains, id("config/app.pxf#server"), graph.Extracted)
	wantEdge(t, g, id("config/app.pxf#server"), graph.KindContains, id("config/app.pxf#server.port"), graph.Extracted)
	for path, kind := range map[string]string{
		"server.host": "string", "server.port": "int", "server.ratio": "float", "server.debug": "bool",
		"server.mode": "ident", "server.started": "timestamp", "server.timeout": "duration",
		"server.raw": "bytes", "server.nothing": "null", "server.tags": "list",
	} {
		n := wantNode(t, g, id("config/app.pxf#"+path), graph.KindField)
		if n.Attrs["kind"] != kind {
			t.Errorf("%s kind = %q, want %q", path, n.Attrs["kind"], kind)
		}
	}
	if n, _ := g.Node(id("config/app.pxf#server.tags")); n.Attrs["len"] != "2" {
		t.Errorf("tags len = %q", n.Attrs["len"])
	}
	n := wantNode(t, g, id("config/app.pxf#server.port"), graph.KindField)
	if n.Name != "port" || n.Pos.Line != 8 || n.Pos.Col != 3 {
		t.Errorf("port node = %+v", n)
	}
	// List elements: by id, by name, by index.
	wantNode(t, g, id("config/app.pxf#regions.us-east-1"), graph.KindBlock)
	wantNode(t, g, id("config/app.pxf#regions.eu-west"), graph.KindBlock)
	wantNode(t, g, id("config/app.pxf#regions.2.weight"), graph.KindField)
	wantEdge(t, g, id("config/app.pxf#regions"), graph.KindContains, id("config/app.pxf#regions.eu-west"), graph.Extracted)
	// Map entries and a quoted keyed block.
	wantNode(t, g, id("config/app.pxf#sources.default.kind"), graph.KindField)
	wantNode(t, g, id("config/app.pxf#us-east-1.capacity"), graph.KindField)
}

// TestNoValueReachesTheGraph is #4's fourth done-when: not the password,
// not any value. A node says what kind of value it holds and nothing more.
func TestNoValueReachesTheGraph(t *testing.T) {
	g, _ := extract(t)
	for _, secret := range []string{"hunter2", "s3cret", "localhost", "8080", "FAST", "AAEC"} {
		for n := range g.Nodes() {
			for k, v := range n.Attrs {
				if strings.Contains(v, secret) {
					t.Errorf("node %s attr %s carries value %q", n.ID, k, secret)
				}
			}
			if strings.Contains(n.Name, secret) {
				t.Errorf("node %s name carries value %q", n.ID, secret)
			}
		}
		for e := range g.Edges() {
			for k, v := range e.Attrs {
				if strings.Contains(v, secret) {
					t.Errorf("edge %s -> %s attr %s carries value %q", e.From, e.To, k, secret)
				}
			}
		}
	}
}

func TestFieldReferences(t *testing.T) {
	g, _ := extract(t)
	url := id("config/app.pxf#server.url")
	e := wantEdge(t, g, url, graph.KindReferences, id("config/app.pxf#server.host"), graph.Extracted)
	if e.Attrs["ref"] != "server.host" {
		t.Errorf("ref attrs = %v", e.Attrs)
	}
	wantEdge(t, g, url, graph.KindReferences, id("config/app.pxf#server.port"), graph.Extracted)
	if n, _ := g.Node(url); n.Attrs["context_refs"] != "env" {
		t.Errorf("url context_refs = %q, want env", n.Attrs["context_refs"])
	}
	if got := dropped(g, id("config/app.pxf#server.fallback"), graph.KindReferences); len(got) != 1 || got[0] != "pxf://config/app.pxf#missing.path" {
		t.Errorf("unresolved ref = %v, want a dropped edge to where it would be", got)
	}
}

func TestDirectivesAndDatasets(t *testing.T) {
	g, _ := extract(t)
	h := wantNode(t, g, id("layers/base.pxf#@header"), graph.KindBlock)
	if h.Attrs["directive"] != "header" || h.Attrs["prefixes"] != "chameleon.v1.LayerHeader" {
		t.Errorf("@header = %+v", h)
	}
	if got := dropped(g, h.ID, graph.KindReferences); len(got) != 1 || got[0] != "proto://chameleon.v1.LayerHeader" {
		t.Errorf("@header type edge = %v", got)
	}
	ds := wantNode(t, g, id("data/rows.pxf#@dataset"), graph.KindBlock)
	if ds.Attrs["rows"] != "2" || ds.Attrs["columns"] != "name count" {
		t.Errorf("@dataset = %+v", ds)
	}
	if got := dropped(g, ds.ID, graph.KindReferences); len(got) != 1 || got[0] != "proto://example.v1.Row" {
		t.Errorf("@dataset type edge = %v", got)
	}
	if len(g.Out(ds.ID)) != 0 {
		t.Error("dataset rows were expanded")
	}
}

// TestChainLayers: a chain specification points at the layer files it
// loads, expanded the way chameleon expands it. ONCE loads the leaf
// environment's file; PER_ENV every ancestor's, root first; a context
// variable or a non-file source produces nothing.
func TestChainLayers(t *testing.T) {
	g, _ := extract(t)
	chain := id("chain.pxf")
	e := wantEdge(t, g, chain, graph.KindReferences, id("layers/base.pxf"), graph.Extracted)
	if e.Attrs["layer"] != "base" || e.Attrs["envs"] != "local prod staging" {
		t.Errorf("base layer attrs = %v", e.Attrs)
	}
	e = wantEdge(t, g, chain, graph.KindReferences, id("layers/env/prod.pxf"), graph.Declared)
	if e.Attrs["envs"] != "local prod staging" { // PER_ENV: every descendant loads its root
		t.Errorf("prod layer envs = %q", e.Attrs["envs"])
	}
	e = wantEdge(t, g, chain, graph.KindReferences, id("layers/env/staging.pxf"), graph.Declared)
	if e.Attrs["envs"] != "local staging" {
		t.Errorf("staging layer envs = %q", e.Attrs["envs"])
	}
	e = wantEdge(t, g, chain, graph.KindReferences, id("layers/secrets/prod.pxf"), graph.Declared)
	if e.Attrs["envs"] != "prod" { // ONCE: the leaf only
		t.Errorf("secrets/prod envs = %q", e.Attrs["envs"])
	}
	// Files the chain names that are not in the tree drop with reason;
	// the ctx-variable and env-source entries are not files at all.
	got := dropped(g, chain, graph.KindReferences)
	want := []string{"proto://chameleon.v1.ChainSpec", "pxf://layers/env/local.pxf", "pxf://layers/secrets/local.pxf", "pxf://layers/secrets/staging.pxf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dropped from chain = %v, want %v", got, want)
	}
	for _, e := range g.Out(chain) {
		if strings.Contains(string(e.To), "region") || strings.Contains(string(e.To), "(env)") {
			t.Errorf("unexpected layer edge %+v", e)
		}
	}
}

func TestKeyable(t *testing.T) {
	if keyable("a.b") || keyable("a/b") || keyable("") || !keyable("us-east-1") {
		t.Error("keyable is wrong")
	}
}

// TestChainErrors: a chain chameleon rejects keeps its node, says why,
// and loads nothing; the report counts it.
func TestChainErrors(t *testing.T) {
	dir := t.TempDir()
	bad := "@type chameleon.v1.ChainSpec\n\nversion = \"1\"\nenvironments = [ { name = \"a\" extends = \"b\" } { name = \"b\" extends = \"a\" } ]\nlayers = [ { id = \"x\" path = \"x.pxf\" } ]\n"
	if err := os.WriteFile(filepath.Join(dir, "chain.pxf"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	n, ok := g.Node(id("chain.pxf"))
	if !ok || n.Attrs["chain_error"] == "" || rep.ChainErrors == 0 || rep.Layers != 0 {
		t.Errorf("cyclic chain: node=%+v report=%+v", n, rep)
	}
}

func TestNoConflictsAndDeterministic(t *testing.T) {
	g, _ := extract(t)
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts: %+v", c)
	}
	dir := fixtureDir(t)
	var outs [2]bytes.Buffer
	for i := range outs {
		b := graph.NewBuilder(dir)
		if _, err := Extract(b, dir, DefaultOptions()); err != nil {
			t.Fatal(err)
		}
		if err := b.Build().WritePXF(&outs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outs[0].Bytes(), outs[1].Bytes()) {
		t.Error("two extractions differ")
	}
}

func TestTestdataOption(t *testing.T) {
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, Options{Testdata: true})
	if err != nil || rep.Files != 9 || rep.Testdata != 0 {
		t.Errorf("with testdata: rep=%+v err=%v", rep, err)
	}
	if _, ok := b.Build().Node(id("testdata/copy.pxf")); !ok {
		t.Error("testdata file absent with Testdata: true")
	}
	empty := t.TempDir()
	if rep, err := Extract(graph.NewBuilder(empty), empty, DefaultOptions()); err != nil || rep.Files != 0 {
		t.Errorf("empty repository: rep=%+v err=%v", rep, err)
	}
}
