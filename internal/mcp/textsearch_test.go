// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/adapter/goasm"
	"github.com/belluikit/alidade/internal/adapter/markdown"
	"github.com/belluikit/alidade/internal/adapter/proto"
	"github.com/belluikit/alidade/internal/adapter/pxf"
	"github.com/belluikit/alidade/internal/adapter/py"
	"github.com/belluikit/alidade/internal/adapter/sql"
	"github.com/belluikit/alidade/internal/adapter/ts"
	"github.com/belluikit/alidade/internal/adapter/ts/svelte"
	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// search_text (#178) finds text the graph does not hold and names the
// declaration holding each hit: exactly in Go, by position elsewhere,
// marked as a guess; it is listed only when enabled.
func TestSearchText(t *testing.T) {
	s := textFixture(t)
	if !listed(t, s, "search_text") {
		t.Fatal("search_text not listed")
	}
	h := &harness{t: t, c: connect(t, s)}
	checkContaining(t, h.callJSON("search_text", map[string]any{"pattern": "not in the active allowlist", "exclude_tests": true}))
	checkPaging(t, h)
	r := h.callJSON("search_text", map[string]any{"pattern": `sweep\s+failed`, "regex": true, "path": "a/"})
	if hits := r["hits"].([]any); len(hits) != 1 || end(r, hits[0].(map[string]any)["in"]) != "go://example.com/m/a#T.M" {
		t.Errorf("method hit %v", r)
	}
	checkExclude(t, h)
	r = h.callJSON("search_text", map[string]any{"pattern": "a", "max_tokens": 200})
	if r["truncated"] != true {
		t.Errorf("a broad search under a small budget is not cut: %v", r)
	}
}

// checkExclude (#23): exclude leaves out every file under any of its
// prefixes, a directory's whole subtree with it, and is refused when it
// is not a list of strings.
func checkExclude(t *testing.T, h *harness) {
	t.Helper()
	ats := func(r map[string]any) []string {
		var out []string
		for _, x := range r["hits"].([]any) {
			out = append(out, x.(map[string]any)["at"].(string))
		}
		return out
	}
	r := h.callJSON("search_text", map[string]any{"pattern": "not in the active allowlist", "exclude": []any{"docs/"}})
	if got := ats(r); len(got) != 2 || got[0] != "a/a.go:8" || got[1] != "a/a_test.go:3" {
		t.Errorf("exclude docs/: hits %v, want a/a.go:8 and a/a_test.go:3", got)
	}
	r = h.callJSON("search_text", map[string]any{"pattern": "not in the active allowlist", "exclude": []any{"a/a_test", "docs"}})
	if got := ats(r); len(got) != 1 || got[0] != "a/a.go:8" {
		t.Errorf("exclude a/a_test and docs: hits %v, want a/a.go:8", got)
	}
	if _, isErr := h.call("search_text", map[string]any{"pattern": "x", "exclude": "docs/"}); !isErr {
		t.Error("exclude given as a string, not a list, was not refused")
	}
}

// checkPaging: test files counted unless left out; files_only lists
// files with counts; a cut answer's next continues it, and every hit is
// reached exactly once across the pages.
func checkPaging(t *testing.T, h *harness) {
	t.Helper()
	all := h.callJSON("search_text", map[string]any{"pattern": "not in the active allowlist"})
	if all["total"] != 3.0 || all["matched"] != 3.0 {
		t.Errorf("with tests: total %v matched %v, want 3 and 3", all["total"], all["matched"])
	}
	files := h.callJSON("search_text", map[string]any{"pattern": "not in the active allowlist", "files_only": true, "exclude_tests": true})
	if m := files["matches"].([]any); len(m) != 2 || files["hits"] != nil {
		t.Errorf("files_only %v", files)
	}
	seen, pages := pageThrough(t, h, "e")
	total := h.callJSON("search_text", map[string]any{"pattern": "e", "max_tokens": 100000})["total"].(float64)
	if pages < 2 {
		t.Errorf("%d page: the budget never cut, so continuing was not tested", pages)
	}
	for at, n := range seen {
		if n != 1 {
			t.Errorf("%s reached %d times across pages", at, n)
		}
	}
	if len(seen) != int(total) {
		t.Errorf("pages reached %d hits, total %v", len(seen), total)
	}
}

