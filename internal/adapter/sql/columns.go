// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"strings"

	"github.com/sqlc-dev/oliphant/ast"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Column attribution: which column of which table each column reference
// in a query names. The parse gives the references and the statement's
// FROM set; the layer's own catalog — the columns each CREATE TABLE and
// ADD COLUMN declared — resolves unqualified names and expands "*". Every
// rule is conservative: a name two tables in scope could answer is an
// ambiguity and no edge, and a name the catalog does not know (an alias,
// a CTE's column, an expression) is no edge.

// A catalog answers a table name — as written, schema-qualified or bare —
// with its canonical name and its columns.
type catalog func(name string) (canonical string, columns []string, ok bool)

// A columnRef is one resolved reference: a column of a table, read or
// written.
type columnRef struct {
	table, column string
	write         bool
}

// A scope is one statement's FROM set: the names a column reference may
// qualify by, each mapped to a canonical table, or to "" for a name that
// is not a table (a CTE, a subquery alias). tables lists the canonical
// tables in scope, for unqualified names and "*".
type scope struct {
	names  map[string]string
	tables []string
	target string // the table the statement writes, for INSERT and UPDATE columns
}

// columnResolver walks one statement tree collecting column references.
type columnResolver struct {
	cat       catalog
	ctes      map[string]bool
	refs      []columnRef
	seen      map[string]int
	ambiguous int
	unknown   int
}

// resolveColumns returns the column references of one top-level statement,
// each (table, column) once, write winning over read.
func resolveColumns(stmt *ast.Node, ctes map[string]bool, cat catalog) (refs []columnRef, ambiguous, unknown int) {
	r := &columnResolver{cat: cat, ctes: ctes, seen: map[string]int{}}
	r.visit(stmt, nil, false)
	return r.refs, r.ambiguous, r.unknown
}

// visit descends the tree. A statement opens a scope; a column reference
// resolves against the scope stack, innermost first; the column list of
// an INSERT and the SET list of an UPDATE are writes on the target.
func (r *columnResolver) visit(m proto.Message, scopes []scope, write bool) {
	switch n := m.(type) {
	case *ast.ColumnRef:
		r.column(n, scopes, write)
		return
	case *ast.FuncCall, *ast.A_Expr:
		if sqlcParam(n) {
			return // a parameter, not a column (#96)
		}
	case *ast.ResTarget:
		r.target(n, scopes, write)
		return
	case *ast.SelectStmt, *ast.InsertStmt, *ast.UpdateStmt, *ast.DeleteStmt, *ast.MergeStmt:
		scopes = append(scopes, r.scopeOf(m))
	}
	_, wrapper := m.(*ast.Node)
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		// The flag set by a statement's write field carries through
		// the Node wrapper around each entry to the ResTarget inside.
		r.children(v, fd, scopes, writeField(m, fd) || wrapper && write)
		return true
	})
}

// children visits the messages a field holds, one or a list.
func (r *columnResolver) children(v protoreflect.Value, fd protoreflect.FieldDescriptor, scopes []scope, write bool) {
	if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
		return
	}
	if !fd.IsList() {
		r.visit(v.Message().Interface(), scopes, write)
		return
	}
	l := v.List()
	for i := range l.Len() {
		r.visit(l.Get(i).Message().Interface(), scopes, write)
	}
}

// target handles a ResTarget: in a write context its name is a column of
// the statement's target table — if the catalog has it; a SET of a column
// the schema does not declare is unknown, not an edge to nothing — and
// its value is read.
func (r *columnResolver) target(n *ast.ResTarget, scopes []scope, write bool) {
	if write && n.Name != "" && len(scopes) > 0 {
		t := scopes[len(scopes)-1].target
		if t != "" && r.has(t, n.Name) {
			r.add(t, n.Name, true)
		} else {
			r.unknown++
		}
	}
	if n.Val != nil {
		r.visit(n.Val, scopes, false)
	}
}

// writeField reports whether a statement field holds column writes: an
// INSERT's column list, an UPDATE's SET list.
func writeField(m proto.Message, fd protoreflect.FieldDescriptor) bool {
	switch m.(type) {
	case *ast.InsertStmt:
		return fd.Name() == "cols"
	case *ast.UpdateStmt:
		return fd.Name() == "target_list"
	}
	return false
}

// scopeOf builds a statement's scope from its relation and FROM set,
// through joins, without descending into subqueries, which are scopes of
// their own.
func (r *columnResolver) scopeOf(m proto.Message) scope {
	s := scope{names: map[string]string{}}
	var items []*ast.Node
	switch n := m.(type) {
	case *ast.SelectStmt:
		items = n.FromClause
	case *ast.InsertStmt:
		s.target = r.bind(&s, n.Relation)
	case *ast.UpdateStmt:
		s.target = r.bind(&s, n.Relation)
		items = n.FromClause
	case *ast.DeleteStmt:
		s.target = r.bind(&s, n.Relation)
		items = n.UsingClause
	case *ast.MergeStmt:
		s.target = r.bind(&s, n.Relation)
		if n.SourceRelation != nil {
			items = []*ast.Node{n.SourceRelation}
		}
	}
	for _, it := range items {
		r.fromItem(&s, it)
	}
	return s
}

