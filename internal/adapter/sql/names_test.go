// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// sqlTree writes files under a temporary root and extracts them.
func sqlTree(t *testing.T, files map[string]string) (*graph.Graph, *Report) {
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

// fq is a query of a file queries.sql at the root of a tree with no sqlc
// configuration, keyed by that file (#118).
func fq(name string) graph.ID { return id("query/queries.sql#" + name) }

func access(t *testing.T, g *graph.Graph, from, to graph.ID) string {
	t.Helper()
	e, ok := hasEdge(g, from, graph.KindQueries, to)
	if !ok {
		t.Errorf("missing edge %s -queries-> %s", from, to)
		return ""
	}
	return e.Attrs["access"]
}

func noEdge(t *testing.T, g *graph.Graph, from graph.ID, kind graph.EdgeKind, to graph.ID) {
	t.Helper()
	if _, ok := hasEdge(g, from, kind, to); ok {
		t.Errorf("unexpected edge %s -%s-> %s", from, kind, to)
	}
}

// TestUnquotedNamesFold pins #96: PostgreSQL folds an unquoted identifier
// to lower case, and the parser reports a query's names folded; a quoted
// one keeps its case.
func TestUnquotedNamesFold(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"schema.sql":  "CREATE TABLE Users (Email text);\nCREATE TABLE \"Mixed\" (\"Id\" int);\n",
		"queries.sql": "-- name: GetUser :one\nSELECT email FROM users;\n\n-- name: GetMixed :one\nSELECT \"Id\" FROM \"Mixed\";\n",
	})
	wantNode(t, g, id("table/users"), graph.KindTable)
	wantNode(t, g, id("column/users.email"), graph.KindColumn)
	wantNode(t, g, id("table/Mixed"), graph.KindTable)
	access(t, g, fq("GetUser"), id("table/users"))
	access(t, g, fq("GetUser"), id("column/users.email"))
	access(t, g, fq("GetMixed"), id("column/Mixed.Id"))
}

// TestAlterNamesTheDeclaredTable pins #96: "ALTER TABLE public.users"
// amends the users table "CREATE TABLE users" declared; PostgreSQL's
// clause order is IF EXISTS then ONLY; and pg_dump's identity clause is
// not a column.
func TestAlterNamesTheDeclaredTable(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE users (id bigint, name text);\n" +
			"ALTER TABLE public.users ADD COLUMN nickname text;\n" +
			"ALTER TABLE IF EXISTS ONLY users ADD COLUMN z int;\n" +
			"ALTER TABLE ONLY public.users ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME users_id_seq);\n" +
			"ALTER TABLE users RENAME COLUMN name TO full_name;\n" +
			"ALTER TABLE users RENAME CONSTRAINT a TO b;\n" +
			"ALTER TABLE users DROP COLUMN IF EXISTS z, ALTER COLUMN id DROP DEFAULT, DROP CONSTRAINT c;\n",
		"queries.sql": "-- name: Nick :one\nSELECT nickname FROM users;\n",
	})
	wantEdge(t, g, id("table/users"), graph.KindContains, id("column/users.nickname"), graph.Extracted)
	// z was added, then dropped: the current schema has no z (#119);
	// name was renamed to full_name, which keeps where it was declared.
	if _, ok := g.Node(id("column/users.z")); ok {
		t.Error("users.z was dropped and is still a node")
	}
	if n := wantNode(t, g, id("column/users.full_name"), graph.KindColumn); n.Attrs["renamed_from"] != "name" || n.Pos.Line != 1 {
		t.Errorf("users.full_name = %+v; want renamed from name, declared at line 1", n)
	}
	access(t, g, fq("Nick"), id("column/users.nickname"))
	for _, bad := range []graph.ID{"sql://column/public.users.nickname", "sql://column/users.GENERATED", "sql://column/users.generated", "sql://table/ONLY", "sql://table/only"} {
		if _, ok := g.Node(bad); ok {
			t.Errorf("phantom node %s", bad)
		}
	}
	if rep.Renames != 1 || rep.ColumnDrops != 1 {
		t.Errorf("Renames = %d, ColumnDrops = %d; want 1, 1 (a constraint and a default are not columns)", rep.Renames, rep.ColumnDrops)
	}
}

