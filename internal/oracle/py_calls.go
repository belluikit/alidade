// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// The Python layer's oracle is execution (#237, ADR 006 as amended): a
// repository's own tests, run under scripts/pytrace.sh, record every call
// one of its files makes into another, and each recorded call is looked
// for among the graph's calls edges. It measures recall over what ran —
// a call no test made is not judged — and a missing edge is listed with
// its call, so a wrapper a decorator returned or a stub a test installed
// can be told from a call the layer lost.

// A PyTraceCall is one call a traced process made: from a line of one
// file to a function, by name and first line, in another.
type PyTraceCall struct {
	CallerFile string
	CallerLine int
	CalleeFile string
	CalleeName string
	CalleeLine int
}

// ReadPyTraces reads every .tsv scripts/pytrace.sh wrote under dir.
func ReadPyTraces(dir string) ([]PyTraceCall, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tsv"))
	if err != nil {
		return nil, err
	}
	seen := map[PyTraceCall]bool{}
	var out []PyTraceCall
	for _, f := range files {
		calls, err := readPyTrace(f)
		if err != nil {
			return nil, err
		}
		for _, c := range calls {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func readPyTrace(path string) ([]PyTraceCall, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []PyTraceCall
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) != 5 {
			return nil, fmt.Errorf("%s: line %q", path, sc.Text())
		}
		line, err1 := strconv.Atoi(p[1])
		first, err2 := strconv.Atoi(p[4])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s: line %q", path, sc.Text())
		}
		out = append(out, PyTraceCall{CallerFile: filepath.ToSlash(p[0]), CallerLine: line, CalleeFile: filepath.ToSlash(p[2]), CalleeName: p[3], CalleeLine: first})
	}
	return out, sc.Err()
}

// A PyCallsResult compares the Python layer's calls edges with what ran.
// Edges is [distinct calls observed between declarations with nodes, of
// them in the graph]. Replaced counts the observed calls a test
// substituted: the graph has a call of another function of the same name,
// or production code reached a function of a test file, which its source
// cannot name. A stub a test installs (af.gh_run = fake_gh_run) runs where
// the source calls the real one, and the real one is the graph's answer.
// ImportProbes counts the calls of a module's own __getattr__ made from an
// import line: from m import x asks hasattr(m, '__path__'), which a plain
// module answers through it, the import system's call and not the code's
// (#254, the owner's decision on #240). NoNode counts the observed calls
// whose callee has no node (a function a call made), ClassBodies the
// class statements Python runs as calls of a code object named after the
// class, and Unread the calls in or into files the layer does not read.
// Missing lists the rest.
type PyCallsResult struct {
	Traced              int
	Edges               [2]int
	Replaced            int
	ImportProbes        int
	NoNode, ClassBodies int
	Unread              int
	Missing             []string
}

// pyFile is one file's declarations that can hold or receive a call,
// innermost last for a line.
type pyFile struct {
	file  graph.ID
	decls []graph.Node
}

// ComparePyCalls measures g's Python calls edges against traced calls,
// reading the callers' lines from root.
func ComparePyCalls(g *graph.Graph, root string, traced []PyTraceCall) *PyCallsResult {
	files, have, bases := indexPy(g)
	r := &PyCallsResult{Traced: len(traced)}
	want := map[[2]graph.ID]PyTraceCall{}
	for _, c := range traced {
		key, ok := r.place(files, c)
		if _, dup := want[key]; ok && !dup {
			want[key] = c
		}
	}
	byName := calleesByName(g, have)
	lines := sourceLines{root: root, files: map[string][]string{}}
	for key, c := range want {
		r.Edges[0]++
		switch {
		case have[key] || constructed(have, bases, key):
			r.Edges[1]++
			continue
		case byName[key[0]][c.CalleeName], !pyTestFile(c.CallerFile) && pyTestFile(c.CalleeFile):
			r.Replaced++
			continue
		case moduleGetattr(key[1]) && importLine.MatchString(lines.at(c.CallerFile, c.CallerLine)):
			r.ImportProbes++
			continue
		}
		r.Missing = append(r.Missing, fmt.Sprintf("%s -> %s (%s:%d calls %s at %s:%d)", key[0], key[1], c.CallerFile, c.CallerLine, c.CalleeName, c.CalleeFile, c.CalleeLine))
	}
	slices.Sort(r.Missing)
	return r
}

// place is the edge a traced call is, between declarations with nodes, or
// false with the reason counted.
func (r *PyCallsResult) place(files map[string]*pyFile, c PyTraceCall) ([2]graph.ID, bool) {
	if strings.HasPrefix(c.CalleeName, "<") || strings.HasPrefix(c.CallerFile, "<") || strings.HasPrefix(c.CalleeFile, "<") {
		// A module body, a lambda or a comprehension, or a frozen module
		// ("<frozen os>") an older trace recorded: no declaration.
		return [2]graph.ID{}, false
	}
	from, to := files[c.CallerFile], files[c.CalleeFile]
	if from == nil || to == nil || from.file == "" || to.file == "" {
		r.Unread++
		return [2]graph.ID{}, false
	}
	callee, ok := to.callee(c.CalleeName, c.CalleeLine)
	if !ok {
		r.NoNode++
		return [2]graph.ID{}, false
	}
	if callee.Kind == graph.KindType {
		// A construction runs __init__; a code object named after the
		// class is its body, run by the class statement.
		r.ClassBodies++
		return [2]graph.ID{}, false
	}
	return [2]graph.ID{from.holder(c.CallerLine), callee.ID}, true
}

