// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package graph is alidade's core: the node and edge schema, the identity
// scheme every layer adapter mints IDs through, and the merge that turns
// independent adapter output into one traversable graph.
//
// It is deliberately the first package to exist. Identity is the one decision
// in this project that costs a migration rather than a revert to change, so it
// is settled before any adapter is written — see docs/adr/002-node-identity.md.
//
// The governing decisions:
//
//	ADR 000  alidade composes analyses; it owns no parser and no type checker
//	ADR 001  one binary, layer adapters emitting into a shared graph
//	ADR 002  identity is declaration-anchored, never positional
//	ADR 004  generated code is in the graph, identified, not filtered by default
//	ADR 005  the graph is built on demand and held for the session
//	ADR 006  every edge carries EXTRACTED / RESOLVED / INFERRED
//
// Adapters own extraction and never decide what a node is called: they ask
// this package to mint an ID from their layer's native identity, which is what
// lets a .pxf @type reference and a .proto message definition converge on one
// node without either adapter knowing the other exists.
package graph
