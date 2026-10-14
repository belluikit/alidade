// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/trendvidia/alidade/internal/graph"
)

// fixture is a hand-built graph spanning three layers, joined the way the
// adapters will join them: a .pxf field references a .proto message, which
// generates a Go type, whose method is called by a hand-written function.
// A Markdown section documents the Go function by an INFERRED edge.
func fixture() *graph.Graph {
	b := graph.NewBuilder("")
	m := b.Minter()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	node := func(id graph.ID, kind graph.Kind, name, file string, line int, gen bool) graph.ID {
		must(b.AddNode(graph.Node{ID: id, Kind: kind, Name: name, Pos: graph.Position{File: file, Line: line, Col: 1}, Generated: gen}))
		return id
	}
	edge := func(from graph.ID, kind graph.EdgeKind, to graph.ID, d graph.Derivation) {
		must(b.AddEdge(graph.Edge{From: from, To: to, Kind: kind, Derivation: d}))
	}
	id := func(v graph.ID, err error) graph.ID { must(err); return v }

	pkg := node(id(m.GoID("example.com/app/analysis", "")), graph.KindPackage, "analysis", "", 0, false)
	pb := node(id(m.GoID("example.com/app/proto/v1", "")), graph.KindPackage, "v1", "", 0, false)
	cfgType := node(id(m.GoID("example.com/app/proto/v1", "WorkspaceConfig")), graph.KindType, "WorkspaceConfig", "proto/v1/config.pb.go", 40, true)
	getReg := node(id(m.GoID("example.com/app/proto/v1", "WorkspaceConfig.GetRegistry")), graph.KindMethod, "GetRegistry", "proto/v1/config.pb.go", 61, true)
	load := node(id(m.GoID("example.com/app/analysis", "LoadConfig")), graph.KindFunc, "LoadConfig", "analysis/config.go", 12, false)
	loadAll := node(id(m.GoID("example.com/app/analysis", "LoadAll")), graph.KindFunc, "LoadAll", "analysis/config.go", 30, false)
	helper := node(id(m.GoID("example.com/app/analysis", "configPath")), graph.KindFunc, "configPath", "analysis/config.go", 50, false)
	msg := node(id(m.ProtoID("app.v1.WorkspaceConfig")), graph.KindMessage, "WorkspaceConfig", "proto/v1/config.proto", 8, false)
	field := node(id(m.PXFID("config/chain.pxf", "registry")), graph.KindBlock, "registry", "config/chain.pxf", 3, false)
	doc := node(id(m.MarkdownID("docs/config.md", "Loading")), graph.KindSection, "Loading", "docs/config.md", 5, false)

	edge(pkg, graph.KindContains, load, graph.Extracted)
	edge(pkg, graph.KindContains, loadAll, graph.Extracted)
	edge(pkg, graph.KindContains, helper, graph.Extracted)
	edge(pb, graph.KindContains, cfgType, graph.Extracted)
	edge(cfgType, graph.KindContains, getReg, graph.Extracted)
	edge(pkg, graph.KindImports, pb, graph.Extracted)
	edge(loadAll, graph.KindCalls, load, graph.Resolved)
	edge(load, graph.KindCalls, helper, graph.Resolved)
	edge(load, graph.KindCalls, getReg, graph.Resolved)
	edge(load, graph.KindReferences, cfgType, graph.Resolved)
	edge(msg, graph.KindGenerates, cfgType, graph.Extracted)
	edge(field, graph.KindReferences, msg, graph.Extracted)
	edge(doc, graph.KindDocuments, load, graph.Inferred)

	// A SQL table whose generated struct is the same WorkspaceConfig type,
	// so "which Go code touches app.users" is a three-hop chain: table ->
	// struct <- LoadConfig <- LoadAll. Its name is the bare "users", as
	// the SQL layer names tables; the qualified name lives in the ID.
	users := node(id(m.SQLID(graph.SQLTable, "app.users")), graph.KindTable, "users", "db/001.sql", 3, false)
	for _, c := range []string{"id", "email"} {
		col := node(id(m.SQLID(graph.SQLColumn, "app.users."+c)), graph.KindColumn, c, "db/001.sql", 4, false)
		edge(users, graph.KindContains, col, graph.Extracted)
	}
	edge(users, graph.KindGenerates, cfgType, graph.Declared)
	// A function the same doc section mentions and nothing else touches:
	// reachable from LoadConfig only through the document.
	unrelated := node(id(m.GoID("example.com/app/analysis", "Unrelated")), graph.KindFunc, "Unrelated", "analysis/other.go", 3, false)
	edge(pkg, graph.KindContains, unrelated, graph.Extracted)
	edge(doc, graph.KindDocuments, unrelated, graph.Inferred)
	// An external package node, as the Go layer emits for a boundary
	// call, named like something a question might say.
	ext := node(id(m.GoID("github.com/aws/aws-sdk-go-v2/config", "")), graph.KindPackage, "config", "", 0, false)
	must(b.AddNode(graph.Node{ID: ext, Kind: graph.KindPackage, Attrs: map[string]string{"external": "true"}}))
	must(b.AddEdge(graph.Edge{From: load, To: ext, Kind: graph.KindCalls, Derivation: graph.Resolved, Attrs: map[string]string{"boundary": "package"}}))
	// One dropped edge and one conflict, for graph_issues.
	edge(doc, graph.KindReferences, "md://docs/missing.md", graph.Extracted)
	must(b.AddNode(graph.Node{ID: load, Kind: graph.KindFunc, Name: "LoadConfigRenamed"}))
	return b.Build()
}

const (
	idLoad    = "go://example.com/app/analysis#LoadConfig"
	idLoadAll = "go://example.com/app/analysis#LoadAll"
	idHelper  = "go://example.com/app/analysis#configPath"
	idGetReg  = "go://example.com/app/proto/v1#WorkspaceConfig.GetRegistry"
	idCfgType = "go://example.com/app/proto/v1#WorkspaceConfig"
	idMsg     = "proto://app.v1.WorkspaceConfig"
	idField   = "pxf://config/chain.pxf#registry"
	idDoc     = "md://docs/config.md#loading"
	idUsers   = "sql://table/app.users"
)