// indexPy gathers the Python files of g with their declarations that hold
// or receive calls, its Python calls edges, and each class's bases, by its
// implements edges.
func indexPy(g *graph.Graph) (map[string]*pyFile, map[[2]graph.ID]bool, map[graph.ID][]graph.ID) {
	files := map[string]*pyFile{}
	at := func(file string) *pyFile {
		if files[file] == nil {
			files[file] = &pyFile{}
		}
		return files[file]
	}
	for n := range g.Nodes() {
		if !strings.HasPrefix(string(n.ID), "py://") {
			continue
		}
		switch n.Kind {
		case graph.KindModule:
			at(n.Pos.File).file = n.ID
		case graph.KindFunc, graph.KindMethod, graph.KindType:
			pf := at(n.Pos.File)
			pf.decls = append(pf.decls, n)
		}
	}
	have := map[[2]graph.ID]bool{}
	bases := map[graph.ID][]graph.ID{}
	for e := range g.Edges() {
		if !strings.HasPrefix(string(e.From), "py://") {
			continue
		}
		switch e.Kind {
		case graph.KindCalls:
			have[[2]graph.ID{e.From, e.To}] = true
		case graph.KindImplements:
			bases[e.From] = append(bases[e.From], e.To)
		}
	}
	return files, have, bases
}

// holder is the innermost declaration spanning a line, or the file.
func (pf *pyFile) holder(line int) graph.ID {
	best, size := pf.file, -1
	for _, n := range pf.decls {
		end, err := strconv.Atoi(n.Attrs["end_line"])
		if err != nil || line < n.Pos.Line || line > end {
			continue
		}
		if size < 0 || end-n.Pos.Line < size {
			best, size = n.ID, end-n.Pos.Line
		}
	}
	return best
}

// callee is the declaration a traced function is: of its name, at or
// after its first line, which is its first decorator's when it has any.
func (pf *pyFile) callee(name string, first int) (graph.Node, bool) {
	var best graph.Node
	found := false
	for _, n := range pf.decls {
		if n.Name == name && n.Pos.Line >= first && (!found || n.Pos.Line < best.Pos.Line) {
			best, found = n, true
		}
	}
	return best, found
}

// constructed reports a traced call of a class's __init__ or __new__ that
// the graph has as a call of the class, what the source wrote, or of a
// class inheriting it through implements edges (#249): constructing a
// subclass with no __init__ of its own runs its base's.
func constructed(have map[[2]graph.ID]bool, bases map[graph.ID][]graph.ID, key [2]graph.ID) bool {
	class, method, ok := cutLast(string(key[1]))
	if !ok || (method != "__init__" && method != "__new__" && method != "__post_init__") {
		return false
	}
	for k := range have {
		if k[0] == key[0] && inherits(bases, k[1], graph.ID(class), map[graph.ID]bool{}) {
			return true
		}
	}
	return false
}

// inherits reports whether class is base or reaches it through bases.
func inherits(bases map[graph.ID][]graph.ID, class, base graph.ID, seen map[graph.ID]bool) bool {
	if class == base {
		return true
	}
	if seen[class] {
		return false
	}
	seen[class] = true
	for _, b := range bases[class] {
		if inherits(bases, b, base, seen) {
			return true
		}
	}
	return false
}

func cutLast(id string) (string, string, bool) {
	i := strings.LastIndexByte(id, '.')
	if i < 0 || i < strings.IndexByte(id, '#') {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

func (r *PyCallsResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "py calls %d / %d distinct calls observed at run time are edges, %d more substituted by a test (a stub it installed), %d a module __getattr__ the import system called, %d missing; %d traced calls, %d to no node, %d class bodies, %d in files the layer does not read\n",
		r.Edges[1], r.Edges[0], r.Replaced, r.ImportProbes, len(r.Missing), r.Traced, r.NoNode, r.ClassBodies, r.Unread)
	for i, s := range r.Missing {
		if i == 12 {
			fmt.Fprintf(&b, "missing py call ... and %d more\n", len(r.Missing)-12)
			break
		}
		fmt.Fprintf(&b, "missing py call %s\n", s)
	}
	return b.String()
}

// calleesByName is, per caller, the names of the declarations its calls
// edges reach.
func calleesByName(g *graph.Graph, have map[[2]graph.ID]bool) map[graph.ID]map[string]bool {
	out := map[graph.ID]map[string]bool{}
	for key := range have {
		n, ok := g.Node(key[1])
		if !ok {
			continue
		}
		if out[key[0]] == nil {
			out[key[0]] = map[string]bool{}
		}
		out[key[0]][n.Name] = true
	}
	return out
}

// pyTestFile reports whether a repo-relative Python file is a test's: in a
// test or tests directory, or named test_*.py, *_test.py or conftest.py.
func pyTestFile(rel string) bool {
	dir, base := filepath.Split(rel)
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		if seg == "test" || seg == "tests" {
			return true
		}
	}
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py"
}

// moduleGetattr reports whether id is a module's own __getattr__, not a
// class's.
func moduleGetattr(id graph.ID) bool {
	_, frag, ok := strings.Cut(string(id), "#")
	return ok && frag == "__getattr__"
}

// importLine matches a line holding an import statement.
var importLine = regexp.MustCompile(`^\s*(from\s+\S+\s+)?import\b`)

// sourceLines reads a repository's files by line, once each.
type sourceLines struct {
	root  string
	files map[string][]string
}

// at is a file's line, 1-based, or "" when it cannot be read.
func (s sourceLines) at(file string, line int) string {
	ls, ok := s.files[file]
	if !ok {
		if b, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(file))); err == nil {
			ls = strings.Split(string(b), "\n")
		}
		s.files[file] = ls
	}
	if line < 1 || line > len(ls) {
		return ""
	}
	return ls[line-1]
}
