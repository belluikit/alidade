// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package py

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/belluikit/alidade/internal/python"
)

// The cache keeps jedi's answers for a tree between sessions (#279): what
// a Live knows, written as PXF @dataset tables, one per kind of row, and
// read back as the Live of an earlier build, stopped. A file keeps its
// answers from it by the rules a rebuild in one session uses ([edits]),
// so an answer kept from disk is one an edit since cannot have changed.
// A cache of another format, engine ([python.Engine]) or root is not
// read, and is written over.

// cacheFormat changes whenever the tables do.
const cacheFormat = 1

// cacheTables are the tables, in the order they are written and read,
// with their columns.
var cacheTables = []struct {
	name string
	cols []string
}{
	{"Cache", []string{"format", "engine", "root"}},
	// The files the build read, and the hash of every file the answers
	// depend on (Live.sums).
	{"File", []string{"file", "size"}},
	{"Sum", []string{"file", "sum"}},
	// A row per file jedi answered, with its counts.
	{"Answer", []string{"file", "imports_outside", "imports_unresolved", "outside", "unplaced", "unresolved",
		"bases_outside", "bases_unplaced", "bases_unresolved", "skipped", "deps_all"}},
	{"Decl", []string{"file", "path", "kind", "line", "col", "end_line"}},
	{"Import", []string{"file", "line", "target"}},
	{"Call", []string{"kind", "file", "line", "col", "caller", "target", "target_line", "target_name", "target_type"}},
	{"Dep", []string{"kind", "file", "name"}},
}

// cachePath is the file under dir that keeps root's answers.
func cachePath(dir, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".pxf")
}

// save writes what l knows under dir, replacing the file whole, so a
// reader never sees half of one.
func (l *Live) save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".py-*.pxf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // after the rename, there is nothing to remove
	w := bufio.NewWriterSize(f, 1<<20)
	l.mu.Lock()
	writeCache(w, l)
	l.mu.Unlock()
	err = errors.Join(w.Flush(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), cachePath(dir, l.root))
}

// writeCache writes l's tables to w; l.mu must be held.
func writeCache(w *bufio.Writer, l *Live) {
	c := &cacheWriter{w: w}
	fmt.Fprintln(w, "# alidade: jedi's answers for one tree, kept between sessions (#279). Safe to delete.")
	c.table(0)
	c.row(num(cacheFormat), str(python.Engine()), str(l.root))
	c.table(1)
	for _, f := range l.files {
		c.row(str(f.Rel), num(int(f.Size)))
	}
	c.table(2)
	for _, p := range sortedKeys(l.sums) {
		c.row(str(p), str(l.sums[p]))
	}
	files := sortedKeys(l.jedi)
	c.table(3)
	for _, rel := range files {
		c.answer(rel, l.jedi[rel])
	}
	c.decls(l, files)
	c.calls(l, files)
	c.deps(l, files)
}

// answer writes a file's row of the Answer table.
func (c *cacheWriter) answer(rel string, r *python.Result) {
	d := r.Deps[rel]
	c.row(str(rel), num(r.ImportsOutside), num(r.ImportsUnresolved), num(r.Outside), num(r.Unplaced), num(r.Unresolved),
		num(r.BasesOutside), num(r.BasesUnplaced), num(r.BasesUnresolved), str(r.Skipped[rel]), &pxf.BoolVal{Value: d != nil && d.All})
}

// decls writes the Decl and Import tables.
func (c *cacheWriter) decls(l *Live, files []string) {
	c.table(4)
	for _, rel := range files {
		for _, d := range l.jedi[rel].Decls {
			c.row(str(d.File), str(d.Path), str(d.Kind), num(d.Line), num(d.Col), num(d.EndLine))
		}
	}
	c.table(5)
	for _, rel := range files {
		for _, i := range l.jedi[rel].Imports {
			c.row(str(i.File), num(i.Line), str(i.Target))
		}
	}
}

// calls writes the Call table: calls, references and bases.
func (c *cacheWriter) calls(l *Live, files []string) {
	c.table(6)
	for _, rel := range files {
		r := l.jedi[rel]
		for _, k := range []struct {
			kind  string
			calls []python.Call
		}{{"call", r.Calls}, {"ref", r.Refs}, {"base", r.Bases}} {
			for _, x := range k.calls {
				c.row(ident(k.kind), str(x.File), num(x.Line), num(x.Col), str(x.Caller), str(x.Target), num(x.TargetLine), str(x.TargetName), str(x.TargetType))
			}
		}
	}
}