// harness is a server over the fixture behind an in-process MCP client,
// so every test goes through the real protocol: schema, dispatch, result
// framing.
type harness struct {
	t      *testing.T
	c      *client.Client
	builds atomic.Int32
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		h.builds.Add(1)
		return fixture(), BuildInfo{Root: "/repo", SchemaRoots: []string{"/elsewhere/protolsp"}, Layers: []string{"go", "proto", "pxf", "md"}, Rebuilt: []string{"md"}, Kept: []string{"go", "proto", "pxf"}, Build: "darwin/arm64", ExcludedGoFiles: 3, GoModules: []string{"example.com/repo"}, GoModulesFailed: []string{"example.com/repo/tools"}, GoModuleEdges: []ModuleEdge{{From: "example.com/repo/tools", To: "example.com/repo", Kind: "calls", Count: 3}, {From: "example.com/repo", Kind: "imports", Count: 7}}, GoTestNodes: 1, Elapsed: 1500 * time.Millisecond}, nil
	})
	h.c = connect(t, s)
	return h
}

func connect(t *testing.T, s *Server) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(s.MCP())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var init mcpsdk.InitializeRequest
	init.Params.ProtocolVersion = mcpsdk.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpsdk.Implementation{Name: "test", Version: "0"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// call invokes a tool and returns its text, and whether it was an error.
func (h *harness) call(name string, args map[string]any) (string, bool) {
	h.t.Helper()
	var req mcpsdk.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := h.c.CallTool(context.Background(), req)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) != 1 {
		h.t.Fatalf("%s: %d content items, want 1", name, len(res.Content))
	}
	text, ok := res.Content[0].(mcpsdk.TextContent)
	if !ok {
		h.t.Fatalf("%s: content is %T, want text", name, res.Content[0])
	}
	return text.Text, res.IsError
}

