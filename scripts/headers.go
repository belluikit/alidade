// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build ignore

// headers checks that every source file of alidade's own starts with its
// license header (#282):
//
//	SPDX-License-Identifier: MIT
//	Copyright (c) 2026 TrendVidia, LLC.
//
// as line comments, after a #! line when there is one. Source files are
// the tracked .go, .proto, .sh, .py and .js files. Left out: testdata,
// whose files are inputs whose line numbers tests assert, and the
// typescript-go submodule, which is Microsoft's.
//
//	go run scripts/headers.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
)

var comment = map[string]string{".go": "//", ".proto": "//", ".js": "//", ".sh": "#", ".py": "#"}

const (
	license   = "SPDX-License-Identifier: MIT"
	copyright = "Copyright (c) 2026 TrendVidia, LLC."
)

func main() {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "headers: git ls-files:", err)
		os.Exit(2)
	}
	checked, missing := 0, 0
	for _, f := range strings.Fields(string(out)) {
		c, ok := comment[path.Ext(f)]
		if !ok || strings.Contains(f, "testdata/") || strings.HasPrefix(f, "third_party/typescript-go/") {
			continue
		}
		text, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "headers:", err)
			os.Exit(2)
		}
		checked++
		lines := strings.SplitN(string(text), "\n", 4)
		if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
			lines = lines[1:]
		}
		if len(lines) < 2 || lines[0] != c+" "+license || lines[1] != c+" "+copyright {
			fmt.Fprintf(os.Stderr, "%s: no license header\n", f)
			missing++
		}
	}
	// Silence is not a pass: a run that looked at nothing says so.
	if checked == 0 {
		fmt.Fprintln(os.Stderr, "headers: no source file found; run from the repository root")
		os.Exit(2)
	}
	if missing > 0 {
		fmt.Fprintf(os.Stderr, "headers: %d of %d source files lack the header: %s %s / %s %s\n", missing, checked, "//", license, "//", copyright)
		os.Exit(1)
	}
	fmt.Printf("headers: %d source files carry the license header\n", checked)
}
