// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// A candidate is one item a sample can draw: a documents edge, or a code
// span, of one repository.
type candidate struct {
	repo, from, to, mention string
}

// TestDrawSamples draws the Markdown samples' items (#2) from the
// repositories ALIDADE_DRAW lists, and writes them unlabelled to the
// directory ALIDADE_DRAW_OUT: a reader then labels each one. Nothing is
// picked: each sample takes its size, allotted to the repositories in
// proportion to their counts, in order of a SHA-256.
//
//	edges     58 documents edges, by "repo|from|to"
//	bareword  40 documents edges whose mention is a bare lowercase word
//	          and whose target is a nested declaration, by "repo|from|to"
//	spans     120 code spans outside fenced blocks, by "repo|section|span"
func TestDrawSamples(t *testing.T) {
	roots := strings.Fields(os.Getenv("ALIDADE_DRAW"))
	out := os.Getenv("ALIDADE_DRAW_OUT")
	if len(roots) == 0 || out == "" {
		t.Skip("ALIDADE_DRAW and ALIDADE_DRAW_OUT are unset")
	}
	commits := map[string]string{}
	var edges, bare, spans []candidate
	for _, root := range roots {
		repo := filepath.Base(root)
		head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatalf("%s: git rev-parse: %v", root, err)
		}
		commits[repo] = strings.TrimSpace(string(head))
		g, rep, err := build.Build(root, build.DefaultOptions())
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		e, b, s := candidates(g, rep, repo)
		t.Logf("%s: %d documents edges, %d of a bare word naming a nested declaration, %d code spans", repo, len(e), len(b), len(s))
		edges, bare, spans = append(edges, e...), append(bare, b...), append(spans, s...)
	}
	for _, d := range []struct {
		file string
		n    int
		all  []candidate
	}{{"edges.pxf", 58, edges}, {"bareword.pxf", 40, bare}, {"spans.pxf", 120, spans}} {
		drawn := draw(d.all, d.n)
		if err := os.WriteFile(filepath.Join(out, d.file), unlabelled(drawn, commits, d.file == "spans.pxf"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d drawn of %d", d.file, len(drawn), len(d.all))
	}
}

var bareWord = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// nested are the kinds a bare word names inside another declaration.
var nested = map[graph.Kind]bool{graph.KindField: true, graph.KindRPC: true, graph.KindMethod: true, graph.KindBlock: true, graph.KindEnumValue: true, graph.KindColumn: true}

func candidates(g *graph.Graph, rep *build.Report, repo string) (edges, bare, spans []candidate) {
	for e := range g.Edges() {
		if e.Kind != graph.KindDocuments {
			continue
		}
		c := candidate{repo, string(e.From), string(e.To), e.Attrs["mention"]}
		edges = append(edges, c)
		if n, ok := g.Node(e.To); ok && nested[n.Kind] && bareWord.MatchString(c.mention) {
			bare = append(bare, c)
		}
	}
	if rep.Markdown == nil {
		return edges, bare, nil
	}
	seen := map[candidate]bool{}
	for _, m := range rep.Markdown.Mentions {
		c := candidate{repo: repo, from: string(m.Section), mention: m.Text}
		if m.Fence || seen[c] {
			continue
		}
		seen[c] = true
		spans = append(spans, c)
	}
	return edges, bare, spans
}

// draw takes n of all: each repository its share by largest remainder,
// and within a repository the items first in SHA-256 order.
func draw(all []candidate, n int) []candidate {
	byRepo := map[string][]candidate{}
	for _, c := range all {
		byRepo[c.repo] = append(byRepo[c.repo], c)
	}
	repos := slices.Sorted(func(yield func(string) bool) {
		for r := range byRepo {
			if !yield(r) {
				return
			}
		}
	})
	if n > len(all) {
		n = len(all)
	}
	quota := map[string]int{}
	type rem struct {
		repo string
		frac float64
	}
	var rems []rem
	given := 0
	for _, r := range repos {
		share := float64(n) * float64(len(byRepo[r])) / float64(len(all))
		quota[r] = int(share)
		given += quota[r]
		rems = append(rems, rem{r, share - float64(quota[r])})
	}
	sort.SliceStable(rems, func(i, j int) bool { return rems[i].frac > rems[j].frac })
	for i := 0; given < n; i, given = i+1, given+1 {
		quota[rems[i%len(rems)].repo]++
	}
	var out []candidate
	for _, r := range repos {
		cs := byRepo[r]
		sort.Slice(cs, func(i, j int) bool { return order(cs[i]) < order(cs[j]) })
		out = append(out, cs[:min(quota[r], len(cs))]...)
	}
	return out
}

func order(c candidate) string {
	key := c.repo + "|" + c.from + "|" + c.to
	if c.to == "" {
		key = c.repo + "|" + c.from + "|" + c.mention
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

// unlabelled writes drawn items as the sample files hold them, each with
// label = TODO for the reader.
func unlabelled(drawn []candidate, commits map[string]string, spans bool) []byte {
	var b strings.Builder
	repo := ""
	for _, c := range drawn {
		if c.repo != repo {
			if repo != "" {
				b.WriteString("}\n\n")
			}
			repo = c.repo
			fmt.Fprintf(&b, "repo {\n  name = %q\n  commit = %q\n", repo, commits[repo])
		}
		if spans {
			file := strings.TrimPrefix(c.from, "md://")
			file, _, _ = strings.Cut(file, "#")
			fmt.Fprintf(&b, "  span {\n    file = %q\n    section = %q\n    text = %q\n    label = TODO\n  }\n", file, c.from, c.mention)
			continue
		}
		fmt.Fprintf(&b, "  edge {\n    from = %q\n    to = %q\n    mention = %q\n    label = TODO\n  }\n", c.from, c.to, c.mention)
	}
	if repo != "" {
		b.WriteString("}\n")
	}
	return []byte(b.String())
}

// draw gives each repository its share and takes each one's items in
// hash order, whatever order they arrive in.
func TestDraw(t *testing.T) {
	var all []candidate
	for i := range 30 {
		all = append(all, candidate{"big", fmt.Sprintf("md://a.md#s%d", i), "go://m/p#X", "X"})
	}
	for i := range 10 {
		all = append(all, candidate{"small", fmt.Sprintf("md://b.md#s%d", i), "go://m/p#Y", "Y"})
	}
	got := draw(all, 8)
	n := map[string]int{}
	for _, c := range got {
		n[c.repo]++
	}
	if n["big"] != 6 || n["small"] != 2 {
		t.Errorf("shares %v, want big 6, small 2", n)
	}
	slices.Reverse(all)
	if again := draw(all, 8); !slices.Equal(again, got) {
		t.Errorf("the draw depends on the order of its input:\n%v\n%v", got, again)
	}
	if all := draw(all, 100); len(all) != 40 {
		t.Errorf("a draw larger than the population took %d of 40", len(all))
	}
}