// TestWritesInsideWith pins #96: a DELETE inside a WITH writes its table,
// at table level as at column level.
func TestWritesInsideWith(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE users (id bigint);\nCREATE TABLE audit_log (user_id bigint);\n",
		"queries.sql": "-- name: Move :exec\nWITH moved AS (DELETE FROM users WHERE id = $1 RETURNING id)\n" +
			"INSERT INTO audit_log (user_id) SELECT id FROM moved;\n",
	})
	if a := access(t, g, fq("Move"), id("table/users")); a != "write" {
		t.Errorf("users access = %q, want write", a)
	}
	if a := access(t, g, fq("Move"), id("table/audit_log")); a != "write" {
		t.Errorf("audit_log access = %q, want write", a)
	}
}

// TestBareNamesResolveOnlyWhenOneSchemaAnswers pins #96: a bare name is
// the relation declared bare or in public, else the one schema declaring
// it; two schemas, neither public, are an ambiguity and no edge.
func TestBareNamesResolveOnlyWhenOneSchemaAnswers(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE billing.items (id int, cost int);\nCREATE TABLE shop.items (id int, title text);\n" +
			"CREATE TABLE app.accounts (id int);\nCREATE TABLE public.notes (id int);\nCREATE TABLE other.notes (id int);\n",
		"queries.sql": "-- name: Items :many\nSELECT id, title FROM items;\n\n" +
			"-- name: Accounts :many\nSELECT id FROM accounts;\n\n" +
			"-- name: Notes :many\nSELECT id FROM notes;\n",
	})
	noEdge(t, g, fq("Items"), graph.KindQueries, id("table/billing.items"))
	noEdge(t, g, fq("Items"), graph.KindQueries, id("table/shop.items"))
	if rep.TablesAmbiguous != 1 {
		t.Errorf("TablesAmbiguous = %d, want 1", rep.TablesAmbiguous)
	}
	access(t, g, fq("Accounts"), id("table/app.accounts"))
	access(t, g, fq("Notes"), id("table/public.notes"))
	noEdge(t, g, fq("Notes"), graph.KindQueries, id("table/other.notes"))
}

// TestSqlcParametersAreNotColumns pins #96: @name and sqlc.arg(name) are
// parameters, whatever columns the table has.
func TestSqlcParametersAreNotColumns(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE users (id bigint, email text, name text);\n",
		"queries.sql": "-- name: ByEmail :one\nSELECT id FROM users WHERE email = @name;\n\n" +
			"-- name: ByEmail2 :one\nSELECT id FROM users WHERE email = sqlc.arg(name);\n\n" +
			"-- name: ByEmail3 :many\nSELECT id FROM users WHERE email = ANY(sqlc.slice(name));\n",
	})
	for _, q := range []string{"ByEmail", "ByEmail2", "ByEmail3"} {
		access(t, g, fq(q), id("column/users.email"))
		noEdge(t, g, fq(q), graph.KindQueries, id("column/users.name"))
	}
}

