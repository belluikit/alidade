// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/trendvidia/alidade/internal/mcp"
)

// TestServeAnswersFromTheFilesOnDisk is #204's done-when on a real build:
// a function added to a file between two calls of one session is found
// by the second, which says the Go layer was rebuilt.
func TestServeAnswersFromTheFilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fresh\n\ngo 1.26\n")
	write("a.go", "package fresh\n\nfunc Alpha() int { return 1 }\n")
	buildFn, fingerprint, _, _, _ := sessionFuncs(dir, defaultOpts())
	s := mcp.New(buildFn).WatchFiles(fingerprint)
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
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	find := func(q string) string {
		req := mcpsdk.CallToolRequest{}
		req.Params.Name, req.Params.Arguments = "find_nodes", map[string]any{"query": q}
		res, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(mcpsdk.TextContent).Text
	}
	if got := find("Beta"); strings.Contains(got, "#Beta") {
		t.Fatalf("Beta before it exists: %s", got)
	}
	// A distinct modification time, whatever the file system's grain.
	time.Sleep(10 * time.Millisecond)
	write("a.go", "package fresh\n\nfunc Alpha() int { return 1 }\n\nfunc Beta() int { return 2 }\n")
	got := find("Beta")
	if !strings.Contains(got, "example.com/fresh#Beta") || !strings.HasPrefix(got, `{"rebuilt":[`) || !strings.Contains(got[:40], `"go"`) {
		t.Fatalf("after adding Beta: %s", got)
	}
	if again := find("Beta"); strings.HasPrefix(again, `{"rebuilt"`) {
		t.Errorf("unchanged since: rebuilt again: %s", again[:60])
	}
}
