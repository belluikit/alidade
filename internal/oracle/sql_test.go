// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// sqlFixture is a sqlc configuration with two migrations, the second
// dropping a column and adding a table, and two queries: one reading a
// column, one joining a second table that only its WHERE names.
var sqlFixture = map[string]string{
	"sqlc.yaml":          "version: \"2\"\nsql:\n  - engine: postgresql\n    schema: migrations\n    queries: query.sql\n    gen:\n      go:\n        package: db\n        out: db\n",
	"migrations/001.sql": "CREATE TABLE users (id bigint PRIMARY KEY, name text, legacy text);\nCREATE TABLE orgs (id bigint PRIMARY KEY);\n",
	"migrations/002.sql": "ALTER TABLE users DROP COLUMN legacy;\nCREATE TABLE teams (id bigint PRIMARY KEY, org_id bigint);\n",
	"query.sql":          "-- name: UserName :one\nSELECT name FROM users WHERE id = $1;\n\n-- name: TeamsOfOrg :many\nSELECT t.id FROM teams t JOIN orgs o ON o.id = t.org_id WHERE t.org_id = $1;\n",
}

func buildSQLFixture(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	if err := SQLCUsable(); err != nil {
		t.Skip(err.Error() + ": the SQL oracle is sqlc")
	}
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range sqlFixture {
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
	return g, dir
}

// TestSQLOracleAgrees: sqlc's catalog and queries, read through the json
// generator from a scratch copy, agree with the graph on the fixture —
// the dropped column gone from both, the JOIN-only table unconfirmed, not
// missing — and the repository is left untouched.
func TestSQLOracleAgrees(t *testing.T) {
	g, dir := buildSQLFixture(t)
	s, err := SQLC(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Configs != 1 || len(s.Failed) != 0 || len(s.Tables) != 3 || len(s.Queries) != 2 {
		t.Fatalf("configs %d, failed %v, tables %d, queries %d; want 1, none, 3, 2", s.Configs, s.Failed, len(s.Tables), len(s.Queries))
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !os.IsNotExist(err) {
		t.Error("the oracle wrote into the repository")
	}
	r := CompareSQL(g, s)
	if r.Tables != [2]int{3, 3} || r.Columns[0] != r.Columns[1] || r.Queries != [2]int{2, 2} || r.QueryTables[0] != r.QueryTables[1] {
		t.Errorf("the fixture disagrees:\n%s", r)
	}
	if r.Unconfirmed != 1 {
		t.Errorf("Unconfirmed = %d; want 1, the table only the JOIN names", r.Unconfirmed)
	}
}

// TestCompareSQLSeesDifferences: with a column removed from the graph and
// a query's edge to its table removed, the comparison reports exactly
// those.
func TestCompareSQLSeesDifferences(t *testing.T) {
	g, dir := buildSQLFixture(t)
	s, err := SQLC(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	m := graph.NewMinter(dir)
	column := sqlID(m, graph.SQLColumn, "users.name")
	var userName graph.ID
	for n := range g.Nodes() {
		if n.Kind == graph.KindQuery && n.Name == "UserName" {
			userName = n.ID
		}
	}
	cut := withoutSQL(t, g, dir, column, userName)
	r := CompareSQL(cut, s)
	if len(r.MissingColumns) != 1 || r.MissingColumns[0] != string(column) {
		t.Errorf("MissingColumns = %v; want only users.name", r.MissingColumns)
	}
	if len(r.MissingQTables) != 1 {
		t.Errorf("MissingQTables = %v; want UserName's users", r.MissingQTables)
	}
}

// withoutSQL copies g without the column node and without the queries
// edges into it or out of the query.
func withoutSQL(t *testing.T, g *graph.Graph, dir string, column, query graph.ID) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder(dir)
	for n := range g.Nodes() {
		if n.ID == column {
			continue
		}
		n.Pos.File = graph.FilePath(n.Pos.File)
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for e := range g.Edges() {
		if e.Kind == graph.KindQueries && (e.To == column || e.From == query) {
			continue
		}
		if err := b.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build()
}
