// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"regexp"
	"slices"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// A match is a node with how well it answered a query.
type match struct {
	node  graph.Node
	score int
}

// Match scores. Exact beats qualified beats prefix beats substring, and a
// hit in the name beats one only in the ID, so a query for "Config" ranks
// the type Config above every node in a package called config.
//
// A qualified term — "app.users", "Workspace.ReferencesFor",
// "steward.project.v1.ProjectConfig" — names a node by more than its last
// segment, which is all Name holds. It matches when it is the whole of the
// ID's body or its tail after a "/", "#" or ".", so the table app.users
// outranks the columns whose IDs merely contain it.
const (
	scoreID        = 8
	scoreExact     = 4
	scoreQualified = 3
	scorePrefix    = 2
	scoreSubstring = 1
)

// scoreNode says how well one node answers a query term, or 0.
func scoreNode(n graph.Node, term string) int {
	id := strings.ToLower(string(n.ID))
	if term == id {
		return scoreID
	}
	name := strings.ToLower(n.Name)
	switch {
	case name == term:
		return scoreExact
	case qualifiedMatch(id, term):
		return scoreQualified
	case strings.HasPrefix(name, term):
		return scorePrefix
	case strings.Contains(name, term), strings.Contains(id, term):
		return scoreSubstring
	}
	return 0
}

// qualifiedMatch reports whether term is the ID's whole body or a tail of
// it that starts at a segment boundary. A Go ID spells the package
// boundary "#" where source spells it "." — facts.Create is
// .../facts#Create, pkg.Type.Method is .../pkg#Type.Method — so a dotted
// term is also tried with each of its dots as the "#" (#84).
func qualifiedMatch(id, term string) bool {
	_, body, ok := strings.Cut(id, "://")
	if !ok {
		return false
	}
	for _, t := range spellings(term) {
		if body == t {
			return true
		}
		for _, sep := range []string{"/", "#", "."} {
			if strings.HasSuffix(body, sep+t) {
				return true
			}
		}
	}
	return false
}

// spellings lists a term as written and, for a dotted one, with each dot
// in turn as a "#": the forms a Go declaration's qualified name takes.
func spellings(term string) []string {
	out := []string{term}
	if strings.Contains(term, "#") {
		return out
	}
	for i, c := range term {
		if c == '.' {
			out = append(out, term[:i]+"#"+term[i+1:])
		}
	}
	return out
}

// findNodes ranks the nodes matching query, optionally of one kind or
// layer, best first and then by ID so ties are stable.
func findNodes(g *graph.Graph, query string, kind graph.Kind, scheme graph.Scheme, h hide) []match {
	term := strings.ToLower(strings.TrimSpace(query))
	var out []match
	for n := range g.Nodes() {
		if h.hides(n) {
			continue
		}
		if kind != "" && n.Kind != kind {
			continue
		}
		if s, _ := n.ID.Scheme(); scheme != "" && s != scheme {
			continue
		}
		if sc := scoreNode(n, term); term != "" && sc > 0 {
			out = append(out, match{n, sc})
		}
	}
	slices.SortFunc(out, func(a, b match) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(string(a.node.ID), string(b.node.ID))
	})
	return out
}

var termRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:[./#:][A-Za-z0-9_./#:-]+)*`)

// stopwords are question words that name nothing in a graph.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "what": true, "where": true, "which": true,
	"who": true, "how": true, "does": true, "this": true, "that": true, "with": true,
	"from": true, "into": true, "are": true, "was": true, "can": true, "all": true,
	"uses": true, "use": true, "used": true, "calls": true, "call": true, "called": true,
	"implements": true, "implement": true, "defined": true, "define": true, "find": true,
	"function": true, "functions": true, "type": true, "types": true, "method": true,
	"methods": true, "package": true, "file": true, "code": true, "graph": true,
	"table": true, "tables": true, "column": true, "columns": true, "query": true,
	"queries": true, "message": true, "messages": true, "field": true, "fields": true,
	"service": true, "services": true, "enum": true, "enums": true, "struct": true,
	"interface": true, "section": true, "document": true, "touches": true, "touch": true,
	"reads": true, "read": true, "writes": true, "write": true,
	"config": true, "configuration": true, "layer": true, "layers": true, "setting": true,
	"settings": true, "sets": true, "set": true, "value": true, "values": true,
}

// terms extracts the identifier-like words of a question: bare words,
// dotted or slashed paths, and whole IDs, minus question words.
func terms(question string) []string {
	var out []string
	for _, t := range termRE.FindAllString(question, -1) {
		if len(t) < 3 || stopwords[strings.ToLower(t)] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// seeds ranks the nodes a question names, summing each node's score over
// the question's terms so a node named by two terms outranks one named by
// one. At most limit are returned.
// hide is what find_nodes and a question's seeds leave out of the
// candidates: with exclude_tests a node from a _test.go file, which the Go
// layer marks (#81); with exclude_generated a generated one (#126).
type hide struct{ tests, generated bool }

func hideParam(req interface{ GetBool(string, bool) bool }) hide {
	return hide{tests: req.GetBool("exclude_tests", false), generated: req.GetBool("exclude_generated", false)}
}

func (h hide) hides(n graph.Node) bool {
	return h.tests && n.Test || h.generated && n.Generated
}

func seeds(g *graph.Graph, question string, limit int, h hide) []match {
	ts := terms(question)
	if len(ts) == 0 {
		return nil
	}
	scores := make(map[graph.ID]match)
	for n := range g.Nodes() {
		if n.Attrs["external"] == "true" || h.hides(n) {
			// A boundary node is where the module's calls leave; it
			// is not a declaration of the repository and a question
			// about the repository does not start there. A test is
			// left out when asked.
			continue
		}
		total := 0
		for _, t := range ts {
			total += scoreNode(n, strings.ToLower(t))
		}
		if total > 0 {
			scores[n.ID] = match{n, total}
		}
	}
	out := make([]match, 0, len(scores))
	for _, m := range scores {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b match) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(string(a.node.ID), string(b.node.ID))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
