// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package svelte

import "testing"

// TestDecode pins the source-map reader on a hand-written map: "AAAA"
// maps column 0 of line 0 to 0:0; ";AACA,IAAI" maps line 1's column 0
// to line 1 column 0 and its column 4 to line 1 column 4.
func TestDecode(t *testing.T) {
	o := &Output{lines: decode("AAAA;AACA,IAAI")}
	for _, c := range []struct{ line, col, wantLine, wantCol int }{
		{0, 0, 0, 0}, {0, 3, 0, 3}, {1, 0, 1, 0}, {1, 4, 1, 4}, {1, 6, 1, 6},
	} {
		l, col, ok := o.Original(c.line, c.col)
		if !ok || l != c.wantLine || col != c.wantCol {
			t.Errorf("Original(%d, %d) = %d, %d, %v; want %d, %d", c.line, c.col, l, col, ok, c.wantLine, c.wantCol)
		}
	}
	if _, _, ok := o.Original(5, 0); ok {
		t.Error("a line past the map maps")
	}
	if got := vlq("2HwBA"); len(got) != 3 || got[0] != 123 || got[1] != 24 || got[2] != 0 {
		t.Errorf("vlq(2HwBA) = %v, want [123 24 0]", got) // 2H: 22 + 7<<5 = 246, 123; wB: 16 + 1<<5 = 48, 24
	}
}
