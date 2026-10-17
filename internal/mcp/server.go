// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/belluikit/alidade/internal/adapter/ts/svelte"
	"os"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/belluikit/alidade/internal/graph"
	"github.com/belluikit/alidade/internal/python"
	"github.com/belluikit/alidade/internal/version"
)

// Version is reported to MCP clients at initialization: the binary's
// module version, as `alidade version` prints it (#70).
var Version = version.Read().Version

// BuildInfo says what a build produced, for graph_stats.
type BuildInfo struct {
	// Root is the repository the graph was built from.
	Root string
	// Layers names the adapters that ran.
	Layers []string
	// Build is the Go layer's build configuration, "GOOS/GOARCH" and
	// tags, or "" when no Go layer ran; ExcludedGoFiles counts the Go
	// files that configuration left out (#43).
	Build           string
	ExcludedGoFiles int
	// GoModules are the module paths the Go layer loaded, and
	// GoModulesFailed the ones under the root that did not load (#67);
	// a failed module's package node carries the error.
	GoModules, GoModulesFailed []string
	// GoModuleEdges counts, per module, the production edges leaving it
	// for another module of the repository or for packages outside it, by
	// kind (#76); GoTestModuleEdges the test edges, the same way, so the
	// two sum to the graph's (#142).
	GoModuleEdges, GoTestModuleEdges []ModuleEdge
	// GoTestNodes counts the Go declarations from _test.go files, each
	// marked test=true with every edge touching it (#81); 0 when tests
	// were not loaded, so a reader can tell which graph this is.
	GoTestNodes int
	// GoTestsExact says the production view's Go nodes and Go-to-Go edges
	// are the build without tests exactly: -tests-exact built it from a
	// load without them (#133).
	GoTestsExact bool
	// GoOutsideTree counts the Go declarations left out because their
	// source is not in the tree — cgo's generated wrappers (#92).
	GoOutsideTree int
	// GoPackagesFailed names the packages that did not type-check, each
	// in the graph with its error and its resolved declarations marked
	// partial; GoPackagesWithoutCalls the packages whose calls are not in
	// the graph because one they import is broken (caliper#38, #95).
	GoPackagesFailed, GoPackagesWithoutCalls []string
	// Skipped are the inputs a layer left out because no ID could name
	// them or they could not be read, each with its reason (#92).
	Skipped []graph.Skip
	// SchemaRoots are the further repositories whose .proto layer joined
	// the graph (ADR 002 rule 12), absolute.
	SchemaRoots []string
	// Rebuilt and Kept say which layers a rebuild ran and which it kept
	// because their files were unchanged (#56); empty on a first build.
	Rebuilt, Kept []string
	// Elapsed is how long the build took.
	Elapsed time.Duration
	// Overlay says how a worktree's graph was laid over the base's (#25);
	// nil for the graph of the checkout the server serves.
	Overlay *OverlayInfo
}

// OverlayInfo is how a worktree's graph was made from the base's.
type OverlayInfo struct {
	// Base is the root of the graph it is laid over.
	Base string
	// Inherited are the layers taken whole from the base, their files
	// being the base's.
	Inherited []string
	// Packages are the Go packages loaded again because a file of theirs
	// differs from the base's, Removed those the worktree no longer has.
	Packages, Removed []string
	// Carried counts the base's calls and implements edges from those
	// packages kept, INFERRED with carried=base: a call through an
	// interface or a function value depends on code a load of fewer
	// packages does not read.
	Carried int
	// GoWhole says the Go layer was built whole: the module files differ,
	// or the base has no Go layer.
	GoWhole bool
}

// A Tree is how the server builds and keeps fresh the graph of another
// worktree of the repository, laid over the graph of the checkout it
// serves (#25): the same hooks New, WatchFiles and WatchProgress give for
// that one, and Close for when the server lets the graph go.
type Tree struct {
	Build       BuildFunc
	Fingerprint func() (string, error)
	Pending     func() (int64, string)
	Refresh     BuildFunc
	Close       func()
}

