// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"regexp"
	"strings"
)

// The scanner reads the shapes sqlc defines as convention, not SQL. A
// table declaration is "CREATE TABLE name (" up to the matching ")", a
// column is the first word of each top-level comma-separated entry that
// is not a constraint, a query is what follows "-- name: X :cmd" up to
// the next such line. Everything else in the file is passed over.

// A columnDecl is one column, from CREATE TABLE or ADD COLUMN.
type columnDecl struct {
	name, typ string
	line      int
}

// A queryDecl is one "-- name:" query.
type queryDecl struct {
	name, cmd string
	line      int
	raw       string // the SQL as written, for the parser
	text      string // the SQL with comments and literals blanked, for the token match
}

var (
	createTableRE = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+|TEMP(?:ORARY)?\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."]+)\s*\(`)
	// An ALTER TABLE statement, then each ADD clause in it: one
	// statement may add several columns, comma-separated.
	// PostgreSQL's order is IF EXISTS, then ONLY, and a trailing "*"
	// names the table with its descendants (#96).
	alterRE = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w."]+)\s*\*?\s+([^;]*)`)
	// A clause ends at a comma or semicolon outside parentheses, so a
	// type such as numeric(10, 2) survives whole.
	// createViewRE matches a view's declaration; a view is a relation, in
	// the namespace tables share (#96).
	createViewRE = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:TEMP(?:ORARY)?\s+)?(?:RECURSIVE\s+)?(MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."]+)`)
	// Leading blanks are spaces and tabs only: \s would let a match
	// start on the empty line above and misreport the query's line.
	nameRE = regexp.MustCompile(`(?m)^[ \t]*--[ \t]*name:[ \t]*(\w+)[ \t]+:(\w+)`)
	// tableRefRE matches the places a table name follows a keyword in a
	// query: FROM, JOIN, INTO, UPDATE, DELETE FROM. A match is only a
	// candidate; it becomes an edge when the name is a known table.
	tableRefRE = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+([\w."]+)`)
)

// constraintWords start a table-level entry that is not a column; after
// a bare ADD, GENERATED is the identity clause of ALTER COLUMN … ADD
// GENERATED, which pg_dump writes, not a column named "generated" (#96).
var constraintWords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "FOREIGN": true,
	"CHECK": true, "EXCLUDE": true, "LIKE": true, "INHERITS": true,
}

// addNotColumn are the words that, after ADD without COLUMN, begin
// something other than a column.
var addNotColumn = map[string]bool{"GENERATED": true}

// columnStopWords end a column's type.
var columnStopWords = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "REFERENCES": true,
	"UNIQUE": true, "CHECK": true, "COLLATE": true, "GENERATED": true, "CONSTRAINT": true,
}

// blank replaces comments and string literals with spaces of the same
// length, so positions survive and nothing inside them is mistaken for a
// declaration. Dollar-quoted strings are left alone; sqlc queries do not
// use them, and a false match inside one only ever names a table that
// must also exist for an edge to appear.
func blank(src string) string {
	b := []byte(src)
	for i := 0; i < len(b); {
		switch {
		case b[i] == '-' && i+1 < len(b) && b[i+1] == '-':
			i = blankUntil(b, i, "\n", false)
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			i = blankUntil(b, i, "*/", true)
		case b[i] == '\'':
			i = blankUntil(b, i, "'", true)
		default:
			i++
		}
	}
	return string(b)
}

// blankUntil spaces out b from i through the next occurrence of end (or
// to the end of b), keeping newlines, and returns the offset after it.
// When inclusive is false the terminator itself is kept.
func blankUntil(b []byte, i int, end string, inclusive bool) int {
	stop := len(b)
	if j := strings.Index(string(b[i+1:]), end); j >= 0 {
		stop = i + 1 + j
		if inclusive {
			stop += len(end)
		}
	}
	for ; i < stop; i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	return i
}

func lineAt(src string, off int) int { return 1 + strings.Count(src[:off], "\n") }

// ident returns a possibly qualified identifier as PostgreSQL resolves it:
// an unquoted part folded to lower case, a quoted one kept as written with
// its quotes removed. oliphant reports a query's names folded already, so
// a schema read without folding answered "users" with nothing when it
// said "CREATE TABLE Users" (#96).
func ident(s string) string {
	var parts []string
	for _, p := range splitIdent(s) {
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			parts = append(parts, strings.ReplaceAll(p[1:len(p)-1], `""`, `"`))
		} else {
			parts = append(parts, strings.ToLower(p))
		}
	}
	return strings.Join(parts, ".")
}

// splitIdent splits a qualified identifier on the dots outside quotes.
func splitIdent(s string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quoted = !quoted
		case s[i] == '.' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// matchParen returns the offset of the ")" closing the "(" at open, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// scanColumns splits a CREATE TABLE body on top-level commas and keeps
// the entries that declare a column.
func scanColumns(s string, from, to int) []columnDecl {
	var cols []columnDecl
	start, depth := from, 0
	for i := from; i <= to; i++ {
		if i < to && s[i] == '(' {
			depth++
		} else if i < to && s[i] == ')' {
			depth--
		}
		if i == to || s[i] == ',' && depth == 0 {
			entry := s[start:i]
			if c, ok := parseColumn(entry); ok {
				c.line = lineAt(s, start+leadingSpace(entry))
				cols = append(cols, c)
			}
			start = i + 1
		}
	}
	return cols
}

func leadingSpace(s string) int { return len(s) - len(strings.TrimLeft(s, " \t\r\n")) }

// parseColumn reads "name type ... constraints" from one table entry.
func parseColumn(entry string) (columnDecl, bool) {
	fields := strings.Fields(entry)
	if len(fields) < 2 || constraintWords[strings.ToUpper(fields[0])] {
		return columnDecl{}, false
	}
	return columnDecl{name: ident(fields[0]), typ: columnType(strings.Join(fields[1:], " "))}, true
}

// columnType keeps the words of a type up to the first constraint word.
func columnType(s string) string {
	var out []string
	for _, w := range strings.Fields(s) {
		if columnStopWords[strings.ToUpper(w)] {
			break
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// scanQueries finds every "-- name: X :cmd" query in a file.
func scanQueries(src string) []queryDecl {
	ms := nameRE.FindAllStringSubmatchIndex(src, -1)
	var qs []queryDecl
	for i, m := range ms {
		end := len(src)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		qs = append(qs, queryDecl{
			name: src[m[2]:m[3]], cmd: src[m[4]:m[5]], line: lineAt(src, m[0]),
			raw: src[m[1]:end], text: blank(src[m[1]:end]),
		})
	}
	return qs
}

// tableRefs lists the table names a query's text names after FROM, JOIN,
// INTO or UPDATE, in order of appearance, each once.
func tableRefs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range tableRefRE.FindAllStringSubmatch(text, -1) {
		name := ident(m[1])
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
