# Using alidade

alidade is an MCP server over stdio. This page covers connecting it to an
agent, its command-line flags, how the graph stays current, and what each
tool answers.

- [Connecting Claude Code](#connecting-claude-code)
- [Using alidade instead of grep](#using-alidade-instead-of-grep)
- [Any MCP client](#any-mcp-client)
- [Flags](#flags)
- [How the graph stays current](#how-the-graph-stays-current)
- [The Go toolchain](#the-go-toolchain)
- [Leaving directories out](#leaving-directories-out)
- [The tools](#the-tools)
- [What the edges mean](#what-the-edges-mean)
- [Limits](#limits)

## Connecting Claude Code

Once:

```
alidade setup
```

That runs `claude mcp add --scope user alidade -- alidade serve -root .`.
It replaces a registration that is already there, so running it again
after an upgrade is fine. The registration lives in `~/.claude.json`, and
`claude mcp get alidade` shows it.

Claude Code starts a stdio server in the project directory, so one
user-scope registration serves every repository you open.

| option | effect |
|---|---|
| `-scope project` | writes `.mcp.json` in the current repository instead |
| `-print` | shows the configuration without registering it |
| `-schema-root DIR` | registers a repository whose generated code comes from schemas kept in another repository; implies project scope |

```
alidade setup -schema-root ../schemas
```

Every tool asks Claude Code to load its schema up front
(`anthropic/alwaysLoad`), not defer it behind tool search. Deferred, no
session in our benchmark ever reached for alidade (#156). That costs about
2,300 tokens of context per request.

## Using alidade instead of grep

`alidade setup -enforce` also adds a hook to `~/.claude/settings.json`
(#208). In the git repository a session runs in, these are refused, and the
model is told what to use instead:

- the Grep tool;
- Bash's `grep`, `egrep`, `fgrep`, `rg`, `ag`, `ack` and `git grep`
  (`git -C <dir> grep` and `git --work-tree <dir> grep` included).

Every worktree of the repository counts as the repository: a search in
another `git worktree` of it is refused too, and the model is told to
pass that worktree as `root`, to `search_text` and to the graph tools.

The model is pointed to `search_text` for text, and to `find_nodes`,
`neighbors` and `shortest_path` for code. Measured, an agent with alidade
and no grep answered every benchmark question correctly (#206).

A search still runs as before when it is:

- outside the repository;
- under a directory `search_text` leaves out (`testdata`, `vendor`,
  `third_party`, `node_modules`, a virtual environment);
- of a command's output through a pipe.

Glob is untouched.

| to | do this |
|---|---|
| see what agents still reach grep for | read `~/.local/state/alidade/hook.log`, where every refused call is appended |
| let grep through for one session | start it with `ALIDADE_ALLOW_GREP=1` in its environment (`ALIDADE_ALLOW_GREP=1 claude`). Set inside a command it is not honoured, and the refusal says so |
| remove the hook, and nothing else | `alidade setup -no-enforce` |
| see the entries without writing them | `alidade setup -enforce -print` |

The rest of the settings file is kept as it was, with a copy of it saved as
`settings.json.bak-alidade`.

## Any MCP client

The equivalent configuration, which `alidade setup -print` shows:

```json
{
  "mcpServers": {
    "alidade": {
      "command": "alidade",
      "args": ["serve", "-root", "/path/to/repo"]
    }
  }
}
```

## Flags

For `alidade serve` and `alidade graph`:

| flag | what it does |
|---|---|
| `-root DIR` | the repository. Default `.` |
| `-schema-root DIR` | a repository whose `.proto` files this one's generated code comes from. Its messages join the graph, and its files are labelled `@<dirname>/` ([ADR 002](adr/002-node-identity.md) rule 12). Repeatable |
| `-go-deps-source` | type-check Go dependencies from source. The default takes their types from export data: the same module graph, 3 to 4 times cheaper, and a boundary with no class-hierarchy fan-out into dependencies |
| `-go-configs auto` | also load the Go build configurations that bring in files the host's constraints exclude. Or give a `GOOS/GOARCH` list |
| `-tests` | include `_test.go` files |
| `-tests-exact` | with `-tests`, make the production graph exactly the no-tests build's, at the cost of a second load |

**Tests.** With `-tests`, each declaration from a `_test.go` file carries
`test = true`, and the core derives the same on every edge touching it. So
`exclude_tests` on the MCP tools gives the production graph, and
`graph_stats` says `go_test_nodes`.

Without `-tests-exact`, a production interface call can keep package-level
edges into packages only tests bring in. With it, the production graph's Go
nodes and Go-to-Go edges are exactly the no-tests build's. One cross-layer
gap remains (#139).

**Generated code** is always in the graph, because it is what joins a
schema to the code that uses it ([ADR 004](adr/004-generated-code.md)).
`exclude_generated` on the MCP tools keeps it out of an answer while a
traversal still crosses it.

**`alidade graph -root DIR`** writes the whole graph to stdout as PXF. A
`build` block after `stats` says what `graph_stats` would, under the same
keys:

- the layers that ran;
- the Go build configuration;
- the Go modules and the production edges between them
  (`go_module_edges`), and with tests loaded, the test edges apart
  (`go_test_module_edges`);
- the build time.

## How the graph stays current

The graph is built on the first tool call and held for the session. That
takes a few seconds for a repository of a few hundred packages.

Before every tool call, the server checks whether any file's size or
modification time changed since the build (3 to 20 ms on our
repositories). If one did, it rebuilds first, and only the layers whose
files' contents changed rerun (1 to 6 s after editing a Go file). So an
answer is always about the files as they are on disk, after an edit or a
checkout alike (#204). An answer that rebuilt says so in `rebuilt`, with
the layers it reran.

**The graph is never persisted.** One thing is kept between sessions:
jedi's answers for each Python file, in the user's cache directory
(`alidade/py/`), with the hashes of every file jedi read for them (#279).
A session reuses an answer only when none of those files changed, so a kept
answer is one the tree as it is now would give. The file can be deleted at
any time.

**Python answers arrive in the background.** While jedi is still
inferring, an answer carries `pending`, saying how many Python files it has
resolved. The calls from the rest are marked INFERRED until jedi reaches
them (#269).

## The Go toolchain

The binary has no runtime dependencies. It needs a Go toolchain on the
machine only to analyse Go: the Go layer runs the `go` command on PATH and
loads packages the way `go build` does. A repository that builds is a
repository alidade can read.

- **Versions.** A release binary is built with stock Go (go1.27.2 today;
  `go version /path/to/alidade` shows it). A `go` from a newer release than
  that is refused, with an error naming both versions, so that it is never
  analysed wrongly. Keep alidade at or above your toolchain, or
  [build it from source](building.md).
- **Which build this is.** `alidade version` gives the module version, the
  commit and the Go it was built with, for a release or a local build
  alike.
- **Several modules.** Every `go.mod` under the root is a module of the
  graph. Each is loaded from its own directory, as `go build` would with no
  `go.work`, and an edge from one module into another resolves to the
  declaration it names.
- **A module that does not build** keeps a package node carrying the
  error. `graph_stats` lists `go_modules` and `go_modules_failed`.

## Leaving directories out

`vendor` and `third_party` directories are left out by default. A
repository that wants another list writes it in `.alidade.pxf` at its root,
and that list replaces the default. A bare name is skipped anywhere; a path
is taken from the root.

```
skip = ["vendor", "third_party", "web/legacy"]
```

## The tools

| tool | for |
|---|---|
| `graph_stats` | what the graph holds, by layer and by derivation, and which Go build configurations it was loaded for |
| `find_nodes` | an ID from a name: `Total`, `Workspace.ReferencesFor`, `app.users`, `chain.pxf` |
| `get_node` | one node with its position and its edge counts |
| `neighbors` | what is within N hops of a node, in, out or both |
| `shortest_path` | how two nodes connect |
| `subgraph_for_question` | the neighbourhood of what a question names, across layers |
| `graph_issues` | what the graph left out (dropped edges, folded by cause) or had to decide (conflicts) |
| `search_text` | text the graph does not hold: a string literal, a log message, a comment. Each hit comes with the declaration holding it, as a node for the tools above. `path` and `exclude` to narrow it by path prefix, `files_only` (a cut first page also counts hits per directory below `path`), `exclude_tests`, and `after` to continue a cut answer. `root` searches another `git worktree` of the repository and `ref` a commit's files; a commit's hits name no declaration, and a worktree's do when its graph is built (see below) |
| `rebuild` | build again now. Rarely needed, since every tool rebuilds the changed layers first |

**Another worktree.** Every graph tool takes `root`: another `git
worktree` of the repository, by its top directory. The answer is that
worktree's graph and says so under `root`. It is built on first use from
the session's graph: a layer whose files are the same is reused, and the
Go layer loads again only the packages whose files differ. Calls through
an interface or a function value from those packages depend on code that
was not loaded again; they are kept from the session's graph, marked
INFERRED and `carried=base`, and may be stale. A package the worktree
did not change is as the session's build saw it. `graph_stats` with
`root` lists what was reused under `overlay`. The server holds four
worktrees' graphs at most, and lets the one used longest ago go.

`search_text` with `root` never waits for that graph. Its hits name their
declarations when a graph tool given the same `root` has built the
worktree's graph and the worktree's files have not changed since;
otherwise they name none, and `note` says why.

**The usual shape of a question:** `subgraph_for_question` with the names
involved. If the answer is too big for one call, `find_nodes` for the exact
node, then `neighbors` or `shortest_path` from it.

**Token budgets.** The tools that return a list (`find_nodes`, `neighbors`,
`shortest_path`, `subgraph_for_question` and `graph_issues`) take
`max_tokens` and keep to it. They say when they were cut and exactly what
they left out. `get_node` and `graph_stats` return one fixed-size answer.

**Filters that match nothing are errors.** A filter naming an edge kind,
node kind or scheme the graph does not hold is an error that lists the ones
it does, never an empty answer.

## What the edges mean

**How an edge was derived.** Every edge carries its derivation, and
`exclude_inferred` asks for the high-confidence graph alone.

| derivation | meaning |
|---|---|
| `EXTRACTED` | stated in the source |
| `RESOLVED` | worked out by a type checker |
| `DECLARED` | given by a generator's or build tool's rule: schema to generated code, chain to layer |
| `INFERRED` | a heuristic |

**`references` in Go** joins:

- a declaration to each type its signature or fields name;
- a function or method to each package-level constant or variable of the
  repository it names in its body.

So "what reads `caliperBinaryEnv`" is `neighbors` in. Uses in a
package-level initializer, and of a dependency's constants and variables,
are not edges.

**`documents`** joins a doc to a declaration. The edge comes from a code
span naming exactly one declaration. A schema field and the `.pxf` entries
that set it count as one (#190). A span naming one of several same-named
declarations gets no edge: `SettleMarket` on two types, say, or a proto
message and its generated Go type (#190). `search_text` finds every doc
that writes the name, and the reader tells which declaration it means.

**Struct fields** are nodes (`go://pkg#Type.field`), contained by their
type and referencing the types they hold. The functions that read or write
a field are not edges (#195: on the benchmark's field questions a text
search answered every session correctly). So "where is this field set" is a
`search_text` question.

**Generated code.** `exclude_generated` leaves generated code out of
`find_nodes` and a question's seeds. It shows a generated node a traversal
crosses as its id and kind only, so a path from a table or a message to the
code that uses it survives.

## Limits

**A file whose path no node ID can carry is left out.** An ID may not
contain whitespace, `#` or an empty path segment. So `docs/My Notes.md`,
`api/my api.proto` or `web/my page.ts` gets no node, and links or imports
pointing at it get no edge. The build still finishes.

- Each such file is listed under `skipped` in `graph_stats`, with its layer
  and reason. The list is capped there, with `skipped_omitted` counting any
  beyond the cap, and is in full in the `build` block.
- The same goes for a link, `${…}` reference or SQL name that no ID can
  carry, and for an entry the walk cannot read.
- Files of formats keyed by name, not by path, are not affected: a SQL
  table under `q dir/`, for example. Their node's position may hold a space
  (#92).