// maxWorktrees is how many worktrees' graphs the server holds besides
// its own checkout's, the owner's decision on #25; the one used longest
// ago goes first. A worktree's
// graph shares the base's contributions, and what it holds of its own is
// a copy of the graph's indexes: about the base graph's size again.
const maxWorktrees = 4

// A ModuleEdge is one count of GoModuleEdges: edges of one kind from a
// module into another, or into packages outside the repository when To
// is "".
type ModuleEdge struct {
	From, To, Kind string
	Count          int
}

// A BuildFunc builds the graph from the working tree. The server calls it
// lazily on the first tool that needs a graph, and again after rebuild.
type BuildFunc func() (*graph.Graph, BuildInfo, error)

// Server holds one graph for the session, and one for each other
// worktree a tool asked about, and the MCP server that exposes them.
type Server struct {
	mcp *server.MCPServer
	// root is the directory the graph is built from, when known before
	// a build: a search of another worktree needs it, and not the graph.
	root string

	mu sync.Mutex
	// base is the graph of the checkout the server serves.
	base held
	// worktrees are other worktrees' graphs, by top directory, opened
	// through worktree (#25).
	worktrees map[string]*held
	worktree  func(root string) (Tree, error)
}

// held is one graph the server holds, and what keeps it fresh.
type held struct {
	build BuildFunc
	g     *graph.Graph
	info  BuildInfo
	built time.Time
	// fresh fingerprints the files the build reads, and fp is its value
	// before the build that made g: a call that finds them different
	// rebuilds first (#204).
	fresh func() (string, error)
	fp    string
	// pending says what a layer completing in the background still lacks,
	// and refresh folds in what it has learnt since (#269); pv is
	// pending's version before the build or refresh that made g.
	pending func() (int64, string)
	refresh BuildFunc
	pv      int64
	// close lets a worktree's graph go; used is when a tool last read it.
	close func()
	used  time.Time
}

// WatchFiles makes every tool answer from the files on disk at the time
// of the call (#204, ADR 005 amended): before a tool reads the graph,
// fingerprint says cheaply whether any file may have changed since the
// build, and if one may, the graph is built again first — the build
// function reruns only the layers whose files' contents changed — and
// the answer carries "rebuilt" with the layers it reran.
func (s *Server) WatchFiles(fingerprint func() (string, error)) *Server {
	s.base.fresh = fingerprint
	return s
}

// WatchProgress makes every tool answer from what a layer completing in
// the background knows at the time of the call (#269, ADR 005 amended):
// when pending's version has moved since the graph was made, refresh
// folds the new answers in first, and while anything is pending the
// answer says what, under "pending".
func (s *Server) WatchProgress(pending func() (int64, string), refresh BuildFunc) *Server {
	s.base.pending, s.base.refresh = pending, refresh
	return s
}

// Worktrees lets the graph tools answer for another worktree of the
// repository, named by root (#25): open makes its graph's Tree, laid over
// the served checkout's graph, the first time a tool asks.
func (s *Server) Worktrees(open func(root string) (Tree, error)) *Server {
	s.worktree = open
	return s
}

// Root says which directory build builds the graph from, so a tool that
// needs only that does not wait for a build (#23).
func (s *Server) Root(dir string) *Server {
	s.root = dir
	return s
}

// servedRoot is the directory the graph is built from: as Root said, or
// as a build reports.
func (s *Server) servedRoot(ctx contextT) (string, error) {
	if s.root != "" {
		return s.root, nil
	}
	_, info, err := s.graph(ctx)
	return info.Root, err
}

// New returns a server over the graph build produces. Nothing is built
// until a tool asks.
func New(build BuildFunc) *Server {
	s := &Server{base: held{build: build}, worktrees: map[string]*held{}}
	s.mcp = server.NewMCPServer("alidade", Version,
		server.WithToolCapabilities(false),
		server.WithInstructions(instructions),
	)
	s.registerTools()
	s.registerTextSearch()
	return s
}