// callJSON invokes a tool that must succeed and decodes its result.
func (h *harness) callJSON(name string, args map[string]any) map[string]any {
	h.t.Helper()
	text, isErr := h.call(name, args)
	if isErr {
		h.t.Fatalf("%s: error result: %s", name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		h.t.Fatalf("%s: result is not JSON: %v\n%s", name, err, text)
	}
	return out
}

func ids(v any, key string) []string {
	var out []string
	for _, item := range v.([]any) {
		out = append(out, item.(map[string]any)[key].(string))
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// edgeStrings renders a result's edges as "from -kind-> to (D)".
func edgeStrings(r map[string]any) []string {
	var out []string
	for _, e := range r["edges"].([]any) {
		m := e.(map[string]any)
		out = append(out, end(r, m["f"])+" -"+m["k"].(string)+"-> "+end(r, m["t"])+" ("+m["d"].(string)+")")
	}
	return out
}

// end reads an edge's end as an ID: an index into the result's nodes
// (#157), or an ID where an answer has no node list.
func end(r map[string]any, v any) string {
	if i, ok := v.(float64); ok {
		if nodes, _ := r["nodes"].([]any); int(i) < len(nodes) {
			return nodes[int(i)].(map[string]any)["id"].(string)
		}
		return fmt.Sprintf("<index %v past %d nodes>", i, len(r["nodes"].([]any)))
	}
	s, _ := v.(string)
	return s
}

// nameOf reads a node's name back: its n, or — compacted away because the
// ID ends with it (#157) — the ID's last segment.
func nameOf(m map[string]any) string {
	if n, _ := m["n"].(string); n != "" {
		return n
	}
	id, _ := m["id"].(string)
	return id[strings.LastIndexAny(id, "#/.")+1:]
}

func TestToolsAreListed(t *testing.T) {
	h := newHarness(t)
	res, err := h.c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"graph_stats", "find_nodes", "get_node", "neighbors", "shortest_path", "subgraph_for_question", "rebuild"} {
		if !has(names, want) {
			t.Errorf("tool %s not listed; have %v", want, names)
		}
	}
	if h.builds.Load() != 0 {
		t.Error("listing tools built the graph; the build is lazy")
	}
}

func TestStatsAndLazyBuild(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("graph_stats", nil)
	if r["nodes"].(float64) != 15 || r["edges"].(float64) != 19 || r["build_ms"].(float64) != 1500 || r["dropped"].(float64) != 1 || r["conflicts"].(float64) != 1 {
		t.Errorf("stats = %v", r)
	}
	checkCounts(t, r["nodes_by_scheme"].(map[string]any), map[string]float64{"go": 9, "sql": 3})
	checkCounts(t, r["edges_by_derivation"].(map[string]any), map[string]float64{"INFERRED": 2, "DECLARED": 1, "RESOLVED": 5})
	if share := r["generated_share"].(float64); r["generated"].(float64) != 2 || share < 0.13 || share > 0.14 {
		t.Errorf("generated = %v share = %v", r["generated"], r["generated_share"])
	}
}

func TestLazyBuildAndRebuild(t *testing.T) {
	h := newHarness(t)
	h.callJSON("graph_stats", nil)
	h.callJSON("graph_stats", nil)
	if n := h.builds.Load(); n != 1 {
		t.Errorf("built %d times across two calls, want 1: the graph is held for the session", n)
	}
	h.callJSON("rebuild", nil)
	if n := h.builds.Load(); n != 2 {
		t.Errorf("built %d times after rebuild, want 2", n)
	}
}

func TestFindNodesRanks(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("find_nodes", map[string]any{"query": "WorkspaceConfig"})
	got := ids(r["nodes"], "id")
	// Two exact name matches (the message and the generated type) rank
	// above the method whose ID merely contains the name.
	if len(got) != 3 || got[2] != idGetReg {
		t.Errorf("find WorkspaceConfig = %v", got)
	}
	if r["matched"].(float64) != 3 {
		t.Errorf("matched = %v, want 3", r["matched"])
	}
}

// TestFindNodesSpellings is #84's pin: every spelling a reader has in
// hand for one declaration — bare, package.Func as source writes it,
// package#Func as the ID does, and the whole ID — ranks it first, and
// package.Type.Method likewise.
func TestFindNodesSpellings(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct{ query, want string }{
		{"LoadConfig", idLoad}, {"analysis.LoadConfig", idLoad}, {"analysis#LoadConfig", idLoad}, {idLoad, idLoad},
		{"WorkspaceConfig.GetRegistry", idGetReg}, {"v1.WorkspaceConfig.GetRegistry", idGetReg}, {"v1#WorkspaceConfig.GetRegistry", idGetReg},
	} {
		r := h.callJSON("find_nodes", map[string]any{"query": tt.query})
		if got := ids(r["nodes"], "id"); len(got) == 0 || got[0] != tt.want {
			t.Errorf("find_nodes %q = %v, want %s first", tt.query, got, tt.want)
		}
	}
	if r := h.callJSON("find_nodes", map[string]any{"query": "analysis.LoadConfig"}); r["matched"].(float64) != 1 {
		t.Errorf("analysis.LoadConfig matched %v, want the one declaration", r["matched"])
	}
}

func TestFindNodesFilters(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("find_nodes", map[string]any{"query": "workspaceconfig", "scheme": "proto"})
	if got := ids(r["nodes"], "id"); len(got) != 1 || got[0] != idMsg {
		t.Errorf("scheme filter = %v", got)
	}
	r = h.callJSON("find_nodes", map[string]any{"query": "load", "kind": "func", "limit": 1})
	if got := ids(r["nodes"], "id"); len(got) != 1 || got[0] != idLoadAll || r["matched"].(float64) != 2 {
		t.Errorf("kind+limit = %v matched %v; want LoadAll (prefix) first of 2", got, r["matched"])
	}
	r = h.callJSON("find_nodes", map[string]any{"query": idLoad})
	if got := ids(r["nodes"], "id"); len(got) != 1 || got[0] != idLoad {
		t.Errorf("find by ID = %v", got)
	}
	if _, isErr := h.call("find_nodes", nil); !isErr {
		t.Error("find_nodes without query succeeded")
	}
}

func TestGetNode(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("get_node", map[string]any{"id": idLoad})
	if r["k"] != "func" || r["n"] != "LoadConfig" || r["at"] != "analysis/config.go:12" {
		t.Errorf("node = %v", r)
	}
	out := r["out"].(map[string]any)
	in := r["in"].(map[string]any)
	if out["calls"].(float64) != 3 || out["references"].(float64) != 1 || in["calls"].(float64) != 1 || in["documents"].(float64) != 1 {
		t.Errorf("degrees out=%v in=%v", out, in)
	}
	if text, isErr := h.call("get_node", map[string]any{"id": "go://nowhere#Nothing"}); !isErr || !strings.Contains(text, "find_nodes") {
		t.Errorf("unknown id: isErr=%v text=%q", isErr, text)
	}
}

func TestNeighborsDirectionsAndFilters(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("neighbors", map[string]any{"id": idLoad, "direction": "out"})
	got := ids(r["nodes"], "id")
	for _, want := range []string{idLoad, idHelper, idGetReg, idCfgType} {
		if !has(got, want) {
			t.Errorf("out neighbors lack %s: %v", want, got)
		}
	}
	if has(got, idLoadAll) {
		t.Errorf("out neighbors include the caller: %v", got)
	}
	r = h.callJSON("neighbors", map[string]any{"id": idLoad, "direction": "in"})
	if got := ids(r["nodes"], "id"); len(got) != 4 || !has(got, idLoadAll) || !has(got, idDoc) {
		t.Errorf("in neighbors = %v", got)
	}
}

// checkWellFormed fails if a result repeats an edge or has an edge whose
// endpoint is not among its nodes.
func checkWellFormed(t *testing.T, r map[string]any) {
	t.Helper()
	seen := map[string]bool{}
	nodes := ids(r["nodes"], "id")
	for _, e := range r["edges"].([]any) {
		m := e.(map[string]any)
		if _, ok := m["f"].(float64); !ok {
			t.Errorf("edge %v names its end by ID, not by index into the nodes", m)
		}
		k := end(r, m["f"]) + m["k"].(string) + end(r, m["t"])
		if seen[k] {
			t.Errorf("edge repeated: %s", k)
		}
		seen[k] = true
		if !has(nodes, end(r, m["f"])) || !has(nodes, end(r, m["t"])) {
			t.Errorf("dangling edge %s", k)
		}
	}
}

func TestNeighborsDepthTwoBothWays(t *testing.T) {
	h := newHarness(t)
	// Both directions at depth 2 meets each edge from both ends; it
	// must appear once, and every edge's endpoints must be on the page.
	r := h.callJSON("neighbors", map[string]any{"id": idLoad, "depth": 2})
	checkWellFormed(t, r)
	if edges := edgeStrings(r); !has(edges, idMsg+" -generates-> "+idCfgType+" (EXTRACTED)") {
		t.Errorf("depth 2 did not reach the proto message through the generated type: %v", edges)
	}
}

func TestNeighborsFilters(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("neighbors", map[string]any{"id": idLoad, "exclude_inferred": true})
	if got := ids(r["nodes"], "id"); has(got, idDoc) {
		t.Errorf("exclude_inferred kept the documents edge: %v", got)
	}
	r = h.callJSON("neighbors", map[string]any{"id": idLoad, "edge_kinds": []string{"calls"}})
	if got := edgeStrings(r); len(got) != 4 { // LoadAll -> LoadConfig, LoadConfig -> configPath, GetRegistry, and the boundary call
		t.Errorf("edge_kinds=calls gave %v", got)
	}
	if _, isErr := h.call("neighbors", map[string]any{"id": idLoad, "direction": "sideways"}); !isErr {
		t.Error("bad direction succeeded")
	}
}

func TestBudgetIsRespected(t *testing.T) {
	h := newHarness(t)
	full := h.callJSON("neighbors", map[string]any{"id": idLoad, "depth": 3})
	if full["truncated"] != nil {
		t.Fatalf("default budget truncated the fixture: %v", full)
	}
	total := int(full["tokens"].(float64))
	for budget := minBudget; budget < total; budget += 40 {
		checkBudget(t, h, budget)
	}
}

// checkBudget asks for the same neighbourhood under one budget and checks
// the cut result: within budget by its own count and by size, marked
// truncated with a non-zero omitted count, start node first, well formed.
func checkBudget(t *testing.T, h *harness, budget int) {
	t.Helper()
	args := map[string]any{"id": idLoad, "depth": 3, "max_tokens": budget}
	text, _ := h.call("neighbors", args)
	if est := (len(text) + 3) / 4; est > budget+envelope {
		t.Errorf("budget %d: result is %d bytes, ~%d tokens", budget, len(text), est)
	}
	r := h.callJSON("neighbors", args)
	if got := int(r["tokens"].(float64)); got > budget {
		t.Errorf("budget %d: result claims %d tokens", budget, got)
	}
	if r["truncated"] != true {
		t.Errorf("budget %d was not marked truncated", budget)
		return
	}
	om := r["omitted"].(map[string]any)
	kept := len(r["nodes"].([]any)) + len(r["edges"].([]any))
	if omitted := int(om["nodes"].(float64) + om["edges"].(float64)); omitted == 0 || kept == 0 {
		t.Errorf("budget %d: kept %d, omitted %d", budget, kept, omitted)
	}
	if got := ids(r["nodes"], "id"); got[0] != idLoad {
		t.Errorf("budget %d: start node not first: %v", budget, got)
	}
	checkWellFormed(t, r)
}

// TestShortestPathCrossesLayers is #6's third done-when: a query whose
// answer spans .pxf -> .proto -> .pb.go -> caller returns one connected
// result.
func TestShortestPathCrossesLayers(t *testing.T) {
	h := newHarness(t)
	// pxf -> proto -> generated Go type runs with edge direction; the
	// last hops (LoadConfig references the type, then calls configPath)
	// run against it, so the chain needs "both".
	r := h.callJSON("shortest_path", map[string]any{"from": idField, "to": idHelper, "direction": "both"})
	if r["found"] != true || r["hops"].(float64) != 4 {
		t.Fatalf("path = %v", r)
	}
	want := []string{
		idField + " -references-> " + idMsg + " (EXTRACTED)",
		idMsg + " -generates-> " + idCfgType + " (EXTRACTED)",
		idLoad + " -references-> " + idCfgType + " (RESOLVED)",
		idLoad + " -calls-> " + idHelper + " (RESOLVED)",
	}
	if got := edgeStrings(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("path edges = %v, want %v", got, want)
	}
	if got := ids(r["nodes"], "id"); strings.Join(got, " ") != strings.Join([]string{idField, idMsg, idCfgType, idLoad, idHelper}, " ") {
		t.Errorf("path nodes = %v", got)
	}
}

func TestShortestPathDirectionAndDepth(t *testing.T) {
	h := newHarness(t)
	// The default direction is out, which reaches the generated type
	// and no further.
	r := h.callJSON("shortest_path", map[string]any{"from": idField, "to": idCfgType})
	if r["found"] != true || r["hops"].(float64) != 2 {
		t.Errorf("out path from pxf to the generated type = %v", r)
	}
	r = h.callJSON("shortest_path", map[string]any{"from": idField, "to": idHelper})
	if r["found"] != false || len(r["edges"].([]any)) != 0 {
		t.Errorf("out-only path from pxf to helper = %v", r)
	}
	r = h.callJSON("shortest_path", map[string]any{"from": idField, "to": idHelper, "direction": "both", "max_depth": 2})
	if r["found"] != false {
		t.Errorf("max_depth 2 found a 4-hop path: %v", r)
	}
	r = h.callJSON("shortest_path", map[string]any{"from": idLoad, "to": idLoad})
	if r["found"] != true || r["hops"].(float64) != 0 || len(r["nodes"].([]any)) != 1 {
		t.Errorf("path to self = %v", r)
	}
}

func TestSubgraphForQuestion(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("subgraph_for_question", map[string]any{"question": "where is WorkspaceConfig used?"})
	seedIDs := r["seeds"].([]any)
	if len(seedIDs) < 2 || seedIDs[0] != idCfgType && seedIDs[0] != idMsg {
		t.Errorf("seeds = %v", seedIDs)
	}
	got := ids(r["nodes"], "id")
	for _, want := range []string{idMsg, idCfgType, idField, idLoad} {
		if !has(got, want) {
			t.Errorf("subgraph lacks %s: %v", want, got)
		}
	}
	edges := edgeStrings(r)
	if !has(edges, idField+" -references-> "+idMsg+" (EXTRACTED)") || !has(edges, idMsg+" -generates-> "+idCfgType+" (EXTRACTED)") {
		t.Errorf("subgraph is not connected across layers: %v", edges)
	}
}

func TestSubgraphForQuestionSeedsAndFilter(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("subgraph_for_question", map[string]any{"question": "what calls LoadConfig", "seeds": 1, "exclude_inferred": true})
	if s := r["seeds"].([]any); len(s) != 1 || s[0] != idLoad {
		t.Errorf("seeds for LoadConfig = %v", s)
	}
	if got := ids(r["nodes"], "id"); has(got, idDoc) || !has(got, idLoadAll) {
		t.Errorf("nodes = %v", got)
	}
	if text, isErr := h.call("subgraph_for_question", map[string]any{"question": "what does the function do"}); !isErr || !strings.Contains(text, "find_nodes") {
		t.Errorf("question naming nothing: isErr=%v %q", isErr, text)
	}
}

func TestBuildErrorIsAToolError(t *testing.T) {
	s := New(func() (*graph.Graph, BuildInfo, error) { return nil, BuildInfo{}, errors.New("no such tree") })
	c := connect(t, s)
	var req mcpsdk.CallToolRequest
	req.Params.Name = "graph_stats"
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(mcpsdk.TextContent).Text, "no such tree") {
		t.Errorf("result = %+v", res)
	}
}

func TestSafeRecoversPanics(t *testing.T) {
	h := safe(func(contextT, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) { panic("boom") })
	var req mcpsdk.CallToolRequest
	req.Params.Name = "t"
	res, err := h(context.Background(), req)
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(mcpsdk.TextContent).Text, "boom") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestTerms(t *testing.T) {
	got := terms("Where is protolsp.v1.WorkspaceConfig used, and what calls LoadConfig or go://x/y#Z?")
	want := []string{"protolsp.v1.WorkspaceConfig", "LoadConfig", "go://x/y#Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("terms = %v, want %v", got, want)
	}
}

// TestQualifiedNameSeeding is #24: a qualified term names the node whose
// ID carries it, above the nodes whose IDs merely contain it, and the
// default depth reaches the Go caller across the layers.
func TestQualifiedNameSeeding(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("find_nodes", map[string]any{"query": "app.users", "limit": 3})
	if got := ids(r["nodes"], "id"); len(got) == 0 || got[0] != idUsers {
		t.Errorf("find app.users = %v, want the table first", got)
	}
	r = h.callJSON("find_nodes", map[string]any{"query": "WorkspaceConfig.GetRegistry"})
	if got := ids(r["nodes"], "id"); len(got) != 1 || got[0] != idGetReg {
		t.Errorf("find Type.Method = %v", got)
	}
	r = h.callJSON("subgraph_for_question", map[string]any{"question": "which Go code touches the app.users table", "seeds": 1})
	if s := r["seeds"].([]any); len(s) != 1 || s[0] != idUsers {
		t.Fatalf("seeds = %v, want the table", s)
	}
	got := ids(r["nodes"], "id")
	for _, want := range []string{idUsers, idCfgType, idLoad, idLoadAll} {
		if !has(got, want) {
			t.Errorf("default depth did not reach %s: %v", want, got)
		}
	}
	r = h.callJSON("subgraph_for_question", map[string]any{"question": "which Go code touches the app.users table", "seeds": 1, "depth": 1})
	if got := ids(r["nodes"], "id"); has(got, idLoad) {
		t.Errorf("depth 1 reached LoadConfig: %v", got)
	}
}

func TestGraphIssues(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("graph_issues", nil)
	d := r["dropped"].([]any)
	c := r["conflicts"].([]any)
	if len(d) != 1 || len(c) != 1 || r["truncated"] != nil {
		t.Fatalf("issues = %v", r)
	}
	checkIssue(t, d[0].(map[string]any), map[string]string{"f": idDoc, "t": "md://docs/missing.md", "reason": "unknown_target", "d": "EXTRACTED"})
	checkIssue(t, c[0].(map[string]any), map[string]string{"subject": idLoad, "field": "name"})
	if total := r["total"].(map[string]any); total["dropped"].(float64) != 1 || total["conflicts"].(float64) != 1 {
		t.Errorf("total = %v", total)
	}
}

func checkIssue(t *testing.T, got map[string]any, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("issue %v: %s = %v, want %q", got, k, got[k], v)
		}
	}
}

