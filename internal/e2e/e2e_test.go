// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package e2e

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

func TestE2E(t *testing.T) {
	roots := strings.Fields(os.Getenv("ALIDADE_E2E"))
	if len(roots) == 0 {
		t.Skip("set ALIDADE_E2E to repository roots, or run scripts/mcp-e2e.sh")
	}
	bin := binary(t)
	src := sourceText(t)
	for _, root := range roots {
		t.Run(filepath.Base(root), func(t *testing.T) {
			c := serve(t, bin, root)
			checkDescriptions(t, c, src)
			g, _, err := build.Build(root, build.DefaultOptions())
			if err != nil {
				t.Fatalf("does not build: %v", err)
			}
			checkAnswers(t, c, g)
		})
	}
}

// binary is ALIDADE_E2E_BIN, or alidade built as the release workflow
// builds it: the toolchain on PATH, -trimpath, -ldflags "-s -w".
func binary(t *testing.T) string {
	if bin := os.Getenv("ALIDADE_E2E_BIN"); bin != "" {
		return bin
	}
	out := filepath.Join(t.TempDir(), "alidade")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "../../cmd/alidade")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, msg)
	}
	return out
}

// serve starts the binary on root and connects to it over stdio.
func serve(t *testing.T, bin, root string) *client.Client {
	t.Helper()
	c, err := client.NewStdioMCPClient(bin, nil, "serve", "-root", root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var init mcpsdk.InitializeRequest
	init.Params.ProtocolVersion = mcpsdk.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpsdk.Implementation{Name: "e2e", Version: "0"}
	if _, err := c.Initialize(context.Background(), init); err != nil {
		t.Fatal(err)
	}
	return c
}

// source is what internal/mcp/tools.go writes: each tool's description,
// and every description each parameter name is given anywhere, "" when
// some use of it has none.
type source struct {
	tools  map[string]string
	params map[string]map[string]bool
}

// sourceText reads the descriptions from the source — the working tree's,
// or ALIDADE_E2E_REF's when an older binary is checked against the source
// it was built from — not from a server:
// a server built by the same compiler as the binary would agree with it
// when both are wrong, which is how #160 passed every test in-process.
func sourceText(t *testing.T) *source {
	s := &source{tools: map[string]string{}, params: map[string]map[string]bool{}}
	for _, file := range []string{"tools.go", "textsearch.go"} {
		if f := sourceFile(t, file); f != nil {
			s.read(f)
		}
	}
	if len(s.tools) == 0 || len(s.params) == 0 {
		t.Fatalf("read %d tools and %d parameters from internal/mcp: the source reader is broken", len(s.tools), len(s.params))
	}
	return s
}

// sourceFile parses one of internal/mcp's files: the working tree's, or
// ALIDADE_E2E_REF's — nil when that release has no such file (search_text
// came after v0.7.0).
func sourceFile(t *testing.T, name string) *ast.File {
	var text any
	if ref := os.Getenv("ALIDADE_E2E_REF"); ref != "" {
		b, err := exec.Command("git", "show", ref+":internal/mcp/"+name).Output()
		if err != nil {
			if name == "tools.go" {
				t.Fatalf("git show %s:internal/mcp/%s: %v", ref, name, err)
			}
			return nil
		}
		text = b
	}
	f, err := parser.ParseFile(token.NewFileSet(), "../mcp/"+name, text, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// read adds one file's tool and parameter descriptions.
func (s *source) read(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callee(call)
		switch {
		case name == "NewTool":
			if tool := stringArg(call, 0); tool != "" {
				s.tools[tool] = firstDescription(call, "WithDescription")
			}
		case strings.HasPrefix(name, "With") && name != "WithDescription" && !strings.HasSuffix(name, "Annotation") && !strings.HasSuffix(name, "Items"):
			if p := stringArg(call, 0); p != "" {
				if s.params[p] == nil {
					s.params[p] = map[string]bool{}
				}
				s.params[p][firstDescription(call, "Description")] = true
			}
		}
		return true
	})
}

func callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

func stringArg(call *ast.CallExpr, i int) string {
	if len(call.Args) <= i {
		return ""
	}
	lit, ok := call.Args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, _ := strconv.Unquote(lit.Value)
	return s
}

// firstDescription is the string the first call of the named function
// within call's arguments carries, or "": a tool's options may sit in an
// append (neighbors' do).
func firstDescription(call *ast.CallExpr, fn string) string {
	found := ""
	for _, a := range call.Args {
		ast.Inspect(a, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && found == "" && callee(c) == fn {
				found = stringArg(c, 0)
			}
			return found == ""
		})
	}
	return found
}

// checkDescriptions compares every listed description with the source.
func checkDescriptions(t *testing.T, c *client.Client, src *source) {
	res, err := c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != len(src.tools) {
		t.Errorf("%d tools listed, %d in the source", len(res.Tools), len(src.tools))
	}
	for _, tool := range res.Tools {
		if os.Getenv("ALIDADE_E2E_REF") == "" && (tool.Meta == nil || tool.Meta.AdditionalFields["anthropic/alwaysLoad"] != true) {
			t.Errorf("%s: not marked anthropic/alwaysLoad, so Claude Code defers it (#156)", tool.Name)
		}
		if want, ok := src.tools[tool.Name]; !ok || tool.Description != want {
			t.Errorf("%s: description %q, source %q", tool.Name, tool.Description, want)
		}
		for name, p := range tool.InputSchema.Properties {
			got, _ := p.(map[string]any)["description"].(string)
			if !src.params[name][got] {
				t.Errorf("%s.%s: listed as %q, which the source never gives %s", tool.Name, name, got, name)
			}
		}
	}
}

