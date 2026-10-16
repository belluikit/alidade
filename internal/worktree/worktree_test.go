// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/worktree/gittest"
)

// TestSame: a repository's main checkout and a worktree added with git
// itself are one repository, from the top or from a directory inside;
// another repository is not, nor is a directory in no repository.
func TestSame(t *testing.T) {
	base := gittest.TempDir(t)
	main, wt, other := filepath.Join(base, "main"), filepath.Join(base, "wt"), filepath.Join(base, "other")
	gittest.Init(t, main, map[string]string{"README.md": "# x\n"})
	gittest.Init(t, other, map[string]string{"README.md": "# x\n"})
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{main, wt, true},
		{wt, main, true},
		{Top(filepath.Join(wt, "sub")), main, true},
		{main, other, false},
		{wt, other, false},
		{base, main, false},
	} {
		if got := Same(c.a, c.b); got != c.want {
			t.Errorf("Same(%s, %s) = %v, want %v (common dirs %q and %q)", c.a, c.b, got, c.want, CommonDir(c.a), CommonDir(c.b))
		}
	}
	if got := Top(filepath.Join(wt, "sub")); got != wt {
		t.Errorf("Top(wt/sub) = %s, want %s", got, wt)
	}
}
