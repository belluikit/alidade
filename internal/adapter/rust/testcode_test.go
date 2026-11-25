// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/belluikit/alidade/internal/graph"
)

// testCodeLib is appended to the fixture's shapes/src/lib.rs: a module
// declared #[cfg(test)], the production module it stands in for, a file
// that opens with #![cfg(...)], an item of each kind of attribute, and a
// production function whose import cfg(test) swaps (#99).
const testCodeLib = `
#[cfg(test)]
mod mocks;
#[cfg(test)]
mod shared;
#[cfg(not(test))]
mod real;
#[cfg(test)]
use mocks::side;
#[cfg(not(test))]
use real::side;
mod extra;

pub fn size() -> u8 {
    side()
}

#[cfg(test)]
#[allow(dead_code)]
fn only_in_tests() {}

#[cfg(all(test, unix))] fn only_in_unix_tests() {}

#[cfg(any(test, feature = "spare"))]
fn not_only_in_tests() {}

#[test]
fn a_test() {
    fn nested_in_a_test() {}
    size();
    nested_in_a_test();
}

/// Not a test, whatever this says: #[test] #[cfg(test)]
pub fn documented() {}

#[cfg(not(test))]
const LIMIT: usize = 1 << 20;
#[cfg(test)]
const LIMIT: usize = 512;

#[cfg(not(test))] fn budget() -> usize { LIMIT }
#[cfg(test)] fn budget() -> usize { LIMIT / 2 }

pub fn limited() -> usize {
    budget()
}

pub struct Gate {
    pub open: bool,
}

impl Gate {
    pub fn production_method(&self) -> bool {
        self.open
    }
}

#[cfg(test)] impl Wrap<u8> {
    fn same_line_attribute(&self) -> u8 {
        self.0
    }
}

#[cfg(test)]
impl Gate {
    fn only_in_test_impl(&self) -> bool {
        self.open
    }

    fn second_in_test_impl(&self) -> bool {
        !self.open
    }
}


#[cfg(test)]
struct OnlyInTests {
    flag: bool,
}

pub struct Wrap<T>(pub T);

#[cfg(test)]
impl<T> Wrap<T>
where
    T: Clone,
{
    fn only_in_test_where(&self) -> T {
        self.0.clone()
    }
}

impl<T> Wrap<T>
where
    T: Copy,
{
    pub fn production_where(&self) -> T {
        self.0
    }
}
`

// TestTestCode marks the items that exist only in a crate's test build
// (#106, the owner's decision on #99): a #[cfg(test)] module's file and
// all in it, an inline #[cfg(test)] module's items, a file opening with
// #![cfg(test)], an item a cfg that needs test is on, a #[test], and an
// integration test. any(test, …) is not only a test's. An edge into test
// code is a test edge, so the production function that cfg(test) points
// at a mock calls it in a test edge, and the call the library build makes,
// to the production module's function, is not read: the limit ADR 009
// states.
func TestTestCode(t *testing.T) {
	dir := fixture(t, map[string]string{
		"shapes/src/mocks.rs": "pub fn side() -> u8 {\n    1\n}\n",
		"shapes/src/real.rs":  "pub fn side() -> u8 {\n    2\n}\n",
		"shapes/src/extra.rs": "#![cfg(test)]\n\npub fn extra_helper() {}\n",
		// an integration test opening with #![cfg(test)]: a crate root,
		// whose module symbol the library's root shares
		"shapes/tests/third.rs": "#![cfg(test)]\n\n#[test]\nfn third() {}\n",
		// a file the library declares #[cfg(test)] and the build script
		// compiles too, through #[path]: not only a test's
		"shapes/src/shared.rs": "pub fn shared_fn() -> u8 {\n    3\n}\n",
	})
	edit(t, filepath.Join(dir, "shapes/src/lib.rs"), "", testCodeLib)
	edit(t, filepath.Join(dir, "shapes/build.rs"), "#[path = \"src/shared.rs\"]\n#[allow(dead_code)]\nmod shared;\n\n", "")
	g, _ := extract(t, dir)
	test := testMarksOf(g)
	for id, want := range map[string]bool{
		"shapes/src/mocks.rs":                           true, // #[cfg(test)] mod mocks;
		"shapes/src/mocks.rs#side()":                    true,
		"shapes/src/extra.rs":                           true, // #![cfg(test)]
		"shapes/src/extra.rs#extra_helper()":            true,
		"shapes/src/lib.rs#tests.check()":               true, // inline #[cfg(test)] mod tests
		"shapes/src/lib.rs#only_in_tests()":             true, // two attributes, one a cfg(test)
		"shapes/src/lib.rs#only_in_unix_tests()":        true, // on the item's own line
		"shapes/src/lib.rs#a_test()":                    true, // #[test]
		"shapes/src/lib.rs#a_test().nested_in_a_test()": true, // held by a test
		"shapes/tests/third.rs#third()":                 true,
		"shapes/src/lib.rs#documented()":                false, // a doc comment is not an attribute
		"shapes/src/lib.rs#Gate.only_in_test_impl()":    true,  // #[cfg(test)] on its impl block
		"shapes/src/lib.rs#OnlyInTests.flag":            true,  // a field of a test struct
		"shapes/src/lib.rs#Gate.open":                   false,
		"shapes/src/lib.rs#LIMIT":                       false, // cfg(test) here, and a not(test) one of the same name
		"shapes/src/lib.rs#budget()":                    false, // the same, attribute and item on one line
		"shapes/src/shared.rs#shared_fn()":              false, // the build script's too
		"shapes/src/lib.rs#Gate.production_method()":    false,
		"shapes/src/lib.rs#Gate.second_in_test_impl()":  true, // past a sibling method
		"shapes/src/lib.rs#Wrap.same_line_attribute()":  true, // #[cfg(test)] impl on one line
		"shapes/src/lib.rs#Wrap.only_in_test_where()":   true, // past a where clause
		"shapes/src/lib.rs#Wrap.production_where()":     false,
		"shapes/tests/first.rs#first()":                 true, // an integration test
		"shapes/tests/common/mod.rs#sample()":           true, // and a module of one
		"shapes/src/lib.rs#not_only_in_tests()":         false,
		"shapes/src/lib.rs#size()":                      false,
		"shapes/src/lib.rs#area_of()":                   false,
		"shapes/src/lib.rs":                             false,
		"shapes/src/geometry.rs#Point":                  false,
	} {
		got, ok := test[id]
		if !ok {
			t.Errorf("%s: no node", id)
		} else if got != want {
			t.Errorf("%s: test %v; want %v", id, got, want)
		}
	}
	// The library build's side(), behind cfg(not(test)), is code
	// rust-analyzer does not read: no node, and no call into it.
	if _, ok := test["shapes/src/real.rs#side()"]; ok {
		t.Error("shapes/src/real.rs#side() has a node: rust-analyzer read cfg(not(test)) code, and ADR 009's limit is gone")
	}
	checkTestCalls(t, g)
}

