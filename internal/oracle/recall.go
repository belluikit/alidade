// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"os"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/belluikit/alidade/internal/graph"
)

// The Markdown recall sample (testdata/markdown-recall-sample.pxf, #189)
// is drawn from the docs rather than from the edges: code spans, each
// labelled by a reader with the declaration it names, if any.

// A Span is one labelled code span.
type Span struct {
	File, Text string
	Section    graph.ID
	Label      string // declaration, nonode, test, external, gone, unclear, not
	To         graph.ID
}

// A RecallSample is the labelled spans of one repository, at the commit
// they were labelled at.
type RecallSample struct {
	Commit string
	Spans  []Span
}

// LoadRecall reads the recall sample, by repository name.
func LoadRecall(path string) (map[string]*RecallSample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := pxf.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("oracle: %s: %w", path, err)
	}
	out := map[string]*RecallSample{}
	for _, e := range doc.Entries {
		blk, ok := e.(*pxf.Block)
		if !ok || blk.Name != "repo" {
			continue
		}
		name, s := readRecallRepo(blk.Entries)
		if name == "" || s.Commit == "" {
			return nil, fmt.Errorf("oracle: %s: a repo block needs name and commit", path)
		}
		out[name] = s
	}
	return out, nil
}

func readRecallRepo(entries []pxf.Entry) (string, *RecallSample) {
	s := &RecallSample{}
	name := ""
	for _, e := range entries {
		switch e := e.(type) {
		case *pxf.Assignment:
			switch e.Key {
			case "name":
				name = value(e.Value)
			case "commit":
				s.Commit = value(e.Value)
			}
		case *pxf.Block:
			if e.Name == "span" {
				s.Spans = append(s.Spans, readSpan(e.Entries))
			}
		}
	}
	return name, s
}

func readSpan(entries []pxf.Entry) Span {
	var sp Span
	for _, e := range entries {
		a, ok := e.(*pxf.Assignment)
		if !ok {
			continue
		}
		switch a.Key {
		case "file":
			sp.File = value(a.Value)
		case "text":
			sp.Text = value(a.Value)
		case "section":
			sp.Section = graph.ID(value(a.Value))
		case "label":
			sp.Label = value(a.Value)
		case "to":
			sp.To = graph.ID(value(a.Value))
		}
	}
	return sp
}

// A RecallResult is the sample read against a graph.
type RecallResult struct {
	// Labels counts the spans by label; Linked the declaration spans
	// whose section has a documents edge to the declaration.
	Labels map[string]int
	Linked int
	// Missed are the declaration spans with no such edge; NoNode the
	// declaration spans whose declaration the graph no longer has.
	Missed, NoNode []string
}

// CompareRecall checks each declaration span against the graph.
func CompareRecall(g *graph.Graph, s *RecallSample) *RecallResult {
	r := &RecallResult{Labels: map[string]int{}}
	for _, sp := range s.Spans {
		r.Labels[sp.Label]++
		if sp.Label != "declaration" {
			continue
		}
		label := fmt.Sprintf("%s: `%s` -> %s", sp.File, sp.Text, sp.To)
		switch {
		case hasEdge(g, sp.Section, graph.KindDocuments, sp.To):
			r.Linked++
		case !nodeIn(g, sp.To):
			r.NoNode = append(r.NoNode, label)
		default:
			r.Missed = append(r.Missed, label)
		}
	}
	return r
}

func nodeIn(g *graph.Graph, id graph.ID) bool {
	_, ok := g.Node(id)
	return ok
}

func (r *RecallResult) String() string {
	var b strings.Builder
	n := r.Labels["declaration"]
	recall := 0.0
	if n > 0 {
		recall = float64(r.Linked) / float64(n)
	}
	fmt.Fprintf(&b, "markdown recall sample: %d of %d declaration spans linked (recall %.4f), %d declarations gone from the graph; labels %v\n",
		r.Linked, n, recall, len(r.NoNode), r.Labels)
	for _, l := range []struct {
		name  string
		items []string
	}{{"missed", r.Missed}, {"declaration gone", r.NoNode}} {
		for _, s := range l.items {
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
