// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package sql is the SQL layer adapter: PostgreSQL tables and columns from
// migrations, named queries from sqlc query files, and the link from both
// to the Go that sqlc generates.
//
// It owns no SQL parser (ADR 000). Queries are parsed by
// github.com/sqlc-dev/oliphant, the pure-Go PostgreSQL grammar sqlc itself
// parses with, and the tables a query names come from that parse. What
// the adapter reads by convention is what sqlc defines as convention:
// sqlc.yaml for the schema, query and output mapping; "-- name: X :cmd"
// annotations for the queries; and, for tables, the CREATE TABLE and ALTER
// TABLE ... ADD COLUMN statements of the schema files, scanned for their
// names and column lists rather than parsed — the schema is where the
// parser's catalog would come from, and that catalog (sqlc's compiler) is
// not importable.
//
// What comes out:
//
//   - table, column and query nodes, keyed as ADR 002 keys SQL: by name,
//     never by file, since a table is the same table however the migration
//     that declares it is later split;
//   - contains (EXTRACTED): table → column;
//   - queries (RESOLVED): query → every table the parse names, with the
//     access — write for the target of INSERT, UPDATE, DELETE, MERGE or
//     COPY FROM, read otherwise — and a name bound by WITH excluded; and
//     query → every column of the catalog a column reference resolves to,
//     with the access — write for an INSERT column list or an UPDATE SET
//     target, read otherwise. A qualified reference resolves through the
//     statement's aliases; an unqualified one when exactly one table in
//     scope has the column, two being an ambiguity and no edge; "*"
//     expands to the scope's columns. A query the parser rejects falls
//     back to a token match over its text (FROM, JOIN, INTO, UPDATE),
//     INFERRED, and names no columns;
//   - generates (DECLARED, via=sqlc): query → the Go method sqlc emits for
//     it (Queries.<Name>), and table → the Go struct sqlc emits for it, by
//     sqlc's own naming rule (singularised, CamelCased, schema-prefixed
//     outside public, initialisms and renames honoured). The edges resolve
//     only when the Go layer saw the type or method, and drop otherwise.
//
// The schema is the current one (#119): each schema file's statements are
// applied in the order sqlc applies them — a configuration's schema paths
// as listed, the .sql files directly in each in name order, and then the
// files no configuration reaches — so a DROP removes, a RENAME renames
// (the column keeping where it was declared, with renamed_from and
// renamed), and a re-CREATE declares anew. What sqlc does not read, the
// layer does not either: a ".down.sql" file, every line after a rollback
// marker (goose, sql-migrate, dbmate, tern), and a file in a subdirectory
// of a configured schema directory. A schema whose rollback files sit
// beside its migrations under another name is read the way sqlc reads it,
// drops included. The one exception to statement order is an ADD COLUMN
// of a table not yet declared — an upgrade script sorted before the
// schema it upgrades — applied at the end; an ADD of a column the table
// already has is an amendment, recorded on it (#69).
//
// Names are PostgreSQL's: an unquoted identifier folds to lower case and a
// quoted one keeps its case; a bare name in a query is the relation
// declared bare or in public, else the one schema declaring it, and two
// schemas are an ambiguity, counted, never a guess. A view is a relation
// node of kind view, in the namespace tables share (#96).
package sql
