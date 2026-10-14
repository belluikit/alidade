// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

func id(s string) graph.ID { return graph.ID("sql://" + s) }

// qid is a query of the fixture's one sqlc package, which generates Go
// into internal/db (#118).
func qid(name string) graph.ID { return id("query/example.com/fixture/internal/db#" + name) }

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

func TestReport(t *testing.T) {
	_, rep := extract(t)
	if rep.Files != 4 || rep.Configs != 1 || rep.Tables != 4 || rep.Columns != 13 || rep.Queries != 13 || rep.Generates != 16 ||
		rep.Drops != 0 || rep.Parsed != 12 || rep.Unparsed != 1 {
		// The fixture's three DROP TABLEs are in goose Down sections,
		// which are not schema (#119).
		t.Errorf("report = %+v", rep)
	}
}

func TestColumnReport(t *testing.T) {
	_, rep := extract(t)
	if rep.ColumnRefs != 35 || rep.ColumnsAmbiguous != 1 || rep.ColumnsUnknown != 1 {
		t.Errorf("column report = refs %d ambiguous %d unknown %d", rep.ColumnRefs, rep.ColumnsAmbiguous, rep.ColumnsUnknown)
	}
}

func TestTablesAndColumns(t *testing.T) {
	g, _ := extract(t)
	users := wantNode(t, g, id("table/users"), graph.KindTable)
	if users.Pos.File != "db/migrations/001_initial.sql" || users.Pos.Line != 4 {
		t.Errorf("users position = %+v", users.Pos)
	}
	for _, c := range []string{"id", "email", "created_at", "metadata"} {
		wantEdge(t, g, id("table/users"), graph.KindContains, id("column/users."+c), graph.Extracted)
	}
	if c := wantNode(t, g, id("column/users.id"), graph.KindColumn); c.Attrs["type"] != "BIGSERIAL" {
		t.Errorf("users.id attrs = %v", c.Attrs)
	}
	// Constraints are not columns; a quoted name loses its quotes; a
	// trailing comment with a paren does not break the column list.
	for _, no := range []string{"column/api_keys.PRIMARY", "column/api_keys.CONSTRAINT", `column/api_keys."key"`} {
		if _, ok := g.Node(id(no)); ok {
			t.Errorf("unexpected node %s", no)
		}
	}
	wantNode(t, g, id("column/api_keys.key"), graph.KindColumn)
	// ADD COLUMN declares a column on an existing table, positioned in
	// the migration that adds it.
	c := wantNode(t, g, id("column/users.deleted_at"), graph.KindColumn)
	if c.Pos.File != "db/migrations/002_add_columns.sql" || c.Pos.Line != 2 {
		t.Errorf("deleted_at position = %+v", c.Pos)
	}
	wantEdge(t, g, id("table/users"), graph.KindContains, id("column/users.deleted_at"), graph.Extracted)
	wantNode(t, g, id("column/api_keys.label"), graph.KindColumn)
	// A schema-qualified table keeps its schema in the ID and attrs; a
	// block comment mentioning CREATE TABLE declares nothing.
	s := wantNode(t, g, id("table/app.sessions"), graph.KindTable)
	if s.Name != "sessions" || s.Attrs["schema"] != "app" {
		t.Errorf("app.sessions = %+v", s)
	}
	if got := len(g.Out(id("table/app.sessions"))); got != 3 {
		t.Errorf("app.sessions has %d edges, want 3 columns", got)
	}
	// A file under no configuration still declares its tables.
	wantNode(t, g, id("table/seed_log"), graph.KindTable)
}