// svelteNote says what the build does with Svelte components (#221).
var svelteNote = func() string {
	if svelte.Available() {
		return "a Svelte component's script and markup calls are in it, a component used in markup a call to it"
	}
	return "calls inside Svelte components are not in it: this build has no V8"
}()

// pythonNote says what the build does with Python files (#237).
var pythonNote = func() string {
	if python.Available() {
		return "Python calls are resolved by jedi's type inference: a call through a parameter is not placed, a call to a decorated function reaches both it and the wrapper its decorator returns, " +
			"a with statement calls __enter__ and __exit__, reading a property calls its function, and a call through a dataclass field annotated Callable reaches each function the class is built with; a Python function named without being called (a stub installed, a callback passed) has a references edge"
	}
	return "Python files are in it without their declarations or calls: this build has no CPython"
}()

// instructions load with the server even when an agent defers the tool
// schemas, so they carry what the graph cannot answer (#173): a text
// question asked of it gets an incomplete answer that looks whole.
var instructions = `alidade is a graph of a repository: Go, TypeScript and Python declarations with
type-resolved call graphs (a JSX element is a call; ` + svelteNote + `; a call
through an interface reaches its implementations, via=interface in TypeScript; a TypeScript or Python class's implements edges also reach
the class it inherits from, its direct base only; ` + pythonNote + `), plus schema
(proto, SQL, PXF) and document layers.
A Go references edge joins a declaration to a type it names, or a function
to a package-level constant or variable it reads or writes. Start with find_nodes for an ID, then neighbors or shortest_path; or
subgraph_for_question with the names involved. Edges state their
derivation; INFERRED is heuristic. graph_stats and graph_issues say what
the graph could not build or place. In an answer, an edge's f and t are
indexes into its nodes; a node's name is its ID's last segment when n is
absent, and at ":12" is a line of the ID's own file. It holds declarations and how they
relate, not text: for a string literal, a log message, a comment or prose,
use search_text, which names the declaration holding each hit. A doc
naming a declaration that shares its name with another is not linked to
either; search_text finds it. Every tool takes root, another git worktree of
the repository, to answer for that worktree's code.`

// alwaysLoad asks the client to load a tool's schema up front rather than
// defer it behind tool search (#156, the owner's decision). Claude Code
// defers MCP tools by default, and a deferred tool was never chosen:
// 0 of 50 sessions on the benchmark used alidade with its schemas
// deferred, 21 of 50 with them loaded — two thirds of the call-graph
// questions, 23% less per correct answer, 11% less overall, the
// schemas' own tokens counted. Documented at
// https://code.claude.com/docs/en/mcp.md#exempt-a-server-from-deferral.
const alwaysLoad = "anthropic/alwaysLoad"

// addTool registers a tool, marked alwaysLoad, behind safe.
func (s *Server) addTool(tool mcpsdk.Tool, h server.ToolHandlerFunc) {
	tool.Meta = &mcpsdk.Meta{AdditionalFields: map[string]any{alwaysLoad: true}}
	s.mcp.AddTool(tool, safe(h))
}

// MCP returns the underlying MCP server, for serving over a transport or
// for an in-process client.
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// ServeStdio serves over standard input and output until the client
// disconnects.
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcp)
}

// graph returns the session's graph, building it on first use, and
// building it again first when the files have changed since (#204).
func (s *Server) graph(ctx contextT) (*graph.Graph, BuildInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base.get(ctx)
}

// graphFor returns the graph a tool call asks about: the session's, or
// with root another worktree's, laid over the session's (#25). An answer
// about another worktree says which, under root.
func (s *Server) graphFor(ctx contextT, req mcpsdk.CallToolRequest) (*graph.Graph, BuildInfo, error) {
	return s.graphOf(ctx, req, false)
}

