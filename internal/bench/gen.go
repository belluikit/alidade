// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bench

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// An Edge is one static call of a repository, declaration to
// declaration, as an oracle that is neither arm resolved it: the Go type
// checker (internal/oracle). An ID is a graph ID, "go://pkg/path#Decl" or
// "go://pkg/path#Type.Method".
type Edge struct{ From, To string }

// Generate draws a question set from a repository's static calls (#284),
// so that no one picks the questions: the candidates of each stratum are
// taken in order of the SHA-256 of "repo|stratum|id", per of each.
//
//   - unique: the direct callers of a function or method whose name no
//     other declaration in the calls shares. A text search for the name
//     finds its uses.
//   - shared: the direct callers of a method whose name at least five
//     declarations share, where a text search finds every one of them.
//   - chain: the functions between two declarations three static calls
//     apart, when one shortest path joins them.
//
// A target of the first two has two to eight callers, none of them a
// package's own initialization. A candidate is passed over when a name
// its answer needs is already in its question, since an answer repeating
// the question would then score.
func Generate(repo, commit string, calls []Edge, per int) *Set {
	g := newCallGraph(calls)
	s := &Set{Repo: repo, Commit: commit}
	for _, stratum := range []string{"unique", "shared", "chain"} {
		var cands []Question
		switch stratum {
		case "chain":
			cands = g.chains(repo)
		default:
			cands = g.callers(repo, stratum)
		}
		slices.SortFunc(cands, func(a, b Question) int {
			return strings.Compare(order(repo, stratum, a.ID), order(repo, stratum, b.ID))
		})
		n := 0
		for _, q := range cands {
			if n == per {
				break
			}
			if !answerable(q) {
				continue
			}
			n++
			q.ID = fmt.Sprintf("%s-%s-%d", repo, stratum, n)
			s.Questions = append(s.Questions, q)
		}
	}
	return s
}

func order(repo, stratum, id string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(repo+"|"+stratum+"|"+id)))
}

// answerable is false when a want is spelled in the question's text.
func answerable(q Question) bool {
	for _, w := range q.Want {
		if found(q.Text, w) {
			return false
		}
	}
	return len(q.Want) > 0
}

type callGraph struct {
	in, out map[string][]string
	// named counts the declarations of the calls by simple name.
	named map[string]int
}

func newCallGraph(calls []Edge) *callGraph {
	g := &callGraph{in: map[string][]string{}, out: map[string][]string{}, named: map[string]int{}}
	seen := map[Edge]bool{}
	decls := map[string]bool{}
	for _, c := range calls {
		if c.From == c.To || seen[c] {
			continue
		}
		seen[c] = true
		g.in[c.To] = append(g.in[c.To], c.From)
		g.out[c.From] = append(g.out[c.From], c.To)
		decls[c.From], decls[c.To] = true, true
	}
	for id := range decls {
		if _, d := split(id); d != "" {
			g.named[simple(d)]++
		}
	}
	return g
}

// split cuts an ID into its package's last path element and its
// declaration, "" for a package's own node.
func split(id string) (pkg, decl string) {
	path, decl, _ := strings.Cut(id, "#")
	return path[strings.LastIndex(path, "/")+1:], decl
}

func simple(decl string) string { return decl[strings.LastIndex(decl, ".")+1:] }

// callers are the candidates of the unique and shared strata, their IDs
// the target's until Generate numbers them.
func (g *callGraph) callers(repo, stratum string) []Question {
	var out []Question
	for _, to := range slices.Sorted(maps.Keys(g.in)) {
		pkg, decl := split(to)
		typ, method, isMethod := strings.Cut(decl, ".")
		n := g.named[simple(decl)]
		if decl == "" || stratum == "unique" && n != 1 || stratum == "shared" && (!isMethod || n < 5) {
			continue
		}
		from := g.in[to]
		if len(from) < 2 || len(from) > 8 || slices.ContainsFunc(from, func(id string) bool { _, d := split(id); return d == "" }) {
			continue
		}
		q := Question{ID: to, Shape: stratum, Want: names(from)}
		if isMethod {
			q.Text = fmt.Sprintf("In %s, which functions and methods call the %s method of %s.%s directly?", repo, method, pkg, typ)
		} else {
			q.Text = fmt.Sprintf("In %s, which functions and methods call the function %s.%s directly?", repo, pkg, decl)
		}
		slices.Sort(from)
		q.Note = fmt.Sprintf("%s. %d declarations in the calls are named %s. The type checker's static callers: %s.", to, n, simple(decl), strings.Join(from, ", "))
		out = append(out, q)
	}
	return out
}

func names(ids []string) []string {
	var out []string
	for _, id := range ids {
		_, d := split(id)
		out = append(out, simple(d))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// chains are the candidates of the chain stratum: for each declaration,
// those exactly three calls away that one shortest path reaches.
func (g *callGraph) chains(repo string) []Question {
	var out []Question
	for _, a := range slices.Sorted(maps.Keys(g.out)) {
		if _, d := split(a); d == "" {
			continue
		}
		dist := map[string]int{a: 0}
		paths := map[string]int{a: 1}
		prev := map[string]string{}
		level := []string{a}
		for depth := 1; depth <= 3; depth++ {
			var next []string
			for _, u := range level {
				for _, v := range g.out[u] {
					if d, ok := dist[v]; !ok {
						dist[v], paths[v], prev[v] = depth, paths[u], u
						next = append(next, v)
					} else if d == depth {
						paths[v] += paths[u]
					}
				}
			}
			level = next
		}
		for _, d := range level {
			if _, decl := split(d); decl == "" || paths[d] != 1 {
				continue
			}
			c, b := prev[d], prev[prev[d]]
			out = append(out, Question{ID: a + "|" + d, Shape: "chain", Want: names([]string{b, c}),
				Text: fmt.Sprintf("In %s, how does %s end up calling %s? Name each function or method in the chain between them.", repo, qualified(a), qualified(d)),
				Note: fmt.Sprintf("%s -> %s -> %s -> %s, the one shortest chain of static calls by the type checker.", a, b, c, d)})
		}
	}
	return out
}

func qualified(id string) string {
	pkg, decl := split(id)
	return pkg + "." + decl
}

// Marshal writes a set as a question file Load reads.
func (s *Set) Marshal(header string) []byte {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(header), "\n") {
		b.WriteString(strings.TrimRight("# "+l, " ") + "\n")
	}
	fmt.Fprintf(&b, "\nrepo = %q\ncommit = %q\n", s.Repo, s.Commit)
	if s.Language != "" {
		fmt.Fprintf(&b, "language = %q\n", s.Language)
	}
	for _, q := range s.Questions {
		var want []string
		for _, w := range q.Want {
			want = append(want, fmt.Sprintf("%q", w))
		}
		fmt.Fprintf(&b, "\nquestion {\n  id = %q\n  shape = %s\n  text = %q\n  want = [%s]\n  note = %q\n}\n", q.ID, q.Shape, q.Text, strings.Join(want, ", "), q.Note)
	}
	return []byte(b.String())
}