// pageThrough follows next from a small-budget search until an answer is
// not cut, counting each hit reached and the pages read.
func pageThrough(t *testing.T, h *harness, pattern string, extra ...map[string]any) (map[string]int, int) {
	t.Helper()
	seen, after := map[string]int{}, ""
	for page := 1; page <= 20; page++ {
		args := map[string]any{"pattern": pattern, "max_tokens": 200}
		for _, e := range extra {
			maps.Copy(args, e)
		}
		if after != "" {
			args["after"] = after
		}
		r := h.callJSON("search_text", args)
		for _, x := range r["hits"].([]any) {
			seen[x.(map[string]any)["at"].(string)]++
		}
		next, _ := r["next"].(string)
		if r["truncated"] != true {
			return seen, page
		}
		if next == "" || next == after {
			t.Fatalf("a cut answer with no way on: next %q after %q", next, after)
		}
		after = next
	}
	t.Fatal("20 pages and still cut")
	return nil, 0
}

// textFixture is a server over a built tree of one Go file and one doc.
func textFixture(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.26\n",
		"a/a.go":      "package a\n\nimport \"errors\"\n\n// Check refuses.\nfunc Check(ok bool) error {\n\tif !ok {\n\t\treturn errors.New(\"repository is not in the active allowlist\")\n\t}\n\treturn nil\n}\n\ntype T struct{}\n\n// M logs.\nfunc (T) M() string { return \"sweep failed\" }\n",
		"docs/x.md":   "# Guide\n\n## Errors\n\nThe message repository is not in the active allowlist means the repo is unknown.\n",
		"a/a_test.go": "package a\n\n// a test says not in the active allowlist too\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return New(func() (*graph.Graph, BuildInfo, error) { return g, BuildInfo{Root: dir}, nil })
}

// checkContaining: the Go hit is in Check, exactly; the doc hit is in its
// section, a guess.
func checkContaining(t *testing.T, r map[string]any) {
	t.Helper()
	hits := r["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits %v", hits)
	}
	in, guess := map[string]string{}, map[string]bool{}
	for _, x := range hits {
		m := x.(map[string]any)
		in[m["at"].(string)] = end(r, m["in"])
		guess[m["at"].(string)] = m["guess"] == true
	}
	if in["a/a.go:8"] != "go://example.com/m/a#Check" || in["docs/x.md:5"] != "md://docs/x.md#guide.errors" {
		t.Errorf("containing declarations %v", in)
	}
	if guess["a/a.go:8"] || !guess["docs/x.md:5"] {
		t.Errorf("guess marks %v: a Go hit is exact, a doc's is placed by position", guess)
	}
}

