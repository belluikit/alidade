# ADR 000: Composition Boundary — alidade composes, it does not analyse

**Status:** Accepted
**Date:** 2026-08-29
**Relates to:** [001](001-core-architecture.md), [003](003-corpus-scope.md)

## Context

alidade maps a repository as a whole: Go code with real types, plus the schema
and document layers no single tool can see. Four of the five analyses it needs
already exist, and four of those five parsers are in-house:

| layer | provided by | in-house |
|---|---|---|
| Go, type-resolved | `caliper/pkg/analysis` | yes |
| `.proto` | `trendvidia/protocompile` | yes |
| `.pxf` | `trendvidia/protowire-go/encoding/pxf` | yes |
| SQL / sqlc | `sqlc.yaml` + migrations; parser deferred | partly |
| Markdown | goldmark | no |

The obvious alternative — teach one existing tool to read all five — was
considered first, and caliper's own governance rejects it in writing:

> `mission_drift_gate = true` … "No scope creep into non-Go languages or
> general-purpose scripting."

That gate would have blocked `.proto`, `.pxf` and Markdown extraction, and it
would have been right to. Caliper's output model is findings
(`{loc, sev, msg, fix, count}`); a graph's is nodes and edges. They are two
products, and one repository holding both serves neither.

`trendvidia/protolsp` is the precedent that composition works: protocompile +
protowire-go + protoregistry become an embeddable `analysis.Workspace` behind an
LSP frontend. alidade is that architecture with a graph frontend.

## Decision

**alidade owns the graph, the identity scheme and the agent-facing surface. It
owns no parser and no type checker.** Every layer is a thin adapter over a
library that already performs the analysis.

Three consequences, which are the reason this is written down:

1. **The dependency direction is one-way and permanent.** alidade imports
   caliper. Caliper never imports alidade and never learns it exists. Anything
   alidade needs from a layer is an upstream change justified on that layer's own
   terms — as `caliper#12` was: it exposed `EngineOptions.Tests` and
   generated-code identification because a *library consumer* needed them, which
   is a reasonable thing for caliper to offer regardless of who asked.

2. **Library boundaries, never wire contracts.** Layers are consumed in-process
   as typed Go APIs versioned by `go.mod`. This is the whole reason the split is
   cheap: a Go API boundary is refactorable under compiler checking, while a
   serialized contract between two processes costs a migration to change. A layer
   reachable only over a wire is an exception that needs its own ADR.

3. **A new format is a new adapter, never a new parser.** If no library exists
   for a format alidade should read, the options are to write that parser in a
   repo that owns the format, or not to support the format. Writing it here is
   the drift that turns alidade into a second caliper.

## What we are accepting

The split is not free, and the cost should not be rediscovered as a surprise.
Standing up a separate repository means building an MCP surface and scaffolding
that came free inside caliper — roughly 300–500 LOC and 2–4 engineer-days,
counted explicitly in the plan. Version coordination across repositories
replaces a single `go build`.

Both are worth paying to keep two tools coherent rather than one tool confused.

## Alternatives considered

**Grow caliper.** Rejected above on its own governance. Also rejected on output
model: adding a graph to a findings engine means two schemas, two caches and two
notions of "a thing" in one binary.

**Fork caliper.** Rejected outright. Two copies of an 8,979-LOC type-checked
analysis engine is the worst of both — alidade would inherit the maintenance and
still diverge.

**Depend on nothing; write a tree-sitter pass per language.** This is what the
tool alidade replaces does, and it is measurably weaker: name-based Go resolution
drops every method call through a receiver and emits zero `implements` edges.
Measured on protolsp, 3,636 real internal call edges are absent from such a
graph. Type resolution is not an optimisation here, it is the product.

## When to reopen this

* **A layer's upstream stops being maintainable in place** — if caliper were
  archived, or protocompile's parser became unavailable, the adapter model has no
  substrate and vendoring becomes the lesser evil.
* **Cross-repo version coordination becomes the dominant cost** — if most alidade
  PRs start needing a paired upstream PR, the boundary is in the wrong place.
* **A format matters that no library covers and no repo wants to own.** Then the
  choice is genuinely between writing a parser here and dropping the format, and
  this ADR should be amended rather than quietly violated.

## Amended 2026-10-08: one analysis of alidade's own, text search (#178, the owner's decision)

