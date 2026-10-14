# ADR 001: Core Architecture — One Binary, Layer Adapters over a Shared Graph

**Status:** Accepted
**Date:** 2026-08-29
**Relates to:** [000](000-composition-boundary.md), [002](002-node-identity.md), [006](006-edge-provenance.md)

## Context

ADR 000 fixes what alidade is not. This fixes what it is.

Five layers must produce one graph an agent can traverse in a single query — the
value is precisely in crossing between them ("which Go code touches the `users`
table", "what configuration depends on this schema field"). A design where each
layer produces its own graph and a caller joins them afterwards pushes the
hardest problem — identity — onto every consumer.

## Decision

**One binary. Each layer is an adapter that emits nodes and edges into a single
shared graph, keyed by one identity scheme (ADR 002). The MCP surface traverses
that graph and knows nothing about which layer produced what.**

```
  caliper ──┐
protocompile ┤
  pxf ───────┼──▶ layer adapters ──▶ graph core ──▶ MCP surface
  sqlc ──────┤     (nodes+edges)      (identity,      (traversal,
  goldmark ──┘                         merge)          budgeting)
```

### Layers own extraction; the core owns identity

An adapter answers one question: given this corpus, what nodes and edges exist?
It never decides what a node *is called* — it asks the core to mint the ID from
the layer's native identity, so that a `.pxf` `@type` reference and a `.proto`
message definition converge on the same node without either adapter knowing the
other exists.

This is the seam that makes cross-layer edges possible at all, and it is why the
graph core lands before any adapter (`#1` precedes `#2`–`#5`, `#8`).

### The merge derives what no single layer can know

An adapter reports what it observed. A fact about an edge that follows from its
*endpoints* is not observed by anyone — the layer that emits the edge may not
have the node the other end refers to, and in the Markdown layer's case it
cannot: `Link` runs after the merge precisely because the Go nodes do not exist
when the Markdown layer extracts. So the merge computes those facts once, where
every contribution has arrived.

Test-ness is the first (2026-09-13, [ADR 004](004-generated-code.md) rule 4): a
node carries `Test` because the Go adapter read a `_test.go` file, and
`Builder.Build` derives every edge's from its endpoints. Asking each adapter to
mark its own edges moved the obligation rather than removing it, and it was
wrong in two of the six layers within one release.

### Construction is a pipeline, not a service

`detect → extract (per layer) → merge → link → serve`. Adapters are independent
and can run concurrently; the merge is the only place two layers meet. No
adapter holds state across runs, and no adapter writes to disk.

The `link` step (added 2026-09-10 with the Markdown layer, #23) is the one
place edges are made across layers: a consumer of the merged graph that
resolves what one layer *mentions* — a code span naming a declaration —
against every layer's nodes, and feeds the resulting edges back through the
same builder before the graph is built again. It is not a way for adapters to
see each other: the Markdown adapter emits mentions, not edges, and the linker
accepts a mention only when exactly one node answers it. A second linker of
this kind — the `${field.path}` references of `.pxf` against a schema, say —
would sit beside it, after the merge, under the same rule.

*Amended 2026-10-08 (#190, the owner's decision):* the second linker exists.
It joins each `.pxf` entry to the `.proto` field it sets, walking the entry's
block path through the message its document declares (`@type`, or a typed
directive's), and gives a chameleon layer with no `@type` the one type the
other layers of its chain declare — DECLARED, by the chain, since the
resolver merges a chain's layers into one message. It runs before the
Markdown linker, which reads its edges: a proto field and the `.pxf` entries
that set it are one answer to a mention, the schema, so a doc naming
`blocked_module_globs` links to the schema field rather than to none of
seven. Two different schema fields, or a schema field and an entry that sets
another, are still two answers and no edge. Measured: on the corpus, 1,434
entries linked; protowire-go's decoder, against protoc's descriptors,
confirms 826 of 826 distinct fields across 23 documents and none extra.

### The MCP surface is a consumer, not a component

The traversal tools sit outside the core and depend on it, not the reverse. A
second frontend — a CLI, an LSP, a hosted service — is an additional consumer of
the same graph, requiring no change to any adapter. This is the same relationship
`protolsp` has between `analysis.Workspace` and its LSP and Connect-RPC
frontends.

## What this buys, concretely

* A cross-layer question is one traversal, not a join in the caller.
* A new layer is one adapter and zero changes to the surface.
* A new frontend is one consumer and zero changes to the layers.
* The expensive, decision-heavy part (identity, merge) exists once and is tested
  once.

## Alternatives considered

**A graph per layer, joined by the consumer.** Rejected: it moves identity —
the one decision that costs a migration to get wrong — out of the place that can
reason about it and into every caller. It also makes the highest-value edges
(`.proto` → `.pb.go`, `.sql` → sqlc output) somebody else's problem.

**A service per layer, composed over RPC.** Rejected for the reason in ADR 000:
it converts a refactorable Go API boundary into a serialized contract, for a tool
that runs against a local checkout in about a second.

**Adapters write directly into a store, no core.** Rejected: identity and merge
have to live somewhere, and "in whichever adapter ran first" is not a design.

## Consequences

The graph core is on the critical path for everything. It is also where the
foundational decision lives (ADR 002), which is why the plan sequences the
identity question before code rather than during it.

Adapters must be able to emit an edge whose target does not exist yet — the
`.pxf` layer references `.proto` messages, and layers run independently. The core
therefore resolves edges after all adapters have reported, and an edge that never
resolves is dropped with a recorded reason rather than attached to a near match
(ADR 002, ADR 006).