func listed(t *testing.T, s *Server, name string) bool {
	t.Helper()
	c := connect(t, s)
	res, err := c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// TestSearchStaysOutsideTheGraph pins ADR 000's amendment (#178): a search
// adds nothing to the graph — the same graph, the same nodes and edges
// after as before — and every hit's in names a node the graph holds. It
// fails the day text search starts writing the graph, which the
// amendment's first bound forbids.
func TestSearchStaysOutsideTheGraph(t *testing.T) {
	s := textFixture(t)
	g, _, err := s.graph(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := g.NodeCount(), edgeCount(g)
	h := &harness{t: t, c: connect(t, s)}
	for _, args := range []map[string]any{
		{"pattern": "not in the active allowlist"}, {"pattern": "e", "max_tokens": 100000},
		{"pattern": "sweep", "files_only": true}, {"pattern": `\w+`, "regex": true, "max_tokens": 100000},
	} {
		checkHitsInGraph(t, g, h.callJSON("search_text", args), args)
	}
	after, _, _ := s.graph(context.Background())
	if after != g || after.NodeCount() != nodes || edgeCount(after) != edges {
		t.Errorf("a search changed the graph: %d nodes %d edges before, %d and %d after", nodes, edges, after.NodeCount(), edgeCount(after))
	}
}

// checkHitsInGraph fails a hit whose in names a node the graph lacks.
func checkHitsInGraph(t *testing.T, g *graph.Graph, r map[string]any, args map[string]any) {
	t.Helper()
	hits, _ := r["hits"].([]any)
	for _, x := range hits {
		m := x.(map[string]any)
		if _, has := m["in"]; !has {
			continue
		}
		if id := end(r, m["in"]); func() bool { _, ok := g.Node(graph.ID(id)); return !ok }() {
			t.Errorf("%v: hit %s names %q, which the graph does not hold", args, m["at"], id)
		}
	}
}

func edgeCount(g *graph.Graph) int {
	n := 0
	for range g.Edges() {
		n++
	}
	return n
}

// TestSearchTextTypeScriptContainment pins #212: a hit in a TypeScript
// function is in that function, exactly — the hit #211 labelled App sat
// in AppContent, declared below it — a hit at module level is in no
// declaration, and a hit in a Svelte component, which the type checker
// does not read, is in none.
func TestSearchTextTypeScriptContainment(t *testing.T) {
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range map[string]string{
		"web/tsconfig.json": `{"compilerOptions": {"jsx": "react-jsx", "module": "esnext", "moduleResolution": "bundler"}, "include": ["src"]}`,
		"web/src/app.tsx": `export function App() {
  return null;
}

function AppContent() {
  const clients = useRpcClients();
  return clients;
}

function useRpcClients() {
  return [];
}

const marker = useRpcClients();

class Store {
  add() {
    return 1;
  }
  clients = useRpcClients();
}

console.log(useRpcClients(), Store);
`,
		"web/src/Card.svelte": "<script lang=\"ts\">\n  const x = useRpcClients();\n</script>\n",
	} {
		writeFile(t, filepath.Join(dir, name), body)
	}
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, c: connect(t, New(func() (*graph.Graph, BuildInfo, error) { return g, BuildInfo{Root: dir}, nil }))}
	r := h.callJSON("search_text", map[string]any{"pattern": "useRpcClients()"})
	in, guess := map[string]string{}, map[string]bool{}
	for _, x := range r["hits"].([]any) {
		m := x.(map[string]any)
		in[m["at"].(string)] = end(r, m["in"])
		guess[m["at"].(string)] = m["guess"] == true
	}
	for at, want := range map[string]string{
		"web/src/app.tsx:6":  "ts://web/src/app.tsx#AppContent",
		"web/src/app.tsx:10": "ts://web/src/app.tsx#useRpcClients",
		"web/src/app.tsx:14": "ts://web/src/app.tsx#marker", // a top-level const holds its initializer
		// Where nearest-above would guess wrong: after add's last line,
		// still in Store; and at module level, in no declaration.
		"web/src/app.tsx:20": "ts://web/src/app.tsx#Store.clients",
		"web/src/app.tsx:23": "",
	} {
		if in[at] != want || guess[at] {
			t.Errorf("%s in %q, guess %v; want %s, exact", at, in[at], guess[at], want)
		}
	}
	// A component's hit is in its declaration when the build translates
	// components (#221), else in none.
	want := ""
	if svelte.Available() {
		want = "ts://web/src/Card.svelte#x"
	}
	if got := in["web/src/Card.svelte:2"]; got != want {
		t.Errorf("a Svelte hit is in %q, want %q", got, want)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSearchTextSkipsOnlyWhatNoLayerReads pins #275 and #266: a directory
// only some layers skip by name is searched — a Go package named build,
// the frontend's dist and out — and installed packages, which every layer
// leaves out, are not: a virtual environment by its pyvenv.cfg, whatever
// its name, and .venv and site-packages by name.
func TestSearchTextSkipsOnlyWhatNoLayerReads(t *testing.T) {
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range map[string]string{
		"go.mod":                          "module example.com/m\n\ngo 1.26\n",
		"internal/build/b.go":             "package build\n\n// Stale says marker here.\nfunc Stale() {}\n",
		"web/dist/notes.md":               "# Notes\n\nmarker here\n",
		"web/out/notes.md":                "# Notes\n\nmarker here\n",
		"env/pyvenv.cfg":                  "home = /usr/bin\n",
		"env/lib/x.py":                    "# marker here\n",
		".venv/lib/README.md":             "marker here\n",
		"lib/site-packages/pkg/README.md": "marker here\n",
		"testdata/x.md":                   "marker here\n",
	} {
		writeFile(t, filepath.Join(dir, name), body)
	}
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	s := New(func() (*graph.Graph, BuildInfo, error) { return g, BuildInfo{Root: dir}, nil })
	h := &harness{t: t, c: connect(t, s)}
	r := h.callJSON("search_text", map[string]any{"pattern": "marker here"})
	got := map[string]string{}
	for _, x := range r["hits"].([]any) {
		m := x.(map[string]any)
		got[m["at"].(string)] = end(r, m["in"])
	}
	want := map[string]string{
		"internal/build/b.go:3": "go://example.com/m/internal/build#Stale",
		"web/dist/notes.md:3":   "md://web/dist/notes.md#notes",
		"web/out/notes.md:3":    "md://web/out/notes.md#notes",
	}
	if len(got) != len(want) {
		t.Errorf("hits %v, want %v", got, want)
	}
	for at, in := range want {
		if got[at] != in {
			t.Errorf("%s: in %q, want %q (hits %v)", at, got[at], in, got)
		}
	}
}

// TestSearchTextSkipsNamesEveryLayerSkips: every name search_text leaves
// out is one each layer's walk leaves out too, testdata aside, which each
// leaves out by default. A name added to one layer's list alone is build
// output of that layer, and another layer may read it (#275).
func TestSearchTextSkipsNamesEveryLayerSkips(t *testing.T) {
	layers := map[string]map[string]bool{
		"proto": proto.SkipDirs, "sql": sql.SkipDirs, "pxf": pxf.SkipDirs, "goasm": goasm.SkipDirs,
		"ts": ts.SkipDirs, "py": py.SkipDirs, "md": markdown.SkipDirs,
	}
	for name := range textSkipDirs {
		if name == "testdata" {
			continue
		}
		for layer, skips := range layers {
			if !skips[name] {
				t.Errorf("search_text skips %s, which the %s layer reads", name, layer)
			}
		}
		// The go tool never enters a directory whose name begins with
		// "." or that is named vendor or testdata; node_modules is
		// the gap build.walkSkip names.
		if !strings.HasPrefix(name, ".") && name != "vendor" && name != "node_modules" {
			t.Errorf("search_text skips %s, which the go tool reads", name)
		}
	}
}

// TestSearchTextPagesInPathOrder: a cut answer's next continues by
// comparing paths as strings, so the walk must visit files in that
// order. A directory "a" and a sibling "a-c" are where a directory walk
// and string order part: the walk enters a/ first, while "a-c/x.md" <
// "a/x.md" as strings, so after a hit in a/ the rest of a-c/ was skipped.
//
// The same holds of a commit's files; and continuing reads no file
// before the one the cut answer ended in, which only its hits' dropping
// kept out of the answer.
func TestSearchTextPagesInPathOrder(t *testing.T) {
	dir := gittest.TempDir(t)
	body := strings.Repeat("marker line\n", 40)
	files := map[string]string{}
	for _, name := range []string{"a/x.md", "a-c/x.md", "a.md", "b/x.md"} {
		files[name] = body
	}
	gittest.Init(t, dir, files)
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	h := &harness{t: t, c: connect(t, s)}
	for _, at := range []map[string]any{{}, {"ref": "HEAD"}} {
		seen, pages := pageThrough(t, h, "marker", at)
		if pages < 3 {
			t.Errorf("%v: %d pages: the budget never cut inside a directory", at, pages)
		}
		if len(seen) != 160 {
			t.Errorf("%v: pages reached %d of 160 hits", at, len(seen))
		}
		r := h.callJSON("search_text", map[string]any{"pattern": "marker", "after": "a/x.md:40", "ref": at["ref"]})
		if r["files"] != 2.0 || r["total"] != 40.0 {
			t.Errorf("%v: continuing after a/x.md:40 read %v files for %v hits, want 2 (a/x.md, b/x.md) and 40", at, r["files"], r["total"])
		}
	}
}

// TestSearchTextInAWorktree pins #23: from a session whose graph is the
// main checkout, root searches another worktree of the repository — a
// string only that worktree holds is found there and not in the main
// checkout — without building the graph, and its hits name no
// declaration, since the graph is another tree's. A directory that is
// not a worktree's top, or a checkout of another repository, is refused.
func TestSearchTextInAWorktree(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt, other := filepath.Join(base, "main"), filepath.Join(base, "wt"), filepath.Join(base, "other")
	gittest.Init(t, main, map[string]string{"go.mod": "module example.com/m\n\ngo 1.26\n", "a/a.go": "package a\n\n// F is old.\nfunc F() {}\n"})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	gittest.Commit(t, wt, map[string]string{"docs/new.md": "# New\n\nlinked externally since the merge\n"}, "new doc")
	gittest.Init(t, other, map[string]string{"README.md": "linked externally\n"})
	builds := 0
	s := New(func() (*graph.Graph, BuildInfo, error) {
		builds++
		return graph.NewBuilder(main).Build(), BuildInfo{Root: main}, nil
	}).Root(main)
	h := &harness{t: t, c: connect(t, s)}
	for _, root := range []string{wt, "../wt"} {
		checkWorktreeHit(t, h.callJSON("search_text", map[string]any{"pattern": "linked externally", "root": root}), root, wt)
	}
	if builds != 0 {
		t.Errorf("a search of another worktree built the graph %d times", builds)
	}
	r := h.callJSON("search_text", map[string]any{"pattern": "linked externally"})
	if r["total"] != 0.0 || r["root"] != nil {
		t.Errorf("the main checkout holds the worktree's text: %v", r)
	}
	r = h.callJSON("search_text", map[string]any{"pattern": "F is old", "root": main})
	if r["total"] != 1.0 || r["root"] != nil {
		t.Errorf("root naming the graph's own checkout: %v", r)
	}
	for _, bad := range []string{other, filepath.Join(wt, "docs"), filepath.Join(base, "nowhere")} {
		if text, isErr := h.call("search_text", map[string]any{"pattern": "x", "root": bad}); !isErr {
			t.Errorf("root %s was not refused: %s", bad, text)
		}
	}
}

// checkWorktreeHit: the worktree's one hit, under its root, in no
// declaration.
func checkWorktreeHit(t *testing.T, r map[string]any, root, wt string) {
	t.Helper()
	hits, _ := r["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["at"] != "docs/new.md:3" || r["root"] != wt {
		t.Errorf("root %s: %v, want docs/new.md:3 under root %s", root, r, wt)
		return
	}
	if hits[0].(map[string]any)["in"] != nil {
		t.Errorf("root %s: a worktree's hit names a declaration of another tree's graph: %v", root, hits[0])
	}
}

// TestSearchTextAtACommit pins #23's second case: ref searches a commit's
// files rather than the ones on disk — an older commit's wording, a
// branch's, another worktree's HEAD — in path order, leaving out what a
// search on disk leaves out, without building the graph. A name git does
// not resolve, or an option in its place, is refused.
func TestSearchTextAtACommit(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt := filepath.Join(base, "main"), filepath.Join(base, "wt")
	gittest.Init(t, main, map[string]string{"docs/a.md": "first wording\n", "vendor/v.md": "first wording\n",
		"a/x.md": "wording\n", "a-c/x.md": "wording\n", "bin.dat": "wording\x00\n",
		"env/pyvenv.cfg": "home = /usr/bin\n", "env/lib/site.md": "wording\n"})
	gittest.Commit(t, main, map[string]string{"docs/a.md": "second wording\n"}, "second")
	writeFile(t, filepath.Join(main, "docs/a.md"), "third wording\n") // not committed
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	gittest.Commit(t, wt, map[string]string{"docs/a.md": "feature wording\n"}, "feature")
	builds := 0
	s := New(func() (*graph.Graph, BuildInfo, error) {
		builds++
		return graph.NewBuilder(main).Build(), BuildInfo{Root: main}, nil
	}).Root(main)
	h := &harness{t: t, c: connect(t, s)}
	for _, c := range []struct{ ref, root, want string }{
		{"HEAD~1", "", "first wording"},
		{"HEAD", "", "second wording"},
		{"feature", "", "feature wording"},
		{"HEAD", wt, "feature wording"},
	} {
		r := h.callJSON("search_text", map[string]any{"pattern": "wording", "ref": c.ref, "root": c.root})
		checkAtCommit(t, r, c.ref, c.want)
	}
	if builds != 0 {
		t.Errorf("a search at a commit built the graph %d times", builds)
	}
	r := h.callJSON("search_text", map[string]any{"pattern": "wording", "ref": "HEAD", "exclude": []any{"a"}, "path": "docs/"})
	if r["total"] != 1.0 {
		t.Errorf("path and exclude at a commit: %v", r)
	}
	for _, bad := range []string{"nope", "--output=x", "HEAD:docs"} {
		if text, isErr := h.call("search_text", map[string]any{"pattern": "x", "ref": bad}); !isErr {
			t.Errorf("ref %q was not refused: %s", bad, text)
		}
	}
}

// checkAtCommit: the commit's three text hits, in path order — a-c/
// before a/, as strings sort — none in vendor/, a virtual environment
// or a binary file, none naming a declaration, and the commit named.
func checkAtCommit(t *testing.T, r map[string]any, ref, want string) {
	t.Helper()
	var got []string
	for _, x := range r["hits"].([]any) {
		m := x.(map[string]any)
		got = append(got, m["at"].(string)+" "+m["text"].(string))
		if m["in"] != nil {
			t.Errorf("ref %s: a commit's hit names a declaration: %v", ref, m)
		}
	}
	wantHits := []string{"a-c/x.md:1 wording", "a/x.md:1 wording", "docs/a.md:1 " + want}
	if strings.Join(got, "|") != strings.Join(wantHits, "|") {
		t.Errorf("ref %s: hits %q, want %q", ref, got, wantHits)
	}
	if sha, _ := r["ref"].(string); len(sha) != 40 {
		t.Errorf("ref %s: the answer names commit %q", ref, r["ref"])
	}
}

// TestSearchTextAtACommitBelowTheTop: a graph built from a directory
// below the checkout's top searches that directory, by paths relative to
// it, at a commit as on disk; a relative root is the repository's, not
// the directory's.
func TestSearchTextAtACommitBelowTheTop(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt := filepath.Join(base, "main"), filepath.Join(base, "wt")
	gittest.Init(t, main, map[string]string{"sub/pkg/x.md": "wording\n", "top.md": "wording\n"})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	sub := filepath.Join(main, "sub")
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(sub).Build(), BuildInfo{Root: sub}, nil
	}).Root(sub)
	h := &harness{t: t, c: connect(t, s)}
	for _, ref := range []string{"", "HEAD"} {
		r := h.callJSON("search_text", map[string]any{"pattern": "wording", "path": "pkg/", "ref": ref})
		hits, _ := r["hits"].([]any)
		if len(hits) != 1 || hits[0].(map[string]any)["at"] != "pkg/x.md:1" {
			t.Errorf("ref %q: %v, want pkg/x.md:1 alone", ref, r)
		}
	}
	if r := h.callJSON("search_text", map[string]any{"pattern": "wording", "root": "../wt"}); r["root"] != wt || r["total"] != 2.0 {
		t.Errorf("root ../wt, from a graph of main/sub: %v, want wt's two hits", r)
	}
}

// TestSearchTextAtACommitUnderAGitHook: a server started from a git hook,
// with GIT_DIR exported, still reads the commits of the tree searched.
func TestSearchTextAtACommitUnderAGitHook(t *testing.T) {
	base := gittest.TempDir(t)
	main, hooked := filepath.Join(base, "main"), filepath.Join(base, "hooked")
	gittest.Init(t, main, map[string]string{"a.md": "main's wording\n"})
	gittest.Init(t, hooked, map[string]string{"a.md": "the hook's wording\n"})
	t.Setenv("GIT_DIR", filepath.Join(hooked, ".git"))
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(main).Build(), BuildInfo{Root: main}, nil
	}).Root(main)
	h := &harness{t: t, c: connect(t, s)}
	r := h.callJSON("search_text", map[string]any{"pattern": "wording", "ref": "HEAD"})
	if hits, _ := r["hits"].([]any); len(hits) != 1 || hits[0].(map[string]any)["text"] != "main's wording" {
		t.Errorf("ref HEAD under GIT_DIR: %v, want main's wording", r)
	}
}