// deps writes the Dep table.
func (c *cacheWriter) deps(l *Live, files []string) {
	c.table(7)
	for _, rel := range files {
		d := l.jedi[rel].Deps[rel]
		if d == nil {
			continue
		}
		for _, k := range []struct {
			kind  string
			names []string
		}{{"file", d.Files}, {"module", d.Modules}, {"name", d.Names}} {
			for _, n := range k.names {
				c.row(ident(k.kind), str(rel), str(n))
			}
		}
	}
}

type cacheWriter struct {
	w   *bufio.Writer
	buf []byte
}

func (c *cacheWriter) table(i int) {
	t := cacheTables[i]
	fmt.Fprintf(c.w, "@dataset alidade.py.%s (%s)\n", t.name, strings.Join(t.cols, ", "))
}

func (c *cacheWriter) row(cells ...pxf.Value) {
	c.buf = append(c.buf[:0], '(')
	for i, v := range cells {
		if i > 0 {
			c.buf = append(c.buf, ", "...)
		}
		c.buf = pxf.AppendValue(c.buf, v)
	}
	c.buf = append(c.buf, ")\n"...)
	c.w.Write(c.buf)
}

func str(s string) pxf.Value   { return &pxf.StringVal{Value: s} }
func num(n int) pxf.Value      { return &pxf.IntVal{Raw: strconv.Itoa(n)} }
func ident(s string) pxf.Value { return &pxf.IdentVal{Name: s} }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// loadLive reads the answers kept under dir for root as the Live of an
// earlier build, stopped: nil when there are none, or none this engine
// can use.
func loadLive(dir, root string) *Live {
	f, err := os.Open(cachePath(dir, root))
	if err != nil {
		return nil
	}
	defer f.Close()
	l, err := readCache(bufio.NewReaderSize(f, 1<<20), root)
	if err != nil {
		return nil
	}
	return l
}

// errCacheOther is a cache of another format, engine or root.
var errCacheOther = errors.New("py: a cache of another format, engine or root")

// readCache reads a cache, written for root by this engine.
func readCache(r io.Reader, root string) (*Live, error) {
	done := make(chan struct{})
	close(done)
	l := &Live{root: root, sums: map[string]string{}, jedi: map[string]*python.Result{}, cancel: func() {}, done: done}
	readers := []func(*cells){
		func(c *cells) {
			if c.int(0) != cacheFormat || c.str(1) != python.Engine() || c.str(2) != root {
				c.fail(errCacheOther)
			}
		},
		func(c *cells) { l.files = append(l.files, python.File{Rel: c.str(0), Size: int64(c.int(1))}) },
		func(c *cells) { l.sums[c.str(0)] = c.str(1) },
		func(c *cells) { readAnswer(c, l) },
		func(c *cells) {
			r := l.answer(c, c.str(0))
			r.Decls = append(r.Decls, python.Decl{File: c.str(0), Path: c.str(1), Kind: c.str(2), Line: c.int(3), Col: c.int(4), EndLine: c.int(5)})
		},
		func(c *cells) {
			r := l.answer(c, c.str(0))
			r.Imports = append(r.Imports, python.Import{File: c.str(0), Line: c.int(1), Target: c.str(2)})
		},
		func(c *cells) { readCall(c, l) },
		func(c *cells) { readDep(c, l) },
	}
	for i, read := range readers {
		next, n, err := readTable(r, i, read)
		if err != nil {
			return nil, err
		}
		if i == 0 && n != 1 {
			return nil, errCacheOther
		}
		r = next
	}
	return l, nil
}

