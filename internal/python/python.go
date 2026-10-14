// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package python reads a repository's Python files (#237, ADR 008): their
// declarations with ast, their imports and calls with jedi, run in a
// CPython linked into the binary (github.com/trendvidia/pylib) when it is
// built with the python tag. Without the tag, Analyze says it is
// unavailable.
package python

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// A File is one file to analyze: repo-relative, with its size, by which
// the work is shared between workers.
type File struct {
	Rel  string
	Size int64
}

// A Decl is a class, function, method, module-level variable or class
// attribute.
type Decl struct {
	File               string
	Path               string // the qualified name, dotted: Square.area, main.inner
	Kind               string // class, function, method, variable, field
	Line, Col, EndLine int
}

// An Import is an import statement jedi resolved to a file inside the root.
type Import struct {
	File   string
	Line   int
	Target string
}

// A Call is a call site jedi resolved to a function or class inside the
// root. A call through a variable holding several is one Call per function.
// A with statement's item is a call site of the __enter__ and __exit__ it
// runs. A Call in Result.Refs is a function named as a value, not called:
// Caller is the declaration holding the name. One in Result.Bases is a
// class's base: Caller is the class, the target the base class.
type Call struct {
	File       string // repo-relative, slash-separated
	Line, Col  int    // of the callee's last character, 1-based
	Caller     string // the functions and classes holding it, dotted; empty at module level
	Target     string // the definition's file
	TargetLine int
	TargetName string
	TargetType string // function or class
}

// Result is an Analyze's outcome.
type Result struct {
	Decls   []Decl
	Imports []Import
	Calls   []Call
	Refs    []Call
	Bases   []Call
	// ImportsOutside resolved to the stdlib or a package, ImportsUnresolved
	// to nothing.
	ImportsOutside, ImportsUnresolved int
	// Of the call sites that are no Call: Outside resolved to the stdlib, a
	// package or a builtin; Unplaced inside the root to no function or
	// class (a parameter, a local value); Unresolved to nothing.
	Outside, Unplaced, Unresolved int
	// Of a class's bases that are no Call, as for call sites.
	BasesOutside, BasesUnplaced, BasesUnresolved int
	// Skipped is each file not read, and why.
	Skipped map[string]string
	// Workers is how many processes resolved the calls, 1 when this one
	// did; WorkerErrors the workers that failed, whose files this process
	// then read itself.
	Workers      int
	WorkerErrors []string
	// Deps is what each file's answers depend on besides its own content,
	// by file: jedi's only, the warm-up pass records none.
	Deps map[string]*Deps
}

// Deps is what jedi read for one file's answers besides the file itself
// (#269): resolve.py's Record says how it is gathered. A file whose Deps
// an edit leaves alone keeps its answers.
type Deps struct {
	// Files are the files inside the root jedi parsed, repo-relative.
	Files []string
	// Modules are the module names jedi looked up, found or not: a file
	// added under one can change what the lookup finds.
	Modules []string
	// Names are the names jedi searched the project's files for: a file
	// that comes to contain one can change the answer.
	Names []string
	// All is set when a search stopped at jedi's cap on the files it
	// opens: which files exist at all then decides what it found.
	All bool
}

// deps is r's Deps for a file, made when it has none.
func (r *Result) deps(file string) *Deps {
	if r.Deps == nil {
		r.Deps = map[string]*Deps{}
	}
	d := r.Deps[file]
	if d == nil {
		d = &Deps{}
		r.Deps[file] = d
	}
	return d
}

// ErrUnavailable is Analyze's error in a build without the python tag.
var ErrUnavailable = errors.New("python: this build has no CPython (built without the python tag)")

// parse reads the resolver's lines (resolve.py says what each holds) into r.
func parse(out string, r *Result) error {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if err := parseLine(strings.Split(line, "\t"), r); err != nil {
			return fmt.Errorf("python: resolver line %q: %w", line, err)
		}
	}
	return nil
}

func parseLine(f []string, r *Result) error {
	p, ok := lineParsers[f[0]]
	if !ok || len(f) != p.fields {
		return errors.New("unknown")
	}
	return p.read(f, r)
}

