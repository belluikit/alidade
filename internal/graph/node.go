// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

// A Kind names what a node is. The vocabulary is deliberately shared across
// layers rather than namespaced per layer: a query for every declaration
// that documents something should not have to know which of seven adapters
// minted it. Where two layers mean genuinely different things by the same
// word, the node's scheme tells them apart.
type Kind string

// The node kinds of the eight layers (ADR 003, ADR 007, ADR 008). An adapter may emit a kind
// not listed here — the core validates that a kind is present, not that it
// is known — but a kind that outlives one adapter belongs in this block,
// since this is the only place a consumer can read the vocabulary.
const (
	// Go.
	KindPackage Kind = "package"
	// KindPackageTests is a package's in-package test files: what they
	// do at package level that the package's production files do not —
	// an import only they make, an init, a blank variable's initializer
	// (#128). It is the go tool's test variant of the package, p
	// [p.test], and a test node.
	KindPackageTests Kind = "package_tests"
	KindFunc         Kind = "func"
	KindMethod       Kind = "method"
	KindType         Kind = "type"
	KindVar          Kind = "var"
	KindConst        Kind = "const"

	// .proto.
	KindProtoFile Kind = "proto_file"
	KindMessage   Kind = "message"
	KindEnum      Kind = "enum"
	KindEnumValue Kind = "enum_value"
	KindService   Kind = "service"
	KindRPC       Kind = "rpc"

	// .pxf.
	KindPXFFile Kind = "pxf_file"
	KindBlock   Kind = "block"

	// Shared by .proto and .pxf, which both call a named member of a
	// block a field.
	KindField Kind = "field"

	// SQL.
	KindTable  Kind = "table"
	KindView   Kind = "view"
	KindColumn Kind = "column"
	KindQuery  Kind = "query"

	// Markdown.
	KindDocument Kind = "document"
	KindSection  Kind = "section"

	// goasm.
	KindGoasmFile Kind = "goasm_file"
	KindTemplate  Kind = "template"

	// Frontend (ADR 007): a script file, and a Svelte component. A
	// generated file's exports reuse type, const and enum above, and a
	// package imported by name reuses package.
	KindModule    Kind = "module"
	KindComponent Kind = "component"
)

// A Position locates a node in the working tree, for navigation only. It is
// never part of identity (ADR 002 rule 1) and never keys anything: every
// edit above a declaration would change it, so a graph keyed on position is
// valid only against the commit that built it.
type Position struct {
	// File is repo-relative and forward-slashed. [Builder.AddNode]
	// normalizes it, so an adapter may pass whatever its upstream
	// library reports.
	File string
	// Line and Col are 1-based. Zero means unknown.
	Line int
	Col  int
}

// zero reports whether the position carries nothing worth recording.
func (p Position) zero() bool { return p.File == "" && p.Line == 0 && p.Col == 0 }

// A Node is one declaration in the graph: a Go function, a .proto message,
// a SQL table, a Markdown section. Nodes are values; the graph holds them
// immutably after [Builder.Build], and a Node handed out by a [Graph] must
// not be modified.
type Node struct {
	// ID is the node's identity, minted by a [Minter]. Two adapters that
	// mint the same ID are describing the same thing, and their
	// contributions are merged.
	ID ID
	// Kind is what the node is. Required.
	Kind Kind
	// Name is the display name — the last segment of the declaration
	// path. It is for output, never for joining: matching on names
	// instead of IDs is the failure this package exists to prevent.
	Name string
	// Pos locates the declaration, for navigation only.
	Pos Position
	// Generated marks a node that came from generated source. Generated
	// code is in the graph by default and identified rather than
	// filtered (ADR 004) — it is the bridge between a schema and the
	// hand-written code that uses it, so removing it disconnects the two
	// halves of the highest-value queries.
	Generated bool
	// Test marks a declaration that came from a test file — a _test.go
	// for the Go layer. Like [Node.Generated] it is observed by the
	// adapter that read the source and by nobody else: no other layer
	// can derive it, and ADR 004 rule 4 puts test files on the same
	// footing as generated code, in the graph and identified rather than
	// filtered. An edge's test-ness is not stored: it follows from its
	// endpoints and [Builder.Build] derives it (ADR 001).
	Test bool
	// Attrs carries layer-specific detail a consumer might display: a
	// proto field number, a Go receiver, a SQL column type. It is never
	// part of identity and nothing in the core traverses it.
	Attrs map[string]string
}

// scheme returns the node's layer, or "" for a malformed ID.
func (n Node) scheme() Scheme {
	s, _ := n.ID.Scheme()
	return s
}
