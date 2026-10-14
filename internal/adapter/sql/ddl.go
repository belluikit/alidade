// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"regexp"
	"slices"
	"strings"
)

// The schema is the current one (#119, the owner's decision): a file's
// statements are read as events in the order they are written, and the
// files are applied in the order sqlc applies them — see schemaOrder — so
// a DROP removes what it drops and a RENAME renames, as in sqlc's own
// catalog. Down migrations are not read: a ".down.sql" file, and every
// line after a rollback marker, as sqlc's migrations package skips them.

// An evKind is the kind of one schema statement, or one clause of an
// ALTER TABLE.
type evKind int

const (
	evCreateTable evKind = iota
	evCreateView
	evAddColumn
	evDropRelation // DROP TABLE, DROP VIEW
	evDropColumn
	evRenameColumn
	evRenameRelation // ALTER TABLE … RENAME TO
)

// A ddlEvent is one statement, or one ALTER TABLE clause, of a schema
// file.
type ddlEvent struct {
	kind  evKind
	table string // the relation as written, folded (ident)
	line  int
	// columns are a CREATE TABLE's; column an ADD COLUMN's.
	columns []columnDecl
	column  columnDecl
	// from and to are a RENAME's names; from is also a DROP COLUMN's.
	from, to     string
	materialized bool
}

var (
	// dropRelationRE names what a DROP TABLE or DROP VIEW drops: one
	// statement may drop several.
	dropRelationRE = regexp.MustCompile(`(?i)\bDROP\s+(?:MATERIALIZED\s+)?(?:TABLE|VIEW)\s+(?:IF\s+EXISTS\s+)?([\w."]+(?:\s*,\s*[\w."]+)*)`)
	// rollbackRE is a down section's first line in the migration tools
	// sqlc knows: goose, sql-migrate, dbmate and tern.
	rollbackRE = regexp.MustCompile(`(?im)^(?:--\s*\+goose\s+down|--\s*\+migrate\s+down|--\s*migrate:down|----\s*create above / drop below\s*----)`)
)

// upSection returns a migration's source up to its rollback marker, if it
// has one, as sqlc's RemoveRollbackStatements does.
func upSection(src string) string {
	if loc := rollbackRE.FindStringIndex(src); loc != nil {
		return src[:loc[0]]
	}
	return src
}

// renameNotColumn and dropNotColumn are the words after RENAME or DROP,
// without COLUMN, that name something other than a column: a constraint,
// a default or a NOT NULL is not a column (#96).
var (
	renameNotColumn = map[string]bool{"CONSTRAINT": true}
	dropNotColumn   = map[string]bool{"CONSTRAINT": true, "DEFAULT": true, "NOT": true, "IDENTITY": true, "EXPRESSION": true}
)

// isDown reports whether a file is a golang-migrate down migration, which
// sqlc does not read as schema.
func isDown(file string) bool { return strings.HasSuffix(file, ".down.sql") }

// scanDDL reads a schema file's statements as events, in source order.
func scanDDL(src string) []ddlEvent {
	up := upSection(src)
	s := blank(up)
	type located struct {
		at int
		ev []ddlEvent
	}
	var all []located
	for _, m := range createTableRE.FindAllStringSubmatchIndex(s, -1) {
		open := m[1] - 1
		close := matchParen(s, open)
		if close < 0 {
			continue
		}
		ev := ddlEvent{kind: evCreateTable, table: ident(s[m[2]:m[3]]), line: lineAt(s, m[0]), columns: scanColumns(s, open+1, close)}
		all = append(all, located{m[0], []ddlEvent{ev}})
	}
	for _, m := range createViewRE.FindAllStringSubmatchIndex(s, -1) {
		ev := ddlEvent{kind: evCreateView, table: ident(s[m[4]:m[5]]), line: lineAt(s, m[0]), materialized: m[2] >= 0, columns: viewColumns(up, s, m[0])}
		all = append(all, located{m[0], []ddlEvent{ev}})
	}
	for _, m := range alterRE.FindAllStringSubmatchIndex(s, -1) {
		all = append(all, located{m[0], alterClauses(s, ident(s[m[2]:m[3]]), m[4], m[5])})
	}
	for _, m := range dropRelationRE.FindAllStringSubmatchIndex(s, -1) {
		var evs []ddlEvent
		for _, name := range strings.Split(s[m[2]:m[3]], ",") {
			evs = append(evs, ddlEvent{kind: evDropRelation, table: ident(strings.TrimSpace(name)), line: lineAt(s, m[0])})
		}
		all = append(all, located{m[0], evs})
	}
	slices.SortStableFunc(all, func(a, b located) int { return a.at - b.at })
	var out []ddlEvent
	for _, l := range all {
		out = append(out, l.ev...)
	}
	return out
}

