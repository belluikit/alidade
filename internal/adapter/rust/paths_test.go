// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// pathsLib is appended to the fixture's shapes/src/lib.rs: modules public
// and private, and uses public, crate-wide, renamed, over several lines
// and by glob, and a use beside an inline module that a macro writes.
const pathsLib = `
pub mod reexp;
mod private;
mod elsewhere;
pub use private::exported;
pub use private::Kept as Held;
pub(crate) use reexp::parse as internal_parse;
pub use reexp::{
    parse as parse_it,
    inner::deep,
};
pub use reexp::globbed::*;

macro_rules! cfg_all {
    ($($item:item)*) => { $( #[cfg(all())] $item )* };
}

cfg_all! {
    pub mod wrapped {
        pub fn inside() {}
    }
    pub fn beside() {}
    pub use reexp::parse as parse_wrapped;
}
`

// TestPublicPaths: an item's node carries the paths outside code names it
// by (#116): its parent's path and its name, a module's only when it is
// declared pub mod, and each name a pub use gives it, renamed or not, on
// one line or several. A method is reached through its type, wherever
// its impl block is. pub(crate) use gives no path, nor does a glob: a
// divergence, pinned. An item's own visibility is not read, so a
// pub(crate) function in a public module has a path too. The package's
// tests, binary and build script have none. A use beside an inline
// module and a function, all written by one macro, is the file's: the
// index gives the module and the function the macro call's whole range,
// when the macro puts an attribute on each item, as tokio's cfg_sync! does.
func TestPublicPaths(t *testing.T) {
	dir := fixture(t, map[string]string{
		"shapes/src/reexp.rs": "pub fn parse() {}\n\npub mod inner {\n    pub fn deep() {}\n}\n\npub mod globbed {\n    pub fn by_glob() {}\n}\n\npub(crate) fn crate_only() {}\n",
		"shapes/src/private.rs": "pub fn exported() {}\n\npub fn not_exported() {}\n\npub struct Kept;\n\nimpl Kept {\n" +
			"    pub fn new() -> Kept {\n        Kept\n    }\n}\n",
		"shapes/src/elsewhere.rs": "impl crate::Square {\n    pub fn from_elsewhere(&self) -> f64 {\n        self.side\n    }\n}\n",
	})
	edit(t, filepath.Join(dir, "shapes/src/lib.rs"), "", pathsLib)
	g, _ := extract(t, dir)
	want := map[string]string{
		"shapes/src/lib.rs":                               "shapes",
		"shapes/src/lib.rs#Square":                        "shapes::Square",
		"shapes/src/elsewhere.rs#Square.from_elsewhere()": "shapes::Square::from_elsewhere",
		"shapes/src/reexp.rs":                             "shapes::reexp",
		"shapes/src/reexp.rs#parse()":                     "shapes::parse_it shapes::reexp::parse shapes::parse_wrapped",
		"shapes/src/lib.rs#beside()":                      "shapes::beside",
		"shapes/src/lib.rs#wrapped.inside()":              "shapes::wrapped::inside",
		"shapes/src/reexp.rs#inner":                       "shapes::reexp::inner",
		"shapes/src/reexp.rs#inner.deep()":                "shapes::deep shapes::reexp::inner::deep",
		"shapes/src/reexp.rs#globbed.by_glob()":           "shapes::reexp::globbed::by_glob", // the glob adds nothing
		"shapes/src/reexp.rs#crate_only()":                "shapes::reexp::crate_only",
		"shapes/src/private.rs":                           "",
		"shapes/src/private.rs#exported()":                "shapes::exported",
		"shapes/src/private.rs#not_exported()":            "",
		"shapes/src/private.rs#Kept":                      "shapes::Held",
		"shapes/src/private.rs#Kept.new()":                "shapes::Held::new",
	}
	for id, w := range want {
		n, ok := g.Node(graph.ID("rust://" + id))
		if !ok {
			t.Errorf("no node %s", id)
			continue
		}
		if got := n.Attrs["paths"]; got != w {
			t.Errorf("%s: paths %q, want %q", id, got, w)
		}
	}
	for n := range g.Nodes() {
		p := n.Attrs["paths"]
		switch rel := strings.TrimPrefix(string(n.ID), "rust://"); {
		case strings.Contains(p, "internal_parse"):
			t.Errorf("%s: paths %q: pub(crate) use gives no path", rel, p)
		case p != "" && (strings.Contains(rel, "/tests/") || strings.HasPrefix(rel, "tool/") || strings.HasPrefix(rel, "shapes/build.rs")):
			t.Errorf("%s: paths %q: only a library's items have paths", rel, p)
		}
	}
}
