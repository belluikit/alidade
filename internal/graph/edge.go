// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

// A Derivation states how an edge was arrived at (ADR 006). Every edge
// carries one and no edge is emitted without one, which is why the zero
// value is invalid rather than a default: an adapter that forgets to say
// gets an error from [Builder.AddEdge], not a silent guess.
//
// The distinction is the point. A Go calls edge from a type-resolved call
// graph and a Markdown doc-to-symbol edge are not the same claim, and
// presenting both as "an edge" invites a consumer — an agent, most likely —
// to reason from the guess as though it were the fact.
type Derivation uint8

// The four derivations, ordered strongest first. The order is load-bearing:
// where two adapters claim the same relationship, the stronger claim wins
// (see [Builder.Build]).
const (
	// derivationUnset is the zero value and is never valid.
	derivationUnset Derivation = iota

	// Extracted: stated explicitly in the source. A .proto import, a
	// .pxf @type, a sqlc "-- name:" annotation.
	Extracted

	// Resolved: derived by a type checker or compiler. A Go call from
	// the SSA call graph, an implements from structural satisfaction.
	// This is not a weaker Extracted — it is a separate claim, and it
	// is the category that distinguishes this graph from a name-matching
	// one.
	Resolved

	// Declared: determined by a build tool's configuration or a
	// generator's documented rule applied to a written option — a
	// .proto message's generated Go type from go_package and
	// protoc-gen-go's naming, a table's sqlc struct, a chain
	// specification's layers per environment. Neither written in the
	// source nor checked by a type system, but not a guess: when the
	// convention holds the edge is exact, and when it does not the
	// target does not exist and the edge drops rather than lands wrong.
	Declared

	// Inferred: derived by heuristic, and may be wrong. A Markdown
	// mention of "Config" may or may not mean the Config type.
	//
	// Inferred is for edges whose relationship is heuristic, never for
	// edges whose endpoint was a guess: an edge that cannot be resolved
	// to a node is dropped, not attached to a near match (ADR 006 rule
	// 5).
	Inferred
)

// String returns the wire name of the derivation.
func (d Derivation) String() string {
	switch d {
	case Extracted:
		return "EXTRACTED"
	case Resolved:
		return "RESOLVED"
	case Declared:
		return "DECLARED"
	case Inferred:
		return "INFERRED"
	default:
		return "INVALID"
	}
}

// valid reports whether the derivation is one of the four.
func (d Derivation) valid() bool {
	return d == Extracted || d == Resolved || d == Declared || d == Inferred
}

// stronger returns whichever of the two derivations a consumer should trust
// more. Where one adapter resolved a relationship and another guessed at
// it, the resolved claim stands: a heuristic agreeing with a type-checked
// fact does not make the fact less certain.
func stronger(a, b Derivation) Derivation {
	if !a.valid() {
		return b
	}
	if !b.valid() {
		return a
	}
	if a < b { // constants are declared strongest first
		return a
	}
	return b
}

// An EdgeKind names a relationship.
type EdgeKind string

// The edge kinds the layers produce. As with [Kind], the core requires that
// an edge names its kind, not that the kind appears here.
const (
	// KindCalls is a Go call, from the type-resolved call graph.
	KindCalls EdgeKind = "calls"
	// KindImplements joins a type to an interface it satisfies, or a class
	// to a class it inherits from: a Python base, a TypeScript extends
	// clause (#249).
	KindImplements EdgeKind = "implements"
	// KindImports joins a file or package to one it imports.
	KindImports EdgeKind = "imports"
	// KindReferences joins a declaration to one it names — a .pxf @type
	// pointing into the .proto graph, most importantly.
	KindReferences EdgeKind = "references"
	// KindGenerates joins a schema to the artifact generated from it: a
	// .proto message to its .pb.go type, a SQL query to its sqlc output.
	// These are the highest-value edges in the plan, and the reason
	// ADR 002 rule 6 keeps the two ends as separate nodes.
	KindGenerates EdgeKind = "generates"
	// KindContains joins a declaration to one nested inside it: a
	// package to its functions, a message to its fields, a document to
	// its sections.
	KindContains EdgeKind = "contains"
	// KindDocuments joins prose to what it describes. Always INFERRED.
	KindDocuments EdgeKind = "documents"
	// KindQueries joins a SQL query to a table it reads or writes.
	KindQueries EdgeKind = "queries"
)

// An Edge is a directed relationship between two nodes.
//
// An adapter may emit an edge whose endpoints do not exist yet — the .pxf
// layer references .proto messages, and the layers run independently — so
// endpoints are resolved once every adapter has reported (ADR 001). An edge
// that never resolves is dropped with a reason and counted, never attached
// to the nearest plausible node: a missing edge looks like missing data,
// while a wrong edge looks like a fact.
type Edge struct {
	// From and To are node IDs, minted by a [Minter].
	From ID
	To   ID
	// Kind is the relationship. Required.
	Kind EdgeKind
	// Derivation is how the edge was arrived at. Required.
	Derivation Derivation
	// Test says the edge touches a declaration from a test file, so it
	// is not in a build without tests. It is derived, never reported: an
	// adapter that sets it is overwritten by [Builder.Build], which
	// computes it from the endpoints once every node contribution is
	// merged. Test-ness is a property of the endpoints, not a way of
	// arriving at the edge, so it is not a [Derivation] (ADR 006); and
	// deriving it in the one place two layers meet is what keeps a layer
	// that emits an edge into another layer's test declaration from
	// having to know it did.
	Test bool
	// Attrs carries layer-specific detail — a call site, a match
	// confidence note. Never traversed by the core.
	Attrs map[string]string
}

// edgeKey identifies an edge for merging. Derivation is deliberately absent:
// two adapters asserting the same relationship by different evidence are
// asserting one edge, and [Builder.Build] keeps the stronger evidence.
type edgeKey struct {
	from ID
	kind EdgeKind
	to   ID
}

func (e Edge) key() edgeKey { return edgeKey{from: e.From, kind: e.Kind, to: e.To} }