func TestGraphIssuesFilters(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("graph_issues", map[string]any{"kind": "dropped"})
	if len(r["dropped"].([]any)) != 1 || len(r["conflicts"].([]any)) != 0 {
		t.Errorf("kind=dropped: %v", r)
	}
	r = h.callJSON("graph_issues", map[string]any{"prefix": "sql://"})
	if len(r["dropped"].([]any)) != 0 || len(r["conflicts"].([]any)) != 0 || r["total"].(map[string]any)["dropped"].(float64) != 0 {
		t.Errorf("prefix=sql://: %v", r)
	}
	r = h.callJSON("graph_issues", map[string]any{"max_tokens": minBudget})
	if r["truncated"] != true {
		t.Logf("fixture issues fit the minimum budget; truncation exercised elsewhere")
	}
}

func checkCounts(t *testing.T, got map[string]any, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (in %v)", k, got[k], v, got)
		}
	}
}

// TestDocumentsAreLeavesForQuestions: a section that mentions LoadConfig
// is shown, but the question does not fan out through it to the other
// things the section mentions. neighbors, which asks for structure, still
// expands through it.
func TestDocumentsAreLeavesForQuestions(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("subgraph_for_question", map[string]any{"question": "what calls LoadConfig", "seeds": 1, "depth": 3})
	got := ids(r["nodes"], "id")
	if !has(got, idDoc) {
		t.Errorf("the documenting section is missing: %v", got)
	}
	if has(got, "go://example.com/app/analysis#Unrelated") {
		t.Errorf("question fanned out through a document to Unrelated: %v", got)
	}
	r = h.callJSON("neighbors", map[string]any{"id": idLoad, "depth": 2})
	if got := ids(r["nodes"], "id"); !has(got, "go://example.com/app/analysis#Unrelated") {
		t.Errorf("neighbors at depth 2 should pass through the document: %v", got)
	}
}