// checkTestCalls: a production call into the item a cfg(test) swaps
// stays production, and the production function that cfg(test) points at
// a mock calls it in a test edge, the library build's call not read.
func checkTestCalls(t *testing.T, g *graph.Graph) {
	t.Helper()
	// A production call into the item a cfg(test) swaps stays production.
	limited := 0
	for e := range g.Edges() {
		if e.Kind != graph.KindCalls || e.From != "rust://shapes/src/lib.rs#limited()" {
			continue
		}
		limited++
		if e.Test {
			t.Errorf("limited() -> %s is a test edge; the library build has budget() too", e.To)
		}
	}
	if limited != 1 {
		t.Errorf("limited() has %d calls edges; want 1, to budget()", limited)
	}
	var calls []string
	for e := range g.Edges() {
		if e.Kind == graph.KindCalls && e.From == "rust://shapes/src/lib.rs#size()" {
			calls = append(calls, string(e.To)+map[bool]string{true: " test", false: ""}[e.Test])
		}
	}
	if want := []string{"rust://shapes/src/mocks.rs#side() test"}; !slices.Equal(calls, want) {
		t.Errorf("size() calls %v; want %v: the test build's call, a test edge, and not the library build's", calls, want)
	}
}

// TestNeedsTest reads which cfg predicates hold only when test does.
func TestNeedsTest(t *testing.T) {
	for pred, want := range map[string]bool{
		"test": true, " test ": true, "all(test, unix)": true, "all(unix, all(test))": true,
		"any(test, test)": true, "any(test, fuzzing)": false, "not(test)": false, "unix": false,
		`feature = "test"`: false, "all()": false, "any()": false, "testing": false,
	} {
		if got := needsTest(pred); got != want {
			t.Errorf("needsTest(%q) = %v; want %v", pred, got, want)
		}
	}
	for pred, want := range map[string]bool{
		"not(test)": true, "all(unix, not(test))": true, "not(unix)": false, "any(not(test), unix)": false, "test": false,
	} {
		if got := needsNotTest(pred); got != want {
			t.Errorf("needsNotTest(%q) = %v; want %v", pred, got, want)
		}
	}
	for attrs, want := range map[string]bool{
		"#[test]\n": true, "#[tokio::test]\n": true, "#[tokio :: test(flavor = \"multi_thread\")]\n": true,
		"#[cfg(test)]\n": true, "#[cfg_attr(test, derive(Debug))]\n": false, "#[allow(dead_code)]\npub ": false,
		"#[cfg(not(test))]\n": false, "#[testing]\n": false,
	} {
		if got := requiresTest(attrs); got != want {
			t.Errorf("requiresTest(%q) = %v; want %v", attrs, got, want)
		}
	}
}

// edit puts before and after around a fixture file's text.
func edit(t *testing.T, path, before, after string) {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(before+string(text)+after), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testMarksOf is each Rust node's test mark, by its ID without the scheme.
func testMarksOf(g *graph.Graph) map[string]bool {
	out := map[string]bool{}
	for n := range g.Nodes() {
		if s, _ := n.ID.Scheme(); s == graph.SchemeRust {
			out[string(n.ID)[len("rust://"):]] = n.Test
		}
	}
	return out
}
