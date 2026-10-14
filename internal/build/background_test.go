// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package build

import (
	"path/filepath"
	"testing"

	"github.com/belluikit/alidade/internal/python"
)

// TestSessionBackground is #269: with the Python layer in the
// background, a build answers from the warm-up pass, an edit stops jedi
// reading the old files and starts it on the new ones, and once jedi is
// done a refresh gives the graph a build that waits for jedi gives.
func TestSessionBackground(t *testing.T) {
	if !python.Available() {
		t.Skip("built without the python tag")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "a.py"), "def f():\n    pass\n\n\nclass C:\n    def m(self):\n        pass\n")
	write(t, filepath.Join(root, "b.py"), "from a import f, C\n\n\ndef g():\n    f()\n    C().m()\n")
	opts := DefaultOptions()
	opts.Py.Background = true
	s, err := NewSession(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, rep := mustSessionBuild(t, s)
	first := rep.Py.Live
	if first == nil {
		t.Fatal("no jedi in the background")
	}
	write(t, filepath.Join(root, "b.py"), "from a import f, C\n\n\ndef g():\n    f()\n    C().m()\n\n\ndef h():\n    g()\n")
	_, rep = mustSessionBuild(t, s)
	if rep.Py.Live == first {
		t.Fatal("the edit did not start jedi again")
	}
	if _, _, done := first.Progress(); !done {
		t.Error("jedi reading the old files was not stopped")
	}
	rep.Py.Live.Wait()
	g, _, err := s.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if _, note := s.Pending(); note != "" {
		t.Errorf("pending after jedi: %q", note)
	}
	sameAsFresh(t, root, g)
}
