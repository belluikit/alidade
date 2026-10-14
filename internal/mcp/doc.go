// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package mcp is the agent-facing surface: an MCP server exposing traversal
// of the graph the layer adapters built.
//
// It is a consumer of the graph, not a component of it (ADR 001): it
// depends on internal/graph and knows nothing about which layer produced
// what. A second frontend would sit beside it, not inside it.
//
// # Tools
//
//   - graph_stats: what the graph holds — node and edge counts by layer and
//     derivation, generated share, dropped edges and conflicts — and what the
//     build skipped: the inputs no ID could name or that could not be read,
//     each with its layer and reason (#92), plus go_outside_tree; and the
//     Go packages that did not compile, which are in the graph with their
//     error, and those whose calls are missing because of them (#95).
//   - find_nodes: nodes by name or ID substring, optionally by kind or
//     layer. The way to get an ID to traverse from.
//   - get_node: one node with its position, attributes and edge counts.
//   - neighbors: the nodes within N hops of one node, with the edges
//     between them.
//   - shortest_path: the shortest chain of edges between two nodes.
//   - subgraph_for_question: the neighbourhood of the declarations a
//     question names, in one connected result across layers.
//   - graph_issues: what the graph left out or had to decide — dropped
//     edges with their reason, conflicts with what was kept and discarded.
//   - rebuild: drop the graph and build it again from the working tree.
//
// # Budget
//
// Every result-bearing tool takes max_tokens and never emits more than
// that. graph_stats is counts and takes none; each list of items it
// carries — the skipped inputs, and the Go packages that failed or lost
// their calls — is capped and says how many it left out.
// Items are added in traversal order until the budget is spent; the
// result then says it is truncated and how many nodes and edges were left
// out, so an agent can narrow the query or raise the budget rather than
// mistake a cut for the whole. Tokens are estimated at four bytes each,
// which overestimates for identifiers and underestimates for prose, and is
// stated here so nobody reads the number as a tokenizer's.
//
// # Derivation
//
// Every edge in every result carries its derivation (ADR 006 rule 3), and
// exclude_inferred asks for the high-confidence graph alone (rule 4):
// EXTRACTED, RESOLVED and DECLARED stay, INFERRED goes.
//
// # Tests
//
// A graph built with -tests marks every declaration from a _test.go file,
// and the core derives the same on every edge touching one (#81).
// exclude_tests,
// on find_nodes and the traversal tools, leaves them out: the result is
// the graph a build without tests gives, so "does this have a production
// caller" and "is it called from a test" are one graph and a filter, not
// two builds. graph_stats says go_test_nodes, 0 when tests were not
// loaded.
//
// # Generated code
//
// Generated code is always in the graph (ADR 004): it is the bridge from
// hand-written code to the schema it came from. exclude_generated keeps it
// out of an answer without cutting the bridge (#126): find_nodes and a
// question's seeds skip generated nodes, and a traversal still crosses
// them, rendering each as its id and kind only, so "which code uses this
// table" is still one path, every edge on it real.
//
// # Format
//
// Results are compact JSON — short keys, repo-relative paths — because the
// transport is JSON-RPC and the reader is an agent. This is the one
// integration edge where JSON belongs; the graph's own serialization is
// PXF.
//
// # Lifetime
//
// The graph is built on the first call that needs it and held for the
// session (ADR 005). rebuild is the only invalidation, and it is the
// caller's decision: a server holding a graph across a checkout is
// reading a working tree that no longer exists.
package mcp
