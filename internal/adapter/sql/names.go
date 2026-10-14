// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sql

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jinzhu/inflection"
)

// sqlc's Go naming, reproduced from internal/codegen/golang/struct.go and
// internal/inflection/singular.go of sqlc-dev/sqlc, which are internal
// and cannot be imported. The rule is small and stable; the edge it
// produces carries INFERRED so a divergence shows up as a dropped edge,
// never as a wrong one.

// structName returns the Go struct sqlc emits for a table. schema is the
// table's schema, "" or "public" for the default.
func (n naming) structName(schema, table string) string {
	name := table
	if schema != "" && schema != "public" {
		name = schema + "_" + table
	}
	if !n.exactTableNames {
		name = singular(name, n.exclusions)
	}
	return n.goName(name)
}

// goName is sqlc's StructName: a rename if there is one, otherwise each
// underscore-separated part title-cased, initialisms upper-cased.
func (n naming) goName(name string) string {
	if r := n.rename[name]; r != "" {
		return r
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, name)
	var out strings.Builder
	for _, p := range strings.Split(name, "_") {
		if n.initialisms[p] {
			out.WriteString(strings.ToUpper(p))
		} else {
			out.WriteString(title(p))
		}
	}
	s := out.String()
	if r, _ := utf8.DecodeRuneInString(s); unicode.IsDigit(r) {
		return "_" + s
	}
	return s
}

// title upper-cases the first letter, as strings.Title does for one word.
func title(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// singular is sqlc's wrapper over jinzhu/inflection, with its exclusions
// and the words it corrects by hand.
func singular(name string, exclusions []string) string {
	for _, e := range exclusions {
		if strings.EqualFold(name, e) {
			return name
		}
	}
	switch strings.ToLower(name) {
	case "campus", "meta", "metadata":
		return name
	case "calories":
		return "calorie"
	case "waves":
		return "wave"
	}
	return inflection.Singular(name)
}

// splitTable separates a schema-qualified table name.
func splitTable(name string) (schema, table string) {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}
