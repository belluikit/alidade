// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build python

package python

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trendvidia/pylib"
)

// TestAnalyzeWorkers: shared between worker processes — this test binary,
// started in worker mode — the fixture gives what one process gives.
func TestAnalyzeWorkers(t *testing.T) {
	defer func(n int64) { inProcess = n }(inProcess)
	inProcess = 0
	r, err := Analyze("testdata/proj", fixture, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Workers != 3 || len(r.WorkerErrors) != 0 {
		t.Fatalf("workers %d, errors %v; want 3 and none", r.Workers, r.WorkerErrors)
	}
	checkFixture(t, r)
	one, err := Analyze("testdata/proj", fixture, 1)
	if err != nil {
		t.Fatal(err)
	}
	one.Workers = r.Workers
	if !reflect.DeepEqual(one, r) {
		t.Errorf("workers' result differs from one process's:\n%+v\n%+v", r, one)
	}
}

// TestAnalyzeWorkerFails: a worker that cannot start has its files read
// in this process, and the result says so.
func TestAnalyzeWorkerFails(t *testing.T) {
	defer func(n int64, e func() (string, error)) { inProcess, executable = n, e }(inProcess, executable)
	inProcess = 0
	executable = func() (string, error) { return "", errors.New("no executable") }
	r, err := Analyze("testdata/proj", fixture, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.WorkerErrors) != 2 {
		t.Errorf("worker errors %v, want 2", r.WorkerErrors)
	}
	checkFixture(t, r)
}

// TestAnalyzeEach (#269): each file's own result comes once, as soon as
// its jobs are done, and together they are the whole result.
func TestAnalyzeEach(t *testing.T) {
	defer func(n int64) { inProcess = n }(inProcess)
	inProcess = 0
	got := map[string]*Result{}
	r, err := AnalyzeEach(context.Background(), "testdata/proj", fixture, 3, func(rel string, f *Result) {
		if got[rel] != nil {
			t.Errorf("%s came twice", rel)
		}
		got[rel] = f
	})
	if err != nil {
		t.Fatal(err)
	}
	checkFixture(t, r)
	if len(got) != len(fixture) {
		t.Errorf("%d files came, want %d", len(got), len(fixture))
	}
	c := newCollector(nil)
	c.results = got
	joined := c.result(r.Workers)
	joined.WorkerErrors = r.WorkerErrors
	if !reflect.DeepEqual(joined, r) {
		t.Errorf("the files' results joined differ from the result:\n%+v\n%+v", joined, r)
	}
}

// TestAnalyzeWorkerCut: a worker whose output stops partway has the jobs
// it did not finish read in this process; each file still comes once.
func TestAnalyzeWorkerCut(t *testing.T) {
	defer func(n int64, e func() (string, error)) { inProcess, executable = n, e }(inProcess, executable)
	inProcess = 0
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "worker")
	script := "#!/bin/bash\nset -o pipefail\n'" + self + "' | head -n 12\n"
	if err := os.WriteFile(cut, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return cut, nil }
	got := map[string]int{}
	r, err := AnalyzeEach(context.Background(), "testdata/proj", fixture, 2, func(rel string, _ *Result) { got[rel]++ })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.WorkerErrors) == 0 {
		t.Error("no worker error: the cut did not cut")
	}
	checkFixture(t, r)
	for _, f := range fixture {
		if got[f.Rel] != 1 {
			t.Errorf("%s came %d times, want once", f.Rel, got[f.Rel])
		}
	}
}

