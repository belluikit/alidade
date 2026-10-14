// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// TestRenamedColumnHasItsCurrentName pins #119 on a case found in-house: a
// migration renaming old_code to code leaves code,
// declared where old_code was, and a query reading it reaches it.
func TestRenamedColumnHasItsCurrentName(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"migrations/003_item_state.up.sql":      "CREATE TABLE shop.item_state (id bigint, old_code text);\n",
		"migrations/008_item_state_code.up.sql": "ALTER TABLE shop.item_state RENAME COLUMN old_code TO code;\n",
		"queries.sql":                           "-- name: Codes :many\nSELECT code FROM shop.item_state;\n",
	})
	if _, ok := g.Node(id("column/shop.item_state.old_code")); ok {
		t.Error("the old name is still a node")
	}
	n := wantNode(t, g, id("column/shop.item_state.code"), graph.KindColumn)
	if n.Pos.File != "migrations/003_item_state.up.sql" || n.Attrs["renamed_from"] != "old_code" ||
		n.Attrs["renamed"] != "migrations/008_item_state_code.up.sql:1" || n.Attrs["type"] != "text" {
		t.Errorf("code = %+v", n)
	}
	access(t, g, fq("Codes"), id("column/shop.item_state.code"))
	if rep.Renames != 1 {
		t.Errorf("Renames = %d, want 1", rep.Renames)
	}
}

// TestDropsAreApplied pins #119: a dropped table or column is gone, a
// table re-created after its drop is its new declaration, and a
// down-migration file is not schema.
func TestDropsAreApplied(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"m/001_init.up.sql":   "CREATE TABLE a (x int, y int);\nCREATE TABLE b (z int);\nCREATE TABLE gone (q int);\n",
		"m/001_init.down.sql": "DROP TABLE a;\nDROP TABLE b;\nDROP TABLE gone;\n",
		"m/002_change.up.sql": "ALTER TABLE a DROP COLUMN y;\nDROP TABLE gone;\nDROP TABLE b;\nCREATE TABLE b (w text);\n",
	})
	wantNode(t, g, id("column/a.x"), graph.KindColumn)
	for _, absent := range []string{"column/a.y", "table/gone", "column/gone.q", "column/b.z"} {
		if _, ok := g.Node(id(absent)); ok {
			t.Errorf("%s survived its drop", absent)
		}
	}
	if n := wantNode(t, g, id("table/b"), graph.KindTable); n.Pos.File != "m/002_change.up.sql" {
		t.Errorf("b is declared at %s, want its re-creation", n.Pos.File)
	}
	wantNode(t, g, id("column/b.w"), graph.KindColumn)
	if rep.DownMigrations != 1 || rep.Drops != 2 || rep.ColumnDrops != 1 {
		t.Errorf("DownMigrations = %d, Drops = %d, ColumnDrops = %d; want 1, 2, 1", rep.DownMigrations, rep.Drops, rep.ColumnDrops)
	}
}

// TestSchemaOrderIsSqlcs pins #119: files apply in the order sqlc reads
// them — a configuration's schema paths as listed — not in path order. A
// table created under schema/ and dropped by legacy/, listed after it, is
// gone, though legacy/ sorts first.
func TestSchemaOrderIsSqlcs(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"sqlc.yaml":      "version: \"2\"\nsql:\n  - engine: postgresql\n    schema: [schema, legacy]\n    queries: q.sql\n",
		"schema/001.sql": "CREATE TABLE old_things (id int);\nCREATE TABLE kept (id int);\n",
		"legacy/001.sql": "DROP TABLE old_things;\n",
		"q.sql":          "-- name: Kept :many\nSELECT id FROM kept;\n",
	})
	if _, ok := g.Node(id("table/old_things")); ok {
		t.Error("old_things survived the drop sqlc applies after it")
	}
	wantNode(t, g, id("table/kept"), graph.KindTable)
}

// TestRenamedTableKeepsItsSchema pins #119: ALTER TABLE … RENAME TO keeps
// the table in its schema, its columns move with it, and a query naming
// the new name reaches it.
func TestRenamedTableKeepsItsSchema(t *testing.T) {
	g, _ := sqlTree(t, map[string]string{
		"m/001.sql": "CREATE TABLE app.people (id int);\nALTER TABLE app.people RENAME TO users;\n",
		"q.sql":     "-- name: Users :many\nSELECT id FROM app.users;\n",
	})
	n := wantNode(t, g, id("table/app.users"), graph.KindTable)
	if n.Attrs["renamed_from"] != "app.people" {
		t.Errorf("app.users = %+v", n)
	}
	wantEdge(t, g, id("table/app.users"), graph.KindContains, id("column/app.users.id"), graph.Extracted)
	if _, ok := g.Node(id("table/app.people")); ok {
		t.Error("the old name is still a node")
	}
	access(t, g, id("query/q.sql#Users"), id("column/app.users.id"))
}

// TestNestedRollbacksAreNotSchema pins #119 on a layout found in-house: sqlc lists
// a schema directory without descending, so the DROPs of a rollback/
// directory inside migrations/ are not applied — and a nested file is
// counted as unread, not read.
func TestNestedRollbacksAreNotSchema(t *testing.T) {
	g, rep := sqlTree(t, map[string]string{
		"sqlc.yaml":                         "version: \"2\"\nsql:\n  - engine: postgresql\n    schema: migrations\n    queries: q.sql\n",
		"migrations/0039_reports.sql":       "CREATE TABLE app.reports (id int);\n",
		"migrations/rollback/0039_down.sql": "DROP TABLE IF EXISTS app.reports;\n",
		"q.sql":                             "-- name: Reports :many\nSELECT id FROM app.reports;\n",
	})
	wantNode(t, g, id("table/app.reports"), graph.KindTable)
	if rep.SchemaUnread != 1 || rep.Drops != 0 {
		t.Errorf("SchemaUnread = %d, Drops = %d; want 1, 0", rep.SchemaUnread, rep.Drops)
	}
}