// graphOf is graphFor, dropping the graph first when rebuild says so. A
// worktree's graph is looked up, opened and built under one hold of the
// lock, so a call for another worktree cannot let it go in between.
func (s *Server) graphOf(ctx contextT, req mcpsdk.CallToolRequest, rebuild bool) (*graph.Graph, BuildInfo, error) {
	p, err := s.worktreeRoot(ctx, req.GetString("root", ""))
	if err != nil {
		return nil, BuildInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == "" {
		if rebuild {
			s.base.g = nil
		}
		return s.base.get(ctx)
	}
	h, err := s.tree(p)
	if err != nil {
		return nil, BuildInfo{}, err
	}
	if rebuild {
		h.g = nil
	}
	g, info, err := h.get(ctx)
	noteRoot(ctx, p)
	return g, info, err
}

// worktreeRoot resolves root to another worktree's top directory, or ""
// for the served checkout.
func (s *Server) worktreeRoot(ctx contextT, root string) (string, error) {
	if root == "" {
		return "", nil
	}
	served, err := s.servedRoot(ctx)
	if err != nil {
		return "", err
	}
	p, other, err := treeParam(root, served)
	if err != nil {
		return "", badParam{err}
	}
	if !other {
		return "", nil
	}
	if s.worktree == nil {
		return "", badParam{fmt.Errorf("root: this server builds no graph of another worktree")}
	}
	return p, nil
}

// tree returns the held graph of the worktree at p, opening it on first
// use, with s.mu held. The served checkout's graph is made fresh first:
// the worktree's is laid over it. What that build notes is the served
// checkout's, not the answer's, so it is noted nowhere.
func (s *Server) tree(p string) (*held, error) {
	if _, _, err := s.base.get(context.Background()); err != nil {
		return nil, err
	}
	h := s.worktrees[p]
	if h == nil {
		t, err := s.worktree(p)
		if err != nil {
			return nil, err
		}
		h = &held{build: t.Build, fresh: t.Fingerprint, pending: t.Pending, refresh: t.Refresh, close: t.Close}
		s.worktrees[p] = h
	}
	h.used = time.Now()
	s.evict()
	return h, nil
}

// evict lets go of the worktrees' graphs beyond maxWorktrees, the one
// used longest ago first.
func (s *Server) evict() {
	for len(s.worktrees) > maxWorktrees {
		var oldest string
		for p, h := range s.worktrees {
			if oldest == "" || h.used.Before(s.worktrees[oldest].used) {
				oldest = p
			}
		}
		if c := s.worktrees[oldest].close; c != nil {
			c()
		}
		delete(s.worktrees, oldest)
	}
}

// get returns the held graph, building it on first use, building it again
// first when its files have changed since (#204), and folding in what a
// layer completing in the background has learnt (#269).
func (h *held) get(ctx contextT) (*graph.Graph, BuildInfo, error) {
	stale := h.g != nil && h.changed()
	if stale {
		h.g = nil
	}
	var pv int64
	if h.pending != nil {
		// Taken before the build or refresh, as the fingerprint is, so
		// what jedi learns while it runs is folded in by the next call.
		pv, _ = h.pending()
	}
	if h.g == nil {
		if err := h.buildNow(pv); err != nil {
			return nil, BuildInfo{}, err
		}
		if stale {
			noteRebuilt(ctx, h.info.Rebuilt)
		}
	} else if err := h.foldIn(pv); err != nil {
		return nil, BuildInfo{}, err
	}
	if h.pending != nil {
		_, note := h.pending()
		notePending(ctx, note)
	}
	return h.g, h.info, nil
}

// changed reports whether the files may have changed since the build.
func (h *held) changed() bool {
	if h.fresh == nil {
		return false
	}
	fp, err := h.fresh()
	return err != nil || fp != h.fp
}

// buildNow builds the graph, pv being what was pending before it.
func (h *held) buildNow(pv int64) error {
	var fp string
	if h.fresh != nil {
		// Taken before the build, so a file changed while it runs is
		// seen by the next call rather than lost.
		fp, _ = h.fresh()
	}
	g, info, err := h.build()
	if err != nil {
		return err
	}
	h.g, h.info, h.built, h.fp, h.pv = g, info, time.Now(), fp, pv
	return nil
}

// foldIn refreshes the graph when what is completing in the background
// has moved past pv's state since the graph was made (#269).
func (h *held) foldIn(pv int64) error {
	if h.pending == nil || pv == h.pv {
		return nil
	}
	g, info, err := h.refresh()
	if err != nil {
		return err
	}
	h.g, h.info, h.built, h.pv = g, info, time.Now(), pv
	return nil
}

// rebuild drops the graph a call asks about and builds it again.
func (s *Server) rebuild(ctx contextT, req mcpsdk.CallToolRequest) (*graph.Graph, BuildInfo, error) {
	return s.graphOf(ctx, req, true)
}

// A callNote carries, through one tool call's context, the layers a
// rebuild the call caused reran, for its answer to say.
type callNote struct {
	rebuilt []string
	pending string
	root    string
}

type callNoteKey struct{}

func noteRebuilt(ctx contextT, layers []string) {
	if n, ok := ctx.Value(callNoteKey{}).(*callNote); ok {
		n.rebuilt = append([]string{}, layers...)
		if n.rebuilt == nil {
			n.rebuilt = []string{}
		}
	}
}

// noteRoot says which worktree's graph a call answered from (#25).
func noteRoot(ctx contextT, root string) {
	if n, ok := ctx.Value(callNoteKey{}).(*callNote); ok {
		n.root = root
	}
}

func notePending(ctx contextT, pending string) {
	if n, ok := ctx.Value(callNoteKey{}).(*callNote); ok {
		n.pending = pending
	}
}

// withRebuilt adds "rebuilt" to a JSON answer whose call rebuilt the
// graph first, so an agent knows the answer is about the files as they
// are now, and why it took longer.
func withRebuilt(res *mcpsdk.CallToolResult, layers []string) *mcpsdk.CallToolResult {
	return withField(res, "rebuilt", layers)
}

// withField adds a field first in a JSON answer.
func withField(res *mcpsdk.CallToolResult, key string, v any) *mcpsdk.CallToolResult {
	if res == nil || res.IsError || len(res.Content) != 1 {
		return res
	}
	tc, ok := res.Content[0].(mcpsdk.TextContent)
	if !ok || !strings.HasPrefix(tc.Text, "{") {
		return res
	}
	b, err := json.Marshal(v)
	if err != nil {
		return res
	}
	sep := ","
	if strings.HasPrefix(tc.Text, "{}") {
		sep = ""
	}
	tc.Text = `{"` + key + `":` + string(b) + sep + tc.Text[1:]
	res.Content[0] = tc
	return res
}

// safe wraps a handler so a panic in one tool is an error result rather
// than the end of the server process.
func safe(fn server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx contextT, req mcpsdk.CallToolRequest) (result *mcpsdk.CallToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "alidade: panic in %s: %v\n", req.Params.Name, r)
				result, err = mcpsdk.NewToolResultErrorf("internal error: %v", r), nil
			}
		}()
		note := &callNote{}
		result, err = fn(context.WithValue(ctx, callNoteKey{}, note), req)
		if note.pending != "" {
			// While a layer completes in the background, part of what the
			// answer rests on is INFERRED, and the agent is told so (#269).
			result = withField(result, "pending", note.pending)
		}
		if note.rebuilt != nil {
			result = withRebuilt(result, note.rebuilt)
		}
		if note.root != "" {
			result = withField(result, "root", note.root)
		}
		return result, err
	}
}
