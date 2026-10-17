// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// trees opens fake worktree graphs, counting how often each is opened and
// closed, and fails the test when one is built after it was closed.
type trees struct {
	t              *testing.T
	mu             sync.Mutex
	opened, closed map[string]int
}

func (w *trees) open(root string) (Tree, error) {
	w.mu.Lock()
	w.opened[root]++
	w.mu.Unlock()
	shut := false
	return Tree{
		Build: func() (*graph.Graph, BuildInfo, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if shut {
				w.t.Errorf("%s: a graph the server let go is built", root)
			}
			return graph.NewBuilder(root).Build(), BuildInfo{Root: root}, nil
		},
		Close: func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			shut = true
			w.closed[root]++
		},
	}, nil
}

// worktreesOf makes a repository and n other worktrees of it.
func worktreesOf(t *testing.T, n int) (string, []string) {
	base := gittest.TempDir(t)
	main := filepath.Join(base, "main")
	gittest.Init(t, main, map[string]string{"README.md": "# m\n"})
	var wts []string
	for i := range n {
		wt := filepath.Join(base, fmt.Sprintf("wt%d", i))
		gittest.Git(t, main, "worktree", "add", "-q", "-b", fmt.Sprintf("b%d", i), wt)
		wts = append(wts, wt)
	}
	return main, wts
}

// TestWorktreesAreLetGoLeastRecentlyUsedFirst pins the owner's decision
// on #25: the server holds maxWorktrees worktrees' graphs, lets go of the
// one used longest ago, and closes it then; and a graph it let go is
// never built again, however calls for several worktrees interleave.
func TestWorktreesAreLetGoLeastRecentlyUsedFirst(t *testing.T) {
	main, wts := worktreesOf(t, maxWorktrees+2)
	w := &trees{t: t, opened: map[string]int{}, closed: map[string]int{}}
	s := New(func() (*graph.Graph, BuildInfo, error) {
		return graph.NewBuilder(main).Build(), BuildInfo{Root: main}, nil
	}).Root(main).Worktrees(w.open)
	get := func(root string) {
		req := mcpsdk.CallToolRequest{}
		req.Params.Arguments = map[string]any{"root": root}
		if _, _, err := s.graphFor(context.Background(), req); err != nil {
			t.Error(err)
		}
	}
	for _, wt := range wts[:maxWorktrees] {
		get(wt)
	}
	get(wts[0]) // wts[1] is now the one used longest ago
	get(wts[maxWorktrees])
	get(wts[0])
	if w.closed[wts[1]] != 1 || w.closed[wts[0]] != 0 || w.opened[wts[0]] != 1 {
		t.Errorf("opened %v, closed %v: want %s let go and %s kept", w.opened, w.closed, wts[1], wts[0])
	}

	var wg sync.WaitGroup
	for i := range 200 * len(wts) {
		wg.Go(func() { get(wts[i%len(wts)]) })
	}
	wg.Wait()
	for _, wt := range wts {
		if w.opened[wt]-w.closed[wt] > 1 {
			t.Errorf("%s: opened %d times, closed %d", wt, w.opened[wt], w.closed[wt])
		}
	}
	if len(s.worktrees) != maxWorktrees {
		t.Errorf("held %d worktrees' graphs, want %d", len(s.worktrees), maxWorktrees)
	}
}