// call calls a tool and returns its answer parsed, failing on an error
// or an answer over its token budget (four bytes a token, as the server
// estimates).
func call(t *testing.T, c *client.Client, tool string, args map[string]any) map[string]any {
	t.Helper()
	var req mcpsdk.CallToolRequest
	req.Params.Name, req.Params.Arguments = tool, args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	var text strings.Builder
	for _, part := range res.Content {
		if tc, ok := part.(mcpsdk.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("%s %v: error %s", tool, args, text.String())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
		t.Fatalf("%s %v: answer does not parse: %v\n%.300s", tool, args, err, text.String())
	}
	if budget := 4000; tool != "graph_stats" && tool != "get_node" && text.Len() > budget*4*11/10 {
		t.Errorf("%s %v: %d bytes, over the %d-token budget", tool, args, text.Len(), budget)
	}
	return out
}

// checkAnswers calls every tool, and checks a sample of declarations'
// callers through neighbors against the graph the same build makes.
func checkAnswers(t *testing.T, c *client.Client, g *graph.Graph) {
	stats := call(t, c, "graph_stats", nil)
	if n, _ := stats["nodes"].(float64); int(n) != g.NodeCount() {
		t.Errorf("graph_stats nodes %v, the build has %d", stats["nodes"], g.NodeCount())
	}
	call(t, c, "graph_issues", nil)
	if listed(t, c, "search_text") { // since #178; a release before it has none
		text := call(t, c, "search_text", map[string]any{"pattern": "func ", "exclude_tests": true, "files_only": true})
		if total, _ := text["total"].(float64); total == 0 {
			t.Error("search_text found no \"func \" in a Go repository")
		}
	}
	sample := callees(g, 20)
	if len(sample) == 0 {
		t.Fatal("no declaration with callers to sample: nothing compared")
	}
	t.Logf("comparing %d declarations' callers, every tool called", len(sample))
	for _, id := range sample {
		n, _ := g.Node(id)
		found := call(t, c, "find_nodes", map[string]any{"query": string(id)})
		if !hasNode(found, id) {
			t.Errorf("find_nodes %s: not in the answer", id)
		}
		call(t, c, "get_node", map[string]any{"id": string(id)})
		got := edgeSet(call(t, c, "neighbors", map[string]any{"id": string(id), "direction": "in", "edge_kinds": []string{"calls"}}), id)
		want := map[string]bool{}
		for _, e := range g.In(id) {
			if e.Kind == graph.KindCalls {
				want[string(e.From)] = true
			}
		}
		if !mapsEqual(got, want) {
			t.Errorf("neighbors %s in: callers %v, the graph has %v", id, keys(got), keys(want))
		}
		caller := keys(want)[0]
		path := call(t, c, "shortest_path", map[string]any{"from": caller, "to": string(id)})
		if found, _ := path["found"].(bool); !found {
			t.Errorf("shortest_path %s -> %s: not found, the graph has the edge", caller, id)
		}
		call(t, c, "subgraph_for_question", map[string]any{"question": "what calls " + n.Name})
	}
}

// callees picks up to n of the repository's Go declarations with 1 to 15
// production callers,
// in a stable order of their IDs' hashes, so a neighbors answer is never
// cut and the sample is the same every run.
func callees(g *graph.Graph, n int) []graph.ID {
	var ids []graph.ID
	for node := range g.Nodes() {
		// The repository's own declarations: an external package node is
		// where calls leave the module, and a question does not seed on it.
		if !strings.HasPrefix(string(node.ID), "go://") || node.Test || node.Pos.File == "" || node.Attrs["external"] == "true" {
			continue
		}
		callers := 0
		for _, e := range g.In(node.ID) {
			if e.Kind == graph.KindCalls {
				callers++
			}
		}
		if callers >= 1 && callers <= 15 {
			ids = append(ids, node.ID)
		}
	}
	hash := func(id graph.ID) uint64 { h := fnv.New64a(); h.Write([]byte(id)); return h.Sum64() }
	slices.SortFunc(ids, func(a, b graph.ID) int {
		ha, hb := hash(a), hash(b)
		switch {
		case ha < hb:
			return -1
		case ha > hb:
			return 1
		}
		return strings.Compare(string(a), string(b))
	})
	return ids[:min(n, len(ids))]
}

func listed(t *testing.T, c *client.Client, name string) bool {
	res, err := c.ListTools(context.Background(), mcpsdk.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func hasNode(answer map[string]any, id graph.ID) bool {
	nodes, _ := answer["nodes"].([]any)
	for _, n := range nodes {
		if m, ok := n.(map[string]any); ok && m["id"] == string(id) {
			return true
		}
	}
	return false
}

// edgeSet is the sources of an answer's calls edges into id. An edge
// names its ends by index into the answer's nodes (#157).
func edgeSet(answer map[string]any, id graph.ID) map[string]bool {
	out := map[string]bool{}
	edges, _ := answer["edges"].([]any)
	for _, e := range edges {
		if m, ok := e.(map[string]any); ok && endID(answer, m["t"]) == string(id) && m["k"] == string(graph.KindCalls) {
			out[endID(answer, m["f"])] = true
		}
	}
	return out
}

// endID reads an edge's end as an ID: an index into the nodes, or an ID.
func endID(answer map[string]any, v any) string {
	if i, ok := v.(float64); ok {
		if nodes, _ := answer["nodes"].([]any); int(i) >= 0 && int(i) < len(nodes) {
			id, _ := nodes[int(i)].(map[string]any)["id"].(string)
			return id
		}
		return ""
	}
	s, _ := v.(string)
	return s
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
