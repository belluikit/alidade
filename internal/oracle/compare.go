// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/trendvidia/alidade/internal/graph"
)

// A Class is what explains one of the graph's calls edges.
type Class string

const (
	// Confirmed edges are calls the type checker names too.
	Confirmed Class = "confirmed"
	// InterfaceDispatch edges reach an implementation of an interface
	// method the caller calls through: the call graph's answer for an
	// interface call, which no call expression names.
	InterfaceDispatch Class = "interface dispatch"
	// FunctionValue edges leave a caller that calls through a function
	// value, whose callee only the call graph's type flow knows.
	FunctionValue Class = "function value"
	// Unexplained edges are none of these: a reader should look.
	Unexplained Class = "unexplained"
)

// A Result is the Go calls comparison for one repository.
type Result struct {
	// Expected and Found count the oracle's calls by kind, and how many
	// of them the graph has; Missing lists the ones it lacks.
	Expected, Found map[CallKind]int
	Missing         []Call
	// NoSource and NoTarget count oracle calls whose caller, or whose
	// callee, is no node of the graph: a declaration the Go layer left
	// out, not an edge it missed. The Go layer leaves out a declaration
	// positioned outside the tree by a //line directive (#109), so a
	// call into one would otherwise read as a missing edge to a node
	// that was never there to point at.
	NoSource, NoTarget int
	// Edges counts the graph's calls edges by what explains them, and
	// Unexplained lists those nothing does.
	Edges       map[Class]int
	Unexplained []graph.Edge
}

// Compare measures g's calls edges against the oracle's calls.
func Compare(g *graph.Graph, c *Calls) *Result {
	r := &Result{Expected: map[CallKind]int{}, Found: map[CallKind]int{}, Edges: map[Class]int{}}
	have := map[[2]graph.ID]bool{}
	for e := range g.Edges() {
		if e.Kind == graph.KindCalls {
			have[[2]graph.ID{e.From, e.To}] = true
		}
	}
	expected := map[[2]graph.ID]bool{}
	for _, call := range c.Calls {
		expected[[2]graph.ID{call.From, call.To}] = true
		if _, ok := g.Node(call.From); !ok {
			r.NoSource++
			continue
		}
		if _, ok := g.Node(call.To); !ok {
			r.NoTarget++
			continue
		}
		r.Expected[call.Kind]++
		if have[[2]graph.ID{call.From, call.To}] {
			r.Found[call.Kind]++
		} else {
			r.Missing = append(r.Missing, call)
		}
	}
	for e := range g.Edges() {
		// The Go oracle explains Go's calls; the frontend layer's are the
		// TypeScript oracle's (#215).
		if e.Kind != graph.KindCalls || !strings.HasPrefix(string(e.From), "go://") {
			continue
		}
		class := classify(g, c, expected, e)
		r.Edges[class]++
		if class == Unexplained {
			r.Unexplained = append(r.Unexplained, e)
		}
	}
	return r
}

// classify says what explains one calls edge.
func classify(g *graph.Graph, c *Calls, expected map[[2]graph.ID]bool, e graph.Edge) Class {
	// The named explanations come first: a caller that calls through a
	// function value explains every unmatched edge out of itself, so
	// testing it before the method name would attribute an interface
	// call's implementations to the function value whenever a caller
	// does both, and the split between the two classes would say more
	// about this order than about the edges.
	switch {
	case expected[[2]graph.ID{e.From, e.To}]:
		return Confirmed
	case c.Interfaces[e.From][methodName(g, e.To)]:
		return InterfaceDispatch
	case isExternal(g, e.To) && len(c.Interfaces[e.From]) > 0:
		// An interface call's implementations outside the graph are
		// edges to their packages, with no method name to match.
		return InterfaceDispatch
	case c.Dynamic[e.From]:
		return FunctionValue
	}
	return Unexplained
}

// methodName is the method a node declares, "" for anything else.
func methodName(g *graph.Graph, id graph.ID) string {
	n, ok := g.Node(id)
	if !ok || n.Kind != graph.KindMethod {
		return ""
	}
	return n.Name
}

func isExternal(g *graph.Graph, id graph.ID) bool {
	n, ok := g.Node(id)
	return ok && n.Attrs["external"] == "true"
}

// Recall is the share of the oracle's calls of one kind the graph has,
// and whether there was anything of that kind to measure. A kind the
// oracle named none of has no recall: a share of 1 over an empty
// denominator reads as a pass, and would hide the very regression that
// emptied it.
func (r *Result) Recall(k CallKind) (float64, bool) {
	if r.Expected[k] == 0 {
		return 0, false
	}
	return float64(r.Found[k]) / float64(r.Expected[k]), true
}

