// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package cli is the alidade command: serve the graph over MCP, or write
// it out. It is the second consumer of the graph beside the MCP surface
// (ADR 001), and the place the build pipeline is wired to a transport.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/alidade/internal/adapter/golang"
	"github.com/trendvidia/alidade/internal/adapter/ts/svelte"
	"github.com/trendvidia/alidade/internal/build"
	"github.com/trendvidia/alidade/internal/graph"
	"github.com/trendvidia/alidade/internal/mcp"
	"github.com/trendvidia/alidade/internal/python"
	"github.com/trendvidia/alidade/internal/version"
)

const usage = `usage: alidade <command> [flags]

commands:
  serve   serve the repository's graph over MCP on stdin/stdout
  graph   build the repository's graph and write it to stdout as PXF, with
          a build block saying what graph_stats would (layers, Go modules,
          the edges between them)
  setup   register alidade as an MCP server with Claude Code
  hook    a Claude Code PreToolUse hook: deny grep in the repository alidade
          serves, pointing the model at alidade's tools (setup -enforce
          installs it; ALIDADE_ALLOW_GREP=1 in a session's starting
          environment lets it through, not one set inside a command)
  version print the version, commit and Go toolchain of this binary

flags (serve and graph):
  -root DIR       repository root (default ".")
  -tests          include _test.go files in the Go layer
  -tests-exact    -tests, with exclude_tests giving the Go build without tests
                  exactly: production Go edges come from a load without tests
                  (a second load, ~50-85% slower); without it, a production
                  interface call can keep package-level edges into packages
                  only tests bring in
  -go-configs     Go build configurations beyond the host's: "auto", or a
                  GOOS/GOARCH list such as "linux/amd64,windows/arm64"
  -go-deps-source type-check Go dependencies from source instead of taking
                  their types from the compiler's export data (the default
                  since v0.4.0): the module's graph is the same either way,
                  the source load is 3-4x slower and its boundary carries
                  class-hierarchy fan-out into dependency implementations
  -schema-root D  a repository whose .proto files this one's generated code
                  comes from; its messages join the graph, its files are
                  labelled @<dirname>/ (repeatable)

flags (setup):
  -scope SCOPE    user (default; one registration serves every repository)
                  or project (writes .mcp.json in the current repository)
  -print          print the MCP client configuration instead of registering
  -schema-root D  pass a schema root to the registered server (repeatable;
                  implies -scope project, since a schema root belongs to one
                  repository); a relative path resolves from the repository
  -enforce        also add alidade's grep hook to ~/.claude/settings.json, so
                  sessions use alidade instead of grep in the repository it
                  serves; with -print, show the entries instead
  -no-enforce     remove the hook from ~/.claude/settings.json, and nothing else
`

// Run executes the command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "setup":
		return setup(args, stdout, stderr)
	case "hook":
		return hook(os.Stdin, stdout, stderr)
	case "version", "-version", "--version", "-v":
		fmt.Fprintln(stdout, identity())
		return 0
	}
	fs := flag.NewFlagSet("alidade "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	root := fs.String("root", ".", "repository root")
	tests := fs.Bool("tests", false, "include _test.go files")
	testsExact := fs.Bool("tests-exact", false, "include _test.go files, with a production view exactly the build without tests (a second load)")
	goConfigs := fs.String("go-configs", "", `Go build configurations beyond the host's: "auto", or "linux/amd64,windows/arm64"`)
	depsSource := fs.Bool("go-deps-source", false, "type-check Go dependencies from source instead of taking their types from export data")
	var schemaRoots []string
	fs.Func("schema-root", "a repository whose .proto files this one's generated code comes from (repeatable)", func(v string) error {
		schemaRoots = append(schemaRoots, v)
		return nil
	})
	if slices.ContainsFunc(args, func(a string) bool {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		return strings.HasPrefix(a, "-") && name == "no-generated"
	}) {
		// Removed (#126): generated code is the bridge between a schema
		// and the code that uses it, and the option cut it for no saving
		// in build time or memory. Say so rather than "flag provided but
		// not defined".
		fmt.Fprintf(stderr, "alidade %s: -no-generated was removed (#126): generated code is always in the graph; pass exclude_generated to the MCP tools to leave it out of an answer\n", cmd)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		// "alidade graph ../repo" would otherwise build "." and exit 0
		// (#100).
		fmt.Fprintf(stderr, "alidade %s: unexpected argument %q; the repository is given with -root\n", cmd, fs.Arg(0))
		return 2
	}
	if abs, err := filepath.Abs(*root); err == nil && filepath.Dir(abs) == abs {
		// A client that starts the server from "/" with the "-root ."
		// setup -print gives it would walk the whole disk (#100).
		fmt.Fprintf(stderr, "alidade %s: %s is the file system root, not a repository; start alidade in the repository, or pass -root\n", cmd, abs)
		return 2
	}
	opts := build.DefaultOptions()
	opts.Go.Tests = *tests || *testsExact
	opts.Go.ExactTests = *testsExact
	opts.Go.Configs = *goConfigs
	opts.Go.DepsFromExportData = !*depsSource
	opts.SchemaRoots = schemaRoots

	switch cmd {
	case "graph":
		return writeGraph(*root, opts, stdout, stderr)
	case "serve":
		return serve(*root, opts, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "alidade: unknown command %q\n%s", cmd, usage)
		return 2
	}
}