func TestQueries(t *testing.T) {
	g, _ := extract(t)
	q := wantNode(t, g, qid("GetUser"), graph.KindQuery)
	if q.Attrs["cmd"] != "one" || q.Pos.File != "db/queries/users.sql" || q.Pos.Line != 1 {
		t.Errorf("GetUser = %+v", q)
	}
	if q := wantNode(t, g, qid("DeleteSession"), graph.KindQuery); q.Attrs["cmd"] != "execrows" {
		t.Errorf("DeleteSession attrs = %v", q.Attrs)
	}
	// queries edges come from the parse: RESOLVED, with the access the
	// statement makes on each table.
	access := func(q, table, want string) {
		t.Helper()
		e := wantEdge(t, g, qid(q), graph.KindQueries, id("table/"+table), graph.Resolved)
		if e.Attrs["access"] != want {
			t.Errorf("%s -> %s access = %q, want %q", q, table, e.Attrs["access"], want)
		}
	}
	access("GetUser", "users", "read")
	access("CreateUser", "users", "write")
	access("ListUserKeys", "api_keys", "read")
	access("ListUserKeys", "users", "read")
	access("DeleteSession", "app.sessions", "write")
	access("UsersWithKeys", "users", "read")    // subquery's table too:
	access("UsersWithKeys", "api_keys", "read") // and sqlc.arg() parses
	access("TouchUser", "users", "write")       // UPDATE target
	access("TouchUser", "app.sessions", "read") // its FROM
	access("CopyUsers", "seed_log", "write")    // INSERT ... SELECT
	access("CopyUsers", "users", "read")
	// A CTE named users shadows the table: the query reads api_keys and
	// nothing else. The token match would have said users.
	access("RecentUsers", "api_keys", "read")
	if _, ok := hasEdge(g, qid("RecentUsers"), graph.KindQueries, id("table/users")); ok {
		t.Error("a CTE named users was taken for the table")
	}
	// An alias after a table name is not a table.
	if _, ok := g.Node(id("table/k")); ok {
		t.Error("alias k became a table")
	}
}

// TestUnparsedFallsBack: a query the parser rejects still links to the
// tables its text names, INFERRED, and the report counts it.
func TestUnparsedFallsBack(t *testing.T) {
	g, rep := extract(t)
	e := wantEdge(t, g, qid("Broken"), graph.KindQueries, id("table/users"), graph.Inferred)
	if e.Attrs["access"] != "" {
		t.Errorf("fallback edge claims access %q", e.Attrs["access"])
	}
	if rep.Unparsed != 1 {
		t.Errorf("Unparsed = %d", rep.Unparsed)
	}
}

func TestParseTables(t *testing.T) {
	for _, tt := range []struct{ sql, want string }{
		{"SELECT 1 FROM a JOIN b ON true", "a b"},
		{"SELECT 1 FROM public.a, s.b", "public.a s.b"},
		{"INSERT INTO a SELECT * FROM b", "a! b"},
		{"UPDATE a SET x = 1 FROM b WHERE a.id = b.id", "a! b"},
		{"DELETE FROM a USING b WHERE a.id = b.id", "a! b"},
		{"WITH a AS (SELECT 1 FROM c) SELECT * FROM a", "c"},
		{"WITH a AS (SELECT 1 FROM c) SELECT * FROM public.a", "c public.a"}, // qualified: not the CTE
		{"SELECT * FROM a WHERE x IN (SELECT y FROM a)", "a"},
		{"SELECT * FROM a; UPDATE a SET x = 1", "a!"},
		{"SELECT unnest($1::text[])", ""},
	} {
		refs, err := parseTables(tt.sql)
		if err != nil {
			t.Errorf("%q: %v", tt.sql, err)
			continue
		}
		var got []string
		for _, r := range refs {
			name := r.name
			if r.write {
				name += "!"
			}
			got = append(got, name)
		}
		want := strings.Fields(tt.want)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%q = %v, want %v", tt.sql, got, want)
		}
	}
	if _, err := parseTables("SELEC 1"); err == nil {
		t.Error("a syntax error parsed")
	}
}