// lineParsers reads each kind of resolver line: how many fields it has,
// and what it adds to a Result.
var lineParsers = map[string]struct {
	fields int
	read   func([]string, *Result) error
}{
	"DECL": {7, func(f []string, r *Result) error {
		n, err := ints(f[4:])
		if err == nil {
			r.Decls = append(r.Decls, Decl{File: f[1], Path: f[2], Kind: f[3], Line: n[0], Col: n[1], EndLine: n[2]})
		}
		return err
	}},
	"IMPORT": {4, func(f []string, r *Result) error {
		n, err := ints(f[2:3])
		if err == nil {
			r.Imports = append(r.Imports, Import{File: f[1], Line: n[0], Target: filepath.ToSlash(f[3])})
		}
		return err
	}},
	"CALL": {9, func(f []string, r *Result) error {
		n, err := ints([]string{f[2], f[3], f[6]})
		if err == nil {
			r.Calls = append(r.Calls, Call{File: f[1], Line: n[0], Col: n[1], Caller: f[4], Target: filepath.ToSlash(f[5]),
				TargetLine: n[2], TargetName: f[7], TargetType: f[8]})
		}
		return err
	}},
	"REF": {9, func(f []string, r *Result) error {
		n, err := ints([]string{f[2], f[3], f[6]})
		if err == nil {
			r.Refs = append(r.Refs, Call{File: f[1], Line: n[0], Col: n[1], Caller: f[4], Target: filepath.ToSlash(f[5]),
				TargetLine: n[2], TargetName: f[7], TargetType: f[8]})
		}
		return err
	}},
	"BASE": {8, func(f []string, r *Result) error {
		n, err := ints([]string{f[2], f[3], f[6]})
		if err == nil {
			r.Bases = append(r.Bases, Call{File: f[1], Line: n[0], Col: n[1], Caller: f[4], Target: filepath.ToSlash(f[5]),
				TargetLine: n[2], TargetName: f[7], TargetType: "class"})
		}
		return err
	}},
	"BASE-OUTSIDE":   {4, func(_ []string, r *Result) error { r.BasesOutside++; return nil }},
	"BASE-UNPLACED":  {4, func(_ []string, r *Result) error { r.BasesUnplaced++; return nil }},
	"BASE-NONE":      {4, func(_ []string, r *Result) error { r.BasesUnresolved++; return nil }},
	"IMPORT-OUTSIDE": {3, func(_ []string, r *Result) error { r.ImportsOutside++; return nil }},
	"IMPORT-NONE":    {3, func(_ []string, r *Result) error { r.ImportsUnresolved++; return nil }},
	"OUTSIDE":        {4, func(_ []string, r *Result) error { r.Outside++; return nil }},
	"UNPLACED":       {4, func(_ []string, r *Result) error { r.Unplaced++; return nil }},
	"NONE":           {4, func(_ []string, r *Result) error { r.Unresolved++; return nil }},
	"SKIP":           {3, func(f []string, r *Result) error { r.Skipped[f[1]] = f[2]; return nil }},
	"DEP":            {3, func(f []string, r *Result) error { d := r.deps(f[1]); d.Files = append(d.Files, f[2]); return nil }},
	"DEP-MODULE":     {3, func(f []string, r *Result) error { d := r.deps(f[1]); d.Modules = append(d.Modules, f[2]); return nil }},
	"DEP-NAME":       {3, func(f []string, r *Result) error { d := r.deps(f[1]); d.Names = append(d.Names, f[2]); return nil }},
	"DEP-ALL":        {2, func(f []string, r *Result) error { r.deps(f[1]).All = true; return nil }},
}

// ints reads fields as integers.
func ints(fields []string) ([]int, error) {
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// A job is one file, or one shard of a large file's call sites.
type job struct {
	rel           string
	shard, shards int
	weight        int64
}

// plan shares files between n workers by size: a file larger than a
// worker's fair share is split into shards of its call sites, and jobs go
// largest first to the least loaded worker. Every file's shard 0 is the
// one that reports its declarations and imports.
func plan(files []File, n int) [][]job {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	share := max(total/int64(n), 1)
	var jobs []job
	for _, f := range files {
		k := int(min((f.Size+share-1)/share, int64(n)))
		k = max(k, 1)
		for s := range k {
			jobs = append(jobs, job{rel: f.Rel, shard: s, shards: k, weight: f.Size / int64(k)})
		}
	}
	slices.SortStableFunc(jobs, func(a, b job) int { return cmp.Compare(b.weight, a.weight) })
	out := make([][]job, n)
	load := make([]int64, n)
	for _, j := range jobs {
		i := slices.Index(load, slices.Min(load))
		out[i] = append(out[i], j)
		load[i] += j.weight
	}
	return slices.DeleteFunc(out, func(js []job) bool { return len(js) == 0 })
}

// Join is the files' results, keyed by file, as one result: what one
// process reading them all gives.
func Join(files map[string]*Result, workers int, workerErrors []string) *Result {
	r := &Result{Skipped: map[string]string{}, Workers: workers, WorkerErrors: workerErrors}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		f := files[rel]
		r.Decls = append(r.Decls, f.Decls...)
		r.Imports = append(r.Imports, f.Imports...)
		r.Calls = append(r.Calls, f.Calls...)
		r.Refs = append(r.Refs, f.Refs...)
		r.Bases = append(r.Bases, f.Bases...)
		r.ImportsOutside += f.ImportsOutside
		r.ImportsUnresolved += f.ImportsUnresolved
		r.Outside += f.Outside
		r.Unplaced += f.Unplaced
		r.Unresolved += f.Unresolved
		r.BasesOutside += f.BasesOutside
		r.BasesUnplaced += f.BasesUnplaced
		r.BasesUnresolved += f.BasesUnresolved
		maps.Copy(r.Skipped, f.Skipped)
		for file, d := range f.Deps {
			*r.deps(file) = *d
		}
	}
	sortResult(r)
	return r
}

// sortResult orders a Result's lists, so workers' interleaving does not
// show.
func sortResult(r *Result) {
	slices.SortFunc(r.Decls, func(a, b Decl) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col), cmp.Compare(a.Path, b.Path))
	})
	slices.SortFunc(r.Imports, func(a, b Import) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Target, b.Target))
	})
	byCall := func(a, b Call) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Target, b.Target), cmp.Compare(a.TargetLine, b.TargetLine), cmp.Compare(a.TargetName, b.TargetName))
	}
	slices.SortFunc(r.Calls, byCall)
	slices.SortFunc(r.Refs, byCall)
	slices.SortFunc(r.Bases, byCall)
	for _, d := range r.Deps {
		// A file split into shards has each shard's.
		for _, l := range []*[]string{&d.Files, &d.Modules, &d.Names} {
			slices.Sort(*l)
			*l = slices.Compact(*l)
		}
	}
}