// TestExternalNodesNeverSeed and containment upward: #29.
func TestExternalNodesNeverSeed(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("subgraph_for_question", map[string]any{"question": "which config sets the registry", "seeds": 3})
	for _, s := range r["seeds"].([]any) {
		if s == "go://github.com/aws/aws-sdk-go-v2/config" {
			t.Errorf("an external package seeded the question: %v", r["seeds"])
		}
	}
	// find_nodes still finds it: it is a node, and an ID an agent may
	// need for neighbors.
	r = h.callJSON("find_nodes", map[string]any{"query": "config", "kind": "package"})
	if got := ids(r["nodes"], "id"); !has(got, "go://github.com/aws/aws-sdk-go-v2/config") {
		t.Errorf("find_nodes lost the external package: %v", got)
	}
}

func TestContainmentUpwardOnly(t *testing.T) {
	h := newHarness(t)
	// From a column, its table is one hop up and is kept; from the
	// table, its columns are the fan-out and are not.
	r := h.callJSON("subgraph_for_question", map[string]any{"question": "who reads app.users.email", "seeds": 1, "depth": 1})
	got := ids(r["nodes"], "id")
	if !has(got, idUsers) {
		t.Errorf("column question did not reach its table: %v", got)
	}
	r = h.callJSON("subgraph_for_question", map[string]any{"question": "which Go code touches the app.users table", "seeds": 1, "depth": 1})
	got = ids(r["nodes"], "id")
	if has(got, "sql://column/app.users.email") {
		t.Errorf("table question fanned into its columns: %v", got)
	}
	if !has(got, idCfgType) {
		t.Errorf("table question lost its generated struct: %v", got)
	}
	// A method reaches its type, and the boundary call is left out.
	r = h.callJSON("subgraph_for_question", map[string]any{"question": "what calls LoadConfig", "seeds": 1, "depth": 1})
	got = ids(r["nodes"], "id")
	if has(got, "go://github.com/aws/aws-sdk-go-v2/config") {
		t.Errorf("question followed a boundary call: %v", got)
	}
	r = h.callJSON("subgraph_for_question", map[string]any{"question": "who calls WorkspaceConfig.GetRegistry", "seeds": 1, "depth": 1})
	if got := ids(r["nodes"], "id"); !has(got, idCfgType) {
		t.Errorf("method question did not reach its type: %v", got)
	}
}

