// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/graph"
)

// statsOut is graph_stats' shape.
type statsOut struct {
	Root           string         `json:"root"`
	SchemaRoots    []string       `json:"schema_roots,omitempty"`
	Layers         []string       `json:"layers"`
	Rebuilt        []string       `json:"layers_rebuilt,omitempty"`
	Kept           []string       `json:"layers_kept,omitempty"`
	Build          string         `json:"go_build,omitempty"`
	ExcludedGo     int            `json:"go_excluded_files,omitempty"`
	GoModules      []string       `json:"go_modules,omitempty"`
	GoFailed       []string       `json:"go_modules_failed,omitempty"`
	GoModuleEdges  []moduleEdge   `json:"go_module_edges,omitempty"`
	GoTestModEdges []moduleEdge   `json:"go_test_module_edges,omitempty"`
	GoTestNodes    int            `json:"go_test_nodes,omitempty"`
	GoTests        string         `json:"go_tests,omitempty"`
	GoOutsideTree  int            `json:"go_outside_tree,omitempty"`
	GoPkgsFailed   []string       `json:"go_packages_failed,omitempty"`
	GoPkgsFailedOm int            `json:"go_packages_failed_omitted,omitempty"`
	GoPkgsNoCalls  []string       `json:"go_packages_without_calls,omitempty"`
	GoPkgsNoCallOm int            `json:"go_packages_without_calls_omitted,omitempty"`
	Skipped        []skipOut      `json:"skipped,omitempty"`
	SkippedOmitted int            `json:"skipped_omitted,omitempty"`
	BuildMS        int64          `json:"build_ms"`
	Nodes          int            `json:"nodes"`
	Edges          int            `json:"edges"`
	NodesByScheme  map[string]int `json:"nodes_by_scheme"`
	EdgesByDeriv   map[string]int `json:"edges_by_derivation"`
	Generated      int            `json:"generated"`
	GeneratedShare float64        `json:"generated_share"`
	Dropped        int            `json:"dropped"`
	DroppedBy      map[string]int `json:"dropped_by_reason,omitempty"`
	Conflicts      int            `json:"conflicts"`
}

// maxSkipped caps each unbounded list graph_stats itemizes: the skipped
// inputs, and the Go packages that failed or lost their calls.
// graph_stats takes no max_tokens, so an unbounded list here is the one
// result that could not be narrowed (see this package's doc, "Budget"): a
// notes repo whose names mostly hold spaces has one entry per file, and
// one broken package puts every transitive importer of it in
// go_packages_without_calls. The rest are counted in the matching
// _omitted key, and the PXF build block, which is a file and not an
// agent's result, keeps them all.
const maxSkipped = 100

// capped cuts a list to maxSkipped and says how many it left out.
func capped(in []string) ([]string, int) {
	if len(in) <= maxSkipped {
		return in, 0
	}
	return in[:maxSkipped], len(in) - maxSkipped
}

// A skipOut is one input a layer left out, and why.
type skipOut struct {
	Layer  string `json:"layer"`
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Reason string `json:"reason"`
}

