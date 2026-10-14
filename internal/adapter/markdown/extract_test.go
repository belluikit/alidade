// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
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

// extract runs the Markdown layer alone over the fixture: links resolve
// among documents, mentions are collected and not yet linked.
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

func id(s string) graph.ID { return graph.ID("md://" + s) }

func edges(g *graph.Graph, from graph.ID, kind graph.EdgeKind) []graph.Edge {
	var out []graph.Edge
	for _, e := range g.Out(from) {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
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

func TestReport(t *testing.T) {
	_, rep := extract(t)
	if rep.Files != 3 || rep.Sections != 10 || rep.Fences != 2 || rep.Links != 10 || rep.Anchors != 1 || rep.AnchorsUnresolved != 3 || rep.External != 3 ||
		rep.Changelogs != 1 || rep.Testdata != 1 || len(rep.Mentions) != 10 {
		t.Errorf("report = %+v", rep)
	}
}

func TestDocumentsAndSections(t *testing.T) {
	g, _ := extract(t)
	doc := wantNode(t, g, id("README.md"), graph.KindDocument)
	if doc.Attrs["title"] != "Fixture" || doc.Attrs["front_matter"] != "tags title" {
		t.Errorf("README attrs = %v", doc.Attrs)
	}
	if d := wantNode(t, g, id("docs/adr/001-shape.md"), graph.KindDocument); d.Attrs["title"] != "ADR 001: Shape" || d.Attrs["front_matter"] != "" {
		t.Errorf("ADR attrs = %v (title from the H1 when there is no front matter)", d.Attrs)
	}
}

func TestSectionsAndContains(t *testing.T) {
	g, _ := extract(t)
	// Heading paths per ADR 002 rule 8, positions after the front matter.
	top := wantNode(t, g, id("README.md#fixture"), graph.KindSection)
	if top.Pos.Line != 6 || top.Attrs["level"] != "1" || top.Name != "Fixture" {
		t.Errorf("top section = %+v", top)
	}
	wantNode(t, g, id("docs/design/overview.md#overview.details.deep"), graph.KindSection)
	for _, e := range []struct{ from, to string }{
		{"README.md", "README.md#fixture"},
		{"README.md#fixture", "README.md#fixture.symbols"},
		{"docs/design/overview.md#overview.details", "docs/design/overview.md#overview.details.deep"},
	} {
		found := false
		for _, c := range edges(g, id(e.from), graph.KindContains) {
			if c.To == id(e.to) && c.Derivation == graph.Extracted {
				found = true
			}
		}
		if !found {
			t.Errorf("missing contains %s -> %s", e.from, e.to)
		}
	}
}

// TestHeadingCollisionPolicy: rule 8's residual — two identical heading
// paths in one document — gets a numeric suffix on the second.
func TestHeadingCollisionPolicy(t *testing.T) {
	g, _ := extract(t)
	wantNode(t, g, id("README.md#fixture.symbols"), graph.KindSection)
	wantNode(t, g, id("README.md#fixture.symbols-2"), graph.KindSection)
	wantNode(t, g, id("docs/adr/001-shape.md#adr-001-shape.decision"), graph.KindSection)
	wantNode(t, g, id("docs/adr/001-shape.md#adr-001-shape.decision-2"), graph.KindSection)
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts: %+v", c)
	}
}

func TestFences(t *testing.T) {
	g, _ := extract(t)
	f := wantNode(t, g, id("README.md#fixture.symbols-2.@fence"), graph.KindBlock)
	if f.Attrs["lang"] != "go" || f.Attrs["lines"] != "1" || f.Pos.Line != 26 {
		t.Errorf("go fence = %+v", f)
	}
	if f2 := wantNode(t, g, id("README.md#fixture.symbols-2.@fence2"), graph.KindBlock); f2.Attrs["lang"] != "" {
		t.Errorf("untagged fence has lang %q", f2.Attrs["lang"])
	}
	if got := edges(g, id("README.md#fixture.symbols-2"), graph.KindContains); len(got) != 2 {
		t.Errorf("section contains %d, want its 2 fences", len(got))
	}
}

func TestLinks(t *testing.T) {
	g, _ := extract(t)
	top := id("README.md#fixture")
	got := map[string]graph.Edge{}
	for _, e := range edges(g, top, graph.KindReferences) {
		got[string(e.To)] = e
	}
	if e, ok := got["md://docs/design/overview.md"]; !ok || e.Derivation != graph.Extracted || e.Attrs["link"] != "docs/design/overview.md" {
		t.Errorf("link to overview = %+v", e)
	}
	// "#fixture" resolves to this document's section. "#symbols" here and
	// "001-shape.md#decision" name headings their documents repeat, whose
	// anchors GitHub numbers by position: each drops with its reason
	// (ADR 002, rule 8's consequence for #5), never resolving to one of
	// them and never landing on the document instead (#98).
	if e, ok := got["md://README.md#fixture"]; !ok || e.Attrs["link"] != "#fixture" {
		t.Errorf("same-document anchor = %+v", e)
	}
	for to := range got {
		if strings.Contains(to, "symbols") || strings.Contains(to, "001-shape") {
			t.Errorf("an ambiguous anchor resolved to %s", to)
		}
	}
	wantDropped(t, g, top, "md://README.md#@ambiguous.symbols")
	wantDropped(t, g, top, "md://docs/adr/001-shape.md#@ambiguous.decision")
}

// wantDropped finds a dropped references edge from one node to another.
func wantDropped(t *testing.T, g *graph.Graph, from graph.ID, to graph.ID) {
	t.Helper()
	for _, d := range g.Dropped() {
		if d.Edge.From == from && d.Edge.To == to && d.Edge.Kind == graph.KindReferences {
			return
		}
	}
	t.Errorf("no dropped edge %s -> %s", from, to)
}

func TestLinksOutAndMissing(t *testing.T) {
	g, _ := extract(t)
	// A relative link out of the fixture and a link to a missing file:
	// the second is emitted and drops with its reason.
	over := id("docs/design/overview.md#overview")
	if got := edges(g, over, graph.KindReferences); len(got) != 0 {
		t.Errorf("overview links = %+v; want none: both drop", got)
	}
	wantDropped(t, g, over, "md://README.md#@ambiguous.symbols")
	dropped := false
	for _, d := range g.Dropped() {
		if d.Edge.From == over && d.Edge.To == id("docs/design/missing.md") {
			dropped = true
		}
	}
	if !dropped {
		t.Error("link to a missing file was not a dropped edge")
	}
}

// TestLinksToCode is #68's done-when on the layer's side: a link to a Go
// file or a directory is an edge to the package there, minted from the
// nearest go.mod, whether or not the path exists — so a link to a path
// that is gone drops with unknown_target and the raw link, exactly as a
// link to a missing document does — while a directory that exists and
// holds no Go is neither an edge nor a drop. The Markdown layer alone has
// no Go nodes, so every one of these drops here; the build test resolves
// the ones that exist.
func TestLinksToCode(t *testing.T) {
	g, _ := extract(t)
	top := id("README.md#fixture")
	got := map[string]string{}
	for _, d := range g.Dropped() {
		if d.Edge.From == top && strings.HasPrefix(string(d.Edge.To), "go://") {
			got[string(d.Edge.To)] = d.Edge.Attrs["link"] + " " + string(d.Reason)
		}
	}
	want := map[string]string{
		"go://example.com/fixture/other":         "other/config.go unknown_target",
		"go://example.com/fixture/synth":         "synth/ unknown_target",
		"go://example.com/fixture/other/gone.go": "other/gone.go unknown_target",
		"go://example.com/fixture/flow":          "flow/ unknown_target",
	}
	for to, w := range want {
		if got[to] != w {
			t.Errorf("link to %s = %q, want %q", to, got[to], w)
		}
	}
	for to := range got {
		if strings.Contains(to, "docs") || strings.Contains(to, "reference") {
			t.Errorf("a directory with no Go, a route under it, or a site path became %s", to)
		}
	}
	for _, e := range edges(g, top, graph.KindReferences) {
		if strings.HasPrefix(string(e.To), "go://") {
			t.Errorf("Go target %s survived the Markdown-only build", e.To)
		}
	}
}

// TestMentionsAreCodeSpansOnly: what Link will see is every code span,
// and nothing from prose.
func TestMentionsAreCodeSpansOnly(t *testing.T) {
	_, rep := extract(t)
	var texts []string
	for _, m := range rep.Mentions {
		texts = append(texts, m.Text)
	}
	want := "governance.pxf api/v1/evaluations.proto synth.Report Render EvaluationsService.Create Config INFERRED Missing synth.Report app.users"
	if got := strings.Join(texts, " "); got != want {
		t.Errorf("mentions = %q\n    want %q", got, want)
	}
	for _, m := range rep.Mentions {
		if m.Text == "synth.Report" && m.Section != id("README.md#fixture.symbols") && m.Section != id("docs/design/overview.md#overview.details.deep") {
			t.Errorf("synth.Report attributed to %s", m.Section)
		}
	}
}

func TestChangelogsOptional(t *testing.T) {
	dir := fixtureDir(t)
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, Options{Changelogs: true, Testdata: true})
	if err != nil || rep.Files != 5 || rep.Changelogs != 0 || rep.Testdata != 0 {
		t.Errorf("with everything: rep=%+v err=%v", rep, err)
	}
	if _, ok := b.Build().Node(id("changelog.d/123.added.md")); !ok {
		t.Error("fragment absent with Changelogs: true")
	}
}

func TestFrontMatter(t *testing.T) {
	body, keys, title, lines := frontMatter([]byte("---\ntitle: T\nz: 1\na: [x]\n---\n# H\n"))
	if string(body) != "# H\n" || strings.Join(keys, ",") != "a,title,z" || title != "T" || lines != 5 {
		t.Errorf("frontMatter = %q %v %q %d", body, keys, title, lines)
	}
	if b, k, _, l := frontMatter([]byte("---\nnot: [closed\n")); string(b) != "---\nnot: [closed\n" || k != nil || l != 0 {
		t.Error("an unterminated block is not front matter")
	}
}

func TestDeterministic(t *testing.T) {
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
