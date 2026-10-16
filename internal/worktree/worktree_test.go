// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package worktree_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/worktree"
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
		{worktree.Top(filepath.Join(wt, "sub")), main, true},
		{main, other, false},
		{wt, other, false},
		{base, main, false},
	} {
		if got := worktree.Same(c.a, c.b); got != c.want {
			t.Errorf("Same(%s, %s) = %v, want %v (common dirs %q and %q)", c.a, c.b, got, c.want, worktree.CommonDir(c.a), worktree.CommonDir(c.b))
		}
	}
	if got := worktree.Top(filepath.Join(wt, "sub")); got != wt {
		t.Errorf("Top(wt/sub) = %s, want %s", got, wt)
	}
}

// TestSameOutsideAnyCheckout: a directory in no repository has no top,
// and "" is of no repository, even when the process runs inside one.
// CommonDir("") once read ".git" from the process's directory, so a
// server serving no repository took any worktree of the one it was
// started in as its own.
func TestSameOutsideAnyCheckout(t *testing.T) {
	base := gittest.TempDir(t)
	main := filepath.Join(base, "main")
	gittest.Init(t, main, map[string]string{"README.md": "# x\n"})
	t.Chdir(main)
	if got := worktree.CommonDir(""); got != "" {
		t.Errorf("CommonDir(\"\") = %q, want \"\"", got)
	}
	if worktree.Same("", main) || worktree.Same(main, "") {
		t.Error("Same takes \"\" for the repository the process runs in")
	}
}

// TestGitEnvLeavesAHooksRepositoryOut: under a git hook, which exports
// GIT_DIR and GIT_INDEX_FILE, git run in a directory still reads that
// directory's repository, not the hook's.
func TestGitEnvLeavesAHooksRepositoryOut(t *testing.T) {
	base := gittest.TempDir(t)
	hooked, dir := filepath.Join(base, "hooked"), filepath.Join(base, "dir")
	gittest.Init(t, hooked, map[string]string{"README.md": "# x\n"})
	t.Setenv("GIT_DIR", filepath.Join(hooked, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(hooked, ".git", "index"))
	gittest.Init(t, dir, map[string]string{"a.md": "a\n"})
	if n := gittest.Git(t, hooked, "rev-list", "--count", "HEAD"); n != "1\n" {
		t.Errorf("the hook's repository has %q commits, want 1: the test's commit went there", n)
	}
	if got := gittest.Git(t, dir, "ls-files"); got != "a.md\n" {
		t.Errorf("dir's repository holds %q, want a.md", got)
	}
}