// TestGeneratesFollowsSqlc: the Go names are sqlc's — Queries.<Name> for a
// query, the singularised CamelCase struct for a table, schema-prefixed
// outside public. The fixture's Go is not loaded here, so the edges drop
// at merge; the build package's test resolves them.
func TestGeneratesFollowsSqlc(t *testing.T) {
	g, _ := extract(t)
	want := map[string]string{
		"query/example.com/fixture/internal/db#GetUser":       "go://example.com/fixture/internal/db#Queries.GetUser",
		"query/example.com/fixture/internal/db#DeleteSession": "go://example.com/fixture/internal/db#Queries.DeleteSession",
		"table/users":        "go://example.com/fixture/internal/db#User",
		"table/api_keys":     "go://example.com/fixture/internal/db#ApiKey",
		"table/app.sessions": "go://example.com/fixture/internal/db#AppSession",
	}
	got := map[string]string{}
	for _, d := range g.Dropped() {
		if d.Edge.Kind != graph.KindGenerates || d.Reason != graph.DropUnknownTarget || d.Edge.Derivation != graph.Declared || d.Edge.Attrs["via"] != "sqlc" {
			t.Errorf("unexpected dropped edge %+v", d)
		}
		got[strings.TrimPrefix(string(d.Edge.From), "sql://")] = string(d.Edge.To)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("generates from %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["table/seed_log"]; ok {
		t.Error("a table outside every sqlc configuration got a generates edge")
	}
	if len(got) != 16 {
		t.Errorf("%d generates edges, want 16: %v", len(got), got)
	}
}

func TestNaming(t *testing.T) {
	n := defaultNaming()
	for _, tt := range []struct{ schema, table, want string }{
		{"", "users", "User"},
		{"public", "api_keys", "ApiKey"},
		{"app", "sessions", "AppSession"},
		{"", "user_ids", "UserID"},
		{"", "metadata", "Metadata"},
		{"", "campus", "Campus"},
		{"", "2fa_codes", "_2faCode"},
		{"trader", "decision_log", "TraderDecisionLog"},
	} {
		if got := n.structName(tt.schema, tt.table); got != tt.want {
			t.Errorf("structName(%q, %q) = %q, want %q", tt.schema, tt.table, got, tt.want)
		}
	}
	exact := defaultNaming()
	exact.exactTableNames = true
	if got := exact.structName("", "users"); got != "Users" {
		t.Errorf("exact table names: %q", got)
	}
	renamed := defaultNaming()
	renamed.rename["user"] = "Account"
	if got := renamed.structName("", "users"); got != "Account" {
		t.Errorf("rename: %q", got)
	}
	excluded := defaultNaming()
	excluded.exclusions = []string{"series"}
	if got := excluded.structName("", "series"); got != "Series" {
		t.Errorf("exclusion: %q", got)
	}
}

func TestScanDDLShapes(t *testing.T) {
	src := `
CREATE UNLOGGED TABLE IF NOT EXISTS "quoted" (
  a int, -- comment, with comma
  b numeric(10, 2) NOT NULL DEFAULT 0,
  c text[] CHECK (c <> '{}'),
  PRIMARY KEY (a),
  FOREIGN KEY (a) REFERENCES other (x)
);
CREATE TABLE s.t (x int);
ALTER TABLE ONLY s.t ADD COLUMN y jsonb DEFAULT '{}' NOT NULL;
DROP TABLE IF EXISTS s.t;
ALTER TABLE s.t DROP COLUMN x;
ALTER TABLE s.t ADD PRIMARY KEY (x);
ALTER TABLE s.t ADD CONSTRAINT c UNIQUE (y);
ALTER TABLE s.t
    ADD COLUMN p text,
    ADD COLUMN IF NOT EXISTS q numeric(10, 2) NOT NULL,
    ADD CONSTRAINT k UNIQUE (p);
`
	evs := scanDDL(src)
	var kinds []evKind
	for _, ev := range evs {
		kinds = append(kinds, ev.kind)
	}
	// In source order: the two CREATEs, the first ADD, the DROP TABLE, the
	// DROP COLUMN, then the multi-clause ALTER's two ADD COLUMNs; the
	// constraint clauses are not events (#119).
	want := []evKind{evCreateTable, evCreateTable, evAddColumn, evDropRelation, evDropColumn, evAddColumn, evAddColumn}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].table != "quoted" || evs[1].table != "s.t" {
		t.Errorf("tables = %q, %q", evs[0].table, evs[1].table)
	}
	checkShapes(t, evs)
}

