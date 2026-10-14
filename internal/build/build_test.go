// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trendvidia/alidade/internal/graph"
)

func TestBuildGoModule(t *testing.T) {
	root := filepath.Join("..", "adapter", "golang", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Go == nil || strings.Join(rep.Go.Modules, ",") != "example.com/fixture" {
		t.Fatalf("Go report = %+v, want the fixture module", rep.Go)
	}
	if !filepath.IsAbs(rep.Root) {
		t.Errorf("Root = %q, want absolute", rep.Root)
	}
	if _, ok := g.Node(graph.ID("go://example.com/fixture/shapes#Total")); !ok {
		t.Error("fixture graph lacks shapes#Total")
	}
	if rep.Elapsed <= 0 {
		t.Error("Elapsed not recorded")
	}
}

// TestBuildNestedModuleOnly: a repository whose only go.mod sits below
// the root still gets a Go layer (#67).
func TestBuildNestedModuleOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"svc/go.mod": "module example.com/svc\n\ngo 1.26\n", "svc/svc.go": "package svc\n\nfunc Serve() {}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Go == nil || strings.Join(rep.Go.Modules, ",") != "example.com/svc" {
		t.Fatalf("Go report = %+v, want the nested module", rep.Go)
	}
	if n, ok := g.Node(graph.ID("go://example.com/svc#Serve")); !ok || n.Pos.File != "svc/svc.go" {
		t.Errorf("svc#Serve = %+v, %v; want it positioned under the root", n, ok)
	}
}

