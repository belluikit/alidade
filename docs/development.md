# Developing alidade

[CONTRIBUTING.md](../CONTRIBUTING.md) covers building and what a pull
request needs. This page describes the checks that stand between a fixture
and a consumer, and how releases are gated.

## Where things live

| package | what it holds |
|---|---|
| `internal/graph` | the core: schema, identity, merge, PXF output |
| `internal/adapter/golang` | the Go layer, over caliper |
| `internal/adapter/proto` | the `.proto` layer, over protocompile, with the `.proto` to `.pb.go` link |
| `internal/adapter/sql` | tables, columns and sqlc queries, with the `.sql` to generated Go link |
| `internal/adapter/pxf` | documents, blocks, fields and chain layers, with the `@type` to `.proto` link |
| `internal/adapter/markdown` | sections, fences, links, and doc to symbol edges from code spans |
| `internal/adapter/goasm` | templates, with the template to generated emitter link |
| `internal/adapter/ts` | files and imports over esbuild's resolver; declarations and a type-resolved call graph over TypeScript 7's compiler; protoc-gen-es exports; the `.proto` to generated TypeScript to screen chain |
| `internal/adapter/py`, `internal/python` | Python files, declarations, imports and calls, resolved by jedi in an embedded CPython |
| `internal/mcp` | the MCP surface |
| `internal/oracle`, `internal/bench` | the oracles and the agent benchmark |

All six layers of [ADR 003](adr/003-corpus-scope.md) and the frontend
layer of [ADR 007](adr/007-frontend-layer.md) are in.

## Everyday checks

`go test ./...` runs the fixture tests. The complexity gate is
`scripts/complexity.sh`. CI runs both, with `go vet` and golangci-lint, on
GitHub-hosted runners (`macos-26` and `ubuntu-24.04`): the whole suite on
macOS arm64 and on Linux amd64, with and without the `v8` and `python`
tags.

Both gates fail when they measured nothing, so that silence is never read
as a pass. The complexity gate exits 2 when a domain path is missing, a
package directory is in no domain, or gocyclo itself errs.

## Checks beyond the fixtures

| check | what it catches |
|---|---|
| `TestSurfacesAgree` (`internal/cli`) | a field of the build report missing from `graph_stats` or from the PXF `build` block |
| `scripts/corpus.sh` | a graph that disagrees with a real tree |
| `scripts/oracle.sh` | a graph that disagrees with an independent source of truth |
| `scripts/pytrace.sh` | Python calls the graph misses, against the calls a test suite really makes |
| `scripts/mcp-e2e.sh` | what an agent actually receives from the release binary |

### `TestSurfacesAgree`

Fails when a field of the build report is missing from `graph_stats` or
from the PXF `build` block, or has no key at all. A build-report field
lands on both surfaces or on neither.

### `scripts/corpus.sh`

Runs `TestCorpus` over the repositories checked out beside this one, every
one that is there, with tests on. It checks what a fixture cannot: that the
graph agrees with the tree.

- Test markers follow `_test.go` exactly.
- Every positioned node names a file in the tree.
- A dropped edge never points at a Go package that is on disk and unloaded.
- A Markdown link never drops while its target is on disk and holds Go.

