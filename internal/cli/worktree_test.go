// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/mcp"
	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// TestGraphToolsAnswerForAWorktree is #25's done-when on a real build: a
// graph tool given root answers from that worktree's graph, laid over the
// served checkout's — a declaration only the worktree has is found, at
// the worktree's position, and one both have is at the worktree's line,
// not the main checkout's — and says which tree it answered for.
func TestGraphToolsAnswerForAWorktree(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt := filepath.Join(base, "main"), filepath.Join(base, "wt")
	gittest.Init(t, main, map[string]string{
		"go.mod": "module example.com/w\n\ngo 1.26\n",
		"a/a.go": "package a\n\nfunc Total() int { return 1 }\n",
		"b/b.go": "package b\n\nimport \"example.com/w/a\"\n\nfunc Use() int { return a.Total() }\n",
	})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "wt", wt)
	gittest.Commit(t, wt, map[string]string{
		"a/a.go": "package a\n\n// Only is the worktree's.\nfunc Only() int { return Total() }\n\nfunc Total() int { return 1 }\n",
	}, "worktree change")

	buildFn, fingerprint, pending, refresh, closeFn, open := sessionFuncs(main, defaultOpts())
	t.Cleanup(closeFn)
	s := mcp.New(buildFn).WatchFiles(fingerprint).WatchProgress(pending, refresh).Worktrees(open).Root(main)
	call := mcpCaller(t, s)

	r := call("find_nodes", map[string]any{"query": "Only", "root": wt})
	if r["root"] != wt {
		t.Errorf("the answer does not say it is the worktree's: root %v", r["root"])
	}
	if at := nodeAt(r, "go://example.com/w/a#Only"); at != "a/a.go:4" {
		t.Errorf("the worktree's own declaration at %q, want a/a.go:4: %v", at, r)
	}
	r = call("find_nodes", map[string]any{"query": "Total", "root": wt})
	if at := nodeAt(r, "go://example.com/w/a#Total"); at != "a/a.go:6" {
		t.Errorf("Total at %q, want the worktree's a/a.go:6, not the main checkout's :3", at)
	}
	r = call("find_nodes", map[string]any{"query": "Only"})
	if at := nodeAt(r, "go://example.com/w/a#Only"); at != "" || r["root"] != nil {
		t.Errorf("without root, the main checkout's graph has Only at %q, root %v", at, r["root"])
	}
	r = call("neighbors", map[string]any{"id": "go://example.com/w/a#Only", "root": wt, "direction": "out"})
	if r["root"] != wt || nodeAt(r, "go://example.com/w/a#Total") == "" {
		t.Errorf("neighbors of the worktree's Only: %v", r)
	}
	st := call("graph_stats", map[string]any{"root": wt})
	o, _ := st["overlay"].(map[string]any)
	if st["root"] != wt || o == nil || o["base"] != main {
		t.Errorf("graph_stats for the worktree: root %v overlay %v", st["root"], st["overlay"])
	}
}

// mcpCaller serves s in process and returns a function calling one of its
// tools, failing the test on an error answer.
func mcpCaller(t *testing.T, s *mcp.Server) func(string, map[string]any) map[string]any {
	t.Helper()
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
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	return func(tool string, args map[string]any) map[string]any {
		t.Helper()
		var req mcpsdk.CallToolRequest
		req.Params.Name, req.Params.Arguments = tool, args
		res, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(mcpsdk.TextContent).Text
		if res.IsError {
			t.Fatalf("%s %v: %s", tool, args, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, text)
		}
		return out
	}
}

// nodeAt returns where an answer places a node, "" when it lists none.
func nodeAt(r map[string]any, id string) string {
	nodes, _ := r["nodes"].([]any)
	for _, n := range nodes {
		m, _ := n.(map[string]any)
		if m["id"] == id {
			at, _ := m["at"].(string)
			return at
		}
	}
	return ""
}
