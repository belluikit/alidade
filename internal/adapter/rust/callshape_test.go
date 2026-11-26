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

// TestInUse: a reference inside a use declaration's tree names a function,
// it neither calls nor refers to it, on the line opening the use or on one
// the tree continues to (#114), after any visibility, a path in it
// included.
func TestInUse(t *testing.T) {
	for _, c := range []struct {
		text string // the reference starts at the "@", which is cut
		want bool
	}{
		{"use crate::codec::@parse;", true},
		{"    use crate::codec::{\n        @parse,\n        decode,\n    };", true},
		{"    use crate::codec::{\n        parse,\n        @decode,\n    };", true},
		{"use crate::{\n    codec::{\n        @parse, // the parser\n    },\n};", true},
		{"use crate::codec::{\n    // the parser:\n    @parse,\n};", true},
		{"pub use crate::codec::@parse;", true},
		{"pub(crate) use crate::codec::@parse;", true},
		{"pub(super) use super::@parse;", true},
		{"pub(in crate::codec) use crate::codec::@parse;", true},
		{"pub(in crate::codec)\nuse crate::codec::{\n    @parse,\n};", true},
		{"use crate::codec::{parse as p, @decode};", true},
		{"use crate::r#fn::@parse;", true},
		// The walk runs on past the use, through the "{" opening the body.
		{"pub fn run() {\n    use crate::codec::{\n        @parse,\n    };", true},
		{"pub fn run() {\n    pub(in crate) use crate::codec::@parse;", true},
		{"if x {\n} else {\n    use crate::codec::{\n        @parse,\n    };", true},
		{"pub fn run() {\n    use crate::codec::parse;\n    @parse();", false},
		{"pub fn run() {\n    r#use::@parse();", false},
		{"pub fn run() {\n    use_it {\n        @parse,\n    };", false},
		{"use crate::codec::*; @parse(1)", false},
		{"let f = @parse;", false},
		{"@parse(1);", false},
		{"// use this\n@parse(1);", false},
		{"let user = 1;\n@parse(1);", false},
		{"loop {\n    @parse(1);\n}", false},
		{"fn reuse() {\n    @parse(1);\n}", false},
		{"let v = [\n    @parse,\n];", false},
		{"usefoo::@parse(1);", false},
		{"let x = reuse::@parse(1);", false},
		// A divergence, pinned: a block comment in the tree stops the walk,
		// so the name after it is read as named outside a use. Letting "/"
		// into the run would read `/* we use it */ f()` as a use, which
		// loses a call; this loses only a false reference, and is rare.
		{"use crate::codec::{/* the parser */ @parse};", false},
		{"/* we use it */ @parse(1);", false},
	} {
		text := []byte(c.text)
		at := bytes.IndexByte(text, '@')
		text = append(text[:at:at], text[at+1:]...)
		line := bytes.Count(text[:at], []byte("\n"))
		col := at - (bytes.LastIndexByte(text[:at], '\n') + 1)
		lines := bytes.Split(text, []byte("\n"))
		if got := inUse(lines, pos{line: int32(line), col: int32(col)}); got != c.want {
			t.Errorf("inUse(%q) = %v; want %v", c.text, got, c.want)
		}
	}
}
