// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"os"
	"path/filepath"
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
func pageThrough(t *testing.T, h *harness, pattern string) (map[string]int, int) {
	t.Helper()
	seen, after := map[string]int{}, ""
	for page := 1; page <= 20; page++ {
		args := map[string]any{"pattern": pattern, "max_tokens": 200}
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
func TestSearchTextPagesInPathOrder(t *testing.T) {
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	body := strings.Repeat("marker line\n", 40)
	for _, name := range []string{"a/x.md", "a-c/x.md", "a.md", "b/x.md"} {
		writeFile(t, filepath.Join(dir, name), body)
	}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(dir).Build(), BuildInfo{Root: dir}, nil
	})
	h := &harness{t: t, c: connect(t, s)}
	seen, pages := pageThrough(t, h, "marker")
	if pages < 3 {
		t.Errorf("%d pages: the budget never cut inside a directory", pages)
	}
	if len(seen) != 160 {
		t.Errorf("pages reached %d of 160 hits", len(seen))
	}
}
