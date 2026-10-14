// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// TestEverySpellingResolves is the property behind #84, over every Go
// declaration of the fixture rather than one example: each spelling a
// reader may have in hand — the whole ID, package#Decl, package.Decl, and
// for a method Type.Method — ranks the declaration first, and its bare
// name ranks it among the best. A spelling that stops resolving for any
// node fails here before a user finds it.
func TestEverySpellingResolves(t *testing.T) {
	g := fixture()
	checked := 0
	for n := range g.Nodes() {
		scheme, _ := n.ID.Scheme()
		if _, _, ok := strings.Cut(string(n.ID), "#"); scheme == graph.SchemeGo && ok {
			checkSpellings(t, g, n)
			checked++
		}
	}
	if checked < 4 {
		t.Fatalf("checked %d Go declarations; the fixture should hold more", checked)
	}
}

// checkSpellings asserts every qualified spelling of one Go declaration
// ranks it first, and its bare name ranks it among the best.
func checkSpellings(t *testing.T, g *graph.Graph, n graph.Node) {
	t.Helper()
	body, decl, _ := strings.Cut(strings.TrimPrefix(string(n.ID), "go://"), "#")
	last := body[strings.LastIndexByte(body, '/')+1:]
	first := []string{string(n.ID), last + "#" + decl, last + "." + decl}
	if strings.Contains(decl, ".") {
		first = append(first, decl) // Type.Method
	}
	for _, q := range first {
		if ms := findNodes(g, q, "", "", hide{}); len(ms) == 0 || ms[0].node.ID != n.ID {
			t.Errorf("find_nodes %q: want %s first, got %v", q, n.ID, firstIDs(ms))
		}
	}
	ms := findNodes(g, n.Name, "", "", hide{})
	if len(ms) == 0 || ms[0].score != scoreNode(n, strings.ToLower(n.Name)) {
		t.Errorf("find_nodes %q: %s is not among the best matches: %v", n.Name, n.ID, firstIDs(ms))
	}
}

func firstIDs(ms []match) []graph.ID {
	var out []graph.ID
	for i, m := range ms {
		if i == 3 {
			break
		}
		out = append(out, m.node.ID)
	}
	return out
}
