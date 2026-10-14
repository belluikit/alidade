// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/build"
	"github.com/trendvidia/alidade/internal/graph"
)

// TestOracleGoCalls is the Go calls harness (#148): for each repository
// in ALIDADE_ORACLE it builds the graph as `alidade graph` does and
// compares its calls edges with the type checker's. It prints the numbers
// and fails only when the harness measured nothing — a repository that
// did not build, or an oracle that read no call — never on a number.
// Skipped when the variable is unset; run scripts/oracle.sh.
func TestOracleGoCalls(t *testing.T) {
	roots := strings.Fields(os.Getenv("ALIDADE_ORACLE"))
	if len(roots) == 0 {
		t.Skip("set ALIDADE_ORACLE to repository roots, or run scripts/oracle.sh")
	}
	measured := 0
	for _, root := range roots {
		t.Run(filepath.Base(root), func(t *testing.T) {
			if measure(t, root) {
				measured++
			}
		})
	}
	// Every repository skipping is a run that measured nothing, which a
	// green result must not hide.
	if measured == 0 {
		t.Fatalf("none of the %d repositories had an oracle to compare with", len(roots))
	}
}

// why says what the oracle found and what it had to pass over: the
// modules, and the three ways a package of one can be read by neither
// side.
func why(c *Calls) string {
	return fmt.Sprintf("%d modules %v, %d unloaded %v, %d that do not type-check %v, %d go.mod skipped %v",
		len(c.Modules), c.Modules, len(c.Unloaded), c.Unloaded, len(c.Failed), c.Failed, len(c.Skipped), c.Skipped)
}

// measure compares one repository with every oracle that has something
// to compare, and reports whether any did. The Go comparisons need a
// module the type checker can load; a repository without one still has
// its SQL, proto and Markdown measured — a skip of the whole repository
// left pxfed's labelled Markdown edge unread.
func measure(t *testing.T, root string) bool {
	g, rep, err := build.Build(root, build.DefaultOptions())
	if err != nil {
		t.Fatalf("does not build: %v", err)
	}
	measured := measureGo(t, g, rep.Root)
	measured = measureSQL(t, g, rep.Root) || measured
	proto, ok := measureProto(t, g, rep.Root)
	measured = ok || measured
	measured = measureMarkdown(t, g, rep.Root) || measured
	measured = measureTS(t, g, rep.Root) || measured
	measured = measureGoasm(t, g, rep.Root) || measured
	measured = measurePXF(t, g, rep.Root, proto) || measured
	measured = measurePython(t, g, rep.Root) || measured
	if !measured {
		t.Skip("no oracle had anything to compare")
	}
	return measured
}

