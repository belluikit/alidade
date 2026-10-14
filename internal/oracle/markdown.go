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

// The Markdown oracle is a hand-labelled sample (testdata/markdown-sample.pxf):
// a documents edge is INFERRED from a code span naming one node, and only
// a reader can say whether the prose meant that declaration (#148).

// A Labelled is one judged documents edge.
type Labelled struct {
	From, To graph.ID
	Mention  string
	Right    bool
	Why      string
	// Envs are the environments a chain link is judged to load in, for
	// the chain census (#187).
	Envs string
}

// A Sample is the labelled edges of one repository, at the commit they
// were judged at.
type Sample struct {
	Commit string
	Edges  []Labelled
}

// LoadSample reads the labelled sample, by repository name.
func LoadSample(path string) (map[string]*Sample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := pxf.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("oracle: %s: %w", path, err)
	}
	out := map[string]*Sample{}
	for _, e := range doc.Entries {
		blk, ok := e.(*pxf.Block)
		if !ok || blk.Name != "repo" {
			continue
		}
		name, s := readRepo(blk.Entries)
		if name == "" || s.Commit == "" {
			return nil, fmt.Errorf("oracle: %s: a repo block needs name and commit", path)
		}
		out[name] = s
	}
	return out, nil
}

func readRepo(entries []pxf.Entry) (string, *Sample) {
	s := &Sample{}
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
			if e.Name == "edge" {
				s.Edges = append(s.Edges, readLabelled(e.Entries))
			}
		}
	}
	return name, s
}

func readLabelled(entries []pxf.Entry) Labelled {
	var l Labelled
	for _, e := range entries {
		a, ok := e.(*pxf.Assignment)
		if !ok {
			continue
		}
		switch a.Key {
		case "from":
			l.From = graph.ID(value(a.Value))
		case "to":
			l.To = graph.ID(value(a.Value))
		case "mention":
			l.Mention = value(a.Value)
		case "label":
			l.Right = value(a.Value) == "right"
		case "why":
			l.Why = value(a.Value)
		case "envs":
			l.Envs = value(a.Value)
		}
	}
	return l
}

// value reads a string or a bare identifier.
func value(v pxf.Value) string {
	switch v := v.(type) {
	case *pxf.StringVal:
		return v.Value
	case *pxf.IdentVal:
		return v.Name
	}
	return ""
}

// SampleResult is a sample read against a graph: the labelled edges still
// in it, right and wrong, and the ones gone — a right one gone is a
// regression, a wrong one gone a fix.
type SampleResult struct {
	Right, Wrong         int
	RightGone, WrongGone []string
	WrongKept            []string
}

// CompareSample checks each labelled edge against the graph.
func CompareSample(g *graph.Graph, s *Sample) *SampleResult {
	r := &SampleResult{}
	for _, l := range s.Edges {
		have := hasEdge(g, l.From, graph.KindDocuments, l.To)
		label := fmt.Sprintf("%s -> %s (`%s`)", l.From, l.To, l.Mention)
		switch {
		case have && l.Right:
			r.Right++
		case have:
			r.Wrong++
			r.WrongKept = append(r.WrongKept, label+": "+l.Why)
		case l.Right:
			r.RightGone = append(r.RightGone, label)
		default:
			r.WrongGone = append(r.WrongGone, label)
		}
	}
	return r
}

func (r *SampleResult) String() string {
	var b strings.Builder
	precision := 0.0
	if n := r.Right + r.Wrong; n > 0 {
		precision = float64(r.Right) / float64(n)
	}
	fmt.Fprintf(&b, "markdown sample: %d right, %d wrong (precision %.4f), %d right gone, %d wrong gone\n",
		r.Right, r.Wrong, precision, len(r.RightGone), len(r.WrongGone))
	for _, l := range []struct {
		name  string
		items []string
	}{{"wrong", r.WrongKept}, {"regression, right edge gone", r.RightGone}, {"fixed, wrong edge gone", r.WrongGone}} {
		for _, s := range l.items {
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
