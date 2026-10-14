// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/graph"
)

// TestToolsRebuildWhenFilesChange is #204: a tool call rebuilds first
// when the fingerprint moved since the build, says so in its answer
// with the layers that reran, and does not when nothing moved.
func TestToolsRebuildWhenFilesChange(t *testing.T) {
	var builds atomic.Int32
	var fp atomic.Value
	fp.Store("v1")
	s := New(func() (*graph.Graph, BuildInfo, error) {
		n := builds.Add(1)
		info := BuildInfo{Root: "/repo", Layers: []string{"go", "md"}}
		if n > 1 {
			info.Rebuilt, info.Kept = []string{"go"}, []string{"md"}
		}
		return fixture(), info, nil
	}).WatchFiles(func() (string, error) { return fp.Load().(string), nil })
	c := connect(t, s)
	call := func() string {
		req := mcpsdk.CallToolRequest{}
		req.Params.Name, req.Params.Arguments = "find_nodes", map[string]any{"query": "Total"}
		res, err := c.CallTool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(mcpsdk.TextContent).Text
	}
	first, second := call(), call()
	if builds.Load() != 1 || strings.Contains(first, `"rebuilt"`) || strings.Contains(second, `"rebuilt"`) {
		t.Fatalf("unchanged files: %d builds, answers %q / %q", builds.Load(), first[:min(60, len(first))], second[:min(60, len(second))])
	}
	fp.Store("v2")
	third := call()
	if builds.Load() != 2 || !strings.HasPrefix(third, `{"rebuilt":["go"],`) {
		t.Fatalf("changed files: %d builds, answer %q", builds.Load(), third[:min(80, len(third))])
	}
	if fourth := call(); builds.Load() != 2 || strings.Contains(fourth, `"rebuilt"`) {
		t.Errorf("after the rebuild, unchanged again: %d builds, %q", builds.Load(), fourth[:min(60, len(fourth))])
	}
}

// TestToolsFoldInBackgroundProgress is #269: while a layer completes in
// the background, a call refreshes first when it has learnt more since
// the graph was made, not otherwise, and the answer says what is pending;
// once nothing is, the answer says nothing.
func TestToolsFoldInBackgroundProgress(t *testing.T) {
	var builds, refreshes, version atomic.Int32
	var note atomic.Value
	note.Store("jedi has resolved the calls of 1 of 2 Python files")
	s := New(func() (*graph.Graph, BuildInfo, error) {
		builds.Add(1)
		return fixture(), BuildInfo{Root: "/repo"}, nil
	}).WatchProgress(func() (int64, string) {
		return int64(version.Load()), note.Load().(string)
	}, func() (*graph.Graph, BuildInfo, error) {
		refreshes.Add(1)
		return fixture(), BuildInfo{Root: "/repo"}, nil
	})
	c := connect(t, s)
	call := func() string {
		req := mcpsdk.CallToolRequest{}
		req.Params.Name, req.Params.Arguments = "find_nodes", map[string]any{"query": "Total"}
		res, err := c.CallTool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(mcpsdk.TextContent).Text
	}
	if first := call(); builds.Load() != 1 || refreshes.Load() != 0 || !strings.HasPrefix(first, `{"pending":"jedi has resolved the calls of 1 of 2`) {
		t.Fatalf("first call: %d builds, %d refreshes, answer %q", builds.Load(), refreshes.Load(), first[:min(80, len(first))])
	}
	if call(); refreshes.Load() != 0 {
		t.Errorf("nothing learnt: %d refreshes, want 0", refreshes.Load())
	}
	version.Add(1)
	note.Store("")
	if third := call(); builds.Load() != 1 || refreshes.Load() != 1 || strings.Contains(third, `"pending"`) {
		t.Errorf("done: %d builds, %d refreshes, answer %q; want 1, 1 and no pending", builds.Load(), refreshes.Load(), third[:min(80, len(third))])
	}
}
