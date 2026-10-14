// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	oliphant "github.com/sqlc-dev/oliphant"
	"github.com/sqlc-dev/oliphant/ast"
)

// A view's columns are the output columns of its SELECT, named as
// PostgreSQL names them (#152): the view's own column list when it has
// one; otherwise each target's alias, a column reference's last name, a
// function call's name, or a cast's operand's. A target PostgreSQL would
// call ?column? — an expression with no name — and a "*" give no column:
// sqlc expands "*" from its catalog, and the view's column then stays
// missing rather than guessed. Without columns a query reading one of a
// view's reached nothing.

// viewColumns parses the view declared at start in src and returns its
// columns, each on the line of its target. blanked is src with comments
// and strings blanked, where the statement's end is looked for; nil when
// the statement does not parse.
func viewColumns(src, blanked string, start int) []columnDecl {
	end := statementEnd(blanked, start)
	tree, err := oliphant.Parse(src[start:end])
	if err != nil || len(tree.Stmts) == 0 || tree.Stmts[0].Stmt == nil {
		return nil
	}
	aliases, query := viewParts(tree.Stmts[0].Stmt)
	if len(aliases) > 0 {
		return aliasColumns(aliases, lineAt(src, start))
	}
	return targetColumns(query.GetSelectStmt(), func(loc int32) int { return lineAt(src, start+int(loc)) })
}

// aliasColumns are the columns a view's own column list names, all on
// the view's line.
func aliasColumns(aliases []*ast.Node, line int) []columnDecl {
	var out []columnDecl
	for _, a := range aliases {
		if s, ok := a.Node.(*ast.Node_String_); ok {
			out = append(out, columnDecl{name: s.String_.Sval, line: line})
		}
	}
	return out
}

// targetColumns are a SELECT's named output columns, each on the line
// of its target; a set operation's left arm names them.
func targetColumns(sel *ast.SelectStmt, line func(loc int32) int) []columnDecl {
	for sel != nil && sel.Op != ast.SetOperation_SETOP_NONE && sel.Larg != nil {
		sel = sel.Larg
	}
	if sel == nil {
		return nil
	}
	var out []columnDecl
	for _, t := range sel.TargetList {
		rt, ok := t.Node.(*ast.Node_ResTarget)
		if !ok {
			continue
		}
		if name := targetName(rt.ResTarget); name != "" {
			out = append(out, columnDecl{name: name, line: line(rt.ResTarget.Location)})
		}
	}
	return out
}

// viewParts returns a view statement's explicit column names and its
// query: a CREATE VIEW parses as ViewStmt, a CREATE MATERIALIZED VIEW as
// CreateTableAsStmt.
func viewParts(stmt *ast.Node) (aliases []*ast.Node, query *ast.Node) {
	switch n := stmt.Node.(type) {
	case *ast.Node_ViewStmt:
		return n.ViewStmt.Aliases, n.ViewStmt.Query
	case *ast.Node_CreateTableAsStmt:
		if into := n.CreateTableAsStmt.Into; into != nil {
			aliases = into.ColNames
		}
		return aliases, n.CreateTableAsStmt.Query
	}
	return nil, nil
}

// targetName is the name PostgreSQL gives an output column, or "" for
// one it calls ?column?, and for "*".
func targetName(rt *ast.ResTarget) string {
	if rt.Name != "" {
		return rt.Name
	}
	return exprName(rt.Val)
}

func exprName(n *ast.Node) string {
	switch v := n.GetNode().(type) {
	case *ast.Node_ColumnRef:
		return lastString(v.ColumnRef.Fields)
	case *ast.Node_FuncCall:
		return lastString(v.FuncCall.Funcname)
	case *ast.Node_TypeCast:
		return exprName(v.TypeCast.Arg)
	}
	return ""
}

// lastString is the last element of a name list when it is a name: "c"
// of p.c, "count" of pg_catalog.count; "" for p.*.
func lastString(fields []*ast.Node) string {
	if len(fields) == 0 {
		return ""
	}
	if s, ok := fields[len(fields)-1].Node.(*ast.Node_String_); ok {
		return s.String_.Sval
	}
	return ""
}

// statementEnd is the offset of the semicolon ending the statement that
// starts at start, outside parentheses, or the end of s.
func statementEnd(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth <= 0 {
				return i
			}
		}
	}
	return len(s)
}