func checkShapes(t *testing.T, evs []ddlEvent) {
	t.Helper()
	cols := evs[0].columns
	if len(cols) != 3 || cols[0].name != "a" || cols[1].typ != "numeric(10, 2)" || cols[2].typ != "text[]" || cols[1].line != 4 {
		t.Errorf("quoted columns = %+v (b on line 4)", cols)
	}
	got := [][3]string{
		{evs[2].table, evs[2].column.name, evs[2].column.typ},
		{evs[4].table, evs[4].from, ""},
		{evs[5].table, evs[5].column.name, evs[5].column.typ},
		{evs[6].table, evs[6].column.name, evs[6].column.typ},
	}
	want := [][3]string{{"s.t", "y", "jsonb"}, {"s.t", "x", ""}, {"s.t", "p", "text"}, {"s.t", "q", "numeric(10, 2)"}}
	if !slices.Equal(got, want) || evs[6].column.line != 17 {
		t.Errorf("ALTER clauses = %v (q on line %d), want %v (q on line 17)", got, evs[6].column.line, want)
	}
}

func TestScanQueriesAndRefs(t *testing.T) {
	src := "-- name: A :one\nSELECT 1 FROM t1 JOIN s.t2 ON true; -- FROM t3 in a comment\n\n-- name: B :exec\nUPDATE t4 SET x = 'FROM t5' WHERE y IN (SELECT y FROM t6);\n"
	qs := scanQueries(src)
	if len(qs) != 2 || qs[0].name != "A" || qs[0].cmd != "one" || qs[1].line != 4 {
		t.Fatalf("queries = %+v", qs)
	}
	if got := strings.Join(tableRefs(qs[0].text), ","); got != "t1,s.t2" {
		t.Errorf("refs of A = %q", got)
	}
	if got := strings.Join(tableRefs(qs[1].text), ","); got != "t4,t6" {
		t.Errorf("refs of B = %q (comments and string literals must not count)", got)
	}
}

func TestConfigForms(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "svc"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "svc", "sqlc.yaml"), []byte(`version: "1"
packages:
  - path: gen
    queries: q/
    schema: s.sql
rename:
  user: Person
`), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sqlc.json"), []byte(`{"version":"2","sql":[{"schema":["a","b"],"queries":"q","gen":{"go":{"out":".","emit_exact_table_names":true,"initialisms":["id","url"]}}}]}`), 0o644))
	c, err := discover(dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.module != "example.com/m" || len(c.configs) != 2 {
		t.Fatalf("corpus = %+v", c)
	}
	checkConfigs(t, c, dir)
}

