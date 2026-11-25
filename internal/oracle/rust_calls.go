// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ianlancetaylor/demangle"

	"github.com/belluikit/alidade/internal/graph"
)

// The Rust layer's oracle is rustc (ADR 009, #86): each library of a
// workspace compiled to LLVM IR at opt-level 0, whose direct calls are
// rustc's own resolution, and compared with the layer's calls edges.
// rustc shares no code with rust-analyzer, which the layer reads. Its
// symbols are mangled in Rust's v0 format, rustc 1.100's default, and
// decoded by github.com/ianlancetaylor/demangle.
//
// Both sides are named the same way, as the comparison ADR 009 reports
// was first run by hand: `Type::method`, `<Type as Trait>::method`, or a
// free function's module path, generic arguments and closures dropped.
// Which module an impl sits in is part of neither name. A caller in an
// inline `tests` module is left out: a library's IR has no test code.

// A RustLib is one library of a workspace: its package, the identifier
// rustc gives its crate, and the directory of its root file, relative to
// the repository.
type RustLib struct {
	Package, Crate, Dir string
	// Features are the package's features, by name; the comparison builds
	// with the default ones unless RustFeature names one of them.
	Features map[string]bool
}

// RustFeature is a feature to build each library with when its package
// declares it ("full", for tokio's crates, whose default features are
// none); "" builds the defaults.
var RustFeature string

// RustCalls is rustc's view of one library: its direct calls, by caller
// and callee name, and the functions it compiled.
type RustCalls struct {
	Lib   RustLib
	Calls map[string]map[string]bool
	Funcs map[string]bool
	// Foreign are the names of methods of a trait outside the crate,
	// `<KeyedError as Into>::into`: the crate's own when it implements
	// them, otherwise code from outside it that rustc compiled for a crate
	// type (a blanket impl, a trait's provided method).
	Foreign map[string]bool
}