// TestSearchTextRootOutsideAnyRepository: a graph built from a directory
// in no repository has no other worktree, even when the server runs
// inside a repository whose worktree root names, and the refusal says
// why rather than calling root another repository's.
func TestSearchTextRootOutsideAnyRepository(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt, plain := filepath.Join(base, "main"), filepath.Join(base, "wt"), filepath.Join(base, "plain")
	gittest.Init(t, main, map[string]string{"a.md": "wording\n"})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	writeFile(t, filepath.Join(plain, "p.md"), "wording\n")
	t.Chdir(main)
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(plain).Build(), BuildInfo{Root: plain}, nil
	}).Root(plain)
	h := &harness{t: t, c: connect(t, s)}
	if text, isErr := h.call("search_text", map[string]any{"pattern": "wording", "root": wt}); !isErr || !strings.Contains(text, "in no git repository") {
		t.Errorf("root %s, from a graph in no repository: %s (refused %v), want it refused as in no repository", wt, text, isErr)
	}
}

// TestSearchTextLeavesNestedWorktreesOut pins #24: from the main
// checkout, a worktree nested inside it is not searched — its hits came
// back once per worktree — while root searches it as its own tree.
func TestSearchTextLeavesNestedWorktreesOut(t *testing.T) {
	main := gittest.TempDir(t)
	gittest.Init(t, main, map[string]string{"docs/x.md": "# X\n\nmarker\n"})
	nested := filepath.Join(main, ".claude/worktrees/f")
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "f", nested)
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(main).Build(), BuildInfo{Root: main}, nil
	}).Root(main)
	h := &harness{t: t, c: connect(t, s)}
	if r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true}); r["total"] != 1.0 {
		t.Errorf("from the main checkout: %v, want docs/x.md alone", r["matches"])
	}
	if r := h.callJSON("search_text", map[string]any{"pattern": "marker", "root": nested}); r["total"] != 1.0 || r["root"] != nested {
		t.Errorf("root at the nested worktree: %v", r)
	}
}