func writeGraph(root string, opts build.Options, stdout, stderr io.Writer) int {
	g, rep, err := build.Build(root, opts)
	if err != nil {
		fmt.Fprintf(stderr, "alidade: %v\n", err)
		return 1
	}
	if err := g.WritePXF(stdout, buildBlock(buildInfo(rep))); err != nil {
		fmt.Fprintf(stderr, "alidade: %v\n", err)
		return 1
	}
	return 0
}

// buildFunc adapts the build pipeline to what the MCP server calls: one
// Session for the server's life, so a rebuild keeps the layers whose
// files did not change (#56).
func buildFunc(root string, opts build.Options) mcp.BuildFunc {
	fn, _, _, _, _ := sessionFuncs(root, opts)
	return fn
}

// sessionFuncs returns the build the server calls, the fingerprint it
// checks before each tool (#204), what is pending in the background with
// the refresh that folds it in (#269), and the close that ends the
// session (#279), over one build session made on first use.
func sessionFuncs(root string, opts build.Options) (mcp.BuildFunc, func() (string, error), func() (int64, string), mcp.BuildFunc, func()) {
	var session *build.Session
	open := func() (*build.Session, error) {
		if session == nil {
			s, err := build.NewSession(root, opts)
			if err != nil {
				return nil, err
			}
			session = s
		}
		return session, nil
	}
	buildFn := func() (*graph.Graph, mcp.BuildInfo, error) {
		s, err := open()
		if err != nil {
			return nil, mcp.BuildInfo{}, err
		}
		g, rep, err := s.Build()
		if err != nil {
			return nil, mcp.BuildInfo{}, err
		}
		return g, buildInfo(rep), nil
	}
	fingerprint := func() (string, error) {
		s, err := open()
		if err != nil {
			return "", err
		}
		return s.Fingerprint()
	}
	pending := func() (int64, string) {
		if session == nil {
			return 0, ""
		}
		return session.Pending()
	}
	refresh := func() (*graph.Graph, mcp.BuildInfo, error) {
		s, err := open()
		if err != nil {
			return nil, mcp.BuildInfo{}, err
		}
		g, rep, err := s.Refresh()
		if err != nil {
			return nil, mcp.BuildInfo{}, err
		}
		return g, buildInfo(rep), nil
	}
	closeFn := func() {
		if session != nil {
			session.Close()
		}
	}
	return buildFn, fingerprint, pending, refresh, closeFn
}

// buildInfo is what both surfaces say about a build: graph_stats over
// MCP, and the build block of the PXF the graph command writes.
func buildInfo(rep *build.Report) mcp.BuildInfo {
	info := mcp.BuildInfo{Root: rep.Root, Layers: rep.Layers(), Rebuilt: rep.Rebuilt, Kept: rep.Kept, Skipped: rep.Skipped(), Elapsed: rep.Elapsed}
	for _, sr := range rep.Schema {
		info.SchemaRoots = append(info.SchemaRoots, sr.Root)
	}
	if rep.Go != nil {
		info.Build, info.ExcludedGoFiles = strings.Join(rep.Go.Configs, " "), rep.Go.Excluded
		info.GoModules, info.GoModulesFailed = rep.Go.Modules, rep.Go.Failed
		info.GoTestNodes, info.GoOutsideTree = rep.Go.Tests, rep.Go.Outside
		info.GoTestsExact = rep.Go.ExactTests
		info.GoPackagesFailed, info.GoPackagesWithoutCalls = rep.Go.FailedPackages, rep.Go.WithoutCalls
		info.GoModuleEdges, info.GoTestModuleEdges = mcpEdges(rep.Go.ModuleEdges), mcpEdges(rep.Go.TestModuleEdges)
	}
	return info
}