// RustLibs lists the libraries of the Cargo workspace at ws, from cargo
// metadata, run on a copy of its lock file so that nothing is written
// into the repository.
func RustLibs(root, ws string) ([]RustLib, error) {
	env, done, err := cargoEnv(ws)
	if err != nil {
		return nil, err
	}
	defer done()
	cmd := exec.Command("cargo", "metadata", "--no-deps", "--format-version", "1", "--manifest-path", filepath.Join(ws, "Cargo.toml"))
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cargo metadata: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var meta struct {
		Packages []struct {
			Name     string              `json:"name"`
			Features map[string][]string `json:"features"`
			Targets  []struct {
				Name    string   `json:"name"`
				Kind    []string `json:"kind"`
				SrcPath string   `json:"src_path"`
			} `json:"targets"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return nil, fmt.Errorf("cargo metadata: %w", err)
	}
	var libs []RustLib
	for _, p := range meta.Packages {
		for _, t := range p.Targets {
			if !slices.Contains(t.Kind, "lib") && !slices.Contains(t.Kind, "rlib") {
				continue
			}
			dir, err := filepath.Rel(root, filepath.Dir(t.SrcPath))
			if err != nil {
				continue
			}
			features := map[string]bool{}
			for f := range p.Features {
				features[f] = true
			}
			libs = append(libs, RustLib{Package: p.Name, Crate: strings.ReplaceAll(t.Name, "-", "_"), Dir: filepath.ToSlash(dir), Features: features})
		}
	}
	slices.SortFunc(libs, func(a, b RustLib) int { return strings.Compare(a.Package, b.Package) })
	return libs, nil
}

// cargoEnv is the environment Cargo runs in for the oracle: a copy of the
// workspace's lock file, so that a Cargo that resolves writes the copy
// (Cargo 1.97's resolver.lockfile-path, as the layer does for #81). done
// removes the copy.
func cargoEnv(ws string) (env []string, done func(), err error) {
	dir, err := os.MkdirTemp("", "alidade-oracle-lock-*")
	if err != nil {
		return nil, nil, err
	}
	done = func() { os.RemoveAll(dir) }
	lock := filepath.Join(dir, "Cargo.lock")
	b, err := os.ReadFile(filepath.Join(ws, "Cargo.lock"))
	if err == nil {
		err = os.WriteFile(lock, b, 0o644)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		done()
		return nil, nil, err
	}
	return append(os.Environ(), "CARGO_RESOLVER_LOCKFILE_PATH="+lock), done, nil
}

// ReadRustCalls compiles one library to LLVM IR, at opt-level 0, in one
// codegen unit and with no LLVM pass run, into target (a directory of the caller's, never the
// repository's), and reads its direct calls.
func ReadRustCalls(ws, target string, lib RustLib) (*RustCalls, error) {
	env, done, err := cargoEnv(ws)
	if err != nil {
		return nil, err
	}
	defer done()
	ll := filepath.Join(target, lib.Crate+".ll")
	args := []string{"rustc", "-q", "--manifest-path", filepath.Join(ws, "Cargo.toml"), "-p", lib.Package, "--lib"}
	if RustFeature != "" && lib.Features[RustFeature] {
		args = append(args, "--features", RustFeature)
	}
	// no-prepopulate-passes runs no LLVM pass, so a call of an
	// #[inline(always)] function, which LLVM inlines even at opt-level 0,
	// stays a call (tokio's UnsafeCell::with_mut: 15 calls without it, 90
	// with).
	args = append(args, "--", "--emit=llvm-ir="+ll, "-C", "opt-level=0", "-C", "codegen-units=1", "-C", "no-prepopulate-passes", "-C", "link-dead-code")
	cmd := exec.Command("cargo", args...)
	cmd.Env = append(env, "CARGO_TARGET_DIR="+target, "CARGO_INCREMENTAL=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cargo rustc -p %s: %w: %s", lib.Package, err, lastLine(stderr.String()))
	}
	f, err := os.Open(ll)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readIR(f, lib)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

var (
	defineRE = regexp.MustCompile(`^define .*?@"?(_R[0-9A-Za-z_$.]+)"?\(`)
	callRE   = regexp.MustCompile(`(?:call|invoke) .*?@"?(_R[0-9A-Za-z_$.]+)"?\(`)
)

// readIR reads the functions an LLVM IR module defines and the functions
// each calls directly, named by rustKey. A call through a pointer, `dyn`
// among them, names no function and is not read.
func readIR(r io.Reader, lib RustLib) (*RustCalls, error) {
	out := &RustCalls{Lib: lib, Calls: map[string]map[string]bool{}, Funcs: map[string]bool{}, Foreign: map[string]bool{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 64<<20)
	cur := ""
	for sc.Scan() {
		line := sc.Text()
		if m := defineRE.FindStringSubmatch(line); m != nil {
			cur = out.key(m[1])
			if cur != "" {
				out.Funcs[cur] = true
			}
			continue
		}
		if strings.HasPrefix(line, "}") {
			cur = ""
			continue
		}
		if cur == "" {
			continue
		}
		for _, m := range callRE.FindAllStringSubmatch(line, -1) {
			if k := out.key(m[1]); k != "" && k != cur {
				if out.Calls[cur] == nil {
					out.Calls[cur] = map[string]bool{}
				}
				out.Calls[cur][k] = true
			}
		}
	}
	return out, sc.Err()
}

// key names a symbol by rustKey, and records whether it is a method of a
// trait outside the crate.
func (rc *RustCalls) key(sym string) string {
	name := demangled(sym)
	k := rustKey(name, rc.Lib.Crate)
	if k != "" && foreignTrait(name, rc.Lib.Crate) {
		rc.Foreign[k] = true
	}
	return k
}

// foreignTrait says whether a demangled `<Type as Trait>::method` names a
// trait outside the crate.
func foreignTrait(name, crate string) bool {
	if !strings.HasPrefix(name, "<") {
		return false
	}
	end := matching(name)
	if end < 0 {
		return false
	}
	_, trait, ok := strings.Cut(name[1:end], " as ")
	tr := rustPath(trait)
	return ok && len(tr) > 0 && tr[0] != crate
}

// demangled is a symbol's name without its parameters' types; "" when it
// is not one demangle reads.
func demangled(sym string) string {
	s, err := demangle.ToString(sym, demangle.NoParams)
	if err != nil {
		return ""
	}
	return s
}

var closureRE = regexp.MustCompile(`::\{(closure|constant|shim:[^}]*)#\d+\}`)

// rustKey names a function rustc compiled, from its demangled name, the
// way layerKey names the layer's: "" for one outside the crate. The
// function a closure is in is the closure's name, as the layer attributes
// a closure's calls to the function holding it.
func rustKey(name, crate string) string {
	name = closureRE.ReplaceAllString(name, "")
	if strings.HasPrefix(name, "<") {
		end := matching(name)
		if end < 0 || !strings.HasPrefix(name[end+1:], "::") {
			return ""
		}
		self, method := name[1:end], rustPath(name[end+3:])
		if len(method) == 0 {
			return ""
		}
		ty, trait, hasTrait := strings.Cut(self, " as ")
		// A reference's or a pointer's type (`&Kind`) is not the crate's:
		// its impls are the standard library's blanket ones.
		tp := rustPath(ty)
		if len(tp) == 0 || tp[0] != crate {
			return ""
		}
		if hasTrait {
			tr := rustPath(trait)
			if len(tr) == 0 {
				return ""
			}
			return "<" + tp[len(tp)-1] + " as " + tr[len(tr)-1] + ">::" + strings.Join(method, "::")
		}
		return tp[len(tp)-1] + "::" + strings.Join(method, "::")
	}
	segs := rustPath(name)
	if len(segs) < 2 || segs[0] != crate {
		return ""
	}
	return typed(segs[1:])
}

// matching is the index of the '>' that closes the '<' at s[0], -1 when
// none does.
func matching(s string) int {
	depth := 0
	for i, c := range s {
		switch c {
		case '<':
			depth++
		case '>':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// rustPath splits a Rust path at "::", its generic arguments dropped.
func rustPath(p string) []string {
	var b strings.Builder
	depth := 0
	for _, c := range p {
		switch {
		case c == '<':
			depth++
		case c == '>':
			depth--
		case depth == 0:
			b.WriteRune(c)
		}
	}
	var out []string
	for _, s := range strings.Split(b.String(), "::") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// typed names `module::Type::method` as `Type::method`: the module an
// impl sits in is part of neither side's name. A free function keeps its
// module path.
func typed(segs []string) string {
	for i := len(segs) - 2; i >= 0; i-- {
		if r := segs[i]; r != "" && r[0] >= 'A' && r[0] <= 'Z' {
			return strings.Join(segs[i:], "::")
		}
	}
	return strings.Join(segs, "::")
}

// layerKey names a function of the layer's, by its ID, as rustKey names
// rustc's: "" for one outside the library's directory. The file's path
// below that directory is its module (lib.rs the crate's own, a mod.rs
// its directory's); `Type[Trait]` is `<Type as Trait>`.
func layerKey(id graph.ID, dir string) string {
	body, ok := strings.CutPrefix(string(id), "rust://")
	if !ok {
		return ""
	}
	file, decl, ok := strings.Cut(body, "#")
	if !ok || !strings.HasPrefix(file, dir+"/") || !strings.HasSuffix(file, ".rs") {
		return ""
	}
	mod := strings.TrimSuffix(strings.TrimPrefix(file, dir+"/"), ".rs")
	switch {
	case mod == "lib":
		mod = ""
	case strings.HasSuffix(mod, "/mod"):
		mod = strings.TrimSuffix(mod, "/mod")
	}
	var segs []string
	for _, s := range strings.Split(mod, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	for _, s := range strings.Split(decl, ".") {
		segs = append(segs, strings.TrimSuffix(s, "()"))
	}
	for i, s := range segs {
		if ty, tr, ok := strings.Cut(s, "["); ok {
			return "<" + ty + " as " + strings.TrimSuffix(tr, "]") + ">::" + strings.Join(segs[i+1:], "::")
		}
	}
	return typed(segs)
}

// A RustCompare is one library's comparison: the callers both sides
// have, and of their edges the ones both have, rustc alone and the layer
// alone, with the causes the comparison can name.
type RustCompare struct {
	Lib                          RustLib
	LayerCallers, RustcCallers   int
	Both                         int
	Agree, RustcOnly, LayerOnly  int
	NoNode                       []string // rustc only, to a function the layer has no node for
	Outside                      int      // rustc's edges to code outside the crate compiled for a crate type, left out
	BoundRustc, BoundLayer       int      // a call through a generic trait bound, each side's
	Dyn                          int      // layer only, to a trait's method rustc has no instantiation of from the caller: dyn dispatch
	Operator                     int      // rustc only, to an operator trait's method
	OtherRustc, OtherLayer       []string
	layerFuncs, rustcFuncs       map[string]bool
	layerCalls, rustcCalls       map[string]map[string]bool
	unmatchedRustc, unmatchedLay [][2]string
}

// operatorTraits are the traits an operator calls a method of, with no
// reference in the source: `==` PartialEq::eq, `+` Add::add, `*x`
// Deref::deref, a scope's end Drop::drop.
var operatorTraits = map[string]bool{
	"PartialEq": true, "PartialOrd": true, "Ord": true, "Add": true, "Sub": true, "Mul": true, "Div": true,
	"Rem": true, "Neg": true, "Not": true, "BitAnd": true, "BitOr": true, "BitXor": true, "Shl": true, "Shr": true,
	"AddAssign": true, "SubAssign": true, "MulAssign": true, "DivAssign": true, "RemAssign": true,
	"BitAndAssign": true, "BitOrAssign": true, "BitXorAssign": true, "ShlAssign": true, "ShrAssign": true,
	"Index": true, "IndexMut": true, "Deref": true, "DerefMut": true, "Drop": true,
}

// CompareRustCalls compares one library's calls edges with rustc's.
func CompareRustCalls(g *graph.Graph, rc *RustCalls) *RustCompare {
	r := &RustCompare{Lib: rc.Lib, layerFuncs: map[string]bool{}, rustcFuncs: rc.Funcs, layerCalls: map[string]map[string]bool{}, rustcCalls: rc.Calls}
	kinds := map[graph.ID]graph.Kind{}
	for n := range g.Nodes() {
		kinds[n.ID] = n.Kind
		if n.Kind == graph.KindFunc || n.Kind == graph.KindMethod {
			if k := layerKey(n.ID, rc.Lib.Dir); k != "" {
				r.layerFuncs[k] = true
			}
		}
	}
	for e := range g.Edges() {
		if e.Kind != graph.KindCalls {
			continue
		}
		from, to := layerKey(e.From, rc.Lib.Dir), layerKey(e.To, rc.Lib.Dir)
		if from == "" || to == "" || from == to || slices.Contains(strings.Split(from, "::"), "tests") {
			continue
		}
		if r.layerCalls[from] == nil {
			r.layerCalls[from] = map[string]bool{}
		}
		r.layerCalls[from][to] = true
	}
	r.rustcCalls = r.provided(r.ownCode(rc))
	r.LayerCallers, r.RustcCallers = len(r.layerCalls), len(r.rustcCalls)
	for _, c := range sortedKeys(r.layerCalls) {
		if r.rustcCalls[c] != nil {
			r.Both++
			r.compareCaller(c)
		}
	}
	r.classify()
	return r
}

// ownCode leaves out the functions rustc compiled from code outside the
// crate for a crate type: a method of a trait outside it that the layer
// has no node for, so that the crate does not declare (`<KeyedError as
// Into>::into`, from the standard library's blanket impl;
// `<Cardinality as PartialEq>::ne`, PartialEq's provided method). Rust's
// legacy symbols name such a function by where it is written, outside the
// crate; the v0 symbols name it by the type. Outside counts the edges.
func (r *RustCompare) ownCode(rc *RustCalls) map[string]map[string]bool {
	outside := func(k string) bool { return rc.Foreign[k] && !r.layerFuncs[k] }
	out := map[string]map[string]bool{}
	for c, tos := range rc.Calls {
		if outside(c) {
			continue
		}
		for to := range tos {
			if outside(to) {
				r.Outside++
				continue
			}
			if out[c] == nil {
				out[c] = map[string]bool{}
			}
			out[c][to] = true
		}
	}
	return out
}

// provided names a trait's provided method by the trait, as the layer
// does: rustc names the copy it compiles for a type that does not
// override it `<BigInt as Message>::encode_to_vec`, and that copy's body
// is the trait's `Message::encode_to_vec`. A method the type declares
// keeps its own name.
func (r *RustCompare) provided(calls map[string]map[string]bool) map[string]map[string]bool {
	name := func(k string) string {
		if tr, m, ok := traitMethod(k); ok && !r.layerFuncs[k] && r.layerFuncs[tr+"::"+m] {
			return tr + "::" + m
		}
		return k
	}
	out := map[string]map[string]bool{}
	for c, tos := range calls {
		from := name(c)
		for to := range tos {
			if to = name(to); to == from {
				continue
			}
			if out[from] == nil {
				out[from] = map[string]bool{}
			}
			out[from][to] = true
		}
	}
	return out
}

// compareCaller counts one caller's edges, and keeps the ones only one
// side has for classify.
func (r *RustCompare) compareCaller(c string) {
	lay, ir := r.layerCalls[c], r.rustcCalls[c]
	for _, to := range sortedKeys(lay) {
		if ir[to] {
			r.Agree++
		} else {
			r.LayerOnly++
			r.unmatchedLay = append(r.unmatchedLay, [2]string{c, to})
		}
	}
	for _, to := range sortedKeys(ir) {
		if lay[to] {
			continue
		}
		r.RustcOnly++
		if !r.layerFuncs[to] {
			r.NoNode = append(r.NoNode, c+" -> "+to)
			continue
		}
		r.unmatchedRustc = append(r.unmatchedRustc, [2]string{c, to})
	}
}

// classify names the causes it can: a call through a generic trait bound
// (the layer's edge goes to the trait's method, `Message::encode_to`,
// rustc's to the instantiation, `<BigInt as Message>::encode_to`, from
// the same caller), and an operator's call, which rustc alone has. The
// rest are listed.
func (r *RustCompare) classify() {
	bound := map[[2]string]bool{}
	for _, e := range r.unmatchedRustc {
		if tr, m, ok := traitMethod(e[1]); ok && r.layerCalls[e[0]][tr+"::"+m] {
			r.BoundRustc++
			bound[[2]string{e[0], tr + "::" + m}] = true
		} else if ok && operatorTraits[tr] {
			r.Operator++
		} else {
			r.OtherRustc = append(r.OtherRustc, e[0]+" -> "+e[1])
		}
	}
	traits := r.traitMethods()
	for _, e := range r.unmatchedLay {
		switch {
		case bound[e]:
			r.BoundLayer++
		case traits[e[1]]:
			r.Dyn++
		default:
			r.OtherLayer = append(r.OtherLayer, e[0]+" -> "+e[1])
		}
	}
}

// traitMethods are the `Trait::method` names of the methods rustc
// compiled an implementation of, `<Type as Trait>::method`: a layer edge
// to one that rustc has no instantiation of from the same caller is a
// call through `dyn`, an indirect call rustc cannot name (ADR 009).
func (r *RustCompare) traitMethods() map[string]bool {
	out := map[string]bool{}
	add := func(k string) {
		if tr, m, ok := traitMethod(k); ok {
			out[tr+"::"+m] = true
		}
	}
	for k := range r.rustcFuncs {
		add(k)
	}
	for c, tos := range r.rustcCalls {
		add(c)
		for k := range tos {
			add(k)
		}
	}
	return out
}

// traitMethod reads `<Type as Trait>::method` as its trait and method.
func traitMethod(k string) (trait, method string, ok bool) {
	rest, ok := strings.CutPrefix(k, "<")
	if !ok {
		return "", "", false
	}
	self, method, ok := strings.Cut(rest, ">::")
	if !ok {
		return "", "", false
	}
	_, trait, ok = strings.Cut(self, " as ")
	return trait, method, ok
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (r *RustCompare) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "rust %s (%s): layer callers %d, rustc callers %d, in both %d\n", r.Lib.Package, r.Lib.Dir, r.LayerCallers, r.RustcCallers, r.Both)
	fmt.Fprintf(&b, "  edges from callers in both: agree %d, rustc only %d, layer only %d (left out: %d of rustc's to code outside the crate)\n",
		r.Agree, r.RustcOnly, r.LayerOnly, r.Outside)
	fmt.Fprintf(&b, "  rustc only: %d to a function with no node, %d through a generic trait bound, %d an operator's, %d other\n",
		len(r.NoNode), r.BoundRustc, r.Operator, len(r.OtherRustc))
	fmt.Fprintf(&b, "  layer only: %d through a generic trait bound, %d to a trait's method through dyn, %d other\n", r.BoundLayer, r.Dyn, len(r.OtherLayer))
	for _, e := range r.NoNode {
		fmt.Fprintf(&b, "    rustc only, no node: %s\n", e)
	}
	for _, e := range r.OtherRustc {
		fmt.Fprintf(&b, "    rustc only: %s\n", e)
	}
	for _, e := range r.OtherLayer {
		fmt.Fprintf(&b, "    layer only: %s\n", e)
	}
	return b.String()
}