// TestNames (#269): the warm-up pass gives every file jedi's declarations,
// and each call it places by name is one jedi places there too.
func TestNames(t *testing.T) {
	names, err := Names("testdata/proj", fixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Analyze("testdata/proj", fixture, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector(nil)
	c.results = names
	r := c.result(1)
	if !reflect.DeepEqual(r.Decls, full.Decls) || !reflect.DeepEqual(r.Skipped, full.Skipped) {
		t.Errorf("declarations or skipped differ from jedi's:\n%v %v\n%v %v", r.Decls, r.Skipped, full.Decls, full.Skipped)
	}
	jedi := map[Call]bool{}
	for _, call := range full.Calls {
		jedi[call] = true
	}
	for _, call := range r.Calls {
		if !jedi[call] {
			t.Errorf("by name %+v, which jedi does not give", call)
		}
	}
	// A class through a package re-export, a nested function, a module
	// reached through sys.path.insert among them; not a method on an
	// inferred instance, which needs jedi.
	if len(r.Calls) != 23 {
		t.Errorf("%d calls placed by name, want 23: %+v", len(r.Calls), r.Calls)
	}
}

// TestNamesAlone (#269): each file answered with the rest only known, as
// after an edit, gets the answers it gets among all.
func TestNamesAlone(t *testing.T) {
	names, err := Names("testdata/proj", fixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range fixture {
		others := slices.Concat(fixture[:i], fixture[i+1:])
		one, err := Names("testdata/proj", []File{f}, others)
		if err != nil {
			t.Fatal(err)
		}
		if len(one) != 1 || !reflect.DeepEqual(one[f.Rel], names[f.Rel]) {
			t.Errorf("%s answered alone: %+v, want %+v", f.Rel, one, names[f.Rel])
		}
	}
}

// TestLastLine: a worker's failure is said by its stderr's last line, the
// exception of a traceback.
func TestLastLine(t *testing.T) {
	tb := "Traceback (most recent call last):\n  File \"<x>\", line 1, in <module>\nMemoryError: out\n\n"
	if got := lastLine(tb); got != "MemoryError: out" {
		t.Errorf("lastLine = %q", got)
	}
	if got := lastLine("one"); got != "one" {
		t.Errorf("lastLine one line = %q", got)
	}
}

// TestCountsReset (#255): jedi's count of each node's inferences, capped
// at 300 per Script, is cleared at each request with its other limits.
// Without that, a Script reused for a file's sites answers nothing through
// a node that passed the cap, typeshed's list on graphify's llm.py.
func TestCountsReset(t *testing.T) {
	if err := start(); err != nil {
		t.Fatal(err)
	}
	probe := strings.Replace(resolveScript, "OUT = main(IN)", countsProbe, 1)
	if probe == resolveScript {
		t.Fatal("the resolver has no OUT = main(IN) to replace")
	}
	out, err := pylib.Run(probe, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "reset" {
		t.Errorf("a count past the cap after a request: %q, want reset", out)
	}
}

const countsProbe = `
script = jedi.Script(code="x = 1\nx\n", environment=jedi.InterpreterEnvironment())
script._inference_state.inferred_element_counts["stale"] = 301
script.infer(2, 0)
OUT = "kept" if "stale" in script._inference_state.inferred_element_counts else "reset"
`

// TestValueSetOrdered (#267): jedi tries an expression's values in the
// order they were added, not in the order of their memory addresses,
// which made two builds of openpilot differ where a limit cut inference
// short.
func TestValueSetOrdered(t *testing.T) {
	if err := start(); err != nil {
		t.Fatal(err)
	}
	probe := strings.Replace(resolveScript, "OUT = main(IN)", orderProbe, 1)
	if probe == resolveScript {
		t.Fatal("the resolver has no OUT = main(IN) to replace")
	}
	out, err := pylib.Run(probe, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ordered" {
		t.Errorf("a ValueSet's iteration: %q, want ordered", out)
	}
}

// TestParsoCacheKept pins the resolver's hold on parso's trees (#272):
// parso drops every tree not used in 10 minutes once it holds 600, and a
// tree's last use starts at its file's modification time.
func TestParsoCacheKept(t *testing.T) {
	if err := start(); err != nil {
		t.Fatal(err)
	}
	probe := strings.Replace(resolveScript, "OUT = main(IN)", cacheProbe, 1)
	if probe == resolveScript {
		t.Fatal("the resolver has no OUT = main(IN) to replace")
	}
	out, err := pylib.Run(probe, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "kept" {
		t.Errorf("parso's tree of a file last edited long ago: %q, want kept", out)
	}
}

// TestParsoNoPickles pins that the resolver keeps parso's trees off disk
// (#279): parso pickles each tree into a directory every process shares,
// and a pickle read while another process writes it raised from inside
// an import, which then resolved to nothing. A tree is neither written
// there nor read back, even when the pickle is newer than its file.
func TestParsoNoPickles(t *testing.T) {
	if err := start(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	probe := strings.Replace(resolveScript, "OUT = main(IN)", pickleProbe, 1)
	out, err := pylib.Run(probe, dir)
	if err != nil {
		t.Fatal(err)
	}
	if out != "0 pickles written, a damaged one not read" {
		t.Errorf("parso's disk cache: %q", out)
	}
}

const pickleProbe = `
import os
import pathlib
d = pathlib.Path(IN)
src = str(d / "m.py")
with open(src, "w") as f:
    f.write("x = 1\n")
grammar = parso.load_grammar()
grammar.parse(path=src, cache=True, cache_path=d)
written = sum(1 for _, _, fs in os.walk(d) for f in fs if f.endswith(".pkl"))
parso.cache.parser_cache.pop(grammar._hashed, None)
pickle = parso.cache._get_hashed_path(grammar._hashed, src, cache_path=d)
os.makedirs(os.path.dirname(pickle), exist_ok=True)
with open(pickle, "wb") as f:
    f.write(b"\x80")
os.utime(pickle, (os.path.getmtime(src) + 60,) * 2)
try:
    grammar.parse(path=src, cache=True, cache_path=d)
    read = "a damaged one not read"
except Exception as e:
    read = "a damaged one read: %r" % e
OUT = "%d pickles written, %s" % (written, read)
`

const cacheProbe = `
for i in range(1000):
    parso.cache._set_cache_item("probe", "/old/%d.py" % i, parso.cache._NodeCacheItem(None, [], change_time=0))
OUT = "kept" if "/old/0.py" in parso.cache.parser_cache["probe"] else "dropped"
del parso.cache.parser_cache["probe"]
`

const orderProbe = `
values = [object() for _ in range(64)]
values.sort(key=id, reverse=True)
half = len(values) // 2
sets = [ValueSet(values), ValueSet(values[:half]) | ValueSet(values[half:]),
        ValueSet.from_sets([values[:half], ValueSet(values[half:])])]
OUT = "ordered" if all(list(s) == values for s in sets) else "by address"
`

// TestDeps (#269): what jedi read for each file's answers besides the
// file: the files it parsed, never the file itself or a stub path it
// tried that is not there; the module names it looked up; the names it
// searched the project for, to infer a parameter from the calls passing
// it.
func TestDeps(t *testing.T) {
	r, err := Analyze("testdata/proj", fixture, 1)
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string][]string{
		"app.py":          {"pkg/__init__.py", "pkg/shapes.py"},
		"pkg/__init__.py": {"pkg/shapes.py"},
		"scripts/tool.py": {"lib/helperlib.py"},
	} {
		if d := r.Deps[file]; d == nil || !slices.Equal(d.Files, want) {
			t.Errorf("%s parsed %+v, want files %v", file, d, want)
		}
	}
	if d := r.Deps["scripts/tool.py"]; d == nil || !slices.Contains(d.Modules, "helperlib") {
		t.Errorf("scripts/tool.py looked up %+v, want helperlib among them", d)
	}
	// Handler's fields are inferred from its calls in the same file, which
	// jedi searches; measure(b)'s one caller is in app.py, and jedi is not
	// let search other files for a parameter's callers (#265).
	if d := r.Deps["pkg/shapes.py"]; d == nil || !slices.Contains(d.Names, "Handler") || slices.Contains(d.Names, "measure") || d.All {
		t.Errorf("pkg/shapes.py searched %+v, want Handler among the names and not measure, under the cap", d)
	}
}
