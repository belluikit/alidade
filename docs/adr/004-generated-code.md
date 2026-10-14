# ADR 004: Generated Code — In the Graph, Identified, Never Filtered by Default

**Status:** Accepted
**Date:** 2026-08-29
**Relates to:** [002](002-node-identity.md), [003](003-corpus-scope.md)

## Context

Most code-intelligence tools skip generated files. The reasoning is sound for a
linter — nobody edits `.pb.go`, so a finding there is noise — and it is exactly
wrong for alidade.

Generated code is the **bridge** between the layers ADR 003 exists to connect:

```
migration (CREATE TABLE users) → query (-- name: GetUser :one)
    → generated Go (func GetUser, type User) → hand-written caller

.proto (message WorkspaceConfig) → generated Go (type WorkspaceConfig)
    → hand-written caller
```

Remove the generated middle and the chain breaks into disconnected halves. "Which
Go code touches the `users` table" becomes unanswerable — which is the question
that motivated the SQL layer in the first place.

The volume is not marginal. Measured through caliper's call graph, workspace
functions originating in generated files:

| repo | `Tests` | workspace funcs | generated |
|---|---|---|---|
| protolsp | false | 1,560 | **550 (35%)** |
| protolsp | true | 4,057 | 582 (14%) |
| protoregistry | false | 743 | **336 (45%)** |
| protoregistry | true | 1,269 | 356 (28%) |

There is also a trap worth recording, since alidade depends on the tool that has
it. Caliper's `EngineOptions.SkipGenerated` filters **AST traversal only** —
`Engine.Walk` and `Engine.FuncNodes`. The SSA program and call graph are built
from every loaded package and never consult it. So a consumer walking the call
graph receives generated functions whatever it sets, and before `caliper#12` this
was undocumented. alidade would have been built on the assumption that the
default had removed them, and been wrong by 35–45%.

## Decision

**Generated code is in the graph by default, marked as generated, and linked to
its source by a `generates` edge.**