func TestBuildProtoLayer(t *testing.T) {
	root := filepath.Join("..", "adapter", "proto", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Go != nil || rep.Proto == nil || rep.Proto.Files != 6 {
		t.Fatalf("report = go %+v proto %+v; want the proto layer alone", rep.Go, rep.Proto)
	}
	if got := rep.Layers(); len(got) != 1 || got[0] != "proto" {
		t.Errorf("Layers = %v", got)
	}
	if _, ok := g.Node(graph.ID("proto://example.v1.Shape")); !ok {
		t.Error("fixture graph lacks example.v1.Shape")
	}
}

// TestSQLReachesGoCaller is #8's third done-when on the SQL fixture, which
// carries a sqlc-style generated package and a hand-written caller: the
// generates edges resolve against the Go layer, and the users table is
// reachable from the caller in a few hops.
func TestSQLReachesGoCaller(t *testing.T) {
	root := filepath.Join("..", "adapter", "sql", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Layers(); strings.Join(got, ",") != "go,sql" {
		t.Fatalf("Layers = %v", got)
	}
	stubbed := map[string]bool{"Queries.GetUser": true, "Queries.ListUsers": true, "Queries.ListUserKeys": true}
	for _, d := range g.Dropped() {
		// Only three queries have a generated stub in the fixture;
		// their edges and every table's must resolve.
		to := string(d.Edge.To)
		decl := to[strings.LastIndexByte(to, '#')+1:]
		if !strings.HasPrefix(decl, "Queries.") || stubbed[decl] {
			t.Errorf("dropped: %+v", d)
		}
	}
	caller := graph.ID("go://example.com/fixture/app#Lookup")
	table := graph.ID("sql://table/users")
	if hops := reach(g, caller, table, 4); hops < 0 {
		t.Errorf("no path from %s to %s within 4 hops", caller, table)
	} else {
		t.Logf("%s reaches %s in %d hops", caller, table, hops)
	}
}

// reach is a breadth-first search over edges in both directions.
func reach(g *graph.Graph, from, to graph.ID, max int) int {
	dist := map[graph.ID]int{from: 0}
	frontier := []graph.ID{from}
	for len(frontier) > 0 {
		var next []graph.ID
		for _, id := range frontier {
			if id == to {
				return dist[id]
			}
			if dist[id] == max {
				continue
			}
			var nbrs []graph.ID
			for _, e := range g.Out(id) {
				nbrs = append(nbrs, e.To)
			}
			for _, e := range g.In(id) {
				nbrs = append(nbrs, e.From)
			}
			for _, n := range nbrs {
				if _, seen := dist[n]; !seen {
					dist[n] = dist[id] + 1
					next = append(next, n)
				}
			}
		}
		frontier = next
	}
	return -1
}

// TestGoasmReachesEmitter is #41's link: the goasm fixture is a Go module
// whose committed emitter was generated from its arm64 templates, so the
// generates edges resolve against the Go layer's method nodes — and the
// one template with no emitter (amd64's Breakpoint) drops, as a DECLARED
// edge must when the convention does not hold (ADR 006 rule 6). The
// emitter is emit_arm64.go, so the Go layer loads for arm64 whatever the
// host (#246).
func TestGoasmReachesEmitter(t *testing.T) {
	t.Setenv("GOARCH", "arm64")
	root := filepath.Join("..", "adapter", "goasm", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Layers(); strings.Join(got, ",") != "go,goasm" {
		t.Fatalf("Layers = %v", got)
	}
	if rep.Goasm.Linked != 4 || rep.Goasm.Unlinked != 1 {
		t.Errorf("goasm report = %+v", rep.Goasm)
	}
	emitter := graph.ID("go://example.com/fixture/jit#Emitter.EmitAddLocalsInt")
	if n, ok := g.Node(emitter); !ok {
		t.Fatalf("Go layer has no %s", emitter)
	} else if !n.Generated {
		t.Errorf("%s is not marked generated", emitter)
	}
	for _, from := range []graph.ID{
		"goasm://jit/templates/arm64.goasm#AddLocalsInt",
		"goasm://jit/templates/amd64.goasm#AddLocalsInt",
	} {
		found := false
		for _, e := range g.Out(from) {
			if e.Kind == graph.KindGenerates && e.To == emitter && e.Derivation == graph.Declared {
				found = true
			}
		}
		if !found {
			t.Errorf("%s -generates-> %s did not resolve", from, emitter)
		}
	}
	var dropped []string
	for _, d := range g.Dropped() {
		dropped = append(dropped, string(d.Edge.From)+" -> "+string(d.Edge.To)+" ("+string(d.Reason)+")")
	}
	want := []string{"goasm://jit/templates/amd64.goasm#Breakpoint -> go://example.com/fixture/jit#Emitter.EmitBreakpoint (unknown_target)"}
	if strings.Join(dropped, "\n") != strings.Join(want, "\n") {
		t.Errorf("dropped = %v\nwant %v", dropped, want)
	}
}

// TestProtoReachesScreen is ADR 007's motivating question: the ts fixture
// carries a .proto, the protoc-gen-es file written from it, and a
// SvelteKit page that imports the generated enum, so a message's
// proto:// node reaches the screen in two hops — generates to the
// export, references back from the page — with every generates edge
// resolved against the .proto layer.
func TestProtoReachesScreen(t *testing.T) {
	root := filepath.Join("..", "adapter", "ts", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Layers(); strings.Join(got, ",") != "proto,ts" {
		t.Fatalf("Layers = %v", got)
	}
	if rep.TS.Generates != 10 || rep.TS.ProtoUnmatched != 0 || rep.TS.Unresolved != 0 {
		t.Errorf("ts report = %+v", rep.TS)
	}
	for _, d := range g.Dropped() {
		t.Errorf("dropped: %+v", d)
	}
	for _, tt := range []struct{ from, to string }{
		{"proto://file/proto/app/v1/user.proto", "ts://web/gen/app/v1/user_pb.ts"},
		{"proto://app.v1.Role", "ts://web/gen/app/v1/user_pb.ts#Role"},
		{"proto://app.v1.User", "ts://web/gen/app/v1/user_pb.ts#UserSchema"},
		{"proto://app.v1.UserService", "ts://web/gen/app/v1/user_pb.ts#UserService"},
	} {
		found := false
		for _, e := range g.Out(graph.ID(tt.from)) {
			if e.Kind == graph.KindGenerates && string(e.To) == tt.to && e.Derivation == graph.Declared {
				found = true
			}
		}
		if !found {
			t.Errorf("%s -generates-> %s did not resolve", tt.from, tt.to)
		}
	}
	role := graph.ID("proto://app.v1.Role")
	page := graph.ID("ts://web/src/routes/+page.svelte")
	if hops := reach(g, role, page, 2); hops != 2 {
		t.Errorf("%s reaches %s in %d hops, want 2", role, page, hops)
	}
}

// TestPXFTypeReachesProto is #4's second done-when: the pxf fixture also
// carries the .proto that declares its @type, so the references edge
// from document to message resolves at merge.
func TestPXFTypeReachesProto(t *testing.T) {
	root := filepath.Join("..", "adapter", "pxf", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Layers(); strings.Join(got, ",") != "proto,pxf" {
		t.Fatalf("Layers = %v", got)
	}
	msg := graph.ID("proto://example.v1.Config")
	for _, doc := range []string{"pxf://config/app.pxf", "pxf://layers/base.pxf", "pxf://layers/env/prod.pxf"} {
		found := false
		for _, e := range g.Out(graph.ID(doc)) {
			if e.Kind == graph.KindReferences && e.To == msg {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not reference %s", doc, msg)
		}
	}
	for _, e := range g.In(graph.ID("proto://example.v1.Row")) {
		if e.From == "pxf://data/rows.pxf#@dataset" {
			return
		}
	}
	t.Error("the dataset does not reference its row message")
}

// TestMarkdownLinksToSymbols is #5's third done-when: the Markdown
// fixture is a doc set with known-correct expected edges — it carries the
// Go, .proto and .pxf its docs mention — and exactly those edges appear.
func TestMarkdownLinksToSymbols(t *testing.T) {
	root := filepath.Join("..", "adapter", "markdown", "testdata", "fixture")
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Layers(); strings.Join(got, ",") != "go,proto,pxf,md" {
		t.Fatalf("Layers = %v", got)
	}
	want := map[string]bool{
		"md://README.md#fixture -> pxf://governance.pxf":                                              true,
		"md://README.md#fixture -> proto://file/api/v1/evaluations.proto":                             true,
		"md://README.md#fixture.symbols -> go://example.com/fixture/synth#Report":                     true,
		"md://README.md#fixture.symbols -> go://example.com/fixture/synth#Render":                     true,
		"md://README.md#fixture.symbols -> proto://example.api.v1.EvaluationsService.Create":          true,
		"md://docs/design/overview.md#overview.details.deep -> go://example.com/fixture/synth#Report": true,
	}
	got := map[string]bool{}
	for e := range g.Edges() {
		if e.Kind == graph.KindDocuments {
			got[string(e.From)+" -> "+string(e.To)] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing documents edge %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected documents edge %s — Config is ambiguous, INFERRED/Missing/app.users name nothing, prose is never scanned", k)
		}
	}
	m := rep.Markdown
	if m.Resolved != 6 || m.Ambiguous != 1 || m.Unmatched != 3 {
		t.Errorf("link report: resolved %d ambiguous %d unmatched %d", m.Resolved, m.Ambiguous, m.Unmatched)
	}
}

// TestMarkdownLinksToCode is #68's done-when: a link to a Go file or a
// directory that exists is a references edge to the package there; a
// link to one that is gone is a dropped edge with unknown_target and the
// raw link, as a link to a missing document already was.
func TestMarkdownLinksToCode(t *testing.T) {
	root := filepath.Join("..", "adapter", "markdown", "testdata", "fixture")
	g, _, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	top := graph.ID("md://README.md#fixture")
	resolved := map[string]string{}
	for _, e := range g.Out(top) {
		if e.Kind == graph.KindReferences && strings.HasPrefix(string(e.To), "go://") {
			resolved[string(e.To)] = e.Attrs["link"]
		}
	}
	if want := map[string]string{"go://example.com/fixture/other": "other/config.go", "go://example.com/fixture/synth": "synth/"}; fmt.Sprint(resolved) != fmt.Sprint(want) {
		t.Errorf("resolved code links = %v, want %v", resolved, want)
	}
	dropped := map[string]string{}
	for _, d := range g.Dropped() {
		if d.Edge.From == top && strings.HasPrefix(string(d.Edge.To), "go://") {
			dropped[string(d.Edge.To)] = d.Edge.Attrs["link"] + " " + string(d.Reason)
		}
	}
	if want := map[string]string{"go://example.com/fixture/other/gone.go": "other/gone.go unknown_target", "go://example.com/fixture/flow": "flow/ unknown_target"}; fmt.Sprint(dropped) != fmt.Sprint(want) {
		t.Errorf("dropped code links = %v, want %v", dropped, want)
	}
}

func TestBuildWithoutGoModule(t *testing.T) {
	g, rep, err := Build(t.TempDir(), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Go != nil || rep.Proto != nil || rep.SQL != nil || rep.PXF != nil || rep.Goasm != nil || rep.TS != nil || rep.Markdown != nil || g.NodeCount() != 0 || len(rep.Layers()) != 0 {
		t.Errorf("empty repository built %d nodes with report %+v", g.NodeCount(), rep)
	}
}

func TestBuildBadRoot(t *testing.T) {
	if _, _, err := Build(filepath.Join(t.TempDir(), "missing"), DefaultOptions()); err == nil {
		t.Error("missing root built an empty graph instead of failing")
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Build(file, DefaultOptions()); err == nil {
		t.Error("file as root built an empty graph instead of failing")
	}
}

// TestRealRepository builds every layer of the module named by
// ALIDADE_REPO and reports what crossed between them: generates edges
// that resolved to a Go type are the .proto -> .pb.go link #3 exists for.
func TestRealRepository(t *testing.T) {
	root := os.Getenv("ALIDADE_REPO")
	if root == "" {
		t.Skip("set ALIDADE_REPO to a repository to measure")
	}
	g, rep, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	st := g.Stats()
	t.Logf("%s in %v, layers %v: %d nodes %v, %d edges %v, dropped %d %v, conflicts %d",
		rep.Root, rep.Elapsed.Round(time.Millisecond), rep.Layers(), st.Nodes, st.NodesByScheme, st.Edges,
		st.EdgesByDerivation, st.Dropped, st.DroppedByReason, st.Conflicts)
	if rep.Go != nil {
		t.Logf("go: %+v", *rep.Go)
	}
	if rep.Proto != nil {
		t.Logf("proto: %+v", *rep.Proto)
	}
	if rep.SQL != nil {
		t.Logf("sql: %+v", *rep.SQL)
	}
	if rep.PXF != nil {
		t.Logf("pxf: %+v", *rep.PXF)
	}
	if rep.Markdown != nil {
		m := *rep.Markdown
		m.Mentions = nil
		t.Logf("md: %+v (mentions %d)", m, len(rep.Markdown.Mentions))
	}
	generates, droppedGen := 0, 0
	for e := range g.Edges() {
		if e.Kind == graph.KindGenerates {
			generates++
		}
	}
	for _, d := range g.Dropped() {
		if d.Edge.Kind == graph.KindGenerates {
			droppedGen++
		} else {
			t.Errorf("dropped non-generates edge: %+v", d)
		}
	}
	t.Logf("generates: %d resolved to a Go type, %d dropped (no Go type in the graph)", generates, droppedGen)
	logDiagnostics(t, g)
}

// logDiagnostics shows the first few conflicts, dropped edges and failed
// files, which is where a real repository's surprises show up.
func logDiagnostics(t *testing.T, g *graph.Graph) {
	t.Helper()
	for i, c := range g.Conflicts() {
		if i == 8 {
			break
		}
		t.Logf("conflict: %s %s kept=%q discarded=%q", c.Subject, c.Field, c.Kept, c.Discarded)
	}
	for i, d := range g.Dropped() {
		if i == 5 {
			break
		}
		t.Logf("dropped: %s -%s-> %s", d.Edge.From, d.Edge.Kind, d.Edge.To)
	}
	failed := 0
	for n := range g.Nodes() {
		if e := n.Attrs["error"]; e != "" && failed < 6 {
			failed++
			t.Logf("failed: %s: %s", n.Pos.File, e)
		}
	}
}

// TestSchemaRootJoinsGeneratedCode is #47's done-when: the ts fixture
// split in two — its generated TypeScript and components in the primary
// root, its .proto in a schema root — resolves every message, enum and
// service generates edge against the schema root, its file nodes are
// labelled apart, and the schema root's Go types are not linked. The
// file-level generates edge, which is file-anchored, is reported
// unmatched rather than guessed across roots.
func TestSchemaRootJoinsGeneratedCode(t *testing.T) {
	primary, schema := splitFixture(t)
	opts := DefaultOptions()
	opts.SchemaRoots = []string{schema}
	g, rep, err := Build(primary, opts)
	if err != nil {
		t.Fatal(err)
	}
	checkSchemaReport(t, rep)
	if _, ok := g.Node("proto://file/@schemas/proto/app/v1/user.proto"); !ok {
		t.Error("schema root's file node is not labelled @schemas/")
	}
	for _, d := range g.Dropped() {
		t.Errorf("dropped: %+v", d)
	}
	if hops := reach(g, "proto://app.v1.Role", "ts://web/src/routes/+page.svelte", 2); hops != 2 {
		t.Errorf("Role reaches the page in %d hops, want 2", hops)
	}
	opts.SchemaRoots = []string{schema, schema}
	if _, _, err := Build(primary, opts); err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Errorf("the same schema root twice: err = %v", err)
	}
}

// splitFixture copies the ts fixture into two temporary roots: the
// generated TypeScript and components as the primary, the .proto and its
// buf.yaml as the schema root.
func splitFixture(t *testing.T) (primary, schema string) {
	t.Helper()
	src := filepath.Join("..", "adapter", "ts", "testdata", "fixture")
	primary, schema = filepath.Join(t.TempDir(), "app"), filepath.Join(t.TempDir(), "schemas")
	isSchema := func(rel string) bool { return strings.HasPrefix(rel, "proto/") || rel == "buf.yaml" }
	copyTree(t, src, primary, func(rel string) bool { return !isSchema(rel) })
	copyTree(t, src, schema, isSchema)
	return primary, schema
}

func checkSchemaReport(t *testing.T, rep *Report) {
	t.Helper()
	if got := rep.Layers(); strings.Join(got, ",") != "proto,ts" {
		t.Errorf("Layers = %v", got)
	}
	if len(rep.Schema) != 1 || rep.Schema[0].Label != "schemas" || rep.Schema[0].Proto.Files != 1 || rep.Schema[0].Proto.Generates != 0 {
		t.Errorf("Schema = %+v", rep.Schema)
	}
	if rep.Proto != nil || rep.TS.ProtoUnmatched != 1 || rep.TS.Generates != 9 {
		t.Errorf("Proto %v, ts report %+v", rep.Proto, rep.TS)
	}
}

// copyTree copies the files of src for which keep(rel) is true into dst.
func copyTree(t *testing.T, src, dst string, keep func(rel string) bool) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if !keep(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pxfOf renders a graph as PXF, for byte comparison.
func pxfOf(t *testing.T, g *graph.Graph) string {
	t.Helper()
	var buf bytes.Buffer
	if err := g.WritePXF(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// sessionFixture copies the sql fixture into a temporary root with a
// Markdown file added, and opens a Session on it.
func sessionFixture(t *testing.T) (*Session, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	copyTree(t, filepath.Join("..", "adapter", "sql", "testdata", "fixture"), root, func(string) bool { return true })
	write(t, filepath.Join(root, "docs.md"), "# Users\n\nThe `Lookup` function reads `users`.\n")
	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// rebuild builds through the session and checks which layers reran and
// which were kept.
func rebuild(t *testing.T, s *Session, wantRebuilt, wantKept string) *graph.Graph {
	t.Helper()
	g, rep, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rep.Rebuilt, ",") != wantRebuilt || strings.Join(rep.Kept, ",") != wantKept {
		t.Errorf("rebuilt %v kept %v; want %q, %q", rep.Rebuilt, rep.Kept, wantRebuilt, wantKept)
	}
	return g
}

// sameAsFresh checks a session's graph against a fresh build's bytes.
func sameAsFresh(t *testing.T, root string, g *graph.Graph) {
	t.Helper()
	fresh, _, err := Build(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if pxfOf(t, g) != pxfOf(t, fresh) {
		t.Error("the session's graph differs from a fresh build's")
	}
}

// TestSessionKeepsUntouchedLayers pins #56: a rebuild with nothing
// changed keeps every layer and gives the same bytes; a file rewritten
// with identical content keeps its layer (content, not mtime); an edited
// SQL file reruns the SQL layer only, and the graph equals a fresh
// build's; an edited Markdown file reruns Markdown only.
func TestSessionKeepsUntouchedLayers(t *testing.T) {
	s, root := sessionFixture(t)
	g1, rep1, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep1.Rebuilt)+len(rep1.Kept) != 0 || strings.Join(rep1.Layers(), ",") != "go,sql,md" {
		t.Fatalf("first build: rebuilt %v kept %v layers %v", rep1.Rebuilt, rep1.Kept, rep1.Layers())
	}
	if g2 := rebuild(t, s, "", "go,sql,md"); pxfOf(t, g2) != pxfOf(t, g1) {
		t.Error("no change: bytes differ")
	}
	write(t, filepath.Join(root, "docs.md"), "# Users\n\nThe `Lookup` function reads `users`.\n")
	rebuild(t, s, "", "go,sql,md") // same content, new mtime

	write(t, filepath.Join(root, "db", "migrations", "002_extra.sql"), "CREATE TABLE audit (id bigint PRIMARY KEY, note text);\n")
	g4 := rebuild(t, s, "sql", "go,md")
	if _, ok := g4.Node("sql://table/audit"); !ok {
		t.Error("sql edit: the new table is not in the graph")
	}
	sameAsFresh(t, root, g4)

	write(t, filepath.Join(root, "docs.md"), "# Users\n\nThe `Lookup` function reads `users` and `audit`.\n")
	sameAsFresh(t, root, rebuild(t, s, "md", "go,sql"))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRealRebuild measures #56 on ALIDADE_REPO: a first build, a rebuild
// with nothing changed, and a rebuild after a Markdown file is touched
// with a new line then restored; skipped when unset. The tree is left
// as it was.
func TestRealRebuild(t *testing.T) {
	root := os.Getenv("ALIDADE_REPO")
	if root == "" {
		t.Skip("ALIDADE_REPO not set")
	}
	s, err := NewSession(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, rep, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first build: %s (signatures %s), layers %v", rep.Elapsed.Round(time.Millisecond), rep.Signature.Round(time.Millisecond), rep.Layers())
	_, rep, err = s.Build()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("no change: %s, rebuilt %v, kept %v", rep.Elapsed.Round(time.Millisecond), rep.Rebuilt, rep.Kept)
	readme := filepath.Join(root, "README.md")
	orig, err := os.ReadFile(readme)
	if err != nil {
		t.Skip("no README.md to touch")
	}
	defer os.WriteFile(readme, orig, 0o644)
	if err := os.WriteFile(readme, append(orig, []byte("\n<!-- alidade rebuild test -->\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rep, err = s.Build()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("README edited: %s, rebuilt %v, kept %v", rep.Elapsed.Round(time.Millisecond), rep.Rebuilt, rep.Kept)
}

// TestRebuiltNamesPresentLayersOnly pins #61: a Go edit changes the
// goasm signature bucket too (it reads .go files for directives), but a
// repository with no .goasm file did not rebuild a goasm layer.
func TestRebuiltNamesPresentLayersOnly(t *testing.T) {
	s, root := sessionFixture(t)
	if _, _, err := s.Build(); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "app", "app.go")
	src, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	write(t, main, string(src)+"\n// edited\n")
	rebuild(t, s, "go", "sql,md")
}
