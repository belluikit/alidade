// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/belluikit/alidade/internal/graph"
)

// A RustReference is rustc's calls in one library as every tool is scored
// against them: the direct calls between two functions the Rust layer has
// a node for, named as CompareRustCalls names them. The layer's nodes are
// the declarations because rust-analyzer's index places each one; a tool
// is scored on its edges, never on which functions it found.
type RustReference struct {
	Lib RustLib
	// Excluded counts rustc's edges to code outside the crate, and in
	// Written those it leaves out.
	Excluded int
	// Renamed counts rustc's functions named by the layer's function
	// declared on their line.
	Renamed int
	funcs   map[string]bool
	written map[string]bool // the layer's functions a derive or a macro writes
	calls   map[string]map[string]bool
	// uncovered are rustc's edges with an end the layer has no node for,
	// each scored by where its ends are declared (rustc's debug info).
	uncovered [][2]string
	locs      map[string][]string
	compiled  map[string]bool
	traits    map[string]bool
}

// NewRustReference makes rustc's calls in rc the reference, with the
// layer's functions in g as the declarations. root is the repository's,
// which rc's locations are under.
//
// A function rustc names differently from the layer, and declares on the
// line of exactly one of the layer's functions, is that function: a module
// `#[path]` renames (tokio's `process::imp`, the layer's
// `process/unix/mod.rs`), or a function nested in another. Renamed counts
// them.
func NewRustReference(g *graph.Graph, rc *RustCalls, root string) *RustReference {
	r := &RustCompare{Lib: rc.Lib, layerFuncs: map[string]bool{}}
	written := map[string]bool{}
	at := map[string][]string{}
	for n := range g.Nodes() {
		if n.Kind == graph.KindFunc || n.Kind == graph.KindMethod {
			if k := layerKey(n.ID, rc.Lib.Dir); k != "" {
				r.layerFuncs[k] = true
				written[k] = written[k] || n.Attrs["written_by"] != ""
				loc := n.Pos.File + ":" + strconv.Itoa(n.Pos.Line)
				if !slices.Contains(at[loc], k) {
					at[loc] = append(at[loc], k)
				}
			}
		}
	}
	rc, renamed := placeByLine(rc, root, r.layerFuncs, at)
	r.rustcFuncs = rc.Funcs
	all := r.provided(r.ownCode(rc))
	ref := &RustReference{Lib: rc.Lib, Excluded: r.Outside, Renamed: renamed, funcs: r.layerFuncs, written: written, calls: map[string]map[string]bool{}, locs: rc.Locs,
		compiled: map[string]bool{}, traits: map[string]bool{}}
	for k := range rc.Funcs {
		ref.compiled[r.providedName(k)] = true
	}
	r.rustcCalls = all
	ref.traits = r.traitMethods()
	for c, tos := range all {
		ref.compiled[c] = true
		for to := range tos {
			if !r.layerFuncs[c] || !r.layerFuncs[to] {
				ref.uncovered = append(ref.uncovered, [2]string{c, to})
				continue
			}
			if ref.calls[c] == nil {
				ref.calls[c] = map[string]bool{}
			}
			ref.calls[c][to] = true
		}
	}
	return ref
}

// Written is the reference without the calls no source text writes as a
// call: those of a function a derive or a macro writes (a derived Clone's
// call of each field's), and those to an operator trait's method, which
// `==`, `*x` or a scope's end calls. What a tool reads from the text
// can be scored on this one alone; every edge it leaves out is counted
// in Excluded.
func (ref *RustReference) Written() *RustReference {
	out := *ref
	out.calls = map[string]map[string]bool{}
	out.uncovered = nil
	for _, e := range ref.uncovered {
		if tr, _, ok := traitMethod(e[1]); ok && operatorTraits[tr] {
			out.Excluded++
			continue
		}
		out.uncovered = append(out.uncovered, e)
	}
	for c, tos := range ref.calls {
		for to := range tos {
			if tr, _, ok := traitMethod(to); ref.written[c] || ok && operatorTraits[tr] {
				out.Excluded++
				continue
			}
			if out.calls[c] == nil {
				out.calls[c] = map[string]bool{}
			}
			out.calls[c][to] = true
		}
	}
	return &out
}

