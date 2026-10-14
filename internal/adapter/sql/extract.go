// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"fmt"
	dirs "github.com/trendvidia/alidade/internal/scope"
	"os"
	"path/filepath"
	"strings"
	"time"

	oliphant "github.com/sqlc-dev/oliphant"
	"github.com/sqlc-dev/oliphant/ast"

	"github.com/trendvidia/alidade/internal/graph"
)

// Options controls the SQL layer.
type Options struct {
	// Skip are the directories left out besides the layer's own
	// (ADR 003, amended for #215): .alidade.pxf's list, vendor and
	// third_party by default. Nil is the default.
	Skip *dirs.Dirs
	// Testdata includes .sql files under testdata directories; out by
	// default, as for the other layers.
	Testdata bool
}

// DefaultOptions returns the layer's defaults.
func DefaultOptions() Options { return Options{} }

// A Report says what Extract found.
type Report struct {
	// Files is how many .sql files were read; Configs how many sqlc
	// packages were configured.
	Files, Configs int
	// Tables, Columns and Queries count emitted nodes. Amendments counts
	// the ADD COLUMNs of a column the corpus already declared — the
	// idempotent-schema idiom, a CREATE TABLE for a fresh database and
	// an ALTER TABLE ... ADD COLUMN IF NOT EXISTS for an existing one —
	// recorded on the column rather than declared again (#69).
	Tables, Columns, Queries, Amendments int
	// Generates counts nodes given a generates edge to Go.
	Generates int
	// Parsed counts queries whose tables came from the parser; Unparsed
	// those the parser rejected, whose tables came from the token match.
	Parsed, Unparsed int
	// ColumnRefs counts column references attributed to a column of the
	// catalog; ColumnsAmbiguous those two tables in scope could answer;
	// ColumnsUnknown those naming no catalog column — an expression, an
	// alias, a CTE's column.
	ColumnRefs, ColumnsAmbiguous, ColumnsUnknown int
	// Drops counts the DROP TABLE and DROP VIEW statements applied,
	// Renames the RENAMEs of a column or table, ColumnDrops the DROP
	// COLUMNs: the graph is the current schema (#119).
	Drops, Renames, ColumnDrops int
	// Views counts CREATE [MATERIALIZED] VIEWs, each a relation node.
	Views int
	// DownMigrations counts the down-migration files not read as
	// schema (#119): "*.down.sql", as sqlc skips them.
	DownMigrations int
	// SchemaUnread counts the .sql files inside a configured schema
	// directory that sqlc does not read — a subdirectory's, such as a
	// rollback/ beside the migrations — and so neither does the layer.
	SchemaUnread int
	// TablesAmbiguous counts query references to a bare table name that
	// several schemas declare, none of them public, which resolve to no
	// table (#96).
	TablesAmbiguous int
	// Testdata counts .sql files skipped under testdata directories.
	Testdata int
	// Skipped are the files that could not be read, the entries the walk
	// could not open, and the names no ID can carry — a column
	// "rate/sec" — each left out on its own (#92).
	Skipped []graph.Skip
	Elapsed time.Duration
}

// Extract reads every .sql file under root and adds the SQL layer to b.
func Extract(b *graph.Builder, root string, opts Options) (*Report, error) {
	start := time.Now()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("sql: %w", err)
	}
	c, err := discover(abs, !opts.Testdata, opts.Skip)
	if err != nil {
		return nil, fmt.Errorf("sql: discover under %s: %w", abs, err)
	}
	x := &extractor{b: b, m: b.Minter(), c: c, tables: map[string]tableInfo{}, byBare: map[string][]string{}, columns: map[string][]string{},
		report: &Report{Files: len(c.files), Configs: len(c.configs), Testdata: c.testdata}}
	for _, u := range c.unread {
		x.skip(u.path, 0, u.err)
	}
	// Schema first, every file, so a query file can refer to a table
	// declared anywhere: every CREATE TABLE before any ADD COLUMN, so an
	// ADD of a column the corpus declares is an amendment wherever the
	// two sit (#69).
	sources := make(map[string]string, len(c.files))
	for _, f := range c.files {
		src, err := os.ReadFile(f)
		if err != nil {
			x.skip(f, 0, err)
			continue
		}
		sources[f] = string(src)
	}
	if err := x.schema(sources); err != nil {
		return nil, err
	}
	for _, f := range c.files {
		if err := x.queries(f, sources[f]); err != nil {
			return nil, err
		}
	}
	x.report.Elapsed = time.Since(start)
	return x.report, nil
}