// TestSearchTextRollsUpACutFileList pins #26: a files_only answer too
// long for its budget leads with hits per directory, two levels deep,
// most first — the noisy directory is visible before any paging — and
// one that fits carries no rollup.
func TestSearchTextRollsUpACutFileList(t *testing.T) {
	h := apiNoiseHarness(t)
	r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 400})
	dirs, _ := r["dirs"].([]any)
	if r["truncated"] != true || len(dirs) != 3 {
		t.Fatalf("a cut answer: truncated %v, dirs %v; want three directories", r["truncated"], r["dirs"])
	}
	want := []string{"api 60 60", ". 1 1", "src/cmd 1 1"}
	for i, d := range dirs {
		m := d.(map[string]any)
		if got := fmt.Sprintf("%s %v %v", m["dir"], m["count"], m["matched"]); got != want[i] {
			t.Errorf("dirs[%d] = %s, want %s", i, got, want[i])
		}
	}
	if m, _ := r["matches"].([]any); len(m) == 0 {
		t.Error("the rollup left no room for files")
	}

	if r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "path": "src/"}); r["dirs"] != nil {
		t.Errorf("an answer that fits carries dirs: %v", r["dirs"])
	}
}

// apiNoiseHarness serves a tree where api/ holds 60 of 62 matching files,
// too many for one files_only page of 400 tokens.
func apiNoiseHarness(t *testing.T) *harness {
	dir := t.TempDir()
	for i := range 60 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("api/go1.%02d.txt", i)), "#56088 marker\n")
	}
	writeFile(t, filepath.Join(dir, "src/cmd/dist/build.go"), "package dist // marker\n")
	writeFile(t, filepath.Join(dir, "README.md"), "marker\n")
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	return &harness{t: t, c: connect(t, s)}
}

