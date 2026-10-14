// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/trendvidia/alidade/internal/adapter/golang"
	"github.com/trendvidia/alidade/internal/graph"
)

// The goasm oracle (#187) runs the generator a //go:generate directive
// pins — `go run github.com/trendvidia/goasm/cmd/goasm@vX` — on a file
// holding the template file's aliases and one template, and reads the
// method it emits with go/parser. The layer names the method with
// goasm's gen.MethodName at the version alidade depends on, and the
// receiver by a constant; the generator at the directive's own version
// is what the repository actually ran.

// Goasm is the oracle's view of a repository's goasm templates.
type Goasm struct {
	// Generates are [template, Go method] graph IDs.
	Generates [][2]graph.ID
	// Templates counts the templates read under a runnable directive.
	Templates int
	// NotCommitted are methods the generator emits that the committed
	// generated file does not declare: the file is stale, and the
	// graph, which reads the committed Go, cannot have the edge.
	NotCommitted []string
	// Failed are templates or directives the oracle could not run, with
	// the reason.
	Failed []string
}

// goasmDirectiveRE is a //go:generate line running a pinned goasm.
var goasmDirectiveRE = regexp.MustCompile(`^//go:generate\s+go\s+run\s+(github\.com/trendvidia/goasm/cmd/goasm@\S+)\s+(.*)$`)

type goasmRun struct {
	pkg           string // the generator, module@version
	dir           string // the directory of the Go file holding the directive
	arch, in, out string // in and out absolute
	goPkg         string // the import path of out's package
}

// GoasmTemplates runs the oracle over every goasm directive under root.
func GoasmTemplates(m *graph.Minter, root string) (*Goasm, error) {
	runs, err := goasmRuns(root)
	if err != nil {
		return nil, err
	}
	g := &Goasm{}
	for _, r := range runs {
		g.run(m, root, r)
	}
	sort.Slice(g.Generates, func(i, j int) bool { return g.Generates[i][0] < g.Generates[j][0] })
	return g, nil
}

// goasmRuns finds the directives, each with the package its output
// belongs to: the module path of the nearest go.mod plus the directory.
func goasmRuns(root string) ([]goasmRun, error) {
	mods, _, err := golang.Modules(root, skipOf(root))
	if err != nil {
		return nil, err
	}
	var runs []goasmRun
	skip := skipOf(root)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && skip.SkipsDir(root, p) {
			return filepath.SkipDir // the build leaves it out too
		}
		if d.IsDir() {
			if n := d.Name(); p != root && (n == ".git" || n == "vendor" || n == "testdata" || n == "node_modules" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			runs = append(runs, directivesIn(p, mods)...)
		}
		return nil
	})
	return runs, err
}

// directivesIn reads one Go file's goasm directives.
func directivesIn(path string, mods []golang.Module) []goasmRun {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []goasmRun
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		mm := goasmDirectiveRE.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if mm == nil {
			continue
		}
		r := goasmRun{pkg: mm[1], dir: filepath.Dir(path)}
		args := strings.Fields(mm[2])
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "-arch":
				r.arch = args[i+1]
			case "-in":
				r.in = filepath.Join(r.dir, args[i+1])
			case "-out":
				r.out = filepath.Join(r.dir, args[i+1])
			}
		}
		r.goPkg = importPathOf(filepath.Dir(r.out), mods)
		out = append(out, r)
	}
	return out
}

// importPathOf is a directory's import path: the module owning it,
// longest directory first, joined with the rest of the path.
func importPathOf(dir string, mods []golang.Module) string {
	best := golang.Module{}
	for _, mod := range mods {
		if (dir == mod.Dir || strings.HasPrefix(dir, mod.Dir+string(filepath.Separator))) && len(mod.Dir) > len(best.Dir) {
			best = mod
		}
	}
	if best.Path == "" {
		return ""
	}
	rel, _ := filepath.Rel(best.Dir, dir)
	if rel == "." {
		return best.Path
	}
	return best.Path + "/" + filepath.ToSlash(rel)
}

// run generates every template of one directive's input, one at a time,
// a few at once.
func (g *Goasm) run(m *graph.Minter, root string, r goasmRun) {
	src, err := os.ReadFile(r.in)
	if err != nil || r.out == "" || r.goPkg == "" {
		g.Failed = append(g.Failed, fmt.Sprintf("%s: directive not runnable (in %q, out %q, package %q): %v", r.dir, r.in, r.out, r.goPkg, err))
		return
	}
	aliases, templates := splitTemplates(src)
	committed := declaredMethods(r.out)
	rel, _ := filepath.Rel(root, r.in)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, t := range templates {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			method, err := generateOne(r, aliases, t.body)
			mu.Lock()
			defer mu.Unlock()
			g.Templates++
			g.record(m, filepath.ToSlash(rel), r, t.name, method, committed, err)
		}()
	}
	wg.Wait()
}

// record adds one template's result.
func (g *Goasm) record(m *graph.Minter, rel string, r goasmRun, name, method string, committed map[string]bool, err error) {
	if err != nil {
		g.Failed = append(g.Failed, fmt.Sprintf("%s#%s: %v", rel, name, err))
		return
	}
	if !committed[method] {
		g.NotCommitted = append(g.NotCommitted, fmt.Sprintf("%s#%s -> %s in %s", rel, name, method, r.out))
	}
	from, err1 := m.GoasmID(rel, name)
	to, err2 := m.GoID(r.goPkg, method)
	if err1 == nil && err2 == nil {
		g.Generates = append(g.Generates, [2]graph.ID{from, to})
	}
}

