// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package goasm

import (
	"os"
	"testing"

	"github.com/trendvidia/alidade/internal/graph"
)

// TestRealRepository runs the layer alone over the repository ALIDADE_REPO
// names and logs what it found; skipped when unset. The generates edges
// all drop here, since no Go layer ran — the build package's test of the
// same name is where they resolve.
func TestRealRepository(t *testing.T) {
	root := os.Getenv("ALIDADE_REPO")
	if root == "" {
		t.Skip("ALIDADE_REPO not set")
	}
	b := graph.NewBuilder(root)
	rep, err := Extract(b, root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	t.Logf("%s: %+v; %d nodes, %d generates edges emitted", root, rep, g.NodeCount(), len(g.Dropped()))
	for _, d := range g.Dropped() {
		t.Logf("  %s -> %s", d.Edge.From, d.Edge.To)
		break
	}
}
