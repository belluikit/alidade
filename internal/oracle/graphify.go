// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// GraphifyRust is graphify's calls edges between Rust functions, from the
// graph.json `graphify update` writes, named by the Rust layer's IDs so
// that RustReference.Named scores them as it scores the layer's. A
// graphify function is the layer's function or method declared on the
// same line of the same file: graphify's node says where its declaration
// starts ("L14") and the layer's where its name is, which is one line for
// every `fn` not split over two.
type GraphifyRust struct {
	Calls map[graph.ID]map[graph.ID]bool
	// At is every calls edge between two of its Rust functions, placed or
	// not, by "file:line" of each end's declaration.
	At map[string]map[string]bool
	// Extracted is Calls without the edges graphify marks INFERRED.
	Extracted map[graph.ID]map[graph.ID]bool
	// Functions is graphify's Rust function nodes; Placed of them have a
	// layer node on their line, Ambiguous more than one.
	Functions, Placed, Ambiguous int
	// Edges is its calls edges between two Rust functions; Inferred of
	// them it marks INFERRED; Unplaced have an end on no layer node.
	Edges, Inferred, Unplaced int
}

// graphifyJSON is what the comparison reads of graphify's graph.json.
type graphifyJSON struct {
	Nodes []struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		File     string `json:"source_file"`
		Location string `json:"source_location"`
	} `json:"nodes"`
	Links []struct {
		Source     string `json:"source"`
		Target     string `json:"target"`
		Relation   string `json:"relation"`
		Confidence string `json:"confidence"`
	} `json:"links"`
}

// ReadGraphifyRust reads graphify's graph.json at path against the
// layer's graph g of the same tree.
func ReadGraphifyRust(path string, g *graph.Graph) (*GraphifyRust, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var gj graphifyJSON
	if err := json.Unmarshal(b, &gj); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := &GraphifyRust{Calls: map[graph.ID]map[graph.ID]bool{}, Extracted: map[graph.ID]map[graph.ID]bool{}, At: map[string]map[string]bool{}}
	fns := out.place(gj, layerAt(g))
	where := map[string]string{}
	for _, n := range gj.Nodes {
		where[n.ID] = n.File + ":" + strings.TrimPrefix(n.Location, "L")
	}
	for _, l := range gj.Links {
		if _, ok := fns[l.Source]; l.Relation != "calls" || !ok {
			continue
		}
		if _, ok := fns[l.Target]; !ok {
			continue
		}
		out.Edges++
		addAt(out.At, where[l.Source], where[l.Target])
		if l.Confidence == "INFERRED" {
			out.Inferred++
		}
		from, to := fns[l.Source], fns[l.Target]
		if from == "" || to == "" {
			out.Unplaced++
			continue
		}
		add(out.Calls, from, to)
		if l.Confidence != "INFERRED" {
			add(out.Extracted, from, to)
		}
	}
	return out, nil
}

// layerAt is the layer's Rust functions and methods by "file:line".
func layerAt(g *graph.Graph) map[string][]graph.ID {
	at := map[string][]graph.ID{}
	for n := range g.Nodes() {
		if s, _ := n.ID.Scheme(); s == graph.SchemeRust && (n.Kind == graph.KindFunc || n.Kind == graph.KindMethod) {
			k := n.Pos.File + ":" + strconv.Itoa(n.Pos.Line)
			at[k] = append(at[k], n.ID)
		}
	}
	return at
}

// place maps each of graphify's Rust function nodes to the layer's node on
// its line, "" when there is none or more than one.
func (out *GraphifyRust) place(gj graphifyJSON, at map[string][]graph.ID) map[string]graph.ID {
	fns := map[string]graph.ID{}
	for _, n := range gj.Nodes {
		if !strings.HasSuffix(n.File, ".rs") || !strings.HasSuffix(n.Label, ")") {
			continue
		}
		out.Functions++
		fns[n.ID] = ""
		switch ids := at[n.File+":"+strings.TrimPrefix(n.Location, "L")]; len(ids) {
		case 0:
		case 1:
			out.Placed++
			fns[n.ID] = ids[0]
		default:
			out.Ambiguous++
		}
	}
	return fns
}

func add(m map[graph.ID]map[graph.ID]bool, from, to graph.ID) {
	if m[from] == nil {
		m[from] = map[graph.ID]bool{}
	}
	m[from][to] = true
}

func addAt(m map[string]map[string]bool, from, to string) {
	if m[from] == nil {
		m[from] = map[string]bool{}
	}
	m[from][to] = true
}

// LayerAt is the layer's calls edges by "file:line" of each end, as
// GraphifyRust.At holds graphify's.
func LayerAt(g *graph.Graph) map[string]map[string]bool {
	pos := map[graph.ID]string{}
	for n := range g.Nodes() {
		if n.Pos.File != "" {
			pos[n.ID] = n.Pos.File + ":" + strconv.Itoa(n.Pos.Line)
		}
	}
	out := map[string]map[string]bool{}
	for e := range g.Edges() {
		if f, t := pos[e.From], pos[e.To]; e.Kind == graph.KindCalls && f != "" && t != "" {
			addAt(out, f, t)
		}
	}
	return out
}