type extractor struct {
	b      *graph.Builder
	m      *graph.Minter
	c      *corpus
	tables map[string]tableInfo // by name as declared
	// byBare lists, per bare name, the schema-qualified names declaring
	// it, for a query that writes the name bare.
	byBare map[string][]string
	// columns lists each table's declared columns, by name as declared:
	// the catalog the column resolver reads.
	columns map[string][]string
	report  *Report
}

// skip records that something in file, at line, was left out.
func (x *extractor) skip(file string, line int, err error) {
	x.report.Skipped = append(x.report.Skipped, x.m.Skip(file, line, err))
}

// lookupTable finds a table or view by a name as a query writes it, and
// returns the name it was declared under. A qualified name is that
// relation, with "public." also answering a relation declared bare; a bare
// name is the relation declared bare or in public — PostgreSQL's default
// search path — and otherwise the one schema that declares it. A bare name
// two other schemas declare is ambiguous and answers nothing (#96): the
// search path that would decide is not in the tree, and dropping beats
// guessing.
func (x *extractor) lookupTable(name string) (string, tableInfo, bool) {
	canonical, ok := x.resolveTable(name)
	if !ok {
		return "", tableInfo{}, false
	}
	return canonical, x.tables[canonical], true
}

// resolveTable is lookupTable's rule.
func (x *extractor) resolveTable(name string) (canonical string, ok bool) {
	if _, ok := x.tables[name]; ok {
		return name, true
	}
	if bare, ok := strings.CutPrefix(name, "public."); ok {
		if _, ok := x.tables[bare]; ok {
			return bare, true
		}
		return "", false
	}
	if strings.Contains(name, ".") {
		return "", false
	}
	if _, ok := x.tables["public."+name]; ok {
		return "public." + name, true
	}
	if c := x.byBare[name]; len(c) == 1 {
		return c[0], true
	}
	return "", false
}

// ambiguousTable reports whether a bare name is declared in more than one
// schema, none of them public: the case lookupTable refuses.
func (x *extractor) ambiguousTable(name string) bool {
	_, ok := x.resolveTable(name)
	return !ok && len(x.byBare[name]) > 1
}

// catalog is the resolver's view of the schema.
func (x *extractor) catalog(name string) (string, []string, bool) {
	canonical, _, ok := x.lookupTable(name)
	if !ok {
		return "", nil, false
	}
	return canonical, x.columns[canonical], true
}

// tableInfo is what the queries pass needs to know about a table.
type tableInfo struct {
	id graph.ID
}

func (x *extractor) tableID(name string) (graph.ID, error) {
	id, err := x.m.SQLID(graph.SQLTable, name)
	if err != nil {
		return "", fmt.Errorf("sql: %w", err)
	}
	return id, nil
}

// structs emits the generates edge from a relation to the struct each
// package that reads its schema has sqlc emit for it: every package, a
// schema shared by two being two structs (#118).
func (x *extractor) structs(id graph.ID, rel string, line int, cfgs []*sqlcConfig, schema, bare string) error {
	for _, cfg := range cfgs {
		if cfg.goPkg == "" {
			continue
		}
		if err := x.generates(id, rel, line, cfg.goPkg, cfg.structName(schema, bare)); err != nil {
			return err
		}
	}
	return nil
}

// declare indexes a relation by its declared name and its bare name.
func (x *extractor) declare(name, bare string, id graph.ID) {
	if _, ok := x.tables[name]; !ok && name != bare {
		x.byBare[bare] = append(x.byBare[bare], name)
	}
	x.tables[name] = tableInfo{id: id}
	if _, ok := x.columns[name]; !ok {
		x.columns[name] = nil
	}
}

