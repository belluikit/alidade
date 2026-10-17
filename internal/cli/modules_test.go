// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/belluikit/alidade/internal/mcp"
)

// TestAModuleTheHostCannotTypeCheckCostsOnlyItsCalls is #37: served from
// a folder holding sibling modules, one whose only package does not
// type-check for the host (gl-js's cgo GLES types on macOS; here a
// declaration naming no type) and another package imports built no SSA
// package, and its call graph's
// error failed the whole graph. The graph tools answer for the other
// module, and graph_stats names the package that failed and the one
// whose calls are missing for it.
func TestAModuleTheHostCannotTypeCheckCostsOnlyItsCalls(t *testing.T) {
	root := t.TempDir()
	for rel, src := range map[string]string{
		"good/go.mod": "module example.com/good\n\ngo 1.26\n",
		"good/a.go":   "package good\n\nfunc Total() int { return helper() }\n\nfunc helper() int { return 1 }\n",
		"bad/go.mod":  "module example.com/bad\n\ngo 1.26\n",
		"bad/b.go":    "package bad\n\nvar X NoSuchType\n\nfunc B() int { return 1 }\n",
		// Importing it, so the module loads with every package left out
		// of the call graph, as gl-js's glutil imports its root package.
		"bad/sub/s.go": "package sub\n\nimport \"example.com/bad\"\n\nfunc S() int { return bad.B() }\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buildFn, fingerprint, pending, refresh, closeFn, _ := sessionFuncs(root, defaultOpts())
	t.Cleanup(closeFn)
	call := mcpCaller(t, mcp.New(buildFn).WatchFiles(fingerprint).WatchProgress(pending, refresh).Root(root))
	r := call("find_nodes", map[string]any{"query": "Total"})
	if at := nodeAt(r, "go://example.com/good#Total"); at != "good/a.go:3" {
		t.Errorf("good#Total at %q, want good/a.go:3: %v", at, r)
	}
	st := call("graph_stats", map[string]any{})
	failed, _ := st["go_packages_failed"].([]any)
	without, _ := st["go_packages_without_calls"].([]any)
	if !slices.Contains(failed, any("example.com/bad")) || !slices.Contains(without, any("example.com/bad/sub")) {
		t.Errorf("graph_stats: failed %v, without calls %v; want example.com/bad and example.com/bad/sub", failed, without)
	}
}