**alidade performs one analysis of its own: a search of the working tree's
text, served by the `search_text` tool.** The graph holds declarations and how
they relate, not text, so a question about a string literal, a log message, a
comment or prose had no answer in it, and an agent went to grep: a search, then
a Read to find the function around the hit. `search_text` answers it in one call
and names the declaration holding each hit as a node of the graph.

It is bounded, and each bound is the reason the rest of this ADR still holds:

1. **It stays outside the graph.** A search adds no node and no edge; the
   graph is still built only from the layers' libraries. A hit's `in` names a
   node the graph already has, or nothing.
2. **It is not a parser of any format.** It matches bytes — a literal, or a Go
   regular expression — over the files the layers read. Placing a hit in its Go
   declaration uses the standard library's `go/parser` for the declaration's
   lines alone; every other file's hit is placed by position, and says so
   (`guess`).
3. **It is the only one.** A second analysis of alidade's own is a second
   amendment, argued on its own measurements, not an extension of this one.

What it rests on, measured on steward with the benchmark (#149), alidade with
`search_text` and no Grep against Grep, the same model, each pair in the same
window: ten text questions, correct as often as Grep (23 of 23 in the
same-window runs, 9/9 and 14/14), at 22% and 33% less per correct answer, with
fewer than half the calls. The first version failed the two largest questions —
test files filled the answer, a cut answer could not be continued — and the
shipped one leaves tests out on request, lists files only, and continues a cut
answer (`after`/`next`).

`TestSearchStaysOutsideTheGraph` fails the day a search changes the graph or a
hit names a node the graph does not hold.

## Amended 2026-10-16: the same search, of another worktree or a commit (#23, the owner's decision)

The search above read the files the layers read, and nothing else. An agent
whose work happens in a `git worktree` the server does not index, or that needs
what a commit held, went back to grep for both (#23). `search_text` now also
reads another worktree of the repository (`root`) and a commit's files (`ref`).
That widens what it searches, not what it is, and each bound still holds:

1. **Outside the graph.** The graph is still the session's checkout's. A hit in
   another worktree or a commit names no declaration: its line numbers belong
   to another tree, and placing them in this graph's declarations would give a
   wrong answer that looks right. The graph is not built to answer them.
2. **No parser.** It still matches bytes. A commit's files come from git itself
   (`ls-tree`, `cat-file --batch`), an existing tool, and a worktree is
   recognised from the files git writes (`.git`, `commondir`).
3. **Still the only one.** It is the same analysis over another set of files.

`TestSearchTextInAWorktree` and `TestSearchTextAtACommit` fail the day such a
hit names a declaration, or the graph is built to answer one.

## Amended 2026-10-08: a library's internal packages, through a shim (#215, the owner's decision)

typescript-go, TypeScript 7's compiler, is consumed in process as a typed Go
library, which is what rule 2 asks — but it publishes no API: every package is
under `internal/`. alidade reaches them through a shim module named under
typescript-go's own path (ADR 007's amendment), with typescript-go a submodule
pinned to a release tag. That is still a library boundary, refactorable under
the compiler: a release that moves what the shim names fails the build, not a
wire. What it is not is a stable one, so an upgrade of the submodule is a
change of its own, with the oracle rerun; and the module, carrying `replace`
directives, cannot be `go install`ed by version. The rule stands — no parser
and no type checker of alidade's own — and this is the first upstream reached
through its internals; a second is an amendment of its own.

## Amended 2026-10-09: a JavaScript library, in an embedded engine, through cgo (#221, the owner's decision)

svelte2tsx, Svelte's own translator of a component into TypeScript, is a
JavaScript library. alidade runs it in process, in V8 embedded through cgo
(tommie/v8go), from a bundle committed with the versions it pins. That keeps
rule 2: a library in process, no wire and no Node. But two things are new, and
this records them:

- **alidade is a cgo binary.** The release links V8 (about 55 MB of the
  binary), builds each target with a C++ toolchain (clang for darwin, zig for
  linux), and every build of it needs `CGO_CXXFLAGS=-nostdinc++`, which V8's
  own libc++ requires and a `#cgo` directive may not set. The `v8` build tag
  keeps a build without a C++ toolchain possible: it translates no component
  and says so.
- **An analysis runs as JavaScript.** It is still the upstream's own library
  (svelte2tsx), not a parser of alidade's, so rule 3 holds. A second embedded
  JavaScript library is an amendment of its own, not an extension of this one.
