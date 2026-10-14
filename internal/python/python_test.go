// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package python

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestParse reads each kind of resolver line, and refuses one it does not
// know or one with a field missing.
func TestParse(t *testing.T) {
	r := &Result{Skipped: map[string]string{}}
	err := parse("DECL\ta.py\tC.m\tmethod\t3\t5\t9\nIMPORT\ta.py\t1\tb.py\nIMPORT-OUTSIDE\ta.py\t2\nIMPORT-NONE\ta.py\t3\n"+
		"CALL\ta.py\t3\t5\tf.g\tb.py\t7\th\tfunction\nOUTSIDE\ta.py\t4\t1\nUNPLACED\ta.py\t4\t9\nNONE\ta.py\t5\t1\nNONE\ta.py\t6\t1\nSKIP\tc.py\tSyntaxError\n", r)
	if err != nil {
		t.Fatal(err)
	}
	want := &Result{
		Decls:          []Decl{{File: "a.py", Path: "C.m", Kind: "method", Line: 3, Col: 5, EndLine: 9}},
		Imports:        []Import{{File: "a.py", Line: 1, Target: "b.py"}},
		Calls:          []Call{{File: "a.py", Line: 3, Col: 5, Caller: "f.g", Target: "b.py", TargetLine: 7, TargetName: "h", TargetType: "function"}},
		ImportsOutside: 1, ImportsUnresolved: 1, Outside: 1, Unplaced: 1, Unresolved: 2,
		Skipped: map[string]string{"c.py": "SyntaxError"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("parse = %+v, want %+v", r, want)
	}
	for _, bad := range []string{"CALL\ta.py\t3\t5\tf\tb.py\t7\th", "CALL\ta.py\tx\t5\tf\tb.py\t7\th\tfunction", "DECL\ta.py\tf\tfunction\t1\t1", "WHAT\ta.py"} {
		if err := parse(bad, &Result{Skipped: map[string]string{}}); err == nil {
			t.Errorf("parse(%q) succeeded", bad)
		}
	}
}

// TestPlan: a file larger than a worker's share is split into shards of
// its call sites, every shard of every file is planned once, and the
// heaviest jobs spread across workers.
func TestPlan(t *testing.T) {
	parts := plan([]File{{"big.py", 800}, {"a.py", 100}, {"b.py", 60}, {"c.py", 40}}, 4)
	if len(parts) != 4 {
		t.Fatalf("plan gave %d workers, want 4: %v", len(parts), parts)
	}
	var got []string
	for _, js := range parts {
		for _, j := range js {
			got = append(got, j.rel+"/"+strconv.Itoa(j.shard)+"of"+strconv.Itoa(j.shards))
		}
	}
	slices.Sort(got)
	want := []string{"a.py/0of1", "b.py/0of1", "big.py/0of4", "big.py/1of4", "big.py/2of4", "big.py/3of4", "c.py/0of1"}
	if !slices.Equal(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
	for _, js := range parts {
		if n := 0; slices.ContainsFunc(js, func(j job) bool { n += btoi(j.rel == "big.py"); return n > 1 }) {
			t.Errorf("a worker has two shards of big.py: %v", js)
		}
	}
	if one := plan([]File{{"a.py", 10}}, 1); len(one) != 1 || len(one[0]) != 1 || one[0][0].shards != 1 {
		t.Errorf("plan for one worker = %v", one)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fixture is testdata/proj's files.
var fixture = []File{{"app.py", 1}, {"pkg/__init__.py", 1}, {"pkg/shapes.py", 1}, {"bad.py", 1}, {"lib/helperlib.py", 1}, {"scripts/tool.py", 1}}

// TestAnalyze pins jedi's answers on testdata/proj, built with the python
// tag. Calls: a class through a package re-export; a method on an
// inferred instance, and on an untyped parameter jedi infers from its
// call site; a module alias; a nested function; a call in a method; a
// variable holding two functions, followed to both; a module reached
// through sys.path.insert; a callee that is an expression, by its value:
// make()(1) and TABLE["s"](2); a with statement, by the __enter__ and
// __exit__ of its item's value, its own (Held) or inherited (Inherits);
// a call and an import after non-ASCII text
// on their line (tool.py:9-10), where ast's byte columns are not jedi's.
// A call through a parameter is unplaced, not guessed, and so is
// factory()(3), whose callee is built from one, and with lock. Functions
// named as values are references. Built without the tag,
// Analyze says it is unavailable.
func TestAnalyze(t *testing.T) {
	r, err := Analyze("testdata/proj", fixture, 1)
	if !Available() {
		if err != ErrUnavailable {
			t.Errorf("Analyze without the python tag = %v, want ErrUnavailable", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	checkFixture(t, r)
}

// checkBases pins each class's bases (#249): in its own file (Square,
// Inherits), in another through an attribute (Big) or a name a package
// re-exports (Tall); a builtin base is outside (Failure), and one that is
// a parameter unplaced, not guessed (kind.Made).
func checkBases(t *testing.T, r *Result) {
	t.Helper()
	var got []string
	for _, c := range r.Bases {
		got = append(got, c.File+":"+strconv.Itoa(c.Line)+" "+c.Caller+" "+c.Target+":"+strconv.Itoa(c.TargetLine)+" "+c.TargetName)
	}
	want := []string{
		"app.py:21 Big pkg/shapes.py:13 Square",
		"app.py:25 Tall pkg/shapes.py:13 Square",
		"pkg/shapes.py:13 Square pkg/shapes.py:6 Shape",
		"pkg/shapes.py:75 Inherits pkg/shapes.py:67 Held",
	}
	if !slices.Equal(got, want) {
		t.Errorf("bases:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if n := [...]int{r.BasesOutside, r.BasesUnplaced, r.BasesUnresolved}; n != [...]int{1, 1, 0} {
		t.Errorf("bases outside, unplaced, unresolved = %v, want [1 1 0]", n)
	}
}

// checkRefs: functions named as values, not called. A parameter (fn in
// functools.wraps(fn)) is none.
func checkRefs(t *testing.T, r *Result) {
	t.Helper()
	var refs []string
	for _, c := range r.Refs {
		refs = append(refs, c.File+":"+strconv.Itoa(c.Line)+" "+c.Caller+" "+c.Target+":"+strconv.Itoa(c.TargetLine)+" "+c.TargetName)
	}
	want := []string{
		"app.py:18 main app.py:14 inner",
		"pkg/shapes.py:31 logged pkg/shapes.py:29 wrapper",
		"pkg/shapes.py:47  pkg/shapes.py:39 small",
		"pkg/shapes.py:47  pkg/shapes.py:43 large",
		"pkg/shapes.py:52 make pkg/shapes.py:43 large",
		"pkg/shapes.py:55  pkg/shapes.py:39 small",
		"pkg/shapes.py:64  pkg/shapes.py:51 make",
		"pkg/shapes.py:86 holds pkg/shapes.py:23 helper",
		"pkg/shapes.py:89  pkg/shapes.py:23 helper",
		"pkg/shapes.py:108 adder pkg/shapes.py:106 add",
		"pkg/shapes.py:129  pkg/shapes.py:125 twice",
		"pkg/shapes.py:129  pkg/shapes.py:111 plus_one",
	}
	if !slices.Equal(refs, want) {
		t.Errorf("refs:\n%s\nwant:\n%s", strings.Join(refs, "\n"), strings.Join(want, "\n"))
	}
}

func checkFixture(t *testing.T, r *Result) {
	t.Helper()
	checkCalls(t, r)
	checkRefs(t, r)
	checkBases(t, r)
	checkDecls(t, r)
	checkImports(t, r)
	// Outside: sum, print, functools.wraps, @dataclass, REGISTRY.append, the four os.path calls, and
	// open, with open and sorted in holds, @property and bit_length in
	// width (#253: Box().size is a read of a property, its call; b.size in
	// measure, through a parameter, is none: its one caller, sized, is in
	// another file, which jedi is not let search); unplaced: fn(*a), factory() and
	// factory()(3), and with lock, each built from a parameter. Imports
	// outside: dataclasses, functools, os, sys, typing.
	got := [...]int{r.Outside, r.Unplaced, r.Unresolved, r.ImportsOutside, r.ImportsUnresolved, len(r.Skipped)}
	if want := [...]int{14, 4, 0, 5, 0, 1}; got != want || r.Skipped["bad.py"] != "SyntaxError" {
		t.Errorf("outside, unplaced, unresolved, imports outside, imports unresolved, skipped = %v, want %v; skipped %v", got, want, r.Skipped)
	}
}

func checkCalls(t *testing.T, r *Result) {
	t.Helper()
	var calls []string
	for _, c := range r.Calls {
		calls = append(calls, strings.Join([]string{c.File + ":" + strconv.Itoa(c.Line), c.Caller, c.Target + ":" + strconv.Itoa(c.TargetLine), c.TargetName, c.TargetType}, " "))
	}
	slices.Sort(calls)
	want := []string{
		"app.py:10 main pkg/shapes.py:13 Square class",
		"app.py:11 main pkg/shapes.py:19 area function",
		"app.py:12 main pkg/shapes.py:23 helper function",
		"app.py:15 main.inner app.py:9 main function",
		"app.py:17 main app.py:5 total function",
		"app.py:39  app.py:33 kind function",
		"app.py:43 width pkg/shapes.py:92 Box class",
		"app.py:43 width pkg/shapes.py:94 size function",
		"app.py:47 sized pkg/shapes.py:92 Box class",
		"app.py:47 sized pkg/shapes.py:98 measure function",
		"app.py:6 total pkg/shapes.py:19 area function",
		"pkg/shapes.py:112 plus_one pkg/shapes.py:105 adder function",
		"pkg/shapes.py:113 plus_one pkg/shapes.py:106 add function",
		"pkg/shapes.py:129  pkg/shapes.py:120 Handler class",
		"pkg/shapes.py:129  pkg/shapes.py:120 Handler class",
		"pkg/shapes.py:134 run_all pkg/shapes.py:111 plus_one function",
		"pkg/shapes.py:134 run_all pkg/shapes.py:125 twice function",
		"pkg/shapes.py:145  pkg/shapes.py:140 registered function",
		"pkg/shapes.py:151 scaled pkg/shapes.py:146 triple function",
		"pkg/shapes.py:151 scaled pkg/shapes.py:29 wrapper function",
		"pkg/shapes.py:151 scaled pkg/shapes.py:35 double function",
		"pkg/shapes.py:20 Square.area pkg/shapes.py:23 helper function",
		"pkg/shapes.py:34  pkg/shapes.py:27 logged function",
		"pkg/shapes.py:36 double pkg/shapes.py:23 helper function",
		"pkg/shapes.py:48  pkg/shapes.py:39 small function",
		"pkg/shapes.py:48  pkg/shapes.py:43 large function",
		"pkg/shapes.py:59 dispatch pkg/shapes.py:43 large function",
		"pkg/shapes.py:59 dispatch pkg/shapes.py:51 make function",
		"pkg/shapes.py:60 dispatch pkg/shapes.py:39 small function",
		"pkg/shapes.py:64  pkg/shapes.py:58 dispatch function",
		"pkg/shapes.py:80 holds pkg/shapes.py:67 Held class",
		"pkg/shapes.py:80 holds pkg/shapes.py:68 __enter__ function",
		"pkg/shapes.py:80 holds pkg/shapes.py:71 __exit__ function",
		"pkg/shapes.py:82 holds pkg/shapes.py:68 __enter__ function",
		"pkg/shapes.py:82 holds pkg/shapes.py:71 __exit__ function",
		"pkg/shapes.py:82 holds pkg/shapes.py:75 Inherits class",
		"scripts/tool.py:10  lib/helperlib.py:1 go function",
		"scripts/tool.py:7  lib/helperlib.py:1 go function",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func checkDecls(t *testing.T, r *Result) {
	t.Helper()
	var decls []string
	for _, d := range r.Decls {
		decls = append(decls, d.File+"#"+d.Path+" "+d.Kind+" "+strconv.Itoa(d.Line)+"-"+strconv.Itoa(d.EndLine))
	}
	for _, w := range []string{"app.py#main.inner function 14-15", "pkg/shapes.py#UNIT variable 3-3", "pkg/shapes.py#Square class 13-20",
		"pkg/shapes.py#Square.sides field 14-14", "pkg/shapes.py#Square.area method 19-20", "pkg/shapes.py#logged.wrapper function 29-30",
		"pkg/shapes.py#pick variable 47-47", "lib/helperlib.py#go function 1-2"} {
		if !slices.Contains(decls, w) {
			t.Errorf("no declaration %q in %v", w, decls)
		}
	}
	if len(r.Decls) != 53 {
		t.Errorf("%d declarations, want 53: %v", len(r.Decls), decls)
	}
}

func checkImports(t *testing.T, r *Result) {
	t.Helper()
	var imports []string
	for _, i := range r.Imports {
		imports = append(imports, i.File+":"+strconv.Itoa(i.Line)+" "+i.Target)
	}
	if want := []string{"app.py:1 pkg/shapes.py", "app.py:2 pkg/__init__.py", "pkg/__init__.py:1 pkg/shapes.py", "scripts/tool.py:5 lib/helperlib.py", "scripts/tool.py:9 lib/helperlib.py"}; !slices.Equal(imports, want) {
		t.Errorf("imports = %v, want %v", imports, want)
	}
}