// String renders the comparison: recall by kind, the edges by class, and
// the first few misses and unexplained edges.
func (r *Result) String() string {
	var b strings.Builder
	for _, k := range []CallKind{Static, Interface, External} {
		if v, ok := r.Recall(k); ok {
			fmt.Fprintf(&b, "recall %-9s %6d / %6d  %.4f\n", k, r.Found[k], r.Expected[k], v)
		} else {
			fmt.Fprintf(&b, "recall %-9s %6d / %6d  none of this kind\n", k, r.Found[k], r.Expected[k])
		}
	}
	fmt.Fprintf(&b, "oracle calls from no graph node: %d, to no graph node: %d\n", r.NoSource, r.NoTarget)
	total := 0
	for _, n := range r.Edges {
		total += n
	}
	for _, cl := range []Class{Confirmed, InterfaceDispatch, FunctionValue, Unexplained} {
		fmt.Fprintf(&b, "edges %-18s %6d  %.4f\n", cl, r.Edges[cl], share(r.Edges[cl], total))
	}
	misses := append([]Call(nil), r.Missing...)
	sort.Slice(misses, func(i, j int) bool { return misses[i].At < misses[j].At })
	for i, m := range misses {
		if i == 10 {
			fmt.Fprintf(&b, "... %d more missing\n", len(misses)-10)
			break
		}
		fmt.Fprintf(&b, "missing %s %s -> %s (%s)\n", m.Kind, m.From, m.To, m.At)
	}
	for i, e := range r.Unexplained {
		if i == 10 {
			fmt.Fprintf(&b, "... %d more unexplained\n", len(r.Unexplained)-10)
			break
		}
		fmt.Fprintf(&b, "unexplained %s -> %s\n", e.From, e.To)
	}
	return b.String()
}

func share(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// An ImportsResult is the imports comparison for one repository: an exact
// set comparison, since `go list` and the graph read the same import
// declarations.
type ImportsResult struct {
	// Expected counts `go list`'s imports; Missing are the ones the
	// graph lacks, and Extra the graph's imports edges, from a package
	// `go list` loaded, that it does not report.
	Expected       int
	Missing, Extra [][2]graph.ID
}

// CompareImports measures g's imports edges against `go list`'s.
func CompareImports(g *graph.Graph, c *Calls) *ImportsResult {
	r := &ImportsResult{Expected: len(c.Imports)}
	want := map[[2]graph.ID]bool{}
	loaded := map[graph.ID]bool{}
	for _, imp := range c.Imports {
		want[imp] = true
		loaded[imp[0]] = true
	}
	have := map[[2]graph.ID]bool{}
	for e := range g.Edges() {
		if e.Kind != graph.KindImports {
			continue
		}
		k := [2]graph.ID{e.From, e.To}
		have[k] = true
		if loaded[e.From] && !want[k] {
			r.Extra = append(r.Extra, k)
		}
	}
	for _, imp := range c.Imports {
		if !have[imp] {
			r.Missing = append(r.Missing, imp)
		}
	}
	return r
}

// String renders the imports comparison and its first few differences.
func (r *ImportsResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "imports %d / %d found, %d extra\n", r.Expected-len(r.Missing), r.Expected, len(r.Extra))
	for i, m := range r.Missing {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "missing import %s -> %s\n", m[0], m[1])
	}
	for i, x := range r.Extra {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "extra import %s -> %s\n", x[0], x[1])
	}
	return b.String()
}

// A SQLResult is the SQL comparison for one repository.
type SQLResult struct {
	// Tables, Columns and Queries count sqlc's and how many of them the
	// graph has; the Missing lists name what it lacks.
	Tables, Columns, Queries                    [2]int
	MissingTables, MissingColumns, MissingQuery []string
	// ExtraTables and ExtraColumns are the graph's, of the tables sqlc
	// has, that sqlc's catalog does not: a column dropped, a table read
	// from a file no configuration lists.
	ExtraTables, ExtraColumns []string
	// OutsideTables are the graph's tables declared in a file no sqlc
	// configuration reads as schema — a script applied by hand — which
	// sqlc's catalog cannot hold.
	OutsideTables []string
	// QueryTables counts, over the matched queries, the tables sqlc says
	// a query touches and how many the graph has; Unconfirmed are the
	// graph's query-to-table edges sqlc does not name, which a JOIN or a
	// WHERE accounts for — sqlc names only the tables of a query's result
	// columns, parameters and INSERT target, and not even a parameter's
	// once it is cast: `DELETE FROM app.sessions WHERE user_id = $1::uuid`
	// reaches sqlc's output with no table at all (steward's
	// DeleteUserSessions, found by the A/B benchmark, #149).
	QueryTables    [2]int
	MissingQTables []string
	Unconfirmed    int
}

