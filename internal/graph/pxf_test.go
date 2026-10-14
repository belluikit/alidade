// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"bytes"
	"errors"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func mustPXF(t *testing.T, g *Graph) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := g.WritePXF(&buf); err != nil {
		t.Fatalf("WritePXF: %v", err)
	}
	return buf.Bytes()
}

// TestWritePXFGolden pins the wire shape. The MCP surface budgets tokens
// against these bytes and consumers read them, so a block renamed or
// reordered here breaks everything downstream at once — this test is the
// place that has to be argued with first.
//
// The graph is small on purpose: two adapters that disagree about one
// table's name, an edge from each of two layers, and an edge whose target
// nothing declared. Every part of the shape appears exactly once.
func TestWritePXFGolden(t *testing.T) {
	const want = `# alidade graph, built on demand from the working tree (ADR 005).
stats {
  nodes = 3
  edges = 2
  generated = 1
  dropped = 1
  conflicts = 1
  nodes_by_scheme {
    md: 1
    sql: 2
  }
  edges_by_derivation {
    EXTRACTED: 1
    INFERRED: 1
  }
  dropped_by_reason {
    unknown_target: 1
  }
}
node {
  id = "md://docs/schema.md#users"
  kind = "section"
  name = "Users"
  file = "docs/schema.md"
  line = 8
}
node {
  id = "sql://query/example.com/m/db#GetUser"
  kind = "query"
  name = "GetUser"
  file = "db/query.sql"
  line = 3
  col = 1
  generated = true
  attrs {
    result: "one"
  }
}
node {
  id = "sql://table/users"
  kind = "table"
  name = "user"
}
edge {
  from = "md://docs/schema.md#users"
  to = "sql://table/users"
  kind = "documents"
  derivation = INFERRED
}
edge {
  from = "sql://query/example.com/m/db#GetUser"
  to = "sql://table/users"
  kind = "queries"
  derivation = EXTRACTED
}
dropped {
  from = "sql://query/example.com/m/db#GetUser"
  to = "sql://table/orders"
  kind = "queries"
  derivation = EXTRACTED
  reason = unknown_target
}
conflict {
  subject = "sql://table/users"
  field = "name"
  kept = "user"
  discarded = "users"
}
`

	if got := string(mustPXF(t, goldenGraph(t))); got != want {
		t.Errorf("wire shape changed\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestWritePXFReparses is the check that matters most about emitting a
// format rather than describing one: alidade reads .pxf as one of its five
// layers (ADR 003), so a graph it cannot parse back would be a tool unable
// to read its own output.
func TestWritePXFReparses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		graph *Graph
	}{
		{"golden", goldenGraph(t)},
		{"fixture", buildFixture(t)},
		{"empty", NewBuilder(testRoot).Build()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := mustPXF(t, tt.graph)
			doc, err := pxf.Parse(data)
			if err != nil {
				t.Fatalf("emitted document does not parse: %v\n%s", err, data)
			}
			// stats is always present, so the entry count is one
			// block per node, edge, dropped edge and conflict, plus
			// it.
			g := tt.graph
			want := 1 + g.NodeCount() + g.EdgeCount() + len(g.Dropped()) + len(g.Conflicts())
			if got := len(doc.Entries); got != want {
				t.Errorf("parsed %d top-level entries, want %d", got, want)
			}
		})
	}
}

// goldenGraph is the small hand-checkable graph the golden pins.
func goldenGraph(t *testing.T) *Graph {
	t.Helper()
	m := NewMinter(testRoot)
	table := mustID(m.SQLID(SQLTable, "users"))
	query := mustID(m.SQLQueryID("example.com/m/db", "GetUser"))
	doc := mustID(m.MarkdownID("docs/schema.md", "Users"))
	missing := mustID(m.SQLID(SQLTable, "orders"))

	b := NewBuilder(testRoot)
	addAll(t, b,
		[]Node{
			{ID: table, Kind: KindTable, Name: "users"},
			{ID: table, Kind: KindTable, Name: "user"},
			{ID: query, Kind: KindQuery, Name: "GetUser", Pos: Position{File: "db/query.sql", Line: 3, Col: 1}, Generated: true, Attrs: map[string]string{"result": "one"}},
			{ID: doc, Kind: KindSection, Name: "Users", Pos: Position{File: "docs/schema.md", Line: 8}},
		},
		[]Edge{
			{From: query, To: table, Kind: KindQueries, Derivation: Extracted},
			{From: doc, To: table, Kind: KindDocuments, Derivation: Inferred},
			{From: query, To: missing, Kind: KindQueries, Derivation: Extracted},
		},
	)
	return b.Build()
}

// errWriter fails on every write.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("no") }

func TestWritePXFReportsWriteFailure(t *testing.T) {
	if err := buildFixture(t).WritePXF(errWriter{}); err == nil {
		t.Fatal("WritePXF succeeded on a failing writer")
	}
}

// TestDerivationText pins the three wire names, which appear both as edge
// values and as the keys of the edges_by_derivation tally.
func TestDerivationText(t *testing.T) {
	for d, want := range map[Derivation]string{
		Extracted: "EXTRACTED",
		Resolved:  "RESOLVED",
		Declared:  "DECLARED",
		Inferred:  "INFERRED",
	} {
		if got := d.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}

	// The zero value is not a derivation. Nothing can emit an edge
	// carrying it — [Builder.AddEdge] rejects one — and if anything ever
	// did, it must not render as though it had said something (ADR 006).
	if got := derivationUnset.String(); got != "INVALID" {
		t.Errorf("zero String() = %q, want INVALID", got)
	}
}

func TestStrongerIgnoresInvalid(t *testing.T) {
	if got := stronger(derivationUnset, Inferred); got != Inferred {
		t.Errorf("stronger(unset, INFERRED) = %s, want INFERRED", got)
	}
	if got := stronger(Resolved, derivationUnset); got != Resolved {
		t.Errorf("stronger(RESOLVED, unset) = %s, want RESOLVED", got)
	}
	if got := stronger(Inferred, Extracted); got != Extracted {
		t.Errorf("stronger(INFERRED, EXTRACTED) = %s, want EXTRACTED", got)
	}
	if got := stronger(Inferred, Declared); got != Declared {
		t.Errorf("stronger(INFERRED, DECLARED) = %s, want DECLARED", got)
	}
	if got := stronger(Declared, Resolved); got != Resolved {
		t.Errorf("stronger(DECLARED, RESOLVED) = %s, want RESOLVED", got)
	}
}
