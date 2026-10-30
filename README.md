# alidade

[![CI](https://github.com/belluikit/alidade/actions/workflows/ci.yml/badge.svg)](https://github.com/belluikit/alidade/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/belluikit/alidade)](https://github.com/belluikit/alidade/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A code graph of your whole repository, for AI agents, served over MCP.**

alidade maps a repository as one graph: code with real types, plus the
schema and document layers no single tool sees. An agent asks it who calls
a function, what implements an interface, or which code uses a database
column, and gets an answer a text search cannot give.

An alidade is the sighting rule on a plane table. You take bearings on
several landmarks, and combining them is what produces the map. That is the
architecture: alidade performs no analysis of its own. It composes analyses
that already exist, and owns the graph they feed.

## What it reads

| layer | read by |
|---|---|
| Go, type-resolved | [caliper](https://github.com/trendvidia/caliper), over the Go type checker |
| TypeScript, JavaScript, Svelte | TypeScript 7's compiler ([typescript-go](https://github.com/microsoft/typescript-go), in process) for declarations and calls; esbuild's resolver for imports |
| Python | jedi, in a CPython linked into the binary ([pylib](https://github.com/trendvidia/pylib)) |
| `.proto` | [protocompile](https://github.com/trendvidia/protocompile) |
| SQL (PostgreSQL, sqlc) | `sqlc.yaml` and the migrations |
| `.pxf` | [protowire-go](https://github.com/trendvidia/protowire-go) |
| Markdown | goldmark |
| goasm templates | [goasm](https://github.com/trendvidia/goasm) |

Generated code joins the layers: a `.proto` message to its Go and
TypeScript types, a SQL query to its generated Go function.

## Why alidade

### One question can cross layers

A schema, the code generated from it, the code that uses that, and the
documents that describe it are one graph. In
[chameleon](https://github.com/trendvidia/chameleon), "what depends on the
`ChainSpec` message?" has five kinds of answer, and alidade returns them
together:

| what | where | how alidade knows |
|---|---|---|
| the message | `proto/chameleon/v1/chain.proto` | read from the `.proto` |
| configuration typed as it | `examples/simple/chain.pxf`, `examples/buf/chain.pxf` | each file's `@type` |
| its generated Go type | `proto/chameleon/v1#ChainSpec` | protoc-gen-go's own rule |
| Go code using that type | `chain#toChainSpec`, `chain#expandLayers`, `chain#readEnvironments`, `chain#Options.normalizeContext`, `bundler#planLayers`, `bundler#unionRecipients` | the Go type checker |
| documents naming it | README "Chain spec", a CHANGELOG entry | a code span naming one declaration |

A text search finds the lines that spell `ChainSpec`. It does not tell the
generated type from its uses, or follow the message into the code that only
ever names the Go type.

### The graph comes from compilers, not from a model

Go is read by the Go type checker, TypeScript by TypeScript's own compiler,
`.proto` by a protobuf compiler, Python by jedi. No model reads your code
to build the graph.

- Building it costs no tokens.
- Two builds of the same tree are byte-identical. A test fails otherwise.

### It is checked against independent ground truth

Each layer is measured against a source that shares no code with it:

| layer | measured against | result |
|---|---|---|
| Go calls | the type checker's view of every call expression | 56,071 of 56,071 found, over 15 repositories; every extra edge explained |
| Go imports | `go list` | 5,667: none missing, none extra |
| SQL | sqlc's own catalog | 75 of 75 tables, 351 of 351 queries |
| Python calls | calls recorded while two projects' test suites ran | 10,234 of 10,612, and 1,002 of 1,192 |

Python is the layer where an answer can be incomplete, and those are its
numbers. The repositories are in-house ones and two open-source Python
projects. The scripts (`scripts/oracle.sh`, `scripts/pytrace.sh`) run on
any repository: see [Developing alidade](docs/development.md).

### An answer is about the files as they are now

Before every tool call, the server checks whether a file changed, and
rebuilds what did. There is no index to refresh after an edit or a
checkout, and none to go stale.

### It says what it does not know

- Every edge states how it was derived, and `exclude_inferred` drops the
  heuristic ones.
- An answer cut to its token budget says what it left out.
- `graph_issues` lists the files that did not compile and the edges the
  graph dropped, and why.
- `graph_stats` lists the files it could not read.
- A Python call jedi has not reached yet is marked as a guess until it has.

### Python at the size of a real project

Measured on a project of 1,644 Python files, where type inference over
everything takes about 16 minutes:

| moment | what you wait |
|---|---|
| first answer of a first session | about 25 seconds, with unresolved calls marked as guesses while inference continues behind |
| after a commit that edits files | under 7 seconds until everything is resolved again: only the files the edit can affect are inferred again |
| a new session, nothing changed | under 2 seconds: it starts from the answers the last session kept |

### One binary, and nothing leaves the machine

No Node, no Python and no database to install: the compilers and
interpreters it needs are linked in. It talks to the agent that started it
over stdio, and opens no port.

## Installing

With Homebrew:

```
brew install belluikit/tap/alidade
```

Or download a binary for macOS or Linux, arm64 or amd64, from the
[releases](https://github.com/belluikit/alidade/releases).

To build from source, see [Building alidade](docs/building.md).

## Quick start

Register alidade with Claude Code, once:

```
alidade setup
```

Then open any repository and ask a question about how its code relates.
The graph is built on the first question, in a few seconds for a
repository of a few hundred packages, and kept current as files change.

To make the agent use alidade and not grep in that repository:

```
alidade setup -enforce
```

For any other MCP client, the configuration is one command,
`alidade serve -root /path/to/repo`. [Using alidade](docs/usage.md) has the
details, the flags, and how to turn enforcement off again.

## The tools

| tool | for |
|---|---|
| `subgraph_for_question` | the neighbourhood of what a question names, across layers |
| `find_nodes` | an ID from a name: `Total`, `Workspace.ReferencesFor`, `app.users` |
| `neighbors` | what is within N hops of a node: its callers, its callees, its implementers |
| `shortest_path` | how two nodes connect |
| `get_node` | one node with its position and its edge counts |
| `search_text` | text the graph does not hold, such as a string literal or a log message, each hit with the declaration holding it |
| `graph_stats` | what the graph holds, by layer and by how each edge was derived |
| `graph_issues` | what the graph left out, and why |
| `rebuild` | build again now. Rarely needed |

Every tool that returns a list keeps to a token budget, and says what it
cut. [Using alidade](docs/usage.md#the-tools) explains each one and what
the edges mean.

## Measured against grep and graphify

47 questions about who calls what, over four public repositories, each
asked three times of the same model. Nobody picked the questions: a rule
draws them from the Go type checker's calls, and from the calls recorded
while a Python test suite ran
([#3](https://github.com/belluikit/alidade/issues/3)).

| an agent with | correct | cost per correct answer | median time |
|---|---|---|---|
| alidade and Read, no grep | 138 of 141 | $0.019 | 6.9 s |
| Read and Grep | 137 of 141 | $0.036 | 7.5 s |
| [graphify](https://github.com/Graphify-Labs/graphify) as its installer sets it up, with Read and Grep | 137 of 141 | $0.038 | 8.9 s |
| graphify's MCP tools and Read, no grep | 108 of 141 | $0.065 | 7.6 s |

**Accuracy is a tie** between alidade, grep, and graphify as installed.

**alidade costs about half as much per correct answer.** The gap is widest
for the callers of a method whose name several types share, where a text
search returns every one of them:

| 12 such questions, three times each | correct | cost per correct answer |
|---|---|---|
| alidade | 33 of 36 | $0.015 |
| grep | 32 of 36 | $0.054 |

**Where it makes no difference:** a function named uniquely enough that
grep's matches are its uses. All three practical setups answered those 60
of 60, within a cent of each other.

### What this does not measure

- **Only call-graph questions.** There are no text, documentation or
  schema-to-code questions, because no oracle generates those yet.
- **Nothing graphify does beyond calls.** graphify reads dozens of
  languages through tree-sitter where alidade reads four through their
  compilers, and it adds documents, images and community structure that
  alidade has no counterpart for. The fourth row is its graph tools with
  no text search beside them, which is not how it is meant to be run.
- **Python answers are the calls a test suite made.** A call no test made
  is in no answer.
- **One model, one harness, ours.** graphify publishes
  [its own benchmark](https://github.com/Graphify-Labs/graphify/blob/main/BENCHMARKS.md);
  its code questions are not published, so we could not run them. If we
  have set graphify up wrongly, please open an issue.

The question sets are in `internal/bench/testdata/gen-*.pxf`, and
`scripts/bench.sh` runs them: see [Developing alidade](docs/development.md).

## Status

Early. The architecture is settled and recorded in the decisions below, and
every layer in the table above is in. Open work is in the
[issues](https://github.com/belluikit/alidade/issues).

A number such as #206 in the code, the ADRs or the changelog, written
before v0.12.0, refers to the tracker alidade had before it was published.
That tracker is not public. The changelog and the ADRs carry the
measurements themselves.

One limit to know: a file whose path holds whitespace or `#` gets no node,
and is listed as skipped. See [Limits](docs/usage.md#limits).

## Documentation

| | |
|---|---|
| [Using alidade](docs/usage.md) | connecting an agent, flags, how the graph stays current, the tools, what the edges mean, limits |
| [Building alidade](docs/building.md) | building from source, the build tags, compilers |
| [Developing alidade](docs/development.md) | the checks beyond the fixtures, the oracles, releases |
| [CONTRIBUTING.md](CONTRIBUTING.md) | what a pull request needs |
| [SECURITY.md](SECURITY.md) | reporting a vulnerability, and what alidade does with a repository it reads |
| [CHANGELOG.md](CHANGELOG.md) | what changed, with the measurements |

### Decisions

Read these before contributing. They answer most "why is it like this"
questions, and each carries the measurement it rests on.

| ADR | |
|---|---|
| [000](docs/adr/000-composition-boundary.md) | alidade composes analyses, it does not perform them |
| [001](docs/adr/001-core-architecture.md) | one binary, layer adapters over a shared graph |
| [002](docs/adr/002-node-identity.md) | node identity is declaration-anchored, never positional |
| [003](docs/adr/003-corpus-scope.md) | six formats and Python, and what is deliberately excluded |
| [004](docs/adr/004-generated-code.md) | generated code is in the graph, identified |
| [005](docs/adr/005-on-demand-construction.md) | built on demand; the graph is never persisted |
| [006](docs/adr/006-edge-provenance.md) | every edge states how it was derived |
| [007](docs/adr/007-frontend-layer.md) | TypeScript and Svelte, joined to `.proto` through protoc-gen-es |
| [008](docs/adr/008-python-engine.md) | Python: declarations, imports and calls through jedi in an embedded CPython |

## License

MIT. See [LICENSE](LICENSE). A release binary also carries other projects'
code, each under its own license: [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)
lists them in full.