// TestSearchTextRollsUpTheFirstPageOnly: a page continued with after
// lists files only, its whole budget theirs: the first page already said
// where they are.
func TestSearchTextRollsUpTheFirstPageOnly(t *testing.T) {
	h := apiNoiseHarness(t)
	r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 400})
	next, _ := r["next"].(string)
	page := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 200, "after": next})
	if m, _ := page["matches"].([]any); next == "" || page["truncated"] != true || len(m) == 0 || page["dirs"] != nil {
		t.Errorf("a page continued after %q: matches %v, dirs %v; want it cut, with files and no rollup", next, page["matches"], page["dirs"])
	}
}

// TestSearchTextRollsUpBelowPath: a cut files_only answer narrowed with
// path rolls up two levels below it, so the next narrowing is in view,
// not one directory holding every hit.
func TestSearchTextRollsUpBelowPath(t *testing.T) {
	dir := t.TempDir()
	for i := range 60 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("src/cmd/compile/internal/f%02d.go", i)), "// marker\n")
	}
	writeFile(t, filepath.Join(dir, "src/cmd/go/main.go"), "// marker\n")
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	h := &harness{t: t, c: connect(t, s)}
	r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 400, "path": "src/cmd/"})
	var got []string
	dirs, _ := r["dirs"].([]any)
	for _, d := range dirs {
		got = append(got, fmt.Sprint(d.(map[string]any)["dir"]))
	}
	if want := []string{"src/cmd/compile/internal", "src/cmd/go"}; !slices.Equal(got, want) {
		t.Errorf("dirs under path src/cmd/ = %v, want %v", got, want)
	}
}