// TestExcludeInferredKeepsDeclared pins ADR 006 rule 6 at the surface: the
// high-confidence graph keeps generator-derived edges and drops guesses.
func TestExcludeInferredKeepsDeclared(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("neighbors", map[string]any{"id": idUsers, "direction": "out", "exclude_inferred": true})
	edges := edgeStrings(r)
	if !has(edges, idUsers+" -generates-> "+idCfgType+" (DECLARED)") {
		t.Errorf("exclude_inferred dropped the DECLARED generates edge: %v", edges)
	}
	r = h.callJSON("neighbors", map[string]any{"id": idLoad, "direction": "in", "exclude_inferred": true})
	if got := ids(r["nodes"], "id"); has(got, idDoc) {
		t.Errorf("exclude_inferred kept the INFERRED documents edge: %v", got)
	}
}

// TestExcludeTests is #81's pin on the surface: a declaration with a
// test caller and a production one; without the filter both arrive,
// with it only the production caller does, and find_nodes leaves the
// test out. The graph is the Go layer's shape under -tests: the test
// declaration and every edge touching it marked test=true.
func TestExcludeTests(t *testing.T) {
	g, ids4 := testGraph(t)
	load, loadAll, ready := ids4[1], ids4[2], ids4[3]
	h := &harness{t: t}
	h.c = connect(t, New(func() (*graph.Graph, BuildInfo, error) {
		return g, BuildInfo{Root: "/repo", Layers: []string{"go"}, GoTestNodes: 1}, nil
	}))

	r := h.callJSON("neighbors", map[string]any{"id": load, "direction": "in", "edge_kinds": []string{"calls"}})
	if got := ids(r["nodes"], "id"); !has(got, ready) || !has(got, loadAll) {
		t.Errorf("callers without the filter = %v; want the test caller and the production one", got)
	}
	r = h.callJSON("neighbors", map[string]any{"id": load, "direction": "in", "edge_kinds": []string{"calls"}, "exclude_tests": true})
	if got := ids(r["nodes"], "id"); has(got, ready) || !has(got, loadAll) {
		t.Errorf("callers with exclude_tests = %v; want the production caller alone", got)
	}
	r = h.callJSON("find_nodes", map[string]any{"query": "assertReady", "exclude_tests": true})
	if r["matched"].(float64) != 0 {
		t.Errorf("find_nodes with exclude_tests matched %v", r["matched"])
	}
	r = h.callJSON("graph_stats", nil)
	if r["go_test_nodes"].(float64) != 1 {
		t.Errorf("go_test_nodes = %v", r["go_test_nodes"])
	}
}

