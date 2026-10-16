// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package worktree tells the checkouts of one git repository apart from
// those of another (#23): the main checkout and each `git worktree`
// share one git directory, so search_text answers for any of them, and
// the grep hook refuses in any of them. It reads the files git writes —
// a linked worktree's .git file and its commondir — rather than running
// git, since the hook runs before every Bash call.
package worktree

import (
	"os"
	"path/filepath"
	"strings"
)

// Top is the top directory of the checkout holding dir, or "".
func Top(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// CommonDir is the git directory a checkout's top shares with every
// other checkout of its repository: the main checkout's .git, which a
// linked worktree's .git file leads to through its gitdir's commondir.
// It is "" when top is no checkout, and has its symbolic links resolved,
// so two of them compare as strings.
func CommonDir(top string) string {
	dotgit := filepath.Join(top, ".git")
	info, err := os.Stat(dotgit)
	if err != nil {
		return ""
	}
	dir := dotgit
	if !info.IsDir() {
		b, err := os.ReadFile(dotgit)
		if err != nil {
			return ""
		}
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		dir = abs(top, strings.TrimSpace(gitdir))
		// A submodule's gitdir has no commondir: it is its own.
		if c, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
			dir = abs(dir, strings.TrimSpace(string(c)))
		}
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return filepath.Clean(dir)
}

// Same reports whether two checkout tops are of one repository.
func Same(a, b string) bool {
	ca := CommonDir(a)
	return ca != "" && ca == CommonDir(b)
}

func abs(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