// readTable reads table i of a cache from r, calling read with each row,
// and returns what follows it with the count of rows.
func readTable(r io.Reader, i int, read func(*cells)) (io.Reader, int, error) {
	t := cacheTables[i]
	tr, err := pxf.NewDatasetReader(r)
	if err != nil {
		return nil, 0, err
	}
	if tr.Type() != "alidade.py."+t.name || !slices.Equal(tr.Columns(), t.cols) {
		return nil, 0, fmt.Errorf("%w: table %s (%s), want %s", errCacheOther, tr.Type(), strings.Join(tr.Columns(), ", "), t.name)
	}
	n := 0
	for {
		row, err := tr.Next()
		if err == io.EOF {
			return tr.Tail(), n, nil
		}
		if err != nil {
			return nil, 0, err
		}
		c := &cells{v: row.Cells}
		read(c)
		if c.err != nil {
			return nil, 0, fmt.Errorf("table %s, row %d: %w", t.name, n+1, c.err)
		}
		n++
	}
}

func readAnswer(c *cells, l *Live) {
	rel := c.str(0)
	if _, ok := l.jedi[rel]; ok {
		c.fail(errors.New("a file answered twice"))
		return
	}
	r := &python.Result{Skipped: map[string]string{}, Workers: 1,
		ImportsOutside: c.int(1), ImportsUnresolved: c.int(2), Outside: c.int(3), Unplaced: c.int(4), Unresolved: c.int(5),
		BasesOutside: c.int(6), BasesUnplaced: c.int(7), BasesUnresolved: c.int(8)}
	if s := c.str(9); s != "" {
		r.Skipped[rel] = s
	}
	if c.bool(10) {
		r.Deps = map[string]*python.Deps{rel: {All: true}}
	}
	l.jedi[rel] = r
}

func readCall(c *cells, l *Live) {
	r := l.answer(c, c.str(1))
	x := python.Call{File: c.str(1), Line: c.int(2), Col: c.int(3), Caller: c.str(4), Target: c.str(5),
		TargetLine: c.int(6), TargetName: c.str(7), TargetType: c.str(8)}
	switch c.ident(0) {
	case "call":
		r.Calls = append(r.Calls, x)
	case "ref":
		r.Refs = append(r.Refs, x)
	case "base":
		r.Bases = append(r.Bases, x)
	default:
		c.fail(errors.New("unknown kind"))
	}
}

func readDep(c *cells, l *Live) {
	rel := c.str(1)
	r := l.answer(c, rel)
	if r.Deps == nil {
		r.Deps = map[string]*python.Deps{}
	}
	d := r.Deps[rel]
	if d == nil {
		d = &python.Deps{}
		r.Deps[rel] = d
	}
	switch c.ident(0) {
	case "file":
		d.Files = append(d.Files, c.str(2))
	case "module":
		d.Modules = append(d.Modules, c.str(2))
	case "name":
		d.Names = append(d.Names, c.str(2))
	default:
		c.fail(errors.New("unknown kind"))
	}
}

// answer is the answer of a file the Answer table has a row for.
func (l *Live) answer(c *cells, rel string) *python.Result {
	r, ok := l.jedi[rel]
	if !ok {
		c.fail(fmt.Errorf("a row for %s, which has no answer", rel))
		return &python.Result{}
	}
	return r
}

// cells reads a row's cells by type; the first one of another type
// fails the row.
type cells struct {
	v   []pxf.Value
	err error
}

func (c *cells) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

func (c *cells) str(i int) string {
	if v, ok := c.cell(i).(*pxf.StringVal); ok {
		return v.Value
	}
	c.fail(fmt.Errorf("column %d is not a string", i+1))
	return ""
}

func (c *cells) int(i int) int {
	if v, ok := c.cell(i).(*pxf.IntVal); ok {
		if n, err := strconv.Atoi(v.Raw); err == nil {
			return n
		}
	}
	c.fail(fmt.Errorf("column %d is not an integer", i+1))
	return 0
}

func (c *cells) ident(i int) string {
	if v, ok := c.cell(i).(*pxf.IdentVal); ok {
		return v.Name
	}
	c.fail(fmt.Errorf("column %d is not an identifier", i+1))
	return ""
}

func (c *cells) bool(i int) bool {
	if v, ok := c.cell(i).(*pxf.BoolVal); ok {
		return v.Value
	}
	c.fail(fmt.Errorf("column %d is not true or false", i+1))
	return false
}

func (c *cells) cell(i int) pxf.Value {
	if i < len(c.v) {
		return c.v[i]
	}
	return nil
}