type goasmTemplate struct{ name, body string }

// templateHeadRE is a template's header line: # TEMPLATE Name(params).
var templateHeadRE = regexp.MustCompile(`^#\s*TEMPLATE\s+([A-Za-z_][A-Za-z0-9_]*)`)

// splitTemplates cuts a template file into its alias lines and its
// templates, each from its header to the next header.
func splitTemplates(src []byte) (aliases string, out []goasmTemplate) {
	var alias strings.Builder
	var cur *goasmTemplate
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.body = body.String()
			out = append(out, *cur)
		}
		body.Reset()
	}
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if mm := templateHeadRE.FindStringSubmatch(t); mm != nil {
			flush()
			cur = &goasmTemplate{name: mm[1]}
		}
		switch {
		case cur != nil:
			body.WriteString(line + "\n")
		case strings.HasPrefix(t, "# ALIAS"):
			alias.WriteString(line + "\n")
		}
	}
	flush()
	return alias.String(), out
}

// generators caches each pinned generator, installed once per run:
// `go run pkg@version` asks the module proxy about deprecation on every
// call, and a run of every template one at a time is many calls.
var generators = struct {
	sync.Mutex
	bin map[string]string
	err map[string]error
}{bin: map[string]string{}, err: map[string]error{}}

// generator installs the pinned generator into a scratch directory and
// returns its path.
func generator(pkg string) (string, error) {
	generators.Lock()
	defer generators.Unlock()
	if b, ok := generators.bin[pkg]; ok {
		return b, generators.err[pkg]
	}
	// One directory per pinned version, kept between runs: the version
	// is in the name, so a reuse is the same generator.
	dir := filepath.Join(os.TempDir(), "alidade-oracle-goasm-"+strings.NewReplacer("/", "_", "@", "_").Replace(pkg))
	err := os.MkdirAll(dir, 0o755)
	if _, statErr := os.Stat(filepath.Join(dir, "goasm")); err == nil && statErr != nil {
		cmd := exec.Command("go", "install", pkg)
		cmd.Env = append(os.Environ(), "GOBIN="+dir)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err = cmd.Run(); err != nil {
			err = fmt.Errorf("go install %s: %v: %s", pkg, err, strings.TrimSpace(stderr.String()))
		}
	}
	bin := filepath.Join(dir, "goasm")
	generators.bin[pkg], generators.err[pkg] = bin, err
	return bin, err
}

// generateOne runs the pinned generator on the aliases and one template
// and returns the one method it emits, as Recv.Name.
func generateOne(r goasmRun, aliases, body string) (string, error) {
	bin, err := generator(r.pkg)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "alidade-oracle-goasm-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "one.goasm"), filepath.Join(dir, "out.go")
	if err := os.WriteFile(in, []byte(aliases+"\n"+body), 0o644); err != nil {
		return "", err
	}
	args := []string{"-in", in, "-out", out}
	if r.arch != "" {
		args = append(args, "-arch", r.arch)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = r.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(stderr.String()))
	}
	ms := declaredMethods(out)
	if len(ms) != 1 {
		return "", fmt.Errorf("the generator emitted %d methods for one template", len(ms))
	}
	for k := range ms {
		return k, nil
	}
	return "", nil
}

// declaredMethods lists the methods a Go file declares, as Recv.Name.
func declaredMethods(path string) map[string]bool {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		t := fd.Recv.List[0].Type
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
		}
		if id, ok := t.(*ast.Ident); ok {
			out[id.Name+"."+fd.Name.Name] = true
		}
	}
	return out
}

// A GoasmResult compares the graph's goasm generates edges with the
// generator's. Counts are [oracle, graph has].
type GoasmResult struct {
	Templates            int
	Generates            [2]int
	Missing, Extra       []string
	NotCommitted, Failed []string
}

// CompareGoasm measures g's generates edges from goasm templates.
func CompareGoasm(g *graph.Graph, o *Goasm) *GoasmResult {
	r := &GoasmResult{Templates: o.Templates, NotCommitted: o.NotCommitted, Failed: o.Failed}
	r.Generates, r.Missing = edgeRecall(g, o.Generates, graph.KindGenerates)
	want := map[[2]graph.ID]bool{}
	for _, e := range o.Generates {
		want[e] = true
	}
	for e := range g.Edges() {
		if e.Kind == graph.KindGenerates && strings.HasPrefix(string(e.From), "goasm://") && !want[[2]graph.ID{e.From, e.To}] {
			r.Extra = append(r.Extra, string(e.From)+" -> "+string(e.To))
		}
	}
	sortAll(&r.Extra, &r.NotCommitted, &r.Failed)
	return r
}

func (r *GoasmResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "goasm templates %d, generates %d / %d, %d extra, %d not in the committed file, %d not run\n",
		r.Templates, r.Generates[1], r.Generates[0], len(r.Extra), len(r.NotCommitted), len(r.Failed))
	for _, l := range []struct {
		name  string
		items []string
	}{{"missing generates", r.Missing}, {"extra generates", r.Extra}, {"not committed", r.NotCommitted}, {"not run", r.Failed}} {
		for i, s := range l.items {
			if i == 5 {
				fmt.Fprintf(&b, "%s ... and %d more\n", l.name, len(l.items)-5)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", l.name, s)
		}
	}
	return b.String()
}
