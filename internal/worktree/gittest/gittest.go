// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package gittest makes git repositories and worktrees for tests, with
// git itself, so a test sees the files git really writes.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/worktree"
)

// Init makes a git repository at dir holding files, committed.
func Init(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "main")
	Commit(t, dir, files, "init")
}

// Commit writes files under dir and commits everything.
func Commit(t testing.TB, dir string, files map[string]string, msg string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

// Git runs git in dir with a fixed identity and none of the user's
// configuration, and returns its output. A GIT_DIR or GIT_INDEX_FILE a
// git hook exported is dropped, or git would write to that repository
// rather than dir's.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(worktree.GitEnv(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TempDir is t.TempDir with its symbolic links resolved, as a path the
// code under test computes is: /var is /private/var on macOS.
func TempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}
