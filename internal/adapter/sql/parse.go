// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"slices"

	oliphant "github.com/sqlc-dev/oliphant"
	"github.com/sqlc-dev/oliphant/ast"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A tableRef is one table a query names, and whether the statement writes
// it (the target of INSERT, UPDATE, DELETE, MERGE or COPY FROM) or reads
// it (everything else, a write target's FROM and USING included).
type tableRef struct {
	name  string // schema-qualified as written, or bare
	write bool
}

// parseTables parses a query with oliphant — the PostgreSQL grammar sqlc
// itself parses with — and returns the tables it names, each once, with
// a name bound by WITH excluded: a common table expression is not a
// table, whatever it is called.
func parseTables(raw string) ([]tableRef, error) {
	tree, err := oliphant.Parse(raw)
	if err != nil {
		return nil, err
	}
	return tablesOf(tree), nil
}

// tablesOf lists the tables a parsed query names, each once.
func tablesOf(tree *ast.ParseResult) []tableRef {
	var refs []tableRef
	seen := map[string]int{}
	for _, rs := range tree.Stmts {
		if rs.Stmt == nil {
			continue
		}
		for _, ref := range statementTables(rs.Stmt) {
			if i, ok := seen[ref.name]; ok {
				refs[i].write = refs[i].write || ref.write
				continue
			}
			seen[ref.name] = len(refs)
			refs = append(refs, ref)
		}
	}
	slices.SortStableFunc(refs, func(a, b tableRef) int {
		if a.write != b.write {
			if a.write {
				return -1
			}
			return 1
		}
		return 0
	})
	return refs
}

// ctesOf collects the names a statement's WITH clauses bind.
func ctesOf(stmt *ast.Node) map[string]bool {
	ctes := map[string]bool{}
	walk(stmt, func(m proto.Message) {
		if n, ok := m.(*ast.CommonTableExpr); ok {
			ctes[n.Ctename] = true
		}
	})
	return ctes
}

// statementTables lists the tables one statement names, in the order
// the walk meets them, with names its WITH clause binds left out unless
// written schema-qualified, which a CTE cannot be.
func statementTables(stmt *ast.Node) []tableRef {
	ctes := ctesOf(stmt)
	var vars []*ast.RangeVar
	targets := map[*ast.RangeVar]bool{}
	walk(stmt, func(m proto.Message) {
		switch n := m.(type) {
		case *ast.RangeVar:
			vars = append(vars, n)
		case *ast.Node:
			// The statement's own target and every one nested in it: a
			// DELETE … RETURNING inside a WITH writes its table too (#96).
			if t := writeTarget(n); t != nil {
				targets[t] = true
			}
		}
	})
	var refs []tableRef
	for _, rv := range vars {
		name := rv.Relname
		if rv.Schemaname != "" {
			name = rv.Schemaname + "." + name
		} else if ctes[name] {
			continue
		}
		refs = append(refs, tableRef{name: name, write: targets[rv]})
	}
	return refs
}

// writeTarget returns the relation a statement writes, or nil. It takes
// any node the walk meets, nil among them, so it reads through the
// generated getters, which answer nil for a missing message.
func writeTarget(n *ast.Node) *ast.RangeVar {
	switch {
	case n.GetInsertStmt() != nil:
		return n.GetInsertStmt().GetRelation()
	case n.GetUpdateStmt() != nil:
		return n.GetUpdateStmt().GetRelation()
	case n.GetDeleteStmt() != nil:
		return n.GetDeleteStmt().GetRelation()
	case n.GetMergeStmt() != nil:
		return n.GetMergeStmt().GetRelation()
	case n.GetCopyStmt().GetIsFrom():
		return n.GetCopyStmt().GetRelation()
	}
	return nil
}

// walk visits every message nested in m, depth first, through message
// fields and lists of messages. The AST has some 270 node kinds; visiting
// them by reflection means naming only the two this layer cares about.
func walk(m proto.Message, visit func(proto.Message)) {
	visit(m)
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := range l.Len() {
				walk(l.Get(i).Message().Interface(), visit)
			}
		case !fd.IsList() && !fd.IsMap() && fd.Kind() == protoreflect.MessageKind:
			walk(v.Message().Interface(), visit)
		}
		return true
	})
}
