// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trendvidia/alidade/internal/adapter/ts/svelte"
	"github.com/trendvidia/alidade/internal/python"
)

// TestEmbeddedRuntimes runs each runtime the build links, in one process:
// V8 translating a component and CPython resolving a call (#237). A
// release links both, and they share the process's C libraries and
// signal handlers, so each is used, not only linked. The version line
// names the runtimes the build has.
func TestEmbeddedRuntimes(t *testing.T) {
	line := identity().String()
	if svelte.Available() != strings.Contains(line, "v8") || python.Available() != strings.Contains(line, "cpython ") {
		t.Errorf("version %q does not name exactly the runtimes linked (v8 %v, python %v)", line, svelte.Available(), python.Available())
	}
	if svelte.Available() {
		out, err := svelte.Convert("<script>let n = 1;</script>\n<p>{n}</p>\n", "C.svelte")
		if err != nil || out == nil {
			t.Fatalf("svelte.Convert = %v", err)
		}
	}
	if python.Available() {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "m.py"), []byte("def f():\n    pass\n\n\ndef g():\n    f()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := python.Analyze(dir, []python.File{{Rel: "m.py", Size: 30}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Calls) != 1 || r.Calls[0].Caller != "g" || r.Calls[0].TargetName != "f" {
			t.Errorf("python.Analyze = %+v, want g calling f", r)
		}
	}
}