// A moduleEdge is one line of go_module_edges: the edges of one kind
// from a module into another module of the repository, or, with
// to_module "external", into packages outside it.
type moduleEdge struct {
	From  string `json:"from_module"`
	To    string `json:"to_module"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

func moduleEdges(in []ModuleEdge) []moduleEdge {
	var out []moduleEdge
	for _, e := range in {
		to := e.To
		if to == "" {
			to = "external"
		}
		out = append(out, moduleEdge{From: e.From, To: to, Kind: e.Kind, Count: e.Count})
	}
	return out
}

func renderStats(g *graph.Graph, info BuildInfo) statsOut {
	st := g.Stats()
	o := statsOut{
		Root: info.Root, SchemaRoots: info.SchemaRoots, Layers: info.Layers, Rebuilt: info.Rebuilt, Kept: info.Kept, Build: info.Build, ExcludedGo: info.ExcludedGoFiles, GoModules: info.GoModules, GoFailed: info.GoModulesFailed, GoModuleEdges: moduleEdges(info.GoModuleEdges), GoTestModEdges: moduleEdges(info.GoTestModuleEdges), GoTestNodes: info.GoTestNodes, BuildMS: info.Elapsed.Milliseconds(),
		Nodes: st.Nodes, Edges: st.Edges, Generated: st.Generated, GeneratedShare: st.GeneratedShare(),
		Dropped: st.Dropped, Conflicts: st.Conflicts,
		NodesByScheme: make(map[string]int), EdgesByDeriv: make(map[string]int), DroppedBy: make(map[string]int),
	}
	if o.Layers == nil {
		o.Layers = []string{}
	}
	o.GoOutsideTree = info.GoOutsideTree
	if info.GoTestsExact {
		o.GoTests = "exact"
	}
	o.GoPkgsFailed, o.GoPkgsFailedOm = capped(info.GoPackagesFailed)
	o.GoPkgsNoCalls, o.GoPkgsNoCallOm = capped(info.GoPackagesWithoutCalls)
	for _, s := range info.Skipped {
		if len(o.Skipped) == maxSkipped {
			o.SkippedOmitted = len(info.Skipped) - maxSkipped
			break
		}
		o.Skipped = append(o.Skipped, skipOut{Layer: s.Layer, File: s.File, Line: s.Line, Reason: s.Reason})
	}
	for k, v := range st.NodesByScheme {
		o.NodesByScheme[string(k)] = v
	}
	for k, v := range st.EdgesByDerivation {
		o.EdgesByDeriv[k.String()] = v
	}
	for k, v := range st.DroppedByReason {
		o.DroppedBy[string(k)] = v
	}
	return o
}

func (s *Server) handleStats(ctx contextT, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, info, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	return jsonResult(renderStats(g, info)), nil
}

func (s *Server) handleRebuild(ctx contextT, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, info, err := s.rebuild(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	return jsonResult(renderStats(g, info)), nil
}

func (s *Server) handleFind(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	query, err := req.RequireString("query")
	if err != nil {
		return errorResult(err), nil
	}
	limit := clamp(req.GetInt("limit", defaultLimit), 1, maxLimit)
	kind, scheme, err := nodeFilterParams(req, g)
	if err != nil {
		return errorResult(err), nil
	}
	matches := findNodes(g, query, kind, scheme, hideParam(req))
	p := newPage(budgetParam(req))
	p.reserve(map[string]int{"matched": len(matches)})
	shown := 0
	for _, m := range matches {
		if shown == limit || !p.addNode(m.node) {
			break
		}
		shown++
	}
	r := p.result()
	r.Extra = map[string]any{"matched": len(matches)}
	return jsonResult(r), nil
}

// nodeDetail is get_node's shape: the node and its degree by edge kind.
type nodeDetail struct {
	nodeOut
	Out map[string]int `json:"out"`
	In  map[string]int `json:"in"`
}

func (s *Server) handleGet(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	id, err := idParam(req, g, "id")
	if err != nil {
		return errorResult(err), nil
	}
	n, _ := g.Node(id)
	d := nodeDetail{nodeOut: renderNode(n), Out: map[string]int{}, In: map[string]int{}}
	for _, e := range g.Out(id) {
		d.Out[string(e.Kind)]++
	}
	for _, e := range g.In(id) {
		d.In[string(e.Kind)]++
	}
	return jsonResult(d), nil
}

func (s *Server) handleNeighbors(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	id, err := idParam(req, g, "id")
	if err != nil {
		return errorResult(err), nil
	}
	dir, err := directionParam(req, dirBoth)
	if err != nil {
		return errorResult(err), nil
	}
	f, err := filterParam(req, g)
	if err != nil {
		return errorResult(err), nil
	}
	start, _ := g.Node(id)
	p := newPage(budgetParam(req))
	p.stubGenerated = f.excludeGenerated
	neighbors(g, p, start, dir, clamp(req.GetInt("depth", defaultDepth), 1, maxDepth), f)
	return jsonResult(p.result()), nil
}

func (s *Server) handlePath(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	from, err := idParam(req, g, "from")
	if err != nil {
		return errorResult(err), nil
	}
	to, err := idParam(req, g, "to")
	if err != nil {
		return errorResult(err), nil
	}
	dir, err := directionParam(req, dirOut)
	if err != nil {
		return errorResult(err), nil
	}
	f, err := filterParam(req, g)
	if err != nil {
		return errorResult(err), nil
	}
	depth := clamp(req.GetInt("max_depth", maxPathDepth), 1, maxPathDepth)
	path := shortestPath(g, from, to, dir, depth, f)
	p := newPage(budgetParam(req))
	p.stubGenerated = f.excludeGenerated
	p.reserve(map[string]any{"found": path != nil, "hops": len(path)})
	start, _ := g.Node(from)
	p.addNode(start)
	for _, st := range path {
		if !p.addStep(st.edge, st.to) {
			break
		}
	}
	r := p.result()
	r.Extra = map[string]any{"found": path != nil, "hops": len(path)}
	return jsonResult(r), nil
}

func (s *Server) handleQuestion(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	question, err := req.RequireString("question")
	if err != nil {
		return errorResult(err), nil
	}
	found := seeds(g, question, clamp(req.GetInt("seeds", 5), 1, 50), hideParam(req))
	if len(found) == 0 {
		return errorResult(fmt.Errorf("no node matches any term of %q; try find_nodes with a name", question)), nil
	}
	depth := clamp(req.GetInt("depth", questionDepth), 1, maxDepth)
	f, err := filterParam(req, g)
	if err != nil {
		return errorResult(err), nil
	}
	f.relationships = true
	p := newPage(budgetParam(req))
	p.stubGenerated = f.excludeGenerated
	// Seeds first, so every seed is on the page before any neighbour
	// is: a cut result then still says what the question named.
	// Each seed's ID is charged with its node, since the result lists it
	// again under seeds.
	ids := make([]string, 0, len(found))
	for _, m := range found {
		if p.reserve(string(m.node.ID)) && p.addNode(m.node) {
			ids = append(ids, string(m.node.ID))
		}
	}
	for _, m := range found {
		neighbors(g, p, m.node, dirBoth, depth, f)
	}
	r := p.result()
	r.Extra = map[string]any{"seeds": ids}
	return jsonResult(r), nil
}

// droppedOut is a dropped edge as graph_issues renders it.
type droppedOut struct {
	edgeOut
	Reason string `json:"reason"`
}

// conflictOut is a conflict as graph_issues renders it.
type conflictOut struct {
	Subject   string `json:"subject"`
	Field     string `json:"field"`
	Kept      string `json:"kept"`
	Discarded string `json:"discarded"`
}

// issuesOut is graph_issues' shape.
type issuesOut struct {
	// Groups folds every dropped edge under the prefix by the end that
	// names no node, its scheme and the edge kind, so a hundred drops
	// with one cause — a repository whose .proto sources live elsewhere
	// (#47) — read as one line before the itemized list.
	Groups    []droppedGroup `json:"dropped_by_missing"`
	Dropped   []droppedOut   `json:"dropped"`
	Conflicts []conflictOut  `json:"conflicts"`
	Total     omitted2       `json:"total"`
	Truncated bool           `json:"truncated,omitempty"`
	Omitted   *omitted2      `json:"omitted,omitempty"`
	Tokens    int            `json:"tokens"`
}

type omitted2 struct {
	Groups    int `json:"dropped_by_missing,omitempty"`
	Dropped   int `json:"dropped"`
	Conflicts int `json:"conflicts"`
}

// A droppedGroup is one cause of dropped edges: which end was missing,
// the scheme of the ID that named nothing, and the kind of edge.
type droppedGroup struct {
	Missing string `json:"missing"` // "source" or "target"
	Scheme  string `json:"scheme"`  // of the missing endpoint
	Kind    string `json:"kind"`    // edge kind
	Count   int    `json:"count"`
	Example string `json:"example"` // one edge, "from -> to"
}

func (s *Server) handleIssues(ctx contextT, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	g, _, err := s.graph(ctx)
	if err != nil {
		return errorResult(err), nil
	}
	kind := req.GetString("kind", "both")
	switch kind {
	case "dropped", "conflicts", "both":
	default:
		return errorResult(fmt.Errorf("kind: %q is not dropped, conflicts or both", kind)), nil
	}
	prefix := req.GetString("prefix", "")
	out := issuesOut{Groups: []droppedGroup{}, Dropped: []droppedOut{}, Conflicts: []conflictOut{}}
	p := newPage(budgetParam(req))
	p.reserve(out.Total) // the totals are rendered whatever fits
	groups := 0
	if kind != "conflicts" {
		out.Groups, groups = droppedGroups(g, prefix, p)
		out.Dropped, out.Total.Dropped = droppedItems(g, prefix, p)
	}
	if kind != "dropped" {
		out.Conflicts, out.Total.Conflicts = conflictItems(g, prefix, p)
	}
	out.Tokens = p.used
	if p.truncated {
		out.Truncated = true
		out.Omitted = &omitted2{Groups: groups - len(out.Groups), Dropped: out.Total.Dropped - len(out.Dropped), Conflicts: out.Total.Conflicts - len(out.Conflicts)}
	}
	return jsonResult(out), nil
}

// droppedGroups folds the dropped edges under prefix by cause, largest
// first, and returns those that fit the page with the number there were.
// Groups are few and are charged to the page before the items, so an
// item is cut before any group is; a group that does not fit is cut too,
// rather than returned over the budget (#100).
func droppedGroups(g *graph.Graph, prefix string, p *page) ([]droppedGroup, int) {
	type key struct{ missing, scheme, kind string }
	counts := map[key]int{}
	examples := map[key]string{}
	for _, d := range g.Dropped() {
		if !strings.HasPrefix(string(d.Edge.From), prefix) {
			continue
		}
		missing, id := "target", d.Edge.To
		if d.Reason == graph.DropUnknownSource {
			missing, id = "source", d.Edge.From
		}
		scheme, _ := id.Scheme()
		k := key{missing, string(scheme), string(d.Edge.Kind)}
		if counts[k] == 0 {
			examples[k] = string(d.Edge.From) + " -> " + string(d.Edge.To)
		}
		counts[k]++
	}
	groups := make([]droppedGroup, 0, len(counts))
	for k, n := range counts {
		groups = append(groups, droppedGroup{k.missing, k.scheme, k.kind, n, examples[k]})
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Missing+a.Scheme+a.Kind < b.Missing+b.Scheme+b.Kind
	})
	shown := []droppedGroup{}
	for _, gr := range groups {
		if p.fit(estimate(gr)) {
			shown = append(shown, gr)
		}
	}
	return shown, len(groups)
}

// droppedItems renders the dropped edges under prefix that fit the page,
// and counts them all.
func droppedItems(g *graph.Graph, prefix string, p *page) ([]droppedOut, int) {
	items, total := []droppedOut{}, 0
	for _, d := range g.Dropped() {
		if !strings.HasPrefix(string(d.Edge.From), prefix) {
			continue
		}
		total++
		item := droppedOut{renderEdge(d.Edge), string(d.Reason)}
		if p.fit(estimate(item)) {
			items = append(items, item)
		}
	}
	return items, total
}

// conflictItems renders the conflicts under prefix that fit the page, and
// counts them all.
func conflictItems(g *graph.Graph, prefix string, p *page) ([]conflictOut, int) {
	items, total := []conflictOut{}, 0
	for _, c := range g.Conflicts() {
		if !strings.HasPrefix(c.Subject, prefix) {
			continue
		}
		total++
		item := conflictOut{c.Subject, c.Field, c.Kept, c.Discarded}
		if p.fit(estimate(item)) {
			items = append(items, item)
		}
	}
	return items, total
}