// placeByLine renames each of rc's functions the layer has no function of
// that name for, and that rustc declares on the line of exactly one of the
// layer's, to that one.
func placeByLine(rc *RustCalls, root string, funcs map[string]bool, at map[string][]string) (*RustCalls, int) {
	rename := map[string]string{}
	for k, locs := range rc.Locs {
		if funcs[k] {
			continue
		}
		if to := layerOn(locs, root, at); len(to) == 1 {
			rename[k] = to[0]
		}
	}
	if len(rename) == 0 {
		return rc, 0
	}
	return rc.renamed(rename), len(rename)
}

// layerOn is the layer's functions declared on any of the lines locs names.
func layerOn(locs []string, root string, at map[string][]string) []string {
	var to []string
	for _, l := range locs {
		r, err := filepath.Rel(root, l)
		if err != nil {
			continue
		}
		for _, n := range at[filepath.ToSlash(r)] {
			if !slices.Contains(to, n) {
				to = append(to, n)
			}
		}
	}
	return to
}

// renamed is rc with each function in rename named as it says.
func (rc *RustCalls) renamed(rename map[string]string) *RustCalls {
	name := func(k string) string {
		if n, ok := rename[k]; ok {
			return n
		}
		return k
	}
	out := &RustCalls{Lib: rc.Lib, Calls: map[string]map[string]bool{}, Funcs: map[string]bool{}, Foreign: map[string]bool{}, Locs: map[string][]string{}}
	for c, tos := range rc.Calls {
		for to := range tos {
			if f, t := name(c), name(to); f != t {
				if out.Calls[f] == nil {
					out.Calls[f] = map[string]bool{}
				}
				out.Calls[f][t] = true
			}
		}
	}
	for k := range rc.Funcs {
		out.Funcs[name(k)] = true
	}
	for k := range rc.Foreign {
		out.Foreign[name(k)] = true
	}
	for k, l := range rc.Locs {
		out.Locs[name(k)] = append(out.Locs[name(k)], l...)
	}
	return out
}

// LayerCalls is the calls edges of g in the reference's library, by the
// names the reference uses.
func (ref *RustReference) LayerCalls(g *graph.Graph) map[string]map[string]bool {
	ids := map[graph.ID]map[graph.ID]bool{}
	for e := range g.Edges() {
		if e.Kind != graph.KindCalls {
			continue
		}
		if ids[e.From] == nil {
			ids[e.From] = map[graph.ID]bool{}
		}
		ids[e.From][e.To] = true
	}
	return ref.Named(ids)
}

// Named names calls edges between the layer's IDs as the reference does,
// leaving out what CompareRustCalls leaves out: an end outside the
// library, a call of a function by itself, and a caller in a tests module,
// which a library's build does not compile.
func (ref *RustReference) Named(ids map[graph.ID]map[graph.ID]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for from, tos := range ids {
		f := layerKey(from, ref.Lib.Dir)
		if f == "" || slices.Contains(strings.Split(f, "::"), "tests") {
			continue
		}
		for to := range tos {
			t := layerKey(to, ref.Lib.Dir)
			if t == "" || t == f {
				continue
			}
			if out[f] == nil {
				out[f] = map[string]bool{}
			}
			out[f][t] = true
		}
	}
	return out
}

