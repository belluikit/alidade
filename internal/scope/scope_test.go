// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package scope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/worktree/gittest"
)

func TestSkips(t *testing.T) {
	d := New([]string{"vendor", "third_party", "web/legacy", " /gen/ "})
	for rel, want := range map[string]bool{
		"vendor": true, "a/b/vendor": true, "third_party": true, "x/third_party": true, // a name, anywhere
		"web/legacy": true, "web/legacy/old": true, "web/legacy2": false, "x/web/legacy": false, // a path, from the root
		"gen": true, "gen/a": true, "src/gen": true, // "gen" without a slash once trimmed is a name
		".": false, "": false, "web": false, "src/vendored": false,
	} {
		if got := d.Skips(rel); got != want {
			t.Errorf("Skips(%q) = %v, want %v", rel, got, want)
		}
	}
	if !d.Skips("web/legacy/a/b.ts") || !d.Skips("x/vendor/y/z.go") || d.Skips("web/new/b.ts") {
		t.Error("a file below a skipped directory is skipped, one elsewhere is not")
	}
	var nilDirs *Dirs
	if !nilDirs.Skips("third_party") || nilDirs.Skips("src") {
		t.Error("a nil list is the default")
	}
}

func TestLoad(t *testing.T) {
	for _, c := range []struct {
		name, file string
		skipped    []string
		read       []string
		err        string
	}{
		{"no file is the default", "", []string{"vendor", "third_party"}, []string{"src"}, ""},
		{"a list replaces the default", "skip = [\"generated\", \"web/legacy\"]\n", []string{"generated", "web/legacy"}, []string{"third_party", "vendor"}, ""},
		{"an empty list skips nothing", "skip = []\n", nil, []string{"third_party", "vendor"}, ""},
		{"a comment and no list is the default", "# nothing yet\n", []string{"third_party"}, nil, ""},
		{"an unknown key is an error", "skp = [\"x\"]\n", nil, nil, "unknown key \"skp\""},
		{"a list of non-strings is an error", "skip = [1]\n", nil, nil, "strings"},
	} {
		checkLoad(t, c.name, c.file, c.skipped, c.read, c.err)
	}
}

func checkLoad(t *testing.T, name, file string, skipped, read []string, wantErr string) {
	t.Helper()
	dir := t.TempDir()
	if file != "" {
		if err := os.WriteFile(filepath.Join(dir, File), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Load(dir)
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) || !strings.Contains(err.Error(), File) {
			t.Errorf("%s: err %v, want one naming %s and %q", name, err, File, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, s := range skipped {
		if !d.Skips(s) {
			t.Errorf("%s: %s read", name, s)
		}
	}
	for _, r := range read {
		if d.Skips(r) {
			t.Errorf("%s: %s skipped", name, r)
		}
	}
}

// TestInstalledPackages: installed packages are left out whatever the
// list says (#266): a virtual environment by its pyvenv.cfg, whatever its
// name, and .venv, venv and site-packages by name; a directory like a
// virtual environment but with no pyvenv.cfg is read.
func TestInstalledPackages(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"env/lib", "notenv/lib", "src"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "env", "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := New(nil)
	for rel, want := range map[string]bool{
		"env": true, "notenv": false, "src": false, ".venv": true, "a/venv": true, "lib/site-packages": true,
	} {
		if got := empty.SkipsDir(root, filepath.Join(root, rel)); got != want {
			t.Errorf("SkipsDir(%q) = %v, want %v", rel, got, want)
		}
	}
	for rel, want := range map[string]bool{
		"env/lib/x.py": true, "env/lib": true, "env": true, "notenv/lib/x.py": false, "src/a.go": false, "a/.venv/b.md": true,
	} {
		if got := empty.SkipsUnder(root, filepath.Join(root, rel)); got != want {
			t.Errorf("SkipsUnder(%q) = %v, want %v", rel, got, want)
		}
	}
	if empty.SkipsDir(root, root) {
		t.Error("the root is never skipped")
	}
}

// TestOtherCheckout pins #24: a worktree of root's repository nested in
// it is skipped, a repository of its own nested the same way is not, nor
// is a directory inside the worktree, which its top already covers.
func TestOtherCheckout(t *testing.T) {
	root := gittest.TempDir(t)
	gittest.Init(t, root, map[string]string{"README.md": "# x\n", "sub/a.md": "# a\n"})
	gittest.Git(t, root, "worktree", "add", "-q", "-b", "f", filepath.Join(root, "wt"))
	gittest.Init(t, filepath.Join(root, "own"), map[string]string{"README.md": "# own\n"})
	d := New(nil)
	for rel, want := range map[string]bool{"wt": true, "own": false, "wt/sub": false} {
		if got := d.SkipsDir(root, filepath.Join(root, rel)); got != want {
			t.Errorf("SkipsDir(%s) = %v, want %v", rel, got, want)
		}
	}
	if !d.SkipsUnder(root, filepath.Join(root, "wt", "README.md")) || d.SkipsUnder(root, filepath.Join(root, "own", "README.md")) {
		t.Error("SkipsUnder: a file of the nested worktree is skipped, one of the nested repository is not")
	}
}
