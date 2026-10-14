// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package markdown

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"

	"github.com/belluikit/alidade/internal/graph"
)

// githubAnchor returns the anchor GitHub gives a heading, and the slug it
// was made from, and records it in occ, the document's count of anchors
// so far. It is
// github-slugger's algorithm, the one GitHub renders with: lowercase;
// letters, numbers, marks, "-" and "_" kept; a space becomes "-"; every
// other character dropped — so "Version 0.21.0 — 2026-09-07" is
// "version-0210--2026-09-07". A repeated anchor takes "-1", "-2", …,
// skipping any already in use.
//
// It is not the slug node IDs are minted from (ADR 002 rule 8 keeps that
// one, which never contains "."): a link is written against what GitHub
// renders, so that is what it is resolved against (#98).
func githubAnchor(occ map[string]int, heading string) (anchor, base string) {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	orig := b.String()
	slug := orig
	for {
		if _, taken := occ[slug]; !taken {
			break
		}
		occ[orig]++
		slug = orig + "-" + strconv.Itoa(occ[orig])
	}
	occ[slug] = 0
	return slug, orig
}

// collision applies the collision policy to a heading's ID: the first
// heading with a path keeps it, and a later one takes the first "-N",
// from 2, that no heading of the document has as its own path and no
// earlier one was given. Without the reserved paths a third "B" heading
// after "B" and "B 2" became "b-2" and folded onto the real one (#98).
func (d *document) collision(id graph.ID) graph.ID {
	base := string(id)
	d.seen[base]++
	if d.seen[base] > 1 || d.used[id] {
		for n := 2; ; n++ {
			cand := graph.ID(base + "-" + strconv.Itoa(n))
			if !d.reserved[cand] && !d.used[cand] {
				id = cand
				break
			}
		}
	}
	d.used[id] = true
	return id
}

// headingPaths is every section ID the document's headings have before
// the collision policy, found by the walk heading uses, for collision to
// keep clear of.
func (d *document) headingPaths(root ast.Node) map[graph.ID]bool {
	out := map[graph.ID]bool{}
	type open struct {
		level    int
		headings []string
	}
	var stack []open
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		var parent []string
		if len(stack) > 0 {
			parent = stack[len(stack)-1].headings
		}
		headings := append(slices.Clone(parent), inlineText(h, d.src))
		if id, err := d.x.m.MarkdownID(d.rel, headings...); err == nil {
			out[id] = true
		}
		stack = append(stack, open{h.Level, headings})
		return ast.WalkSkipChildren, nil
	})
	return out
}

// anchorTarget resolves an anchor against a walked document: the section
// GitHub would scroll to, matched as GitHub's own navigation does,
// without regard to case — when that heading's anchor is its own. One of
// several headings with the same text has an anchor GitHub numbers by
// position ("#changed", "#changed-1"), and ADR 002 rule 8's consequence
// for #5 is that such a link drops with a reason rather than resolve by
// position. Either miss is an ID that is no node — "@ambiguous." or
// "@anchor." and the anchor's slug, which no heading can produce — so
// the link drops at merge, never silently (#98).
func (d *document) anchorTarget(anchor string) (graph.ID, bool) {
	a := strings.ToLower(anchor)
	id, ok := d.anchors[a]
	if ok && d.family[d.base[a]] == 1 {
		return id, true
	}
	kind := "@anchor."
	if ok {
		kind = "@ambiguous."
	}
	s, _ := githubAnchor(map[string]int{}, anchor)
	if s == "" {
		s = "empty"
	}
	return graph.ID(string(d.docID) + "#" + kind + s), false
}