// fromItem adds one FROM item to a scope: a table, a join's two sides, or
// a subquery's alias (which names no table).
func (r *columnResolver) fromItem(s *scope, n *ast.Node) {
	switch it := n.Node.(type) {
	case *ast.Node_RangeVar:
		r.bind(s, it.RangeVar)
	case *ast.Node_JoinExpr:
		if it.JoinExpr.Larg != nil {
			r.fromItem(s, it.JoinExpr.Larg)
		}
		if it.JoinExpr.Rarg != nil {
			r.fromItem(s, it.JoinExpr.Rarg)
		}
	case *ast.Node_RangeSubselect:
		if a := it.RangeSubselect.Alias; a != nil {
			s.names[a.Aliasname] = ""
		}
	}
}

// bind adds a table reference to a scope under its alias and its name, and
// returns its canonical table name, or "" for a CTE or an unknown table.
func (r *columnResolver) bind(s *scope, rv *ast.RangeVar) string {
	if rv == nil {
		return ""
	}
	written := rv.Relname
	if rv.Schemaname != "" {
		written = rv.Schemaname + "." + written
	}
	canonical := ""
	if rv.Schemaname != "" || !r.ctes[rv.Relname] {
		if c, _, ok := r.cat(written); ok {
			canonical = c
		}
	}
	if canonical != "" {
		s.tables = append(s.tables, canonical)
	}
	s.names[written] = canonical
	s.names[rv.Relname] = canonical
	if rv.Alias != nil && rv.Alias.Aliasname != "" {
		s.names[rv.Alias.Aliasname] = canonical
	}
	return canonical
}

// column resolves one reference: "col", "q.col", "s.t.col", "*" or "q.*".
func (r *columnResolver) column(c *ast.ColumnRef, scopes []scope, write bool) {
	var parts []string
	star := false
	for _, f := range c.Fields {
		switch v := f.Node.(type) {
		case *ast.Node_String_:
			parts = append(parts, v.String_.Sval)
		case *ast.Node_AStar:
			star = true
		}
	}
	switch {
	case star && len(parts) == 0:
		r.expand(innermostTables(scopes), write)
	case star:
		if t := lookupName(scopes, strings.Join(parts, ".")); t != "" {
			r.expand([]string{t}, write)
		}
	case len(parts) == 1:
		r.unqualified(parts[0], scopes, write)
	case len(parts) >= 2:
		r.qualified(strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1], scopes, write)
	}
}

func innermostTables(scopes []scope) []string {
	if len(scopes) == 0 {
		return nil
	}
	return scopes[len(scopes)-1].tables
}

// lookupName finds what a qualifier names, innermost scope first.
func lookupName(scopes []scope, q string) string {
	for i := len(scopes) - 1; i >= 0; i-- {
		if t, ok := scopes[i].names[q]; ok {
			return t
		}
	}
	return ""
}

// expand adds every column of each table.
func (r *columnResolver) expand(tables []string, write bool) {
	for _, t := range tables {
		if _, cols, ok := r.cat(t); ok {
			for _, col := range cols {
				r.add(t, col, write)
			}
		}
	}
}

// qualified resolves "q.col": q must name a table in scope that has col.
func (r *columnResolver) qualified(q, col string, scopes []scope, write bool) {
	t := lookupName(scopes, q)
	if t == "" {
		// Not an alias in scope: perhaps a table named outright.
		if c, _, ok := r.cat(q); ok {
			t = c
		}
	}
	if t == "" || !r.has(t, col) {
		r.unknown++
		return
	}
	r.add(t, col, write)
}

// unqualified resolves "col": exactly one table of the innermost scope
// that has it, else the next scope out; two is an ambiguity.
func (r *columnResolver) unqualified(col string, scopes []scope, write bool) {
	for i := len(scopes) - 1; i >= 0; i-- {
		var found []string
		for _, t := range scopes[i].tables {
			if r.has(t, col) {
				found = append(found, t)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			r.add(found[0], col, write)
			return
		default:
			r.ambiguous++
			return
		}
	}
	r.unknown++
}

func (r *columnResolver) has(table, col string) bool {
	_, cols, ok := r.cat(table)
	if !ok {
		return false
	}
	for _, c := range cols {
		if c == col {
			return true
		}
	}
	return false
}

// add records a reference once per (table, column), write winning.
func (r *columnResolver) add(table, col string, write bool) {
	if table == "" || col == "" {
		return
	}
	key := table + "\x00" + col
	if i, ok := r.seen[key]; ok {
		r.refs[i].write = r.refs[i].write || write
		return
	}
	r.seen[key] = len(r.refs)
	r.refs = append(r.refs, columnRef{table: table, column: col, write: write})
}

// sqlcParam reports whether an expression is one of sqlc's parameter
// forms rather than an expression over columns: sqlc.arg(name),
// sqlc.narg(name), sqlc.slice(name) — and @name, which the grammar reads
// as the prefix operator "@" applied to a column named name (#96).
func sqlcParam(m proto.Message) bool {
	switch n := m.(type) {
	case *ast.FuncCall:
		parts := names(n.Funcname)
		return len(parts) == 2 && parts[0] == "sqlc" && (parts[1] == "arg" || parts[1] == "narg" || parts[1] == "slice")
	case *ast.A_Expr:
		op := names(n.Name)
		return n.Lexpr == nil && len(op) == 1 && op[0] == "@"
	}
	return false
}

// names reads a list of String nodes, as a function or operator name is.
func names(list []*ast.Node) []string {
	var out []string
	for _, x := range list {
		if s, ok := x.Node.(*ast.Node_String_); ok {
			out = append(out, s.String_.Sval)
		}
	}
	return out
}