1. **Every node carries a `generated` boolean.** Consumers filter; the graph does
   not decide for them. `InterProceduralContext.IsGeneratedFunc` and
   `Engine.IsGeneratedFile` (added in `caliper#12`) supply this for the Go layer.

   *The opt-out removed, 2026-10-06 (#126, the owner's decision):* `-no-generated`
   is gone, and generated code is always in the graph. Under it the chain from
   hand-written code to its schema lost its middle: `handler → pb.Msg ←
   message Msg` with no `pb.Msg`. That was 732 dropped `generates` edges on
   steward, and nothing connected code to schema. It bought no build time or
   memory, because caliper loads and builds SSA for generated code either way;
   measured on protoregistry, protolsp, steward, govm and gameflow, three runs
   each, wall time within 0–5% and peak memory unchanged. It only shrank the
   graph by 9–39% of its nodes. This rule's "consumers filter" now holds at the
   surface: the MCP tools' `exclude_generated` leaves generated nodes out of
   `find_nodes` and a question's seeds, while a traversal still crosses them,
   showing each as its id and kind. The note below records what the option
   meant while it existed.

   *What `-no-generated` left out, 2026-10-06 (#111, the owner's decision):*
   the Go layer's opt-out gives the graph a build without generated code would,
   the same principle rule 4 states for tests, with no exception. A method
   declared on a generated type leaves with the type even when it is
   hand-written, because without the generated file it would not compile. Every
   pass that emits from or to a declaration (calls, references, implements,
   initializer calls) asks one predicate whether the declaration was left out,
   so none emits an edge to or from a node the build meant to omit. Such an edge
   would read as a miss (ADR 002 rule 3) when it is not one.
   The alternative, keeping a generated type that hand-written code extends,
   shipped briefly in #108 for #95. It made production nodes depend on
   `-tests`: a method in a `_test.go` file kept its type. It also amounted to an
   exception this rule does not have. Measured when deciding: none of steward,
   govm, drover, voya, alidade or caliper declares a hand-written method on a
   generated type, so nothing real loses an edge. If a repository does, the
   narrower keep rule (production methods only, every pass agreeing) is the
   option to revisit.

2. **Generated artifacts are distinct nodes from their sources**, joined by
   `generates` (ADR 002, rule 6 — the same decision seen from the identity
   side).

3. **The graph reports its generated share.** A caller that does not know 45% of
   protoregistry's functions are generated will misread any centrality or
   hotspot measure taken over it.

4. **Test files follow the same principle**, via `EngineOptions.Tests`. Tests are
   roughly half the Go source in some repos and are where intent is documented;
   including them takes protolsp from 1,560 to 4,057 workspace functions. Whether
   they are on by default is an adapter-level parameter, not a graph-level rule.

   *Marked as well, 2026-09-13 (#81):* the same principle means in and marked,
   and until then tests were in and unmarked — a test caller and a production
   caller were told apart by the caller's filename. A declaration from a
   `_test.go` file now carries `Node.Test`, so leaving the marked ones out
   gives the graph a build without tests would; the MCP tools take
   `exclude_tests` for exactly that.

   *Typed, and split by the kind of fact, 2026-09-13:* test-ness lives in two
   places for a reason. On a **node** it is observed — this declaration came
   from a `_test.go` file — which only the adapter that read the source can
   know, so it is a field like `Generated`, set by that adapter and unioned in
   the merge. On an **edge** it is *derived*: `isTest(from) || isTest(to)`,
   computed by `Builder.Build` and never reported by an adapter (see
   [ADR 001](001-core-architecture.md)).

   Making the edge marker an emitted field would have moved the obligation
   rather than removed it: it returns the moment a seventh layer emits an edge
   touching a test declaration, which is exactly how the Markdown layer came to
   emit 68 unmarked ones. It also showed up as nondeterminism — on govm, an
   edge deduplicated per `(from, to)` took the marking of whichever declaration
   the call graph reached first, so the same binary on the same tree produced
   different output run to run. Deriving it makes the wrong state
   unrepresentable rather than merely corrected. And test-ness is not a
   [derivation](006-edge-provenance.md): EXTRACTED, RESOLVED, DECLARED and
   INFERRED say how an edge was arrived at, while this says something about its
   endpoints, which is why holding it in the edge's own observed data forced
   every emit site to go and fetch it.

   *Test packages, 2026-10-06 (#113, the owner's decision):* a package node has
   no file to carry the flag, so the Go layer marks it from its files. Until now
   that was written down only in code. Two shapes are test packages. One is the go
   tool's external test package: named with the `_test` suffix, every file a
   `_test.go` file. The other is a package whose directory holds Go files, all
   of them `_test.go` files, whatever its name: acceptance suites,
   architecture tests, a `hack/` of test helpers. A build without tests has
   no such package, since `go list` without `-test` reports no Go files for it,
   so leaving it out is what this rule's principle asks. The files are
   counted on disk, not in the build, so the answer doesn't change with the
   host: a package whose only production file is `foo_linux.go` is production
   on every platform, and a `//go:build ignore` file counts as production. The
   package is marked with tests loaded or not, because the flag is a fact
   about the package's files, not about the option. Measured when deciding: 6
   such packages across steward, govm, drover, voya, alidade and caliper, all
   test suites. None can be imported by a non-test build.

   What this costs, recorded so it is a decision and not a surprise: a package
   declaring `init` in both a production and a test file is one node (ADR 002
   forbids a positional identity), and it is a production node, so a call only
   the test `init` makes is a production edge. Per-declaration precision is not
   available once several declarations share one node, and a consistent answer
   is worth more than a precise one that flips between runs. *(No longer a cost
   since #128; see below.)*

   *What test files do at package level, 2026-10-06 (#128, the owner's
   decision):* with `-tests`, an import that only a package's in-package test
   files make was an edge from the production package node, so `exclude_tests`
   kept it, and the production view was not the build without tests: 151 such
   edges on steward, 144 on govm. Those files now have a node,
   `go://<pkg>#(test)`, kind `package_tests` (ADR 002 rule 5). It is a test
   node, contained by the package, and positioned at the first test file.
   It carries:
   - **the imports no production file of the package makes.** An import
     both make stays the package's. The production imports of a build with
     tests are now those of a build without them, edge for edge, which `go
     list`'s `Imports`/`TestImports` confirm on steward, govm, drover, voya,
     alidade and gameflow;
   - **the `init` its files declare.** `p#init` is only the production
     inits, so the cost recorded above is gone;
   - **its blank variables' initializer calls.**

   A test package, external or with no production files, needs no such
   node: its package node is a test node already. Edge test-ness is still
   derived from the endpoints, never reported: the edges move to a test
   node, they are not flagged.

   An **external** package node (a package the graph names and did not read)
   has no source an adapter observed. It is marked test in `Builder.Build`
   when every edge into it from its own layer is a test edge. A document
   mentioning it is not code using it. The rule is conservative: a node a
   production edge reaches stays production, so no production edge points
   into a test node. It therefore misses a package only tests link but
   production interface calls fan out into, such as `go://testing` on govm
   (#133).

   *Exact on request, 2026-10-07 (#133, the owner's decision):* loading tests
   widens the analysed program, because dependencies come from export data and
   test imports bring more packages in. A production interface call then gains
   implementations, and with them package-level edges, that the build without
   tests does not have: 9–34 per repository. No rule over import closures
   reproduces that. `go list -deps` counts `crypto/x509` as linked in steward's
   production build, yet no production code of steward touches it, so the
   no-tests graph has no such node. Measured against the no-tests graph, the
   closure rule on its own missed more than the rule above, and the two
   together still missed six packages across govm, drover and voya. So
   `-tests-exact` builds the production view from a load without tests. The
   load with them contributes only its test nodes, the external packages its
   test edges reach (with the flag they were observed with, so this graph's
   own rule decides), and its edges touching a test declaration. Measured on
   govm, voya, drover, steward and gameflow, its production Go nodes and
   Go-to-Go edges equal the no-tests build's exactly. The second load costs
   47–84%, which is why it is opt-in; `graph_stats` says `go_tests: "exact"`.
   One cross-layer gap remains under both modes: a Markdown mention that
   test declarations make ambiguous stops resolving (#139). An edge from
   another layer into an external package marked test is now derived test too.
   Left production, it was a production edge into a test node.

## What we are accepting

The graph is larger and noisier than a hand-written-only view. A naive "most
connected node" query over protoregistry will surface generated sqlc models,
because they genuinely are the most connected things in that repo.

That is a reporting problem, not a modelling one: the `generated` flag makes it
fixable at the surface, whereas exclusion at build time is not recoverable at all.

## Alternatives considered

**Exclude generated code, like a linter does.** Rejected: it deletes the highest-
value edges in the plan and makes two of five layers pointless.

**Include it but hide it behind an opt-in.** Rejected as the same thing with more
steps — a default that hides the schema↔code bridge means the common case is the
broken one.

**Collapse generated nodes into their source.** Considered and rejected in ADR
002 (rule 6): tidier, but "where is the generated Go for this message" stops
being answerable and a schema generating into several languages has nowhere to
put the others.

## When to reopen this

* **The `generated` flag proves insufficient to keep surface output readable** —
  if consumers end up reimplementing the same filter, it belongs in the core.
* **A repo's generated share exceeds hand-written by enough to distort every
  query.** protoregistry at 45% is close; if a repo reaches 80%, default
  behaviour deserves revisiting.
* **Caliper changes `SkipGenerated` to reach the call graph.** That would silently
  remove generated nodes from the Go layer; `caliper#12` pins the current
  behaviour with a test, so the change would surface as a failure there first.
