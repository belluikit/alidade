// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/trendvidia/alidade/internal/graph"
)

// The schema catalog: every relation and column as the migrations leave
// them, built by applying each schema file's events in the order sqlc
// reads the files (#119). A relation two schema sets both declare — one
// database's tables written into two packages' schema files — keeps every
// declaration site, as the graph always has, so its node is still the
// merge of them.

// A site is one place a relation or column is declared: file as the file
// system names it, for its node's position, and rel as a position shows
// it, for the attributes that name a site (#124).
type site struct {
	file, rel string
	line      int
	cfgs      []*sqlcConfig
}

func (s site) String() string { return s.rel + ":" + strconv.Itoa(s.line) }

type colState struct {
	name, typ string
	sites     []site
	// amended lists the ADD COLUMNs that re-declared it (#69); renamed
	// the RENAMEs it went through, from being its first name.
	amended     []string
	from        string
	renamedAt   []string
	tableOrphan bool // added to a relation no schema file declares
}

type relState struct {
	name               string
	view, materialized bool
	sites              []site
	cols               []*colState
	from               string
	renamedAt          []string
}

func (r *relState) col(name string) (int, *colState) {
	for i, c := range r.cols {
		if c.name == name {
			return i, c
		}
	}
	return -1, nil
}

type schemaCatalog struct {
	rels map[string]*relState
	// late are the ADD COLUMNs of a relation not declared when they were
	// read — an upgrade script sorted before the schema it upgrades —
	// applied once every file has been (#69).
	late []lateAdd
	rep  *Report
}

type lateAdd struct {
	ev ddlEvent
	at site
}

// resolve finds a relation by a name as a statement writes it, by the
// rule a query's names resolve by (see lookupTable).
func (c *schemaCatalog) resolve(name string) *relState {
	if r, ok := c.rels[name]; ok {
		return r
	}
	if bare, ok := strings.CutPrefix(name, "public."); ok {
		return c.rels[bare]
	}
	if strings.Contains(name, ".") {
		return nil
	}
	if r, ok := c.rels["public."+name]; ok {
		return r
	}
	var found *relState
	for key, r := range c.rels {
		if _, bare := splitTable(key); bare == name {
			if found != nil {
				return nil // two schemas: a guess either way
			}
			found = r
		}
	}
	return found
}

// apply applies one event read at a site.
func (c *schemaCatalog) apply(ev ddlEvent, at site) {
	at.line = ev.line
	switch ev.kind {
	case evCreateTable, evCreateView:
		c.create(ev, at)
	case evAddColumn:
		c.add(ev, at, false)
	case evDropRelation:
		c.rep.Drops++
		if r := c.resolve(ev.table); r != nil {
			delete(c.rels, r.name)
		}
	case evDropColumn:
		c.rep.ColumnDrops++
		c.dropColumn(ev)
	case evRenameColumn:
		c.rep.Renames++
		c.renameColumn(ev, at)
	case evRenameRelation:
		c.rep.Renames++
		c.rename(ev, at)
	}
}

// dropColumn applies DROP COLUMN.
func (c *schemaCatalog) dropColumn(ev ddlEvent) {
	if r := c.resolve(ev.table); r != nil {
		if i, _ := r.col(ev.from); i >= 0 {
			r.cols = slices.Delete(r.cols, i, i+1)
		}
	}
}

// renameColumn applies RENAME COLUMN: the column keeps its declaration
// and records the name it was declared under and where it was renamed.
func (c *schemaCatalog) renameColumn(ev ddlEvent, at site) {
	r := c.resolve(ev.table)
	if r == nil {
		return
	}
	if _, col := r.col(ev.from); col != nil {
		if col.from == "" {
			col.from = col.name
		}
		col.name = ev.to
		col.renamedAt = append(col.renamedAt, at.String())
	}
}

// create declares a relation, or adds a declaration site to one already
// declared — CREATE TABLE IF NOT EXISTS, or the same table in a second
// schema set — with its columns' sites alongside.
func (c *schemaCatalog) create(ev ddlEvent, at site) {
	r, ok := c.rels[ev.table]
	if !ok {
		r = &relState{name: ev.table, view: ev.kind == evCreateView, materialized: ev.materialized}
		c.rels[ev.table] = r
	}
	r.sites = append(r.sites, at)
	for _, cd := range ev.columns {
		if _, col := r.col(cd.name); col != nil {
			col.sites = append(col.sites, site{file: at.file, rel: at.rel, line: cd.line})
			continue
		}
		r.cols = append(r.cols, &colState{name: cd.name, typ: cd.typ, sites: []site{{file: at.file, rel: at.rel, line: cd.line}}})
	}
}

// add applies an ADD COLUMN: a new column, or, when the relation has it
// already, an amendment of it (#69). A relation not yet declared waits
// for the end; at the end, a column of a relation no file declares is
// still a column, contained by a relation that drops.
func (c *schemaCatalog) add(ev ddlEvent, at site, final bool) {
	r := c.resolve(ev.table)
	if r == nil && !final {
		c.late = append(c.late, lateAdd{ev, at})
		return
	}
	if r == nil {
		r = &relState{name: ev.table}
		c.rels[ev.table] = r
	}
	if _, col := r.col(ev.column.name); col != nil {
		col.amended = append(col.amended, at.String())
		c.rep.Amendments++
		return
	}
	r.cols = append(r.cols, &colState{name: ev.column.name, typ: ev.column.typ, sites: []site{at}, tableOrphan: len(r.sites) == 0})
}