// TestViewsAreRelations pins #96: a view is a node, a query naming it
// reaches it, and sqlc's struct for it is linked by the rule a table's is.
func TestViewsAreRelations(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"sqlc.yaml": "version: \"2\"\nsql:\n  - engine: postgresql\n    schema: schema.sql\n    queries: queries.sql\n" +
			"    gen:\n      go:\n        package: db\n        out: db\n",
		"schema.sql": "CREATE TABLE users (id bigint, active bool);\n" +
			"CREATE OR REPLACE VIEW active_users AS SELECT id FROM users WHERE active;\n" +
			"CREATE MATERIALIZED VIEW IF NOT EXISTS app.user_counts AS SELECT count(*) FROM users;\n",
		"queries.sql": "-- name: Active :many\nSELECT id FROM active_users;\n",
	})
	if n := wantNode(t, g, id("table/active_users"), graph.KindView); n.Name != "active_users" {
		t.Errorf("view name = %q", n.Name)
	}
	if n := wantNode(t, g, id("table/app.user_counts"), graph.KindView); n.Attrs["materialized"] != "true" {
		t.Errorf("materialized view attrs = %v", n.Attrs)
	}
	access(t, g, id("query/example.com/m/db#Active"), id("table/active_users"))
	if rep.Views != 2 {
		t.Errorf("Views = %d, want 2", rep.Views)
	}
	found := false
	for _, d := range g.Dropped() {
		found = found || d.Edge.From == id("table/active_users") && d.Edge.To == "go://example.com/m/db#ActiveUser"
	}
	if !found {
		t.Error("no generates edge from the view to sqlc's ActiveUser")
	}
}

// TestSqlcV1PackageOptions pins #96: a version 1 package's own
// emit_exact_table_names is honoured.
func TestSqlcV1PackageOptions(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.26\n",
		"sqlc.yaml":  "version: \"1\"\npackages:\n  - path: db\n    schema: schema.sql\n    queries: schema.sql\n    emit_exact_table_names: true\n",
		"schema.sql": "CREATE TABLE users (id bigint);\n",
	})
	found := false
	for _, d := range g.Dropped() {
		found = found || d.Edge.From == id("table/users") && d.Edge.To == "go://example.com/m/db#Users"
	}
	if !found {
		t.Errorf("no generates edge to db#Users; dropped %+v", g.Dropped())
	}
}

// TestNilNodesInTheTree pins a panic found on an in-house repository
// while #96 was measured: the walk for nested write targets meets a nil
// node — this query has the shape of the one that held it — and a reader
// of every node must take it.
func TestNilNodesInTheTree(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE app.projects (id uuid, title text, account_id bigint);\n" +
			"CREATE TABLE app.accounts (id bigint, owner_id uuid, closed_at timestamptz);\n" +
			"CREATE TABLE app.jobs (project_id uuid, created_at timestamptz, status text, result jsonb);\n",
		"queries.sql": `-- name: GetPartHistory :many
WITH visible AS (
    SELECT r.id, r.title
      FROM app.projects r
      JOIN app.accounts i ON i.id = r.account_id
     WHERE i.owner_id = sqlc.arg('user_id')::uuid
       AND i.closed_at IS NULL
)
SELECT vr.id::text AS project_id, vr.title, ej.created_at,
       (d.elem->>'score')::int AS score
  FROM visible vr
  JOIN app.jobs ej ON ej.project_id = vr.id
 CROSS JOIN LATERAL jsonb_array_elements(ej.result->'parts') AS d(elem)
 WHERE ej.status = 'done'
   AND ej.created_at >= now() - make_interval(days => sqlc.arg('window_days')::int)
 ORDER BY vr.title, ej.created_at;
`,
	})
	for _, table := range []string{"app.projects", "app.accounts", "app.jobs"} {
		if a := access(t, g, fq("GetPartHistory"), id("table/"+table)); a != "read" {
			t.Errorf("%s access = %q, want read", table, a)
		}
	}
}