// testGraph is the Go layer's shape under -tests: a package, a
// declaration with a production caller and a test caller, only the test
// declaration itself marked — the edges touching it are the core's to
// derive. It returns the graph and the IDs of package, callee,
// production caller, test caller.
func testGraph(t *testing.T) (*graph.Graph, [4]string) {
	t.Helper()
	b := graph.NewBuilder("")
	gid := func(decl string) graph.ID {
		id, err := b.Minter().GoID("example.com/app/analysis", decl)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	pkg, load, loadAll, ready := gid(""), gid("LoadConfig"), gid("LoadAll"), gid("assertReady")
	nodes := []graph.Node{
		{ID: pkg, Kind: graph.KindPackage, Name: "analysis"},
		{ID: load, Kind: graph.KindFunc, Name: "LoadConfig", Pos: graph.Position{File: "analysis/config.go", Line: 12, Col: 1}},
		{ID: loadAll, Kind: graph.KindFunc, Name: "LoadAll", Pos: graph.Position{File: "analysis/config.go", Line: 30, Col: 1}},
		{ID: ready, Kind: graph.KindFunc, Name: "assertReady", Pos: graph.Position{File: "analysis/ready_test.go", Line: 9, Col: 1}, Test: true},
	}
	edges := []graph.Edge{
		{From: pkg, To: load, Kind: graph.KindContains, Derivation: graph.Extracted},
		{From: pkg, To: loadAll, Kind: graph.KindContains, Derivation: graph.Extracted},
		{From: pkg, To: ready, Kind: graph.KindContains, Derivation: graph.Extracted},
		{From: loadAll, To: load, Kind: graph.KindCalls, Derivation: graph.Resolved},
		{From: ready, To: load, Kind: graph.KindCalls, Derivation: graph.Resolved},
	}
	for _, n := range nodes {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		if err := b.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build(), [4]string{string(pkg), string(load), string(loadAll), string(ready)}
}

// TestStatsNameTheGoModules: graph_stats lists the Go modules the layer
// loaded and the ones under the root that did not (#67), so a reader can
// tell a module that is absent from one that failed.
func TestStatsNameTheGoModules(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("graph_stats", nil)
	if got := r["go_modules"].([]any); len(got) != 1 || got[0] != "example.com/repo" {
		t.Errorf("go_modules = %v", r["go_modules"])
	}
	if got := r["go_modules_failed"].([]any); len(got) != 1 || got[0] != "example.com/repo/tools" {
		t.Errorf("go_modules_failed = %v", r["go_modules_failed"])
	}
	if r["go_test_nodes"].(float64) != 1 {
		t.Errorf("go_test_nodes = %v", r["go_test_nodes"])
	}
	if got := fmt.Sprint(r["go_module_edges"]); got != "[map[count:3 from_module:example.com/repo/tools kind:calls to_module:example.com/repo] map[count:7 from_module:example.com/repo kind:imports to_module:external]]" {
		t.Errorf("go_module_edges = %s", got)
	}
}

// TestStatsNameTheGoBuild pins #43's first step on the surface: graph_stats
// says which GOOS/GOARCH the Go layer loaded for and how many Go files
// that left out, so a reader of the graph knows what it is a graph of;
// with no Go layer the fields are absent, not empty.
func TestStatsNameTheGoBuild(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("graph_stats", nil)
	if r["go_build"] != "darwin/arm64" || r["go_excluded_files"].(float64) != 3 {
		t.Errorf("go_build = %v, go_excluded_files = %v", r["go_build"], r["go_excluded_files"])
	}
	if roots := r["schema_roots"].([]any); len(roots) != 1 || roots[0] != "/elsewhere/protolsp" {
		t.Errorf("schema_roots = %v", r["schema_roots"])
	}
	if rb := r["layers_rebuilt"].([]any); len(rb) != 1 || rb[0] != "md" || len(r["layers_kept"].([]any)) != 3 {
		t.Errorf("layers_rebuilt = %v, layers_kept = %v", r["layers_rebuilt"], r["layers_kept"])
	}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return fixture(), BuildInfo{Root: "/repo", Layers: []string{"proto"}}, nil
	})
	c := connect(t, s)
	res, err := c.CallTool(context.Background(), mcpsdk.CallToolRequest{Params: mcpsdk.CallToolParams{Name: "graph_stats"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(mcpsdk.TextContent).Text; strings.Contains(text, "go_build") {
		t.Errorf("go_build present with no Go layer: %s", text)
	}
}

// droppedFixture is a graph whose four dropped edges have two causes:
// three generates edges whose proto:// source is not in the graph, and
// one references edge whose go:// target is not.
func droppedFixture(t *testing.T) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder("/repo")
	for _, id := range []graph.ID{"ts://web/gen/a_pb.ts#A", "ts://web/gen/a_pb.ts#B", "ts://web/gen/b_pb.ts#C", "md://docs/x.md"} {
		if err := b.AddNode(graph.Node{ID: id, Kind: graph.KindType, Name: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []graph.Edge{
		{From: "proto://pkg.A", To: "ts://web/gen/a_pb.ts#A", Kind: graph.KindGenerates, Derivation: graph.Declared},
		{From: "proto://pkg.B", To: "ts://web/gen/a_pb.ts#B", Kind: graph.KindGenerates, Derivation: graph.Declared},
		{From: "proto://pkg.C", To: "ts://web/gen/b_pb.ts#C", Kind: graph.KindGenerates, Derivation: graph.Declared},
		{From: "md://docs/x.md", To: "go://example.com/app#Gone", Kind: graph.KindReferences, Derivation: graph.Extracted},
	} {
		if err := b.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build()
}

// issuesOn serves g and returns graph_issues' result for args.
func issuesOn(t *testing.T, g *graph.Graph) func(args map[string]any) map[string]any {
	t.Helper()
	c := connect(t, New(func() (*graph.Graph, BuildInfo, error) { return g, BuildInfo{Root: "/repo"}, nil }))
	return func(args map[string]any) map[string]any {
		res, err := c.CallTool(context.Background(), mcpsdk.CallToolRequest{Params: mcpsdk.CallToolParams{Name: "graph_issues", Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(res.Content[0].(mcpsdk.TextContent).Text), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
}

// TestGraphIssuesGroups pins #47's first step: dropped edges fold by the
// missing end, its scheme and the edge kind, largest group first, each
// with one example, and the groups survive a budget that truncates the
// items.
func TestGraphIssuesGroups(t *testing.T) {
	call := issuesOn(t, droppedFixture(t))
	groups := call(nil)["dropped_by_missing"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups = %v", groups)
	}
	checkIssue(t, groups[0].(map[string]any), map[string]string{"missing": "source", "scheme": "proto", "kind": "generates", "example": "proto://pkg.A -> ts://web/gen/a_pb.ts#A"})
	checkIssue(t, groups[1].(map[string]any), map[string]string{"missing": "target", "scheme": "go", "kind": "references"})
	if groups[0].(map[string]any)["count"].(float64) != 3 || groups[1].(map[string]any)["count"].(float64) != 1 {
		t.Errorf("counts: %v", groups)
	}
	if gs := call(map[string]any{"prefix": "md://"})["dropped_by_missing"].([]any); len(gs) != 1 || gs[0].(map[string]any)["scheme"] != "go" {
		t.Errorf("prefix md://: groups = %v", gs)
	}
	if gs := call(map[string]any{"max_tokens": minBudget})["dropped_by_missing"].([]any); len(gs) != 2 {
		t.Errorf("under the minimum budget the groups were cut: %v", gs)
	}
}

// The exclude_generated tests pin #126 (the owner's decision):
// exclude_generated keeps generated code out of an answer without
// cutting the path through it. The fixture's bridge is a SQL table ->
// generated struct <- hand-written function.

// nodeIn returns the rendered node with an ID from a result, or nil.
func nodeIn(r map[string]any, id string) map[string]any {
	for _, n := range r["nodes"].([]any) {
		if m := n.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

// isStub reports whether a rendered node is a generated node reduced to
// its id and kind: no name, position or attributes.
func isStub(m map[string]any) bool {
	return m != nil && m["gen"] == true && m["n"] == nil && m["at"] == nil && m["attrs"] == nil
}

// TestExcludeGeneratedFindNodes: find_nodes skips a generated node.
func TestExcludeGeneratedFindNodes(t *testing.T) {
	h := newHarness(t)
	if r := h.callJSON("find_nodes", map[string]any{"query": "WorkspaceConfig"}); !has(ids(r["nodes"], "id"), idCfgType) {
		t.Fatalf("without the filter find_nodes misses the generated type: %v", r)
	}
	got := ids(h.callJSON("find_nodes", map[string]any{"query": "WorkspaceConfig", "exclude_generated": true})["nodes"], "id")
	if has(got, idCfgType) || !has(got, idMsg) {
		t.Errorf("find_nodes with exclude_generated = %v; want the message and not the generated type", got)
	}
}

// TestExcludeGeneratedPathCrossesTheBridge: shortest_path still goes
// through the generated struct, every edge real, the struct a stub and the
// hand-written function whole.
func TestExcludeGeneratedPathCrossesTheBridge(t *testing.T) {
	h := newHarness(t)
	r := h.callJSON("shortest_path", map[string]any{"from": idUsers, "to": idLoad, "direction": "both", "exclude_generated": true})
	if r["found"] != true || r["hops"].(float64) != 2 {
		t.Fatalf("path through the generated struct = %v", r)
	}
	want := []string{
		idUsers + " -generates-> " + idCfgType + " (DECLARED)",
		idLoad + " -references-> " + idCfgType + " (RESOLVED)",
	}
	if got := edgeStrings(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("path edges = %v, want %v", got, want)
	}
	if s := nodeIn(r, idCfgType); !isStub(s) || s["k"] != "type" {
		t.Errorf("generated node on the path = %v; want its id, kind and gen only", s)
	}
	if n := nodeIn(r, idLoad); n == nil || nameOf(n) == "" {
		t.Errorf("a hand-written node was stubbed: %v", n)
	}
}

// TestExcludeGeneratedNeighborsStub: neighbors shows a generated node it
// reaches as a stub with the filter, whole without.
func TestExcludeGeneratedNeighborsStub(t *testing.T) {
	h := newHarness(t)
	if s := nodeIn(h.callJSON("neighbors", map[string]any{"id": idLoad, "direction": "out", "exclude_generated": true}), idCfgType); !isStub(s) {
		t.Errorf("neighbors with exclude_generated shows the generated type as %v", s)
	}
	if s := nodeIn(h.callJSON("neighbors", map[string]any{"id": idLoad, "direction": "out"}), idCfgType); s == nil || nameOf(s) != "WorkspaceConfig" {
		t.Errorf("neighbors without the filter shows the generated type as %v", s)
	}
}

// TestExcludeGeneratedSeeds: a generated node does not seed a question.
func TestExcludeGeneratedSeeds(t *testing.T) {
	h := newHarness(t)
	q := map[string]any{"question": "who uses WorkspaceConfig"}
	if seeds := h.callJSON("subgraph_for_question", q)["seeds"].([]any); seeds[0] != idCfgType {
		t.Fatalf("without the filter the generated type does not seed the question: %v", seeds)
	}
	q["exclude_generated"] = true
	if seeds := h.callJSON("subgraph_for_question", q)["seeds"].([]any); slices.Contains(seeds, any(idCfgType)) {
		t.Errorf("a generated node seeded the question: %v", seeds)
	}
}

// Every parameter is described as itself. alidade v0.7.0 listed one
// description — exclude_tests's — on every described parameter of every
// tool (#157), so an agent read "Leave out declarations from _test.go
// files" as the meaning of query, id and max_tokens.
func TestParameterDescriptionsAreTheirOwn(t *testing.T) {
	h := newHarness(t)
	res, err := h.c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		seen := map[string]string{}
		for name, p := range tool.InputSchema.Properties {
			d, _ := p.(map[string]any)["description"].(string)
			if d == "" {
				continue
			}
			if other, dup := seen[d]; dup {
				t.Errorf("%s: %s and %s share the description %q", tool.Name, name, other, d)
			}
			seen[d] = name
		}
		if tool.Name == "find_nodes" {
			q, _ := tool.InputSchema.Properties["query"].(map[string]any)["description"].(string)
			if !strings.Contains(q, "name") {
				t.Errorf("find_nodes query is described as %q", q)
			}
		}
	}
}

// The server says what the graph cannot answer (#173), in the
// instructions — which load even when an agent defers the tool schemas —
// and in find_nodes, the tool a text search would be mistaken for.
func TestSaysWhatItCannotSee(t *testing.T) {
	h := newHarness(t)
	init, err := h.c.Initialize(context.Background(), mcpsdk.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(init.Instructions, "not text") || !strings.Contains(init.Instructions, "search_text") {
		t.Errorf("instructions do not say the graph holds no text:\n%s", init.Instructions)
	}
	res, err := h.c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "find_nodes" && !strings.Contains(tool.Description, "not file contents") {
			t.Errorf("find_nodes description %q does not say it matches names only", tool.Description)
		}
	}
}

// Every tool asks to be loaded up front (#156): a client that defers MCP
// tools behind search never found alidade's.
func TestEveryToolAlwaysLoads(t *testing.T) {
	h := newHarness(t)
	res, err := h.c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range res.Tools {
		if tool.Meta == nil || tool.Meta.AdditionalFields[alwaysLoad] != true {
			t.Errorf("%s: _meta %+v lacks %s", tool.Name, tool.Meta, alwaysLoad)
		}
	}
}