// CompareSQL measures g's SQL layer against sqlc's catalog and queries.
func CompareSQL(g *graph.Graph, s *SQL) *SQLResult {
	r := &SQLResult{}
	graphTables := map[graph.ID]bool{}
	for n := range g.Nodes() {
		if n.Kind == graph.KindTable || n.Kind == graph.KindView {
			graphTables[n.ID] = true
		}
	}
	for id, cols := range s.Tables {
		r.Tables[0]++
		if !graphTables[id] {
			r.MissingTables = append(r.MissingTables, string(id))
			continue
		}
		r.Tables[1]++
		r.compareColumns(g, id, cols)
	}
	for id := range graphTables {
		if _, ok := s.Tables[id]; ok || len(s.Tables) == 0 {
			continue
		}
		if n, _ := g.Node(id); !s.SchemaFiles[graph.FilePath(n.Pos.File)] {
			r.OutsideTables = append(r.OutsideTables, string(id))
			continue
		}
		r.ExtraTables = append(r.ExtraTables, string(id))
	}
	var queries []graph.Node
	for n := range g.Nodes() {
		if n.Kind == graph.KindQuery {
			queries = append(queries, n)
		}
	}
	for _, q := range s.Queries {
		r.Queries[0]++
		id, ok := findQuery(queries, q)
		if !ok {
			r.MissingQuery = append(r.MissingQuery, q.Dir+"/"+q.File+"#"+q.Name)
			continue
		}
		r.Queries[1]++
		r.compareQueryTables(g, id, q, s.Tables)
	}
	return r
}

func (r *SQLResult) compareColumns(g *graph.Graph, table graph.ID, cols []graph.ID) {
	have := map[graph.ID]bool{}
	for _, e := range g.Out(table) {
		if e.Kind == graph.KindContains {
			have[e.To] = true
		}
	}
	want := map[graph.ID]bool{}
	for _, c := range cols {
		want[c] = true
		r.Columns[0]++
		if have[c] {
			r.Columns[1]++
		} else {
			r.MissingColumns = append(r.MissingColumns, string(c))
		}
	}
	for c := range have {
		if !want[c] {
			r.ExtraColumns = append(r.ExtraColumns, string(c))
		}
	}
}

// compareQueryTables compares the tables sqlc says a query touches with
// the tables the graph's queries edges reach, directly or through one of
// their columns.
func (r *SQLResult) compareQueryTables(g *graph.Graph, query graph.ID, q Query, tables map[graph.ID][]graph.ID) {
	have := map[graph.ID]bool{}
	for _, e := range g.Out(query) {
		if e.Kind != graph.KindQueries {
			continue
		}
		if n, ok := g.Node(e.To); ok && n.Kind == graph.KindColumn {
			for _, in := range g.In(e.To) {
				if in.Kind == graph.KindContains {
					have[in.From] = true
				}
			}
			continue
		}
		have[e.To] = true
	}
	want := map[graph.ID]bool{}
	for _, t := range q.Tables {
		if _, ok := tables[t]; !ok {
			continue // a CTE's name, which sqlc reports as a column's table
		}
		want[t] = true
		r.QueryTables[0]++
		if have[t] {
			r.QueryTables[1]++
		} else {
			r.MissingQTables = append(r.MissingQTables, q.File+"#"+q.Name+" -> "+string(t))
		}
	}
	for t := range have {
		if !want[t] {
			r.Unconfirmed++
		}
	}
}

// String renders the SQL comparison and its first few differences.
func (r *SQLResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sql tables %d / %d, columns %d / %d, queries %d / %d, query tables %d / %d (unconfirmed %d)\n",
		r.Tables[1], r.Tables[0], r.Columns[1], r.Columns[0], r.Queries[1], r.Queries[0], r.QueryTables[1], r.QueryTables[0], r.Unconfirmed)
	fmt.Fprintf(&b, "sql extra tables %d, extra columns %d, tables outside sqlc's schema files %d\n", len(r.ExtraTables), len(r.ExtraColumns), len(r.OutsideTables))
	for _, l := range []struct {
		what string
		ids  []string
	}{{"missing table", r.MissingTables}, {"missing column", r.MissingColumns}, {"missing query", r.MissingQuery}, {"missing query table", r.MissingQTables}, {"extra table", r.ExtraTables}, {"extra column", r.ExtraColumns}, {"outside sqlc", r.OutsideTables}} {
		sorted := append([]string(nil), l.ids...)
		sort.Strings(sorted)
		for i, id := range sorted {
			if i == 5 {
				fmt.Fprintf(&b, "... %d more %s\n", len(sorted)-5, l.what)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.what, id)
		}
	}
	return b.String()
}

// findQuery finds the graph's node for one of sqlc's queries: the query
// of that name in a file of that base name under the configuration's
// directory — sqlc reports the file's base name, the graph its path.
func findQuery(queries []graph.Node, q Query) (graph.ID, bool) {
	for _, n := range queries {
		file := graph.FilePath(n.Pos.File)
		if n.Name == q.Name && path.Base(file) == q.File && (q.Dir == "." || strings.HasPrefix(file, q.Dir+"/")) {
			return n.ID, true
		}
	}
	return "", false
}