func mcpEdges(es []golang.ModuleEdge) []mcp.ModuleEdge {
	var out []mcp.ModuleEdge
	for _, e := range es {
		out = append(out, mcp.ModuleEdge{From: e.From, To: e.To, Kind: string(e.Kind), Count: e.Count})
	}
	return out
}

// buildBlock renders a build's info as the PXF block the graph command
// writes after stats, under the keys graph_stats uses, so a session with
// only the CLI reads what an MCP session would (#76 follow-up). Lists
// are space-joined: layer names and module paths carry no spaces.
func buildBlock(info mcp.BuildInfo) pxf.Entry {
	moduleEdges := edgeBlock("go_module_edges", info.GoModuleEdges)
	testModuleEdges := edgeBlock("go_test_module_edges", info.GoTestModuleEdges)
	var skips []pxf.Entry
	for _, s := range info.Skipped {
		skips = append(skips, block("skip", str("layer", s.Layer), str("file", s.File), num("line", s.Line), str("reason", s.Reason)))
	}
	var skipped pxf.Entry
	if len(skips) > 0 {
		skipped = block("skipped", skips...)
	}
	return block("build",
		str("root", info.Root),
		str("schema_roots", strings.Join(info.SchemaRoots, " ")),
		str("layers", strings.Join(info.Layers, " ")),
		str("layers_rebuilt", strings.Join(info.Rebuilt, " ")),
		str("layers_kept", strings.Join(info.Kept, " ")),
		str("go_build", info.Build),
		num("go_excluded_files", info.ExcludedGoFiles),
		str("go_modules", strings.Join(info.GoModules, " ")),
		str("go_modules_failed", strings.Join(info.GoModulesFailed, " ")),
		num("go_test_nodes", info.GoTestNodes),
		str("go_tests", exactTests(info.GoTestsExact)),
		num("go_outside_tree", info.GoOutsideTree),
		str("go_packages_failed", strings.Join(info.GoPackagesFailed, " ")),
		str("go_packages_without_calls", strings.Join(info.GoPackagesWithoutCalls, " ")),
		moduleEdges,
		testModuleEdges,
		skipped,
		num("build_ms", int(info.Elapsed.Milliseconds())),
	)
}

// edgeBlock renders module edge counts under name, nil when there are
// none.
func edgeBlock(name string, es []mcp.ModuleEdge) pxf.Entry {
	var edges []pxf.Entry
	for _, e := range es {
		to := e.To
		if to == "" {
			to = "external"
		}
		edges = append(edges, block("edge", str("from_module", e.From), str("to_module", to), str("kind", e.Kind), num("count", e.Count)))
	}
	if len(edges) == 0 {
		return nil
	}
	return block(name, edges...)
}

// exactTests is the build block's go_tests: "exact" when -tests-exact
// built the production view from a load without tests (#133).
func exactTests(exact bool) string {
	if exact {
		return "exact"
	}
	return ""
}

// The PXF helpers omit a field that carries nothing, as the graph's do.

func block(name string, entries ...pxf.Entry) pxf.Entry {
	return &pxf.Block{Name: name, Entries: slices.DeleteFunc(entries, func(e pxf.Entry) bool { return e == nil })}
}

func str(key, v string) pxf.Entry {
	if v == "" {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.StringVal{Value: v}}
}

func num(key string, v int) pxf.Entry {
	if v == 0 {
		return nil
	}
	return &pxf.Assignment{Key: key, Value: &pxf.IntVal{Raw: strconv.Itoa(v)}}
}

func serve(root string, opts build.Options, stderr io.Writer) int {
	// The server answers from the Python warm-up pass while jedi reads the
	// tree behind it (#269); the graph command waits for jedi.
	opts.Py.Background = true
	// jedi's answers are kept between sessions (#279), beside the jedi
	// the python package unpacks.
	if dir, err := os.UserCacheDir(); err == nil {
		opts.Py.Cache = filepath.Join(dir, "alidade", "py")
	}
	buildFn, fingerprint, pending, refresh, closeFn := sessionFuncs(root, opts)
	defer closeFn()
	if err := mcp.New(buildFn).WatchFiles(fingerprint).WatchProgress(pending, refresh).ServeStdio(); err != nil {
		fmt.Fprintf(stderr, "alidade: %v\n", err)
		return 1
	}
	return 0
}

// identity is the running build's identity, with the runtimes its tags
// linked: V8 for Svelte components (#221), CPython for Python calls (#237).
func identity() version.Info {
	info := version.Read()
	if svelte.Available() {
		info.Embeds = append(info.Embeds, "v8")
	}
	if python.Available() {
		info.Embeds = append(info.Embeds, "cpython "+python.CPythonVersion)
	}
	return info
}
