// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"path/filepath"
	"testing"
)

// TestCargoConfigRerunsRust: Cargo's configuration is the Rust layer's
// under a .cargo directory, at either name Cargo reads, in the root or
// below it, and a config.toml anywhere else is not (#117).
func TestCargoConfigRerunsRust(t *testing.T) {
	for _, tc := range []struct {
		file  string
		reads bool
	}{
		{".cargo/config.toml", true},
		{".cargo/config", true},
		{"crates/a/.cargo/config.toml", true},
		{"site/config.toml", false},
		{"config.toml", false},
		{"site/config", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"a\"\nversion = \"0.1.0\"\n")
			write(t, filepath.Join(root, "src", "lib.rs"), "pub fn a() {}\n")
			file := filepath.Join(root, filepath.FromSlash(tc.file))
			write(t, file, "[build]\n")
			before := rustSignature(t, root)
			write(t, file, "[build]\njobs = 1\n")
			if changed := rustSignature(t, root) != before; changed != tc.reads {
				t.Errorf("an edit to %s changed the Rust layer's signature: %v, want %v", tc.file, changed, tc.reads)
			}
		})
	}
}

func rustSignature(t *testing.T, root string) string {
	t.Helper()
	sigs, err := signatures(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sigs["rust"] == "" {
		t.Fatal("no Rust signature")
	}
	return sigs["rust"]
}

// TestCargoConfigIsAPathToMarkdown: .cargo/config has no extension, so
// its existence decides how a Markdown link to it resolves, as any such
// file's does (TestRebuildSeesExtensionlessPaths).
func TestCargoConfigIsAPathToMarkdown(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "# Hi\n\nSee [the config](.cargo/config).\n")
	// The directory is there already: a new directory is a change of its
	// own to the Markdown layer.
	write(t, filepath.Join(root, ".cargo", "config.toml"), "[build]\n")
	before, err := signatures(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".cargo", "config"), "[build]\n")
	after, err := signatures(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before["md"] == after["md"] {
		t.Error("creating .cargo/config left the Markdown layer's signature as it was")
	}
}