// measureGo compares the Go calls and imports with the type checker's and
// go list's, when the repository has a module the type checker loads.
func measureGo(t *testing.T, g *graph.Graph, root string) bool {
	calls, err := GoCalls(graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Packages == 0 {
		// Every way there can be no oracle, said: a repository with no
		// module at all reads differently from one whose modules the type
		// checker refused, and a skip that names neither is a number a
		// reader cannot act on.
		t.Logf("go: no oracle: %s", why(calls))
		return false
	}
	if calls.Sites == 0 || len(calls.Calls) == 0 {
		t.Fatalf("the oracle read %d packages but %d call sites and %d calls: it measured nothing", calls.Packages, calls.Sites, len(calls.Calls))
	}
	if len(calls.Imports) == 0 {
		t.Fatalf("the oracle read %d packages and no import: it measured nothing", calls.Packages)
	}
	r := Compare(g, calls)
	// The same four counts in the measured path: a recall read without
	// them is read against a denominator that quietly shrank.
	t.Logf("%d packages, %d call sites, %s\n%s%s%s%s", calls.Packages, calls.Sites, why(calls), r, CompareImports(g, calls), CompareUses(g, calls), CompareFields(g, calls))
	return true
}

// measureGoasm compares the goasm layer with the generator its
// directives pin, for a repository that has any.
func measureGoasm(t *testing.T, g *graph.Graph, root string) bool {
	o, err := GoasmTemplates(graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if o.Templates == 0 && len(o.Failed) == 0 {
		return false
	}
	if o.Templates > 0 && len(o.Generates) == 0 && len(o.Failed) < o.Templates {
		t.Fatalf("the generator ran on %d templates and the oracle read no method: it measured nothing", o.Templates)
	}
	t.Logf("%s", CompareGoasm(g, o))
	return o.Templates > 0
}

// measurePXF compares the PXF layer's references with the spec reader's,
// each type confirmed against protoc's declarations when there are any.
func measurePXF(t *testing.T, g *graph.Graph, root string, proto *Proto) bool {
	p, err := PXFFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Files == 0 {
		return false
	}
	t.Logf("%s", ComparePXF(g, p, declaredMessages(proto)))
	types := maps.Clone(p.FileTypes)
	inferred := map[string]string{}
	if s := judgedCensus(t, root); s != nil {
		inferred = CensusTypes(s, p.FileTypes)
		maps.Copy(types, inferred)
	}
	sets, unplaced, failed := SchemaFields(root, types, proto)
	t.Logf("layers typed by their chain: %d\n%s", len(inferred), ComparePXFSchema(g, sets, unplaced, failed))
	measureCensus(t, g, root)
	return true
}

// measureCensus reads the chain census for a repository checked out at
// the commit it was judged at.
func measureCensus(t *testing.T, g *graph.Graph, root string) {
	if s := judgedCensus(t, root); s != nil {
		t.Logf("%s", CompareCensus(g, s))
	}
}

// judgedCensus is the chain census of a repository checked out at the
// commit it was judged at, or nil.
func judgedCensus(t *testing.T, root string) *Sample {
	census, err := LoadSample(filepath.Join("testdata", "chain-census.pxf"))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := census[filepath.Base(root)]
	if !ok {
		return nil
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if got := strings.TrimSpace(string(head)); err != nil || got != s.Commit {
		t.Logf("chain census: not compared, judged at %s and checked out at %q", s.Commit[:12], got)
		return nil
	}
	return s
}

// measureProto compares the proto layer with protoc, for a repository
// that has .proto files.
func measureProto(t *testing.T, g *graph.Graph, root string) (*Proto, bool) {
	p, err := Protoc(graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) == 0 && len(p.Failed) == 0 {
		return nil, false
	}
	for f, err := range p.Failed {
		t.Logf("protoc refused %s: %s", f, err)
	}
	if len(p.Files) > 0 && len(p.Decls) == 0 {
		t.Fatalf("protoc compiled %d files and the oracle read no declaration: it measured nothing", len(p.Files))
	}
	t.Logf(".pb.go files %d\n%s", p.GoFiles, CompareProto(g, p))
	return p, len(p.Files) > 0
}

// measureSQL compares the SQL layer with sqlc, when the repository has a
// sqlc configuration: none is no measurement, not a failure, and a
// configuration sqlc refuses is reported, not compared.
func measureSQL(t *testing.T, g *graph.Graph, root string) bool {
	s, err := SQLC(graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Configs == 0 && len(s.Failed) == 0 {
		return false
	}
	for cfg, err := range s.Failed {
		t.Logf("sqlc refused %s: %s", cfg, err)
	}
	if s.Configs > 0 && len(s.Tables) == 0 {
		t.Fatalf("sqlc generated for %d configurations and the oracle read no table: it measured nothing", s.Configs)
	}
	t.Logf("sqlc configurations %d\n%s", s.Configs, CompareSQL(g, s))
	return s.Configs > 0
}

// measureMarkdown reads the labelled Markdown samples for a repository
// checked out at the commit they were labelled at; at any other commit
// the labels are not evidence, and it says so.
func measureMarkdown(t *testing.T, g *graph.Graph, root string) bool {
	measured := false
	for _, file := range []string{"markdown-sample.pxf", "markdown-bareword-sample.pxf"} {
		samples, err := LoadSample(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		s, ok := samples[filepath.Base(root)]
		if !ok {
			continue
		}
		head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Logf("%s: not compared, git rev-parse: %v", file, err)
			continue
		}
		if got := strings.TrimSpace(string(head)); got != s.Commit {
			t.Logf("%s: not compared, labelled at %s and checked out at %s", file, s.Commit[:12], got[:12])
			continue
		}
		t.Logf("%s %s", file, CompareSample(g, s))
		measured = true
	}
	return measureRecall(t, g, root) || measured
}

// measureRecall reads the Markdown recall sample for a repository checked
// out at the commit it was labelled at (#189).
func measureRecall(t *testing.T, g *graph.Graph, root string) bool {
	samples, err := LoadRecall(filepath.Join("testdata", "markdown-recall-sample.pxf"))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := samples[filepath.Base(root)]
	if !ok {
		return false
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if got := strings.TrimSpace(string(head)); err != nil || got != s.Commit {
		t.Logf("markdown recall sample: not compared, labelled at %s and checked out at %q", s.Commit[:12], got)
		return false
	}
	t.Logf("%s", CompareRecall(g, s))
	return true
}

// measureTS compares the frontend layer with TypeScript's resolution and
// the _pb.ts descriptors, for a repository with frontend files.
func measureTS(t *testing.T, g *graph.Graph, root string) bool {
	ts, err := Frontend(g, graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if ts.NoTypeScript {
		t.Logf("ts: frontend files and no typescript package installed (node_modules/typescript, or ALIDADE_TYPESCRIPT): imports not compared")
	}
	if len(ts.Files) == 0 && ts.PBFiles == 0 {
		return false
	}
	if len(ts.Files) > 0 && len(ts.Imports) == 0 && len(ts.Unresolved) == 0 {
		t.Fatalf("TypeScript read %d files and resolved no import: it measured nothing", len(ts.Files))
	}
	t.Logf("%s", CompareTS(g, ts))
	calls, err := FrontendCalls(g, graph.NewMinter(root), root)
	if err != nil {
		t.Fatal(err)
	}
	if calls != nil {
		if calls.Sites > 0 && calls.Resolved == 0 {
			t.Fatalf("TypeScript walked %d call sites and resolved none: it measured nothing", calls.Sites)
		}
		t.Logf("%s", CompareTSCalls(g, calls))
	}
	return true
}

// measurePython compares the Python layer's calls with the calls a run of
// the repository's tests made (#237), when ALIDADE_PYTRACE names the
// directory scripts/pytrace.sh wrote them to for this root.
func measurePython(t *testing.T, g *graph.Graph, root string) bool {
	dir := os.Getenv("ALIDADE_PYTRACE")
	if dir == "" {
		return false
	}
	traced, err := ReadPyTraces(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(traced) == 0 {
		t.Fatalf("ALIDADE_PYTRACE=%s holds no traced call: it measured nothing", dir)
	}
	r := ComparePyCalls(g, root, traced)
	if r.Edges[0] == 0 {
		t.Fatalf("none of %d traced calls is between declarations of this graph: a trace of another root?", len(traced))
	}
	t.Logf("%s", r)
	return true
}
