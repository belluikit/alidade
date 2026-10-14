# ADR 006: Edge Provenance — Every Edge States How It Was Derived

**Status:** Accepted
**Date:** 2026-08-29. Amended 2026-09-10: a fourth category, `DECLARED`, for
edges a build tool's configuration or a generator's documented rule determines
— the reopen trigger below, reached by the `.proto` (#19), SQL (#20) and `.pxf`
(#21) layers. Amended 2026-10-06: rule 7, the call graph per Go call site (#94).
**Relates to:** [001](001-core-architecture.md), [002](002-node-identity.md)

## Context

alidade's edges do not all have the same standing, and the difference is large.

A Go `calls` edge from caliper's SSA call graph is a fact: the type checker
resolved the callee. A `.proto` import edge is a fact: it is written in the file.
A Markdown doc → symbol edge is a **guess**: an ADR mentioning "Config" may or may
not mean the `Config` type, and a naive matcher will attach every document
mentioning a common word to an unrelated declaration.

Presenting both as "an edge" invites a consumer — an agent, most likely — to
reason from the guess as if it were the fact. That is the specific failure this
tool exists to prevent, so the distinction has to be in the data rather than in
documentation.

There is precedent worth borrowing. The tree-sitter tool alidade replaces tags
every edge `EXTRACTED` (explicit in the source) or `INFERRED` (derived by
resolution), and surfaces the tag in query output. That model is sound; what made
its Go graph weak was not the labelling but that name-based resolution produced
`INFERRED` edges where types were needed.

## Decision

**Every edge carries a `derivation` field, and no edge is emitted without one.**

Four values, ordered by how much a consumer should trust them:

| value | meaning | example |
|---|---|---|
| `EXTRACTED` | stated explicitly in the source | `.proto` import, `.pxf` `@type`, sqlc `-- name:` |
| `RESOLVED` | derived by a type checker or compiler | Go `calls` from the SSA call graph, `implements` from structural satisfaction (or, in Python and TypeScript, from a class's resolved base), SQL query → table from the parse |
| `DECLARED` | determined by a build tool's configuration or a generator's documented rule applied to a written option | `.proto` message → `.pb.go` type (go_package + protoc-gen-go naming), table → sqlc struct, chain → layer file per environment |
| `INFERRED` | derived by heuristic; may be wrong | Markdown doc → symbol, name-based association |

Rules:

1. **`RESOLVED` is not a weaker `EXTRACTED`.** It is a separate claim: not
   written down, but computed by something that knows the type system. It is the
   category that distinguishes alidade's Go layer from a name-matching one, and
   collapsing it into `INFERRED` would hide the entire point.

2. **A layer may not emit above its evidence.** The Markdown adapter cannot emit
   `RESOLVED`; the Go adapter emits `RESOLVED` for call-graph edges and
   `EXTRACTED` for imports. An adapter that cannot say which it has must emit
   `INFERRED`.

3. **The surface reports derivation.** A traversal result states each edge's
   derivation, so an agent can weigh a documentation link differently from a
   call-graph edge without asking.

4. **`INFERRED` edges are filterable, and separately counted.** A consumer must
   be able to ask for the high-confidence graph alone. If `INFERRED` edges come
   to dominate an adapter's output, that is a signal the heuristic is too loose —
   visible because they are counted.

5. **Dropping beats guessing.** Where an edge cannot be resolved to a node, it is
   dropped with a recorded reason (ADR 002, rule 3) rather than emitted as
   `INFERRED` against a near match. `INFERRED` is for edges whose *relationship*
   is heuristic, not for edges whose *endpoint* was a guess.

6. **`DECLARED` is exact when the convention holds and absent when it does
   not.** It names a target by a rule — protoc-gen-go's naming, sqlc's, a
   chain's `${env}` expansion — that the graph does not check; what it does
   check is that the target exists. So a `DECLARED` edge is either right or
   dropped, never wrong, and a consumer asking for the high-confidence graph
   (`exclude_inferred`) keeps it. When a layer can call the generator's own
   library it does (protogen, chameleon); when it cannot (sqlc's naming is
   internal) the rule is reproduced and pinned, and a divergence shows up as a
   drop.

7. **A Go `calls` edge's callee comes from the call graph suited to its call
   site** (2026-10-06, #94, the owner's decision). At a static call the type
   checker names the callee. At a call through an interface, CHA gives every
   implementation of the method. At a call through a function value, VTA gives
   the functions whose values can reach the site. All three are `RESOLVED`,
   because each is the type flow of the SSA program, not a heuristic. CHA alone
   answered a function-value call with every function in the program that has
   a matching signature. That is shape matching, which this rule's table never
   meant by "type checker". It made about half of govm's `calls` edges, and with
   `-tests` it made production `vm#VM.execute` call 530 test functions.

   The assumptions, each pinned in `internal/adapter/golang/callgraph_test.go`:
   - **VTA is not used at interface calls.** It loses an implementation whose
     receiver value is made in dependency code it does not see, such as
     `walker.Observe → pgconn.PgError.Error` on drover.
     `TestInterfaceCallsKeepEveryImplementation` fails the day interface
     calls go through VTA.
   - **VTA refines CHA.** It is seeded with CHA's graph, its callees at a site
     are a subset of CHA's, and the site set is read from CHA. Measured
     on govm, steward, drover, voya and alidade: no edge added, only removed.
   - **A site VTA reaches nothing from has no edge.** Either no function value
     of the program flows there, or the one that does was made outside it
     (`signal.NotifyContext`'s `stop`, a plugin symbol). Rule 5 applies.

   What would reopen this: a function value built in dependency code that wraps
   a module function and is called from module code. VTA would lose that edge.
   It was not found in a sample of 8 of the 118 such sites on steward and govm.
   steward's middleware (`s.cors(handler)`) is the module-built case, and VTA
   gives exactly the 13 handlers it wraps, where CHA gave 65 callees.

8. **A Python `calls` edge is `RESOLVED`: jedi's type inference names its
   callee** (2026-10-09, #237, the owner's decision; ADR 008). Python has no
   compiler that types a program, so this is inference over a dynamic
   language, not a checker's proof. It is type flow, though, which rule 7
   counts, and not the name or shape matching that rule 7 excluded. What
   earns the label is a measurement against what actually ran: agent-config's
   46 Python test suites under a recorder of every call made. On the 326
   executed call sites that hold one call each, jedi's callee was the one that
   ran at 321, and at 2 more the function a decorator wraps (the callee as the
   source names it), with 0 wrong and 3 missed. `INFERRED` would have had
   `exclude_inferred` drop every Python call, which rule 1 calls hiding the
   point; a fifth category failed this ADR's test, since no consumer would act
   on it differently.

   The assumptions, each pinned:
   - **A call through a parameter is not guessed.** jedi infers a parameter
     from the function's call sites, and that inference made the one wrong
     answer measured (a test's function passed as `fn`). Such a call is
     counted as unplaced. `TestAnalyze` pins it.
   - **A call through a variable is followed** to the functions it holds
     (`pick = small if … else large`), one edge each. `TestAnalyze` pins it.
   - **Execution is the oracle.** `scripts/pytrace.sh` records a repository's
     tests and `internal/oracle` compares (`ALIDADE_PYTRACE`).

   What would reopen this: the execution oracle on a class-heavy repository
   showing jedi wrong on a meaningful share of method calls. agent-config is
   mostly plain functions, so its measurement does not cover that case.

## What this buys, concretely

* An agent can be told "traverse `RESOLVED` only" for a refactoring question, and
  "include `INFERRED`" for an exploratory one.
* A doc → symbol matcher that over-matches shows up as an `INFERRED` count, not
  as mysteriously wrong answers.
* The Go layer's advantage over name matching becomes measurable rather than
  asserted: count `RESOLVED` edges.

## Alternatives considered

**A numeric confidence score.** Rejected: nobody can calibrate 0.7 against 0.8
across five unrelated layers, and a float invites arithmetic that means nothing.
Three named categories with clear rules are auditable.

**Two values (`EXTRACTED` / `INFERRED`).** This is the prior tool's model, and it
has no place to put type-resolved edges — the category that most distinguishes
this graph. Type resolution would be labelled the same as a name guess.

**No provenance; keep only high-confidence edges.** Rejected: it discards the
Markdown layer entirely, and quietly decides for every consumer that exploratory
questions are not worth answering.

## When to reopen this

* ~~A fourth category is genuinely needed~~ — reached and taken (`DECLARED`,
  2026-09-10): a consumer traversing `RESOLVED`-only for a refactoring question
  needs schema → generated-code edges, and could not have them while they sat
  with heuristics. A fifth category needs the same test: a consumer that would
  act differently.
* **`INFERRED` edges exceed a meaningful share of the graph.** That means a
  heuristic adapter is dominating, and the answer is tightening the adapter, not
  relabelling.
* **Consumers ignore the field.** If nothing filters on derivation after a period
  of real use, the categories are not carrying weight and should be simplified.