// queryColumns emits a queries edge from a query to each column of the
// catalog it references, with the access it makes, resolved per
// statement against the statement's own FROM set.
func (x *extractor) queryColumns(query graph.ID, q queryDecl, rel string, tree *ast.ParseResult) error {
	for _, rs := range tree.Stmts {
		if rs.Stmt == nil {
			continue
		}
		refs, ambiguous, unknown := resolveColumns(rs.Stmt, ctesOf(rs.Stmt), x.catalog)
		x.report.ColumnRefs += len(refs)
		x.report.ColumnsAmbiguous += ambiguous
		x.report.ColumnsUnknown += unknown
		for _, ref := range refs {
			id, err := x.m.SQLID(graph.SQLColumn, ref.table+"."+ref.column)
			if err != nil {
				x.skip(rel, q.line, err)
				continue
			}
			access := "read"
			if ref.write {
				access = "write"
			}
			if err := x.b.AddEdge(graph.Edge{From: query, To: id, Kind: graph.KindQueries, Derivation: graph.Resolved,
				Attrs: map[string]string{"access": access}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// generates emits the DECLARED link from a SQL declaration, at rel and
// line, to the Go sqlc names for it.
func (x *extractor) generates(from graph.ID, rel string, line int, pkg, decl string) error {
	to, err := x.m.GoID(pkg, decl)
	if err != nil {
		x.skip(rel, line, err)
		return nil
	}
	x.report.Generates++
	return x.b.AddEdge(graph.Edge{From: from, To: to, Kind: graph.KindGenerates, Derivation: graph.Declared,
		Attrs: map[string]string{"via": "sqlc"}})
}

// queries emits the named queries a file declares, each linked to the
// tables its text names and to the Go method sqlc emits for it.
func (x *extractor) queries(file, src string) error {
	qs := scanQueries(src)
	if len(qs) == 0 {
		return nil
	}
	pos, err := x.m.PosPath(file)
	if err != nil {
		return fmt.Errorf("sql: %w", err)
	}
	cfgs, _ := x.c.configFor(file)
	for _, pkg := range queryPackages(cfgs, string(pos)) {
		for _, q := range qs {
			if err := x.query(pkg, q, file); err != nil {
				return err
			}
		}
	}
	return nil
}

// A queryPackage is the package a query file is compiled in: the key its
// queries' IDs carry, and the Go package sqlc generates them into, ""
// when none.
type queryPackage struct{ key, goPkg string }

// queryPackages lists the packages that compile a query file, each once:
// every sqlc package listing it, keyed by the Go package it generates
// into — or, generating none, and for a file no package lists, by the
// file's own repo-relative path, which is never empty as a root
// directory's would be, and separates any two packages since sqlc
// refuses a name repeated within one (#118).
func queryPackages(cfgs []*sqlcConfig, rel string) []queryPackage {
	dir := rel
	var out []queryPackage
	seen := map[string]bool{}
	add := func(p queryPackage) {
		if !seen[p.key] {
			seen[p.key] = true
			out = append(out, p)
		}
	}
	for _, cfg := range cfgs {
		if cfg.goPkg != "" {
			add(queryPackage{key: cfg.goPkg, goPkg: cfg.goPkg})
		} else {
			add(queryPackage{key: dir})
		}
	}
	if len(out) == 0 {
		add(queryPackage{key: dir})
	}
	return out
}

// query emits one named query of one package: its node, its table and
// column edges, and the method sqlc generates for it.
func (x *extractor) query(pkg queryPackage, q queryDecl, rel string) error {
	id, err := x.m.SQLQueryID(pkg.key, q.name)
	if err != nil {
		x.skip(rel, q.line, err)
		return nil
	}
	x.report.Queries++
	if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindQuery, Name: q.name,
		Pos: graph.Position{File: rel, Line: q.line, Col: 1}, Attrs: map[string]string{"cmd": q.cmd}}); err != nil {
		return err
	}
	if err := x.queryTables(id, q, rel); err != nil {
		return err
	}
	if pkg.goPkg == "" {
		return nil
	}
	return x.generates(id, rel, q.line, pkg.goPkg, "Queries."+q.name)
}

// queryTables emits a queries edge for every known table a query names.
// The parser's answer is RESOLVED, with the access the statement makes;
// when the parser rejects the query, the token match over its text stands
// in, INFERRED. A name that is no table of the schema is not an edge
// either way.
func (x *extractor) queryTables(query graph.ID, q queryDecl, rel string) error {
	var refs []tableRef
	d := graph.Resolved
	tree, err := oliphant.Parse(q.raw)
	if err != nil {
		x.report.Unparsed++
		d = graph.Inferred
		for _, name := range tableRefs(q.text) {
			refs = append(refs, tableRef{name: name})
		}
	} else {
		x.report.Parsed++
		refs = tablesOf(tree)
		if err := x.queryColumns(query, q, rel, tree); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		_, t, ok := x.lookupTable(ref.name)
		if !ok && x.ambiguousTable(ref.name) {
			x.report.TablesAmbiguous++
		}
		if !ok {
			continue
		}
		e := graph.Edge{From: query, To: t.id, Kind: graph.KindQueries, Derivation: d}
		if d == graph.Resolved {
			e.Attrs = map[string]string{"access": "read"}
			if ref.write {
				e.Attrs["access"] = "write"
			}
		}
		if err := x.b.AddEdge(e); err != nil {
			return err
		}
	}
	return nil
}
