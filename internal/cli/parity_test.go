// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/alidade/internal/graph"
	"github.com/trendvidia/alidade/internal/mcp"
)

// surfaceKeys names, for every field of mcp.BuildInfo, the key it is
// rendered under on both surfaces: graph_stats over MCP and the build
// block of the PXF the graph command writes. A field added to BuildInfo
// without a line here fails TestSurfacesAgree, and a key missing from
// either surface fails it too — which is how go_module_edges reached
// MCP alone for a release (#76 follow-up, PR #79) and must not again.
var surfaceKeys = map[string]string{
	"Root": "root", "Layers": "layers", "Rebuilt": "layers_rebuilt", "Kept": "layers_kept",
	"Build": "go_build", "ExcludedGoFiles": "go_excluded_files", "SchemaRoots": "schema_roots",
	"GoModules": "go_modules", "GoModulesFailed": "go_modules_failed", "GoModuleEdges": "go_module_edges", "GoTestModuleEdges": "go_test_module_edges",
	"GoTestNodes": "go_test_nodes", "GoTestsExact": "go_tests", "GoOutsideTree": "go_outside_tree", "GoPackagesFailed": "go_packages_failed", "GoPackagesWithoutCalls": "go_packages_without_calls", "Skipped": "skipped", "Elapsed": "build_ms",
}

// fullInfo is a BuildInfo with every field non-zero, so every key is
// rendered rather than omitted as empty.
func fullInfo() mcp.BuildInfo {
	return mcp.BuildInfo{
		Root: "/repo", Layers: []string{"go", "md"}, Rebuilt: []string{"md"}, Kept: []string{"go"},
		Build: "darwin/arm64", ExcludedGoFiles: 2, SchemaRoots: []string{"/elsewhere"},
		GoModules: []string{"example.com/a", "example.com/a/b"}, GoModulesFailed: []string{"example.com/a/tools"},
		GoModuleEdges:     []mcp.ModuleEdge{{From: "example.com/a/b", To: "example.com/a", Kind: "calls", Count: 4}, {From: "example.com/a", Kind: "imports", Count: 9}},
		GoTestModuleEdges: []mcp.ModuleEdge{{From: "example.com/a", Kind: "imports", Count: 2}},
		GoTestNodes:       7, GoTestsExact: true, GoOutsideTree: 3, GoPackagesFailed: []string{"example.com/a/wip"}, GoPackagesWithoutCalls: []string{"example.com/a/user"}, Skipped: []graph.Skip{{Layer: "md", File: "docs/My Notes.md", Reason: "segment contains whitespace"}},
		Elapsed: 1200 * time.Millisecond,
	}
}

func TestSurfacesAgree(t *testing.T) {
	info := fullInfo()
	typ := reflect.TypeOf(info)
	for i := range typ.NumField() {
		f := typ.Field(i)
		if _, ok := surfaceKeys[f.Name]; !ok {
			t.Errorf("BuildInfo.%s has no surface key: add it to surfaceKeys and render it in graph_stats and the build block", f.Name)
		}
		if reflect.ValueOf(info).Field(i).IsZero() {
			t.Errorf("fullInfo leaves BuildInfo.%s zero, so its key would be omitted and this test would not see it", f.Name)
		}
	}
	block := string(pxf.FormatDocument(&pxf.Document{Entries: []pxf.Entry{buildBlock(info)}}))
	stats := statsJSON(t, info)
	for field, key := range surfaceKeys {
		if !strings.Contains(block, key+" = ") && !strings.Contains(block, key+" {") {
			t.Errorf("build block lacks %s (BuildInfo.%s):\n%s", key, field, block)
		}
		if _, ok := stats[key]; !ok {
			t.Errorf("graph_stats lacks %s (BuildInfo.%s): %v", key, field, stats)
		}
	}
}

// statsJSON serves a graph with info over the real protocol and returns
// graph_stats' result.
func statsJSON(t *testing.T, info mcp.BuildInfo) map[string]any {
	t.Helper()
	s := mcp.New(func() (*graph.Graph, mcp.BuildInfo, error) { return graph.NewBuilder("").Build(), info, nil })
	c, err := client.NewInProcessClient(s.MCP())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var init mcpsdk.InitializeRequest
	init.Params.ProtocolVersion = mcpsdk.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpsdk.Implementation{Name: "test", Version: "0"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	var req mcpsdk.CallToolRequest
	req.Params.Name = "graph_stats"
	res, err := c.CallTool(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("graph_stats: %v %+v", err, res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