// TestSearchTextRollupLeavesRoomToContinue: the rollup never takes the
// room the first file needs, so a cut answer always lists one and can be
// continued, even when that file's path is long.
func TestSearchTextRollupLeavesRoomToContinue(t *testing.T) {
	dir := t.TempDir()
	long := filepath.Join("a", "b", strings.Repeat("c", 110), strings.Repeat("d", 110), strings.Repeat("e", 110), strings.Repeat("f", 110)+".md")
	writeFile(t, filepath.Join(dir, long), "marker\n")
	for i := range 80 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("d%02d/x.md", i)), "marker\n")
	}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	h := &harness{t: t, c: connect(t, s)}
	r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 200})
	if m, _ := r["matches"].([]any); r["truncated"] != true || len(m) == 0 || r["next"] == nil {
		t.Errorf("a cut answer led by a %d-byte path: matches %v, next %v; want it listed, and a next to continue", len(long), r["matches"], r["next"])
	}
}

// TestSearchTextSaysARollupWasCut: a rollup with more directories than
// fit says how many it left out, so its counts are never read as all.
func TestSearchTextSaysARollupWasCut(t *testing.T) {
	dir := t.TempDir()
	for i := range 80 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("d%02d/x.md", i)), "marker\n")
	}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	h := &harness{t: t, c: connect(t, s)}
	r := h.callJSON("search_text", map[string]any{"pattern": "marker", "files_only": true, "max_tokens": 400})
	d, _ := r["dirs"].([]any)
	omitted, _ := r["dirs_omitted"].(float64)
	if len(d) == 0 || omitted == 0 || len(d)+int(omitted) != 80 {
		t.Errorf("dirs %d listed, %v omitted; want some of each, adding up to 80", len(d), r["dirs_omitted"])
	}
}

// TestRollUpChargesItsKeys: the rollup is charged for what it adds to
// the answer, its keys and brackets with its entries, so tokens does not
// understate it.
func TestRollUpChargesItsKeys(t *testing.T) {
	c := &textPage{ts: &textSearch{}, budget: 400, used: envelope, matched: map[string]int{}}
	for i := range 3 {
		f := fmt.Sprintf("d%d/x.md", i)
		c.order = append(c.order, f)
		c.matched[f] = i + 1
	}
	c.rollUp(0)
	added := estimate(struct {
		Dirs    []dirCount `json:"dirs"`
		Omitted int        `json:"dirs_omitted"`
	}{c.r.Dirs, c.r.DirsOmitted})
	if len(c.r.Dirs) != 3 || c.used-envelope < added {
		t.Errorf("rollUp listed %d dirs and charged %d tokens, want 3 and at least the %d they add", len(c.r.Dirs), c.used-envelope, added)
	}
}