// A RustScore is one tool's calls edges in one library against the
// reference.
type RustScore struct {
	Tool string
	Lib  RustLib
	// Reference is the reference's edges; of them, Found the tool has, and
	// FoundBound it has as an edge to the trait's method, a call through a
	// generic bound that rustc names by the instantiation.
	Reference, Found, FoundBound int
	// Edges is the tool's edges from a function rustc compiled, to one the
	// layer has a node for. Of them, Right are the reference's, Bound a
	// call through a generic bound, Dyn one to a trait's method rustc has
	// no instantiation of from that caller (a call through dyn, which IR
	// does not name), and Wrong none of those.
	Edges, Right, Bound, Dyn int
	Wrong                    []string
	// Missed are the reference's edges the tool has neither way.
	Missed []string
	// Uncovered is rustc's edges with an end the layer has no node for,
	// code rust-analyzer did not read (a cfg it left off); FoundUncovered
	// of them the tool has between the functions declared where rustc's
	// ends are. Neither is in Reference or Found.
	Uncovered, FoundUncovered int
}

// Score scores one tool's calls, named by Named.
func (ref *RustReference) Score(tool string, calls map[string]map[string]bool) *RustScore {
	s := &RustScore{Tool: tool, Lib: ref.Lib}
	for _, c := range sortedKeys(ref.calls) {
		for _, to := range sortedKeys(ref.calls[c]) {
			s.Reference++
			tr, m, ok := traitMethod(to)
			switch {
			case calls[c][to]:
				s.Found++
			case ok && calls[c][tr+"::"+m]:
				s.FoundBound++
			default:
				s.Missed = append(s.Missed, c+" -> "+to)
			}
		}
	}
	for _, c := range sortedKeys(calls) {
		if !ref.compiled[c] || !ref.funcs[c] {
			continue
		}
		for _, to := range sortedKeys(calls[c]) {
			if !ref.funcs[to] {
				continue
			}
			s.Edges++
			switch {
			case ref.calls[c][to]:
				s.Right++
			case ref.bound(c, to):
				s.Bound++
			case ref.traits[to]:
				s.Dyn++
			default:
				s.Wrong = append(s.Wrong, c+" -> "+to)
			}
		}
	}
	return s
}

// ScoreUncovered scores a tool on the edges with an end the layer has no
// node for, by where rustc says each end is declared. calls is the tool's
// edges between functions, by "file:line" relative to root.
func (ref *RustReference) ScoreUncovered(s *RustScore, root string, calls map[string]map[string]bool) {
	rel := func(k string) []string {
		var out []string
		for _, l := range ref.locs[k] {
			if r, err := filepath.Rel(root, l); err == nil && !strings.HasPrefix(r, "..") {
				out = append(out, filepath.ToSlash(r))
			}
		}
		return out
	}
	for _, e := range ref.uncovered {
		s.Uncovered++
		if hasAny(calls, rel(e[0]), rel(e[1])) {
			s.FoundUncovered++
		}
	}
}

func hasAny(calls map[string]map[string]bool, from, to []string) bool {
	for _, f := range from {
		for _, t := range to {
			if calls[f][t] {
				return true
			}
		}
	}
	return false
}

// bound says whether an edge to `Trait::method` is rustc's edge to an
// instantiation of it, `<Type as Trait>::method`, from the same caller.
func (ref *RustReference) bound(c, to string) bool {
	for k := range ref.calls[c] {
		if tr, m, ok := traitMethod(k); ok && tr+"::"+m == to {
			return true
		}
	}
	return false
}

func (s *RustScore) String() string {
	return fmt.Sprintf("%s %s: found %d + %d bound of %d rustc edges (%s), and %d of %d with an end the layer has no node for; with them %s; of its %d edges %d right, %d bound, %d dyn, %d wrong (%s right or bound)",
		s.Tool, s.Lib.Package, s.Found, s.FoundBound, s.Reference, pct(s.Found+s.FoundBound, s.Reference),
		s.FoundUncovered, s.Uncovered, pct(s.Found+s.FoundBound+s.FoundUncovered, s.Reference+s.Uncovered),
		s.Edges, s.Right, s.Bound, s.Dyn, len(s.Wrong), pct(s.Right+s.Bound, s.Edges-s.Dyn))
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}