// TestQueriesArePerPackage pins #118: sqlc scopes a query's name to its
// package, so GetUser in package a and GetUser in package b are two
// queries, each linked to its own method and with its own edges; and a
// schema both packages read links its table to both packages' structs.
func TestQueriesArePerPackage(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"sqlc.yaml": "version: \"2\"\nsql:\n" +
			"  - engine: postgresql\n    schema: db/schema.sql\n    queries: db/a.sql\n    gen:\n      go:\n        package: a\n        out: a\n" +
			"  - engine: postgresql\n    schema: db/schema.sql\n    queries: db/b.sql\n    gen:\n      go:\n        package: b\n        out: b\n",
		"db/schema.sql": "CREATE TABLE users (id bigint, email text);\nCREATE TABLE orders (id bigint);\n",
		"db/a.sql":      "-- name: GetUser :one\nSELECT id FROM users;\n",
		"db/b.sql":      "-- name: GetUser :one\nSELECT id FROM orders;\n",
	})
	qa, qb := id("query/example.com/m/a#GetUser"), id("query/example.com/m/b#GetUser")
	wantNode(t, g, qa, graph.KindQuery)
	wantNode(t, g, qb, graph.KindQuery)
	access(t, g, qa, id("table/users"))
	noEdge(t, g, qa, graph.KindQueries, id("table/orders"))
	access(t, g, qb, id("table/orders"))
	noEdge(t, g, qb, graph.KindQueries, id("table/users"))
	if rep.Queries != 2 {
		t.Errorf("Queries = %d, want 2", rep.Queries)
	}
	for _, c := range g.Conflicts() {
		t.Errorf("conflict %+v", c)
	}
	want := map[[2]graph.ID]bool{
		{qa, "go://example.com/m/a#Queries.GetUser"}:     false,
		{qb, "go://example.com/m/b#Queries.GetUser"}:     false,
		{id("table/users"), "go://example.com/m/a#User"}: false,
		{id("table/users"), "go://example.com/m/b#User"}: false,
	}
	for _, d := range g.Dropped() {
		if _, ok := want[[2]graph.ID{d.Edge.From, d.Edge.To}]; ok && d.Edge.Kind == graph.KindGenerates {
			want[[2]graph.ID{d.Edge.From, d.Edge.To}] = true
		}
	}
	for e, seen := range want {
		if !seen {
			t.Errorf("no generates edge %s -> %s", e[0], e[1])
		}
	}
}

// TestViewColumns pins #152: a view's columns are its SELECT's output
// columns, named as PostgreSQL names them, and a query reading one
// reaches it. The materialized view has the shape of an in-house one.
func TestViewColumns(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"schema.sql": "CREATE TABLE app.reviews (author_id bigint, result jsonb, created_at timestamptz, status text);\n" +
			"CREATE TABLE users (id bigint, name text);\n" +
			`CREATE MATERIALIZED VIEW app.author_part_scores AS
SELECT p.author_id,
       dim.value->>'name'              AS part,
       avg((dim.value->>'score')::int)::int AS mean_score,
       count(*)                         AS sample_size,
       max(p.created_at)                AS last_at
  FROM app.reviews p,
       jsonb_array_elements(p.result->'parts') AS dim
 WHERE p.status = 'done'
 GROUP BY p.author_id, dim.value->>'name';
CREATE VIEW named (uid, uname) AS SELECT id, name FROM users;
CREATE VIEW shapes AS SELECT id::text, lower(name), id + 1 FROM users UNION SELECT 'x', 'y', 2;
CREATE VIEW everything AS SELECT * FROM users;
`,
		"queries.sql": "-- name: TopPart :many\nSELECT author_id, mean_score FROM app.author_part_scores WHERE part = $1;\n",
	})
	view := "column/app.author_part_scores."
	for _, c := range []string{"author_id", "part", "mean_score", "sample_size", "last_at"} {
		wantNode(t, g, id(view+c), graph.KindColumn)
	}
	for _, c := range []string{"named.uid", "named.uname", "shapes.id", "shapes.lower"} {
		wantNode(t, g, id("column/"+c), graph.KindColumn)
	}
	for _, c := range []string{"named.id", "everything.id"} {
		if _, ok := g.Node(id("column/" + c)); ok {
			t.Errorf("column/%s: a view with its own column list, or SELECT *, should not have it", c)
		}
	}
	for _, c := range []string{"author_id", "mean_score", "part"} {
		access(t, g, fq("TopPart"), id(view+c))
	}
}
