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
	"slices"
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
	if top == "" {
		// filepath.Join would make ".git", the process's own.
		return ""
	}
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

// localEnv are the variables that point git at a repository other than
// the one its working directory is in, as `git rev-parse
// --local-env-vars` lists them. A git hook exports some of them, and -C
// does not override them.
var localEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// GitEnv is the process's environment without localEnv, for a git
// command that must read the repository its directory is in.
func GitEnv() []string {
	env := os.Environ()
	return slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(localEnv, name)
	})
}

func abs(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