// rename applies ALTER TABLE … RENAME TO, which keeps the relation in its
// schema.
func (c *schemaCatalog) rename(ev ddlEvent, at site) {
	r := c.resolve(ev.table)
	if r == nil {
		return
	}
	to := ev.to
	if schema, _ := splitTable(r.name); schema != "" && !strings.Contains(to, ".") {
		to = schema + "." + to
	}
	delete(c.rels, r.name)
	if r.from == "" {
		r.from = r.name
	}
	r.name = to
	r.renamedAt = append(r.renamedAt, at.String())
	c.rels[to] = r
}

// schema builds the catalog from every schema file, in sqlc's order, and
// emits it.
func (x *extractor) schema(sources map[string]string) error {
	c := &schemaCatalog{rels: map[string]*relState{}, rep: x.report}
	order, unread := x.c.schemaOrder()
	x.report.SchemaUnread += unread
	for _, f := range order {
		if isDown(f) {
			x.report.DownMigrations++
			continue
		}
		rel, err := x.m.PosPath(f)
		if err != nil {
			return fmt.Errorf("sql: %w", err)
		}
		_, cfgs := x.c.configFor(f)
		for _, ev := range scanDDL(sources[f]) {
			c.apply(ev, site{file: f, rel: string(rel), cfgs: cfgs})
		}
	}
	for _, l := range c.late {
		c.add(l.ev, l.at, true)
	}
	names := make([]string, 0, len(c.rels))
	for name := range c.rels {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := x.relation(c.rels[name]); err != nil {
			return err
		}
	}
	return nil
}

// relation emits one relation of the final catalog: a node per
// declaration site, the generates edges to each package's struct, and its
// columns. The rename history goes on the first site's contribution, so
// the merged node carries it once.
func (x *extractor) relation(r *relState) error {
	id, err := x.tableID(r.name)
	if err != nil {
		for _, s := range r.sites {
			x.skip(s.file, s.line, err)
		}
		return nil
	}
	schema, bare := splitTable(r.name)
	kind := graph.KindTable
	if r.view {
		kind = graph.KindView
	}
	for i, s := range r.sites {
		attrs := r.attrs(schema, i == 0)
		if r.view {
			x.report.Views++
		} else {
			x.report.Tables++
		}
		if err := x.b.AddNode(graph.Node{ID: id, Kind: kind, Name: bare, Pos: graph.Position{File: s.file, Line: s.line, Col: 1}, Attrs: attrs}); err != nil {
			return err
		}
		if err := x.structs(id, s.file, s.line, s.cfgs, schema, bare); err != nil {
			return err
		}
	}
	x.declare(r.name, bare, id)
	for _, col := range r.cols {
		if err := x.columnState(id, r.name, col); err != nil {
			return err
		}
	}
	return nil
}

// attrs are a relation's attributes on one declaration's contribution:
// its rename history only on the first, so the merged node has it once.
func (r *relState) attrs(schema string, first bool) map[string]string {
	attrs := map[string]string{}
	if schema != "" {
		attrs["schema"] = schema
	}
	if r.materialized {
		attrs["materialized"] = "true"
	}
	if first && r.from != "" {
		attrs["renamed_from"], attrs["renamed"] = r.from, strings.Join(r.renamedAt, " ")
	}
	return attrs
}

// columnState emits one column of the final catalog, a contribution per
// declaration site, its amendments and rename history on the first.
func (x *extractor) columnState(table graph.ID, tableName string, col *colState) error {
	id, err := x.m.SQLID(graph.SQLColumn, tableName+"."+col.name)
	if err != nil {
		for _, s := range col.sites {
			x.skip(s.file, s.line, err)
		}
		return nil
	}
	x.columns[tableName] = append(x.columns[tableName], col.name)
	for i, s := range col.sites {
		x.report.Columns++
		attrs := map[string]string{}
		if col.typ != "" {
			attrs["type"] = col.typ
		}
		if i == 0 && len(col.amended) > 0 {
			// In path order, not application order: where the
			// re-declarations are, whichever was read first (#69).
			attrs["amended"] = strings.Join(slices.Sorted(slices.Values(col.amended)), " ")
		}
		if i == 0 && col.from != "" {
			attrs["renamed_from"], attrs["renamed"] = col.from, strings.Join(col.renamedAt, " ")
		}
		if err := x.b.AddNode(graph.Node{ID: id, Kind: graph.KindColumn, Name: col.name, Pos: graph.Position{File: s.file, Line: s.line, Col: 1}, Attrs: attrs}); err != nil {
			return err
		}
	}
	return x.b.AddEdge(graph.Edge{From: table, To: id, Kind: graph.KindContains, Derivation: graph.Extracted})
}