// alterClauses reads the clauses of one ALTER TABLE body, s[from:to], in
// order: each top-level comma-separated clause that adds, drops or renames
// a column, or renames the table. Everything else — constraints, a
// column's type or default, an identity — is passed over.
func alterClauses(s, table string, from, to int) []ddlEvent {
	var out []ddlEvent
	start, depth := from, 0
	for i := from; i <= to; i++ {
		if i < to && s[i] == '(' {
			depth++
		} else if i < to && s[i] == ')' {
			depth--
		}
		if i < to && (s[i] != ',' || depth > 0) {
			continue
		}
		clause := s[start:i]
		if ev, ok := alterClause(clause, table); ok {
			ev.line = lineAt(s, start+leadingSpace(clause))
			ev.column.line = ev.line
			out = append(out, ev)
		}
		start = i + 1
	}
	return out
}

// alterClause classifies one ALTER TABLE clause.
func alterClause(clause, table string) (ddlEvent, bool) {
	w := words(strings.Fields(clause))
	switch w.up(0) {
	case "ADD":
		return w.add(table)
	case "DROP":
		return w.drop(table)
	case "RENAME":
		return w.rename(table)
	}
	return ddlEvent{}, false
}

// words are one clause's words, read by position.
type words []string

func (w words) up(i int) string {
	if i < len(w) {
		return strings.ToUpper(w[i])
	}
	return ""
}

// object returns where a clause's object starts after its keyword: past
// an optional COLUMN, which it reports, and an optional phrase.
func (w words) object(phrase ...string) (int, bool) {
	i, explicit := 1, w.up(1) == "COLUMN"
	if explicit {
		i++
	}
	return skipPhrase(w, i, phrase...), explicit
}

// add reads ADD [COLUMN] [IF NOT EXISTS] name type ….
func (w words) add(table string) (ddlEvent, bool) {
	i, explicit := w.object("IF", "NOT", "EXISTS")
	if i+1 >= len(w) || constraintWords[w.up(i)] || !explicit && addNotColumn[w.up(i)] {
		return ddlEvent{}, false
	}
	return ddlEvent{kind: evAddColumn, table: table, column: columnDecl{name: ident(w[i]), typ: columnType(strings.Join(w[i+1:], " "))}}, true
}

// drop reads DROP [COLUMN] [IF EXISTS] name.
func (w words) drop(table string) (ddlEvent, bool) {
	i, explicit := w.object("IF", "EXISTS")
	if i >= len(w) || !explicit && dropNotColumn[w.up(i)] {
		return ddlEvent{}, false
	}
	return ddlEvent{kind: evDropColumn, table: table, from: ident(w[i])}, true
}

// rename reads RENAME TO name, and RENAME [COLUMN] a TO b.
func (w words) rename(table string) (ddlEvent, bool) {
	if w.up(1) == "TO" && len(w) > 2 {
		return ddlEvent{kind: evRenameRelation, table: table, to: ident(w[2])}, true
	}
	i, explicit := w.object()
	if i+2 >= len(w) || w.up(i+1) != "TO" || !explicit && renameNotColumn[w.up(i)] {
		return ddlEvent{}, false
	}
	return ddlEvent{kind: evRenameColumn, table: table, from: ident(w[i]), to: ident(w[i+2])}, true
}

// skipPhrase steps past a phrase of words at i, matched without regard
// to case, when it is there.
func skipPhrase(w []string, i int, phrase ...string) int {
	if i+len(phrase) > len(w) {
		return i
	}
	for j, p := range phrase {
		if !strings.EqualFold(w[i+j], p) {
			return i
		}
	}
	return i + len(phrase)
}