Each check is a class of miss a consumer found on a real repository first
(#67, #68, #81). Run it before a release, and add a check when the next
class appears. It fails when no repository is there to check, or when none
of them builds.

### `scripts/oracle.sh`

Measures the graph against an independent source of truth, not against
itself (#148). It prints the numbers, and fails only when it measured
nothing.

| what | measured against |
|---|---|
| Go calls | the type checker's view of every call expression. Recall of the calls it names (static, through an interface, leaving the module), and each of the graph's `calls` edges classed as confirmed, interface dispatch, function value, or unexplained |
| Go uses of package-level constants and variables | the type checker's own record of what each identifier names, over a load of its own |
| Go imports | `go list` |
| SQL | sqlc's own catalog and queries: its json generator, run on a scratch copy of each configuration, never in the repository |
| Proto declarations, imports and references | protoc, Google's compiler, which shares no code with protocompile |
| Proto to generated Go types | the type tables protoc-gen-go writes into each `.pb.go` |
| Frontend imports | TypeScript's own module resolution, from the `typescript` package the repository installs |
| Frontend calls | that same package's type checker (TypeScript 5 or 6 under Node, where the layer runs TypeScript 7's Go compiler), and a second writing of the layer's declaration rules, edge by edge |
| Generated TypeScript exports | the descriptor protoc-gen-es embeds in each `_pb.ts` |
| goasm templates | the generator each `//go:generate` line pins, run on one template at a time |
| PXF type references and `${path}` references | a reader written from the protowire specification over the raw text, each name confirmed by protoc |
| PXF chain links | a census judged by reading (`internal/oracle/testdata/chain-census.pxf`), since only chameleon's own chain package expands them |
| Markdown `documents` edges | hand-labelled samples pinned to each repository's commit, since only a reader can judge them |

Notes on the frontend imports: a `.svelte` component is resolved through
TypeScript's arbitrary-extension rule, a Node builtin through Node's own
list, and a SvelteKit or Astro virtual module through the type checker's
ambient modules.

The Markdown samples are two: one drawn from the edges, for precision
(`internal/oracle/testdata/markdown-sample.pxf`), and one drawn from the
code spans of the docs, for recall (`markdown-recall-sample.pxf`).

Not checked: a PXF document's structure (its blocks and fields), since
protowire-go is the only PXF parser.

### `scripts/pytrace.sh`

The Python layer's oracle is execution ([ADR 008](adr/008-python-engine.md)).
`scripts/pytrace.sh ROOT OUT command [args...]` runs a command, usually a
repository's test suite, and records every call a Python process makes
inside the repository. The graph's `calls` edges are then measured against
the calls that actually happened.

### `scripts/mcp-e2e.sh`

Checks what an agent receives (#174): the binary built as the release
builds it, serving real repositories over stdio.

- Every listed tool and parameter description is checked against the
  source. Not against a server, which the same compiler would make agree
  when both are wrong (#160).
- Every tool's answer must parse and keep to its budget.
- A sample of declarations' callers must match the graph.

`ALIDADE_E2E_BIN` and `ALIDADE_E2E_REF` check a release as shipped, against
the source it was built from.

## The agent benchmark

`internal/bench` asks questions of headless Claude Code sessions, one per
question and arm, and scores each answer against the names it must
contain. An arm is a set of tools: alidade and Read, grep alone, or
graphify in two setups. It spends API money, capped per session, so it
runs only when asked.

- **Question sets** are in `internal/bench/testdata`. The `gen-*.pxf` sets
  are drawn by rule, so nobody picks the questions:
  `TestGenerateSet` draws a Go repository's from the type checker's calls,
  and `scripts/pybenchgen.py` a Python repository's from the calls its
  test suite made under `scripts/pytrace.sh`.
- **Running it:** `scripts/bench.sh`, with the repository checked out at
  the commit its set names. `internal/bench/live_test.go` lists the
  settings, among them the arms and the two graphify needs.
- **Reading it:** `TestReport` folds results files into one line per arm
  and kind of question, with cost per correct answer and response time.

The comparison in the README is
[#3](https://github.com/trendvidia/alidade/issues/3), which says how each
arm was set up.

## Releases

The release workflow builds nothing for a tag that is not on main, or whose
CI (`vet + build + test` and `golangci-lint`) did not pass on that commit.
Its gate job (`scripts/release-gate.sh`) checks both against GitHub, and
the build checks out the exact commit the gate passed. A dispatch with
`dry_run` runs the gate alone, for any ref.

The release builds the Linux arm64 and Darwin amd64 binaries by
cross-compilation. The release job tests the Darwin amd64 one under Rosetta
where the runner has it. No job runs the Linux arm64 one before it ships.

Each release archive carries `THIRD_PARTY_NOTICES.txt` beside the binary.
`go run scripts/notices.go` regenerates it when a dependency moves, and CI
fails when it is out of date.
