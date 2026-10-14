// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// tree writes files under a temporary root and extracts them.
func tree(t *testing.T, files map[string]string) (*graph.Graph, *Report) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return b.Build(), rep
}

func wantRef(t *testing.T, g *graph.Graph, from, to string) {
	t.Helper()
	for _, e := range g.Out(id(from)) {
		if e.Kind == graph.KindReferences && e.To == id(to) {
			return
		}
	}
	t.Errorf("no references edge %s -> %s", from, to)
}

// TestGitHubAnchors pins #98: a "#anchor" link is resolved by the anchor
// GitHub renders, which keeps "_", drops "." and other punctuation, and
// turns each space into "-" — not by the node-ID slug, under which these
// links all vanished. An anchor naming no heading drops with its reason.
func TestGitHubAnchors(t *testing.T) {
	g, rep := tree(t, map[string]string{"CHANGELOG.md": `# Changelog

See [the release](#version-0210--2026-09-07), [the thing](#snake_case-thing),
[shouting](#Version-0210--2026-09-07) and [nothing](#no-such-heading).

## Version 0.21.0 — 2026-09-07

## snake_case thing
`})
	wantRef(t, g, "CHANGELOG.md#changelog", "CHANGELOG.md#changelog.version-0-21-0-2026-09-07")
	wantRef(t, g, "CHANGELOG.md#changelog", "CHANGELOG.md#changelog.snake-case-thing")
	wantDropped(t, g, id("CHANGELOG.md#changelog"), "md://CHANGELOG.md#@anchor.no-such-heading")
	if rep.Anchors != 3 || rep.AnchorsUnresolved != 1 {
		t.Errorf("Anchors = %d, AnchorsUnresolved = %d; want 3, 1", rep.Anchors, rep.AnchorsUnresolved)
	}
}

// TestCrossDocumentAnchors pins #98 and ADR 002's consequence of rule 8:
// "other.md#heading" is that document's section when the heading's anchor
// is its own, and a missing one drops with its reason.
func TestCrossDocumentAnchors(t *testing.T) {
	g, _ := tree(t, map[string]string{
		"README.md":     "# Readme\n\nSee [why](docs/b.md#section-one) and [gone](docs/b.md#section-two).\n",
		"docs/b.md":     "# B\n\n## Section One\n",
		"docs/plain.md": "# Plain\n",
	})
	wantRef(t, g, "README.md#readme", "docs/b.md#b.section-one")
	wantDropped(t, g, id("README.md#readme"), "md://docs/b.md#@anchor.section-two")
}

// TestLinkTargetsAreNormalised pins #98: a query selects a view of a file,
// "%20" is the path's space, and a path out of the repository is counted
// as external, as a URL is; files of the frontend and goasm layers are
// link targets like any file node; a file of no layer is counted.
func TestLinkTargetsAreNormalised(t *testing.T) {
	g, rep := tree(t, map[string]string{
		"README.md": "# Readme\n\n[a](other.md?plain=1) [b](sub%20dir/c.md) [c](../../outside.md) " +
			"[d](web/app.ts) [e](jit/arm64.goasm) [f](diagram.png)\n",
		"other.md":        "# Other\n",
		"sub dir/c.md":    "# C\n",
		"web/app.ts":      "export const x = 1;\n",
		"jit/arm64.goasm": "",
	})
	wantRef(t, g, "README.md#readme", "other.md")
	// The space reaches the path: the file is skipped as unnameable (#92),
	// and the link is recorded against it rather than lost.
	if rep.External != 1 || rep.Unkeyed != 1 {
		t.Errorf("External = %d, Unkeyed = %d; want 1, 1", rep.External, rep.Unkeyed)
	}
	for _, to := range []graph.ID{"ts://web/app.ts", "goasm://jit/arm64.goasm"} {
		found := false
		for _, e := range g.Out(id("README.md#readme")) {
			found = found || e.To == to
		}
		for _, d := range g.Dropped() {
			found = found || d.Edge.To == to
		}
		if !found {
			t.Errorf("no link edge to %s", to)
		}
	}
}

// TestSuffixesSkipRealHeadings pins #98: the collision suffix never takes
// an ID a real heading of the document has.
func TestSuffixesSkipRealHeadings(t *testing.T) {
	g, _ := tree(t, map[string]string{"log.md": "# Log\n\n## B\n\n## B\n\n## B 2\n"})
	n := 0
	for node := range g.Nodes() {
		if node.Kind == graph.KindSection {
			n++
		}
	}
	if n != 4 {
		t.Errorf("%d sections, want 4: Log, B, the second B, B 2", n)
	}
	if s, ok := g.Node(id("log.md#log.b-2")); !ok || s.Name != "B 2" {
		t.Errorf("log.b-2 = %+v, %v; want the real heading B 2", s, ok)
	}
	if _, ok := g.Node(id("log.md#log.b-3")); !ok {
		t.Error("the second B is not log.b-3")
	}
	for _, c := range g.Conflicts() {
		t.Errorf("conflict %+v", c)
	}
}

// TestFencesNumberWithinTheirSection pins #98 and ADR 002 rule 1: adding a
// fenced block to one section renumbers no other section's blocks.
func TestFencesNumberWithinTheirSection(t *testing.T) {
	g, _ := tree(t, map[string]string{"d.md": "# T\n\n## A\n\n```\na\n```\n\n```\nb\n```\n\n## Other\n\n```\nc\n```\n"})
	for _, want := range []string{"d.md#t.a.@fence", "d.md#t.a.@fence2", "d.md#t.other.@fence"} {
		if _, ok := g.Node(id(want)); !ok {
			t.Errorf("missing %s", want)
		}
	}
}

// TestFrontMatterVariants pins #98: front matter with Windows line endings,
// or whose closing line ends the file, is front matter, not a section.
func TestFrontMatterVariants(t *testing.T) {
	g, _ := tree(t, map[string]string{
		"crlf.md": "---\r\ntitle: x\r\n---\r\n# Real\r\n",
		"eof.md":  "---\ntitle: y\n---",
	})
	if _, ok := g.Node(id("crlf.md#title-x")); ok {
		t.Error("CRLF front matter became a section")
	}
	if _, ok := g.Node(id("crlf.md#real")); !ok {
		t.Error("the heading after CRLF front matter is missing")
	}
	if n, _ := g.Node(id("crlf.md")); n.Attrs["title"] != "x" {
		t.Errorf("crlf.md title = %q", n.Attrs["title"])
	}
	if _, ok := g.Node(id("eof.md#title-y")); ok {
		t.Error("front matter closing at end of file became a section")
	}
}
