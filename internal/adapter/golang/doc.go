// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package golang is the Go layer adapter: it consumes caliper's type-resolved
// analysis of a module and emits the Go nodes and edges of the graph.
//
// The adapter owns no analysis (ADR 000). Packages, declarations and the
// call graph all come from github.com/trendvidia/caliper/pkg/analysis, and
// this package's job is translation: caliper's *ssa.Function and
// types.Object become nodes keyed by ADR 002's go:// grammar, and the SSA
// call graph, interface satisfaction and type references become edges that
// each state their derivation (ADR 006).
//
// What comes out, for the target module:
//
//   - package, func, method, type, var and const nodes, and field nodes
//     for the fields of a struct type literal, go://pkg#Type.field (#193);
//   - contains (EXTRACTED): package → declaration, type → method and field;
//   - imports (EXTRACTED): package → package;
//   - calls (RESOLVED): from the SSA call graph;
//   - implements (RESOLVED): type → interface, by structural satisfaction;
//   - references (RESOLVED): declaration → named type in its signature or
//     fields, a struct field → the named types its type writes (a type
//     parameter's constraint being its type's); function or method →
//     package-level constant or variable of the graph it names in its
//     body (#180).
//
// Two of caliper's defaults are deliberately not inherited. Generated code
// is in the graph and marked, never silently absent (ADR 004); tests are in
// or out by an explicit option. Both are fields of [Options], and Extract
// sets caliper's EngineOptions from them rather than from
// analysis.DefaultOptions.
//
// The output is filtered to the repository's modules. Caliper's SSA program
// is whole-program — every dependency and the standard library — and
// measured on real repositories it is 20–290x the size of the module's own
// graph. A call from the module to a function outside the repository is
// kept at package granularity: a calls edge to the callee's package node,
// marked boundary=package, so "what here calls into x/tools" is answerable
// without 50,000 nodes that no query about this module wants.
//
// A repository may hold several modules (#67): every go.mod under the root
// is one, loaded on its own from its own directory, which is how the go
// tool builds a repository whose modules no go.work joins. A call, import,
// reference or interface satisfaction from one module into another lands
// on the other's declaration, since a Go ID carries its module path (ADR
// 002 rule 5), whichever way the type checker reached it — a replace
// directive or a version.
package golang