func checkConfigs(t *testing.T, c *corpus, dir string) {
	t.Helper()
	v2, v1 := c.configs[0], c.configs[1]
	if v2.goPkg != "example.com/m" || !v2.exactTableNames || !v2.initialisms["url"] || len(v2.schema) != 2 {
		t.Errorf("v2 config = %+v", v2)
	}
	if v1.goPkg != "example.com/m/svc/gen" || v1.rename["user"] != "Person" || !strings.HasSuffix(v1.queries[0], filepath.Join("svc", "q")) {
		t.Errorf("v1 config = %+v", v1)
	}
	q, s := c.configFor(filepath.Join(dir, "svc", "q", "x.sql"))
	if len(q) != 1 || q[0].goPkg != v1.goPkg || len(s) != 0 {
		t.Errorf("configFor query file = %v, %v", q, s)
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

func TestEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil || rep.Files != 0 || b.Build().NodeCount() != 0 {
		t.Errorf("empty repository: rep=%+v err=%v", rep, err)
	}
}

// TestColumnRefs is #33's done-when: qualified references through aliases,
// unqualified ones unique to a table, "*", INSERT and UPDATE targets as
// writes; an ambiguous unqualified name and a CTE's own column make none.
func TestColumnRefs(t *testing.T) {
	g, _ := extract(t)
	col := func(q, column, access string) {
		t.Helper()
		e := wantEdge(t, g, qid(q), graph.KindQueries, id("column/"+column), graph.Resolved)
		if e.Attrs["access"] != access {
			t.Errorf("%s -> %s access = %q, want %q", q, column, e.Attrs["access"], access)
		}
	}
	none := func(q, column string) {
		t.Helper()
		if _, ok := hasEdge(g, qid(q), graph.KindQueries, id("column/"+column)); ok {
			t.Errorf("%s -> %s should not exist", q, column)
		}
	}
	col("KeysWithOwner", "api_keys.key", "read")   // k."key", through the alias
	col("KeysWithOwner", "users.email", "read")    // u.email
	col("KeysWithOwner", "api_keys.label", "read") // unqualified, only api_keys has it
	col("KeysWithOwner", "api_keys.expires_at", "read")
	col("KeysWithOwner", "users.id", "read")    // the join condition
	none("SessionsAndKeys", "api_keys.user_id") // user_id: both tables in scope have it
	none("SessionsAndKeys", "app.sessions.user_id")
	col("SessionsAndKeys", "app.sessions.token", "read")
	for _, c := range []string{"user_id", "key", "expires_at", "label"} {
		col("AllKeys", "api_keys."+c, "read") // *
	}
	col("CreateUser", "users.email", "write")      // INSERT column list
	col("TouchUser", "users.email", "write")       // UPDATE SET
	col("TouchUser", "app.sessions.token", "read") // its WHERE, through the alias
	col("TouchUser", "users.id", "read")           // users.id in the WHERE, qualified by table name
	col("CopyUsers", "seed_log.ran_at", "write")   // INSERT ... SELECT target column
	col("CopyUsers", "users.created_at", "read")   // its source
	col("RecentUsers", "api_keys.user_id", "read") // inside the CTE body
	col("RecentUsers", "api_keys.expires_at", "read")
	none("RecentUsers", "users.id")                  // "id" names the CTE's column, not the table's
	col("UsersWithKeys", "api_keys.user_id", "read") // inside the subquery
	if _, ok := hasEdge(g, qid("Broken"), graph.KindQueries, id("column/users.id")); ok {
		t.Error("an unparsed query named columns")
	}
}

// TestIdempotentSchemaAmendsColumns is #69's done-when: a CREATE TABLE
// and a later ALTER TABLE ... ADD COLUMN IF NOT EXISTS of the same column
// — a fresh database takes the first, an upgraded one the second — are
// one column, positioned at the CREATE site, with the ADD recorded on it
// as "amended" rather than reported as a position conflict; the ALTER
// COLUMN ... DROP DEFAULT that completes the idiom adds nothing. The
// CREATE wins wherever it sits: here it is in the file that sorts second.
func TestIdempotentSchemaAmendsColumns(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a_upgrade.sql": "ALTER TABLE attempts ADD COLUMN IF NOT EXISTS machine text NOT NULL DEFAULT '';\n" +
			"ALTER TABLE attempts ALTER COLUMN machine DROP DEFAULT;\n" +
			"ALTER TABLE attempts ADD COLUMN IF NOT EXISTS note text;\n",
		"b_schema.sql": "CREATE TABLE IF NOT EXISTS attempts (\n    id bigint PRIMARY KEY,\n    machine text NOT NULL\n);\n" +
			"ALTER TABLE attempts ADD COLUMN IF NOT EXISTS machine text NOT NULL DEFAULT '';\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := graph.NewBuilder(dir)
	rep, err := Extract(b, dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	if c := g.Conflicts(); len(c) != 0 {
		t.Errorf("conflicts: %+v", c)
	}
	if rep.Columns != 3 || rep.Amendments != 2 {
		t.Errorf("report = %+v; want 3 columns, 2 amendments", rep)
	}
	checkColumn(t, g, "attempts.machine", graph.Position{File: "b_schema.sql", Line: 3, Col: 1}, "a_upgrade.sql:1 b_schema.sql:5")
	checkColumn(t, g, "attempts.note", graph.Position{File: "a_upgrade.sql", Line: 3, Col: 1}, "")
}

func checkColumn(t *testing.T, g *graph.Graph, col string, pos graph.Position, amended string) {
	t.Helper()
	n, ok := g.Node(id("column/" + col))
	if !ok || n.Pos != pos || n.Attrs["amended"] != amended || n.Attrs["type"] != "text" {
		t.Errorf("%s = %+v, %v; want position %+v, amended %q", col, n, ok, pos, amended)
	}
}
