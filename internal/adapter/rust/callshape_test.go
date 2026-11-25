// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bytes"
	"testing"
)

// TestCallShaped: a reference is a call when "(" follows it, or a
// turbofish and "(" (#100); a turbofish alone names the function as a
// value. The turbofish's arguments nest, may span lines, and `->` in one
// closes nothing.
func TestCallShaped(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"f(1)", true},
		{"f (1)", true},
		{"f::<u8>(1)", true},
		{"f::<u8> (1)", true},
		{"f::<Vec<Option<u8>>>(x)", true},
		{"f::<fn() -> u8>(g)", true},
		{"f::<\n    u8,\n>(1)", true},
		{"f::<u8>\n    (1)", true},
		{"f::<W>,", false}, // a vtable's entry: tokio's drop_arc_raw::<W>
		{"f::<fn() -> u8>;", false},
		{"f", false},
		{"f::<u8", false}, // never closed
		{"f.map(g)", false},
	} {
		lines := bytes.Split([]byte(c.text), []byte("\n"))
		if got := callShaped(lines, pos{line: 0, col: 1}); got != c.want {
			t.Errorf("callShaped(%q) = %v; want %v", c.text, got, c.want)
		}
	}
}
