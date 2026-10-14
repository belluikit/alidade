// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"bytes"
	"math/rand"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

const testRoot = "/repo"

// mustID unwraps a mint in a fixture, where the inputs are constants and a
// failure means the test itself is wrong.
func mustID(id ID, err error) ID {
	if err != nil {
		panic("graph test: " + err.Error())
	}
	return id
}

// fixture is a slice of a real cross-layer graph: a .proto message
// generating a Go type, a sqlc query generating a Go function and reading a
// table, and an ADR section that mentions the table. It exercises all three
// derivations and every cross-layer edge shape the plan cares about.
func fixture(t *testing.T) ([]Node, []Edge) {
	t.Helper()
	m := NewMinter(testRoot)

	protoFile := mustID(m.ProtoFileID("proto/app/v1/user.proto"))
	message := mustID(m.ProtoID("app.v1.User"))
	field := mustID(m.ProtoID("app.v1.User.email"))
	genPkg := mustID(m.GoID("github.com/x/app/gen", ""))
	genType := mustID(m.GoID("github.com/x/app/gen", "User"))
	genFunc := mustID(m.GoID("github.com/x/app/gen", "GetUser"))
	caller := mustID(m.GoID("github.com/x/app/store", "Store.Load"))
	table := mustID(m.SQLID(SQLTable, "users"))
	query := mustID(m.SQLQueryID("example.com/m/db", "GetUser"))
	doc := mustID(m.MarkdownID("docs/schema.md", "Users"))

	nodes := []Node{
		{ID: protoFile, Kind: KindProtoFile, Name: "user.proto", Pos: Position{File: "proto/app/v1/user.proto", Line: 1, Col: 1}},
		{ID: message, Kind: KindMessage, Name: "User", Pos: Position{File: "proto/app/v1/user.proto", Line: 10, Col: 1}},
		{ID: field, Kind: KindField, Name: "email", Pos: Position{File: "proto/app/v1/user.proto", Line: 12, Col: 3}, Attrs: map[string]string{"number": "2"}},
		{ID: genPkg, Kind: KindPackage, Name: "gen"},
		{ID: genType, Kind: KindType, Name: "User", Pos: Position{File: "gen/user.pb.go", Line: 30, Col: 6}, Generated: true},
		{ID: genFunc, Kind: KindFunc, Name: "GetUser", Pos: Position{File: "gen/query.sql.go", Line: 15, Col: 1}, Generated: true},
		{ID: caller, Kind: KindMethod, Name: "Load", Pos: Position{File: "store/store.go", Line: 44, Col: 1}},
		{ID: table, Kind: KindTable, Name: "users"},
		{ID: query, Kind: KindQuery, Name: "GetUser", Pos: Position{File: "db/query.sql", Line: 3, Col: 1}},
		{ID: doc, Kind: KindSection, Name: "Users", Pos: Position{File: "docs/schema.md", Line: 8, Col: 1}},
	}
	edges := []Edge{
		{From: protoFile, To: message, Kind: KindContains, Derivation: Extracted},
		{From: message, To: field, Kind: KindContains, Derivation: Extracted},
		{From: message, To: genType, Kind: KindGenerates, Derivation: Extracted},
		{From: genPkg, To: genType, Kind: KindContains, Derivation: Extracted},
		{From: genPkg, To: genFunc, Kind: KindContains, Derivation: Extracted},
		{From: query, To: genFunc, Kind: KindGenerates, Derivation: Extracted},
		{From: query, To: table, Kind: KindQueries, Derivation: Extracted},
		{From: caller, To: genFunc, Kind: KindCalls, Derivation: Resolved},
		{From: caller, To: genType, Kind: KindReferences, Derivation: Resolved},
		{From: doc, To: table, Kind: KindDocuments, Derivation: Inferred},
	}
	return nodes, edges
}

// addAll reports each node and edge in the given order.
func addAll(t *testing.T, b *Builder, nodes []Node, edges []Edge) {
	t.Helper()
	for _, n := range nodes {
		if err := b.AddNode(n); err != nil {
			t.Fatalf("AddNode(%s): %v", n.ID, err)
		}
	}
	for _, e := range edges {
		if err := b.AddEdge(e); err != nil {
			t.Fatalf("AddEdge(%s -> %s): %v", e.From, e.To, err)
		}
	}
}

func buildFixture(t *testing.T) *Graph {
	t.Helper()
	b := NewBuilder(testRoot)
	nodes, edges := fixture(t)
	addAll(t, b, nodes, edges)
	return b.Build()
}

// TestExtractorsConvergeOnOneNode is the fourth done-when of #1. The .pxf
// layer references a .proto message it did not define; both adapters mint
// the same ID, and the graph must end up with one node carrying what each
// of them knew.
func TestExtractorsConvergeOnOneNode(t *testing.T) {
	m := NewMinter(testRoot)
	message := mustID(m.ProtoID("app.v1.User"))

	b := NewBuilder(testRoot)
	// The .proto adapter: knows where it is declared.
	if err := b.AddNode(Node{ID: message, Kind: KindMessage, Name: "User", Pos: Position{File: "proto/app/v1/user.proto", Line: 10, Col: 1}}); err != nil {
		t.Fatal(err)
	}
	// The .pxf adapter: knows only that something points at it.
	if err := b.AddNode(Node{ID: message, Kind: KindMessage, Attrs: map[string]string{"referenced_by": "config/chain.pxf"}}); err != nil {
		t.Fatal(err)
	}
	g := b.Build()

	if g.NodeCount() != 1 {
		t.Fatalf("NodeCount() = %d, want 1", g.NodeCount())
	}
	n, ok := g.Node(message)
	if !ok {
		t.Fatal("merged node missing")
	}
	if n.Name != "User" {
		t.Errorf("Name = %q, want User", n.Name)
	}
	if n.Pos.File != "proto/app/v1/user.proto" || n.Pos.Line != 10 {
		t.Errorf("Pos = %+v, want the .proto adapter's position", n.Pos)
	}
	if n.Attrs["referenced_by"] != "config/chain.pxf" {
		t.Errorf("Attrs = %v, want the .pxf adapter's attribute kept", n.Attrs)
	}
	if len(g.Conflicts()) != 0 {
		t.Errorf("Conflicts() = %v, want none: the two adapters agreed", g.Conflicts())
	}
}

// TestUnresolvableEdgeIsDroppedNotAttached is the third done-when, and the
// rule ADR 002 borrows from protolsp: a wrong edge is worse than a missing
// one, because a missing edge looks like missing data while a wrong edge
// looks like a fact. The target here is one character from two real nodes.
func TestUnresolvableEdgeIsDroppedNotAttached(t *testing.T) {
	m := NewMinter(testRoot)
	users := mustID(m.SQLID(SQLTable, "users"))
	userAccounts := mustID(m.SQLID(SQLTable, "user_accounts"))
	query := mustID(m.SQLQueryID("example.com/m/db", "GetUser"))
	missing := mustID(m.SQLID(SQLTable, "user"))

	b := NewBuilder(testRoot)
	addAll(t, b,
		[]Node{
			{ID: users, Kind: KindTable, Name: "users"},
			{ID: userAccounts, Kind: KindTable, Name: "user_accounts"},
			{ID: query, Kind: KindQuery, Name: "GetUser"},
		},
		[]Edge{{From: query, To: missing, Kind: KindQueries, Derivation: Extracted}},
	)
	g := b.Build()

	if g.EdgeCount() != 0 {
		t.Fatalf("EdgeCount() = %d, want 0", g.EdgeCount())
	}
	for _, id := range []ID{users, userAccounts} {
		if in := g.In(id); len(in) != 0 {
			t.Errorf("In(%s) = %v, want none: the edge must not land on a near match", id, in)
		}
	}
	if out := g.Out(query); len(out) != 0 {
		t.Errorf("Out(%s) = %v, want none", query, out)
	}

	dropped := g.Dropped()
	if len(dropped) != 1 {
		t.Fatalf("Dropped() = %v, want one entry", dropped)
	}
	if dropped[0].Reason != DropUnknownTarget {
		t.Errorf("Reason = %q, want %q", dropped[0].Reason, DropUnknownTarget)
	}
	if dropped[0].Edge.To != missing {
		t.Errorf("dropped edge target = %q, want %q", dropped[0].Edge.To, missing)
	}
	if got := g.Stats().DroppedByReason[DropUnknownTarget]; got != 1 {
		t.Errorf("Stats().DroppedByReason[%s] = %d, want 1: the count is part of the output (ADR 002 rule 3)", DropUnknownTarget, got)
	}
}

func TestUnknownSourceIsReportedSeparately(t *testing.T) {
	m := NewMinter(testRoot)
	table := mustID(m.SQLID(SQLTable, "users"))
	ghost := mustID(m.SQLQueryID("example.com/m/db", "Ghost"))

	b := NewBuilder(testRoot)
	addAll(t, b,
		[]Node{{ID: table, Kind: KindTable, Name: "users"}},
		[]Edge{{From: ghost, To: table, Kind: KindQueries, Derivation: Extracted}},
	)
	g := b.Build()

	dropped := g.Dropped()
	if len(dropped) != 1 || dropped[0].Reason != DropUnknownSource {
		t.Fatalf("Dropped() = %v, want one unknown_source", dropped)
	}
}

// TestBuildIsOrderIndependent is the second done-when, stated the way it
// actually bites: adapters run concurrently (ADR 001), so "two builds of an
// unchanged tree" means two builds that saw the same facts in different
// orders. Byte-identical output is the only version of that guarantee worth
// having.
func TestBuildIsOrderIndependent(t *testing.T) {
	nodes, edges := fixture(t)
	want := mustPXF(t, buildFixture(t))

	rng := rand.New(rand.NewSource(1))
	for i := range 50 {
		n := slices.Clone(nodes)
		e := slices.Clone(edges)
		rng.Shuffle(len(n), func(a, b int) { n[a], n[b] = n[b], n[a] })
		rng.Shuffle(len(e), func(a, b int) { e[a], e[b] = e[b], e[a] })

		b := NewBuilder(testRoot)
		addAll(t, b, n, e)
		if got := mustPXF(t, b.Build()); !bytes.Equal(got, want) {
			t.Fatalf("permutation %d produced different bytes\n got: %s\nwant: %s", i, got, want)
		}
	}
}

// TestConcurrentAdaptersProduceIdenticalGraph runs the same facts through
// the Builder from several goroutines at once, which is how the adapters
// will actually report. Run under -race, it also pins that the Builder is
// safe to share.
func TestConcurrentAdaptersProduceIdenticalGraph(t *testing.T) {
	nodes, edges := fixture(t)
	want := mustPXF(t, buildFixture(t))

	b := NewBuilder(testRoot)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	record := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(b.AddNode(n))
		}()
	}
	for _, e := range edges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(b.AddEdge(e))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		t.Errorf("concurrent add: %v", err)
	}

	if got := mustPXF(t, b.Build()); !bytes.Equal(got, want) {
		t.Errorf("concurrent build differs\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildTwiceIsIdentical pins that Build does not consume or mutate the
// Builder: the second call must see exactly what the first did.
func TestBuildTwiceIsIdentical(t *testing.T) {
	b := NewBuilder(testRoot)
	nodes, edges := fixture(t)
	// Two adapters that disagree, so the second build has conflicts and
	// merged attributes to reproduce rather than a trivially empty set.
	nodes = append(nodes,
		Node{ID: nodes[1].ID, Kind: KindType, Name: "UserMessage", Attrs: map[string]string{"number": "9", "extra": "x"}},
	)
	addAll(t, b, nodes, edges)

	first := mustPXF(t, b.Build())
	second := mustPXF(t, b.Build())
	if !bytes.Equal(first, second) {
		t.Errorf("second Build differs\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestStrongerDerivationWins(t *testing.T) {
	m := NewMinter(testRoot)
	from := mustID(m.GoID("github.com/x/app", "Load"))
	to := mustID(m.SQLID(SQLTable, "users"))

	// A heuristic adapter and a type-resolving one find the same
	// relationship. Order must not matter, so try both.
	for _, order := range [][]Derivation{{Inferred, Resolved}, {Resolved, Inferred}} {
		b := NewBuilder(testRoot)
		addAll(t, b, []Node{
			{ID: from, Kind: KindFunc, Name: "Load"},
			{ID: to, Kind: KindTable, Name: "users"},
		}, []Edge{
			{From: from, To: to, Kind: KindQueries, Derivation: order[0]},
			{From: from, To: to, Kind: KindQueries, Derivation: order[1]},
		})
		g := b.Build()

		if g.EdgeCount() != 1 {
			t.Fatalf("%v: EdgeCount() = %d, want 1", order, g.EdgeCount())
		}
		if got := g.Out(from)[0].Derivation; got != Resolved {
			t.Errorf("%v: Derivation = %s, want RESOLVED", order, got)
		}
		if len(g.Conflicts()) != 0 {
			t.Errorf("%v: Conflicts() = %v, want none: agreeing by different evidence is not a conflict", order, g.Conflicts())
		}
	}
}

// TestConflictsAreRecorded covers the case convergence gets wrong: two
// adapters minting one ID for two different things. The merge still has to
// be deterministic, and it must say so rather than pick quietly.
func TestConflictsAreRecorded(t *testing.T) {
	m := NewMinter(testRoot)
	id := mustID(m.ProtoID("app.v1.User"))

	b := NewBuilder(testRoot)
	addAll(t, b, []Node{
		{ID: id, Kind: KindMessage, Name: "User", Pos: Position{File: "a.proto", Line: 1}, Attrs: map[string]string{"k": "one"}},
		{ID: id, Kind: KindEnum, Name: "Users", Pos: Position{File: "b.proto", Line: 2}, Generated: true, Attrs: map[string]string{"k": "two"}},
	}, nil)
	g := b.Build()

	if g.NodeCount() != 1 {
		t.Fatalf("NodeCount() = %d, want 1", g.NodeCount())
	}
	fields := make([]string, 0, len(g.Conflicts()))
	for _, c := range g.Conflicts() {
		fields = append(fields, c.Field)
		if c.Subject != string(id) {
			t.Errorf("Subject = %q, want %q", c.Subject, id)
		}
	}
	for _, want := range []string{"kind", "name", "position", "generated", "attr k"} {
		if !slices.Contains(fields, want) {
			t.Errorf("no conflict recorded for %q; got %v", want, fields)
		}
	}
	if g.Stats().Conflicts != len(g.Conflicts()) {
		t.Errorf("Stats().Conflicts = %d, want %d", g.Stats().Conflicts, len(g.Conflicts()))
	}
	// A disagreement about generated resolves toward generated: a
	// declaration one adapter saw in generated source is generated
	// (ADR 004).
	if n, _ := g.Node(id); !n.Generated {
		t.Error("Generated = false, want true")
	}
}

func TestEdgeAttrsMerge(t *testing.T) {
	m := NewMinter(testRoot)
	from := mustID(m.GoID("github.com/x/app", "Load"))
	to := mustID(m.GoID("github.com/x/app", "Save"))

	b := NewBuilder(testRoot)
	addAll(t, b, []Node{
		{ID: from, Kind: KindFunc, Name: "Load"},
		{ID: to, Kind: KindFunc, Name: "Save"},
	}, []Edge{
		{From: from, To: to, Kind: KindCalls, Derivation: Resolved, Attrs: map[string]string{"site": "store.go:44"}},
		{From: from, To: to, Kind: KindCalls, Derivation: Resolved, Attrs: map[string]string{"note": "via interface", "site": "store.go:99"}},
	})
	g := b.Build()

	if g.EdgeCount() != 1 {
		t.Fatalf("EdgeCount() = %d, want 1", g.EdgeCount())
	}
	e := g.Out(from)[0]
	if e.Attrs["note"] != "via interface" {
		t.Errorf("Attrs = %v, want the union", e.Attrs)
	}
	if e.Attrs["site"] != "store.go:44" {
		t.Errorf("Attrs[site] = %q, want the lower-sorting value kept deterministically", e.Attrs["site"])
	}
	if len(g.Conflicts()) != 1 || g.Conflicts()[0].Field != "attr site" {
		t.Errorf("Conflicts() = %v, want one for attr site", g.Conflicts())
	}
}

func TestAddNodeValidates(t *testing.T) {
	m := NewMinter(testRoot)
	ok := mustID(m.SQLID(SQLTable, "users"))

	tests := []struct {
		name string
		node Node
		want string
	}{
		{"no scheme", Node{ID: "users", Kind: KindTable}, "no known scheme"},
		{"unknown scheme", Node{ID: "rust://users", Kind: KindTable}, "no known scheme"},
		{"no kind", Node{ID: ok}, "no kind"},
		{"position outside the root", Node{ID: ok, Kind: KindTable, Pos: Position{File: "/elsewhere/x.sql"}}, "outside the repository root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewBuilder(testRoot).AddNode(tt.node)
			if err == nil {
				t.Fatal("AddNode succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestAddEdgeRequiresADerivation is ADR 006 as a type: an adapter that
// forgets to say how it knows gets an error at the call site, not a silent
// default that a consumer would later read as fact.
func TestAddEdgeRequiresADerivation(t *testing.T) {
	m := NewMinter(testRoot)
	a := mustID(m.SQLID(SQLTable, "users"))
	b := mustID(m.SQLQueryID("example.com/m/db", "GetUser"))

	tests := []struct {
		name string
		edge Edge
		want string
	}{
		{"zero derivation", Edge{From: b, To: a, Kind: KindQueries}, "no derivation"},
		{"out-of-range derivation", Edge{From: b, To: a, Kind: KindQueries, Derivation: Derivation(9)}, "no derivation"},
		{"no kind", Edge{From: b, To: a, Derivation: Extracted}, "no kind"},
		{"source has no scheme", Edge{From: "GetUser", To: a, Kind: KindQueries, Derivation: Extracted}, "source"},
		{"target has no scheme", Edge{From: b, To: "users", Kind: KindQueries, Derivation: Extracted}, "target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewBuilder(testRoot).AddEdge(tt.edge)
			if err == nil {
				t.Fatal("AddEdge succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestAddNodeNormalizesPosition keeps rule 2 true at the seam where
// absolute paths enter: adapters get them from their upstream libraries,
// and a serialized position that names the build machine is the same bug as
// an ID that does.
func TestAddNodeNormalizesPosition(t *testing.T) {
	m := NewMinter(testRoot)
	id := mustID(m.GoID("github.com/x/app", "Load"))

	b := NewBuilder(testRoot)
	if err := b.AddNode(Node{ID: id, Kind: KindFunc, Name: "Load", Pos: Position{File: "/repo/app/load.go", Line: 3}}); err != nil {
		t.Fatal(err)
	}
	n, _ := b.Build().Node(id)
	if n.Pos.File != "app/load.go" {
		t.Errorf("Pos.File = %q, want app/load.go", n.Pos.File)
	}
}

// TestBuilderDoesNotAliasCallerMaps pins that a caller mutating its own map
// after reporting cannot reach into the graph.
func TestBuilderDoesNotAliasCallerMaps(t *testing.T) {
	m := NewMinter(testRoot)
	id := mustID(m.SQLID(SQLTable, "users"))

	attrs := map[string]string{"owner": "app"}
	b := NewBuilder(testRoot)
	if err := b.AddNode(Node{ID: id, Kind: KindTable, Name: "users", Attrs: attrs}); err != nil {
		t.Fatal(err)
	}
	attrs["owner"] = "someone else"

	n, _ := b.Build().Node(id)
	if n.Attrs["owner"] != "app" {
		t.Errorf("Attrs[owner] = %q, want app", n.Attrs["owner"])
	}
}

func TestMinterIsSharedWithAdapters(t *testing.T) {
	b := NewBuilder(testRoot)
	if b.Minter().Root() != testRoot {
		t.Errorf("Minter().Root() = %q, want %q", b.Minter().Root(), testRoot)
	}
}

func TestEmptyGraph(t *testing.T) {
	g := NewBuilder(testRoot).Build()
	if g.NodeCount() != 0 || g.EdgeCount() != 0 {
		t.Errorf("empty build has %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
	}
	if g.Stats().GeneratedShare() != 0 {
		t.Errorf("GeneratedShare() = %v, want 0", g.Stats().GeneratedShare())
	}
	for range g.Nodes() {
		t.Error("Nodes() yielded from an empty graph")
	}
	for range g.Edges() {
		t.Error("Edges() yielded from an empty graph")
	}
}

// TestCompareNodesIsATotalOrder pins the ordering the merge rests on.
// Order-independence is only as good as this comparison being total: if two
// contributions ever compare equal on every field they differ in, the fold
// becomes arrival-ordered again and the byte-identical guarantee quietly
// stops holding.
func TestCompareNodesIsATotalOrder(t *testing.T) {
	base := Node{Kind: KindMessage, Name: "User", Pos: Position{File: "a.proto", Line: 2, Col: 3}, Attrs: map[string]string{"k": "1"}}

	differs := []struct {
		name string
		with func(Node) Node
	}{
		{"kind", func(n Node) Node { n.Kind = KindType; return n }},
		{"name", func(n Node) Node { n.Name = "Zebra"; return n }},
		{"file", func(n Node) Node { n.Pos.File = "z.proto"; return n }},
		{"line", func(n Node) Node { n.Pos.Line = 99; return n }},
		{"col", func(n Node) Node { n.Pos.Col = 99; return n }},
		{"generated", func(n Node) Node { n.Generated = true; return n }},
		{"test", func(n Node) Node { n.Test = true; return n }},
		{"positionless", func(n Node) Node { n.Pos = Position{}; return n }},
		{"attrs", func(n Node) Node { n.Attrs = map[string]string{"k": "2"}; return n }},
	}

	if got := compareNodes(base, base); got != 0 {
		t.Errorf("compareNodes(n, n) = %d, want 0", got)
	}
	for _, d := range differs {
		t.Run(d.name, func(t *testing.T) {
			other := d.with(base)
			forward, backward := compareNodes(base, other), compareNodes(other, base)
			if forward == 0 {
				t.Fatalf("nodes differing in %s compare equal", d.name)
			}
			if forward != -backward {
				t.Errorf("comparison is not symmetric: %d forward, %d backward", forward, backward)
			}
		})
	}
}

// TestTestIsUnionedInTheMerge pins #102: a node's Test is a union, as ADR
// 004 rule 4 says, whichever contribution arrives first — a positionless
// one included, which used to sort first and win with Test=false — and a
// node that knows its position is folded from that position.
func TestTestIsUnionedInTheMerge(t *testing.T) {
	m := NewMinter(testRoot)
	id := mustID(m.GoID("example.com/p", "Helper"))
	at := Position{File: "p/helper_test.go", Line: 3, Col: 6}
	for _, pair := range [][2]Node{
		{{ID: id, Kind: KindFunc, Name: "Helper", Pos: at, Test: true}, {ID: id, Kind: KindFunc, Name: "Helper", Pos: at}},
		{{ID: id, Kind: KindFunc, Name: "Helper", Pos: at, Test: true}, {ID: id, Kind: KindFunc, Name: "Helper"}},
	} {
		var got []Node
		for _, order := range [][2]Node{pair, {pair[1], pair[0]}} {
			b := NewBuilder(testRoot)
			addAll(t, b, order[:], nil)
			g := b.Build()
			n, _ := g.Node(id)
			got = append(got, n)
			if !n.Test || n.Pos != at {
				t.Errorf("arrival %v: Test = %v, Pos = %v; want true, %v", order, n.Test, n.Pos, at)
			}
		}
		if !reflect.DeepEqual(got[0], got[1]) {
			t.Errorf("the arrival order decides the node: %+v vs %+v", got[0], got[1])
		}
	}
}

// TestAttrConflictResolvesTheSameWayBothWays covers the half of the merge
// that only shows up when two adapters disagree: whichever reported first,
// the same value has to survive.
func TestAttrConflictResolvesTheSameWayBothWays(t *testing.T) {
	m := NewMinter(testRoot)
	id := mustID(m.SQLID(SQLTable, "users"))

	for _, order := range [][]string{{"alpha", "omega"}, {"omega", "alpha"}} {
		b := NewBuilder(testRoot)
		addAll(t, b, []Node{
			{ID: id, Kind: KindTable, Name: "users", Attrs: map[string]string{"owner": order[0]}},
			{ID: id, Kind: KindTable, Name: "users", Attrs: map[string]string{"owner": order[1]}},
		}, nil)
		g := b.Build()

		n, _ := g.Node(id)
		if n.Attrs["owner"] != "alpha" {
			t.Errorf("%v: Attrs[owner] = %q, want alpha", order, n.Attrs["owner"])
		}
		if len(g.Conflicts()) != 1 || g.Conflicts()[0].Discarded != "omega" {
			t.Errorf("%v: Conflicts() = %v", order, g.Conflicts())
		}
	}
}

// TestNewIDGuardsTheSchemeSeparator covers the invariant [ID.Scheme]
// depends on. No current minting function can reach it — each rejects the
// characters first — but it is what a sixth scheme would land on.
func TestNewIDGuardsTheSchemeSeparator(t *testing.T) {
	if _, err := newID(SchemeGo, "a://b"); err == nil {
		t.Error("newID accepted a body containing the scheme separator")
	}
	if _, err := newID(SchemeGo, ""); err == nil {
		t.Error("newID accepted an empty body")
	}
}

func TestCheckSlashPathRejectsTrailingSlash(t *testing.T) {
	if err := checkSlashPath("a/b/"); err == nil {
		t.Error("checkSlashPath accepted a trailing slash")
	}
}

// addProtoFile adds a .proto file node, a message node and the contains
// edge between them through bb.
func addProtoFile(t *testing.T, bb *Builder, file, msg string) {
	t.Helper()
	fid, err := bb.Minter().ProtoFileID(file)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := bb.Minter().ProtoID(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		bb.AddNode(Node{ID: fid, Kind: KindProtoFile, Name: "x.proto", Pos: Position{File: file}}),
		bb.AddNode(Node{ID: mid, Kind: KindMessage, Name: "M"}),
		bb.AddEdge(Edge{From: fid, To: mid, Kind: KindContains, Derivation: Extracted}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestSecondaryBuilderSharesTheGraph pins that a secondary root's Builder
// writes into the same graph, that the same relative path in two roots is
// two nodes, that a declaration-keyed ID from either root is one node,
// and that a label cannot be used twice.
func TestSecondaryBuilderSharesTheGraph(t *testing.T) {
	b := NewBuilder("/repo")
	sb, err := b.Secondary("schemas", "/elsewhere/schemas")
	if err != nil {
		t.Fatal(err)
	}
	addProtoFile(t, b, "/repo/proto/x.proto", "a.M")
	addProtoFile(t, sb, "/elsewhere/schemas/proto/x.proto", "a.M")
	g := b.Build()
	primary, ok1 := g.Node("proto://file/proto/x.proto")
	secondary, ok2 := g.Node("proto://file/@schemas/proto/x.proto")
	if !ok1 || !ok2 || primary.Pos.File != "proto/x.proto" || secondary.Pos.File != "@schemas/proto/x.proto" {
		t.Errorf("file nodes: %v %+v / %v %+v", ok1, primary.Pos, ok2, secondary.Pos)
	}
	if g.NodeCount() != 3 || len(g.In("proto://a.M")) != 2 || len(g.Dropped()) != 0 {
		t.Errorf("nodes = %d, in-edges of a.M = %d, dropped = %d; want 3, 2, 0", g.NodeCount(), len(g.In("proto://a.M")), len(g.Dropped()))
	}
	if _, err := b.Secondary("schemas", "/somewhere/else"); err == nil {
		t.Error("a label was accepted twice")
	}
}

// TestLayersDropTogether pins #56's mechanism: contributions carry the
// layer of the Builder that made them, Drop removes one layer's and no
// other's, a secondary root's Builder keeps its layer, and an untagged
// contribution is the "" layer.
func TestLayersDropTogether(t *testing.T) {
	b := NewBuilder("/repo")
	a, c := b.Layer("a"), b.Layer("c")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.AddNode(Node{ID: "sql://table/t", Kind: KindTable, Name: "t"}))
	must(a.AddNode(Node{ID: "go://m#Shared", Kind: KindFunc, Name: "Shared"}))
	must(c.AddNode(Node{ID: "go://m#Shared", Kind: KindFunc, Name: "Shared"}))
	must(c.AddNode(Node{ID: "go://m#Only", Kind: KindFunc, Name: "Only"}))
	must(a.AddEdge(Edge{From: "sql://table/t", To: "go://m#Shared", Kind: KindGenerates, Derivation: Declared}))
	must(c.AddEdge(Edge{From: "go://m#Only", To: "go://m#Shared", Kind: KindCalls, Derivation: Resolved}))
	must(b.AddNode(Node{ID: "md://x.md", Kind: KindDocument, Name: "x"}))
	sb, err := a.Secondary("s", "/elsewhere")
	must(err)
	must(sb.AddNode(Node{ID: "proto://file/@s/x.proto", Kind: KindProtoFile, Name: "x"}))

	if g := b.Build(); g.NodeCount() != 5 || len(g.Out("sql://table/t")) != 1 {
		t.Fatalf("before drop: %d nodes", g.NodeCount())
	}
	nodes, edges := b.Drop("a")
	if nodes != 3 || edges != 1 {
		t.Errorf("Drop(a) = %d nodes, %d edges; want 3 (t, Shared, the secondary's file) and 1", nodes, edges)
	}
	g := b.Build()
	for id, want := range map[ID]bool{"sql://table/t": false, "proto://file/@s/x.proto": false, "go://m#Shared": true, "go://m#Only": true, "md://x.md": true} {
		if _, ok := g.Node(id); ok != want {
			t.Errorf("after Drop(a): %s present = %v, want %v", id, ok, want)
		}
	}
	if len(g.Out("go://m#Only")) != 1 || len(g.Dropped()) != 0 {
		t.Errorf("c's edge: %d out, %d dropped", len(g.Out("go://m#Only")), len(g.Dropped()))
	}
	checkDropTwice(t, b)
}

func checkDropTwice(t *testing.T, b *Builder) {
	t.Helper()
	if n, _ := b.Drop(""); n != 1 {
		t.Errorf("Drop(\"\") = %d, want the one untagged node", n)
	}
	if n, _ := b.Drop("a"); n != 0 {
		t.Errorf("Drop(a) again = %d", n)
	}
}

func TestWithMinterKeepsTheLayer(t *testing.T) {
	b := NewBuilder("/repo")
	if b.WithMinter(NewMinter("/other")).Minter().Root() != "/other" || b.Layer("z").WithMinter(NewMinter("/o")).layer != "z" {
		t.Error("WithMinter changed the layer or kept the old minter")
	}
}

// TestEdgeTestIsDerived: an edge's test-ness is computed from its
// endpoints when the graph is built, not taken from what the adapter
// reported — a layer emitting an edge into another layer's test
// declaration cannot leave it out, and one that guesses cannot get it
// wrong (ADR 001).
func TestEdgeTestIsDerived(t *testing.T) {
	b := NewBuilder("")
	m := b.Minter()
	id := func(decl string) ID {
		v, err := m.GoID("example.com/p", decl)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	prod, tst := id("Prod"), id("Helper")
	for _, n := range []Node{
		{ID: prod, Kind: KindFunc, Name: "Prod"},
		{ID: tst, Kind: KindFunc, Name: "Helper", Test: true},
	} {
		if err := b.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	// The middle edge claims to be a test edge and is not one; the last
	// claims not to be and is. Both claims are overwritten.
	for _, e := range []Edge{
		{From: tst, To: prod, Kind: KindCalls, Derivation: Resolved},
		{From: prod, To: prod, Kind: KindCalls, Derivation: Resolved, Test: true},
		{From: prod, To: tst, Kind: KindReferences, Derivation: Resolved, Test: false},
	} {
		if err := b.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	g := b.Build()
	for _, want := range []struct {
		from ID
		kind EdgeKind
		to   ID
		test bool
	}{
		{tst, KindCalls, prod, true},   // from a test declaration
		{prod, KindCalls, prod, false}, // neither end is one, despite the claim
		{prod, KindReferences, tst, true},
	} {
		var got Edge
		found := false
		for _, e := range g.Out(want.from) {
			if e.Kind == want.kind && e.To == want.to {
				got, found = e, true
			}
		}
		if !found || got.Test != want.test {
			t.Errorf("edge %s -%s-> %s: Test = %v, %v; want %v", want.from, want.kind, want.to, got.Test, found, want.test)
		}
	}
}

// docEdgeIsTest: a document's edge into an external package marked test
// does not decide the marker, but it touches a test node, so it is a test
// edge, in every index of the graph (#133).
func docEdgeIsTest(t *testing.T, g *Graph, doc, to ID) {
	t.Helper()
	var all []Edge
	for e := range g.Edges() {
		all = append(all, e)
	}
	seen := 0
	for _, list := range [][]Edge{all, g.Out(doc), g.In(to)} {
		for _, e := range list {
			if e.From != doc || e.To != to {
				continue
			}
			seen++
			if !e.Test {
				t.Errorf("the document's edge into a test node is a production edge: %+v", e)
			}
		}
	}
	if seen != 3 {
		t.Errorf("the document's edge appears %d times across Edges, Out and In, want 3", seen)
	}
}

// TestExternalNodesOnlyTestCodeUses pins #128's external half: an external
// node is a test node when every edge into it from its own layer is a test
// edge. A production edge keeps it production, an edge from another layer
// (a document) does not count, and a node nothing reaches is left alone.
func TestExternalNodesOnlyTestCodeUses(t *testing.T) {
	m := NewMinter(testRoot)
	testFn := mustID(m.GoID("example.com/m/p", "TestX"))
	prodFn := mustID(m.GoID("example.com/m/p", "Run"))
	doc := mustID(m.MarkdownID("README.md"))
	ext := func(path string) ID { return mustID(m.GoID(path, "")) }
	b := NewBuilder(testRoot)
	nodes := []Node{
		{ID: testFn, Kind: KindFunc, Name: "TestX", Test: true},
		{ID: prodFn, Kind: KindFunc, Name: "Run"},
		{ID: doc, Kind: KindDocument, Name: "README.md"},
	}
	for _, p := range []string{"testing", "crypto/tls", "github.com/stretchr/testify/require", "unused"} {
		nodes = append(nodes, Node{ID: ext(p), Kind: KindPackage, Name: path.Base(p), Attrs: map[string]string{"external": "true"}})
	}
	addAll(t, b, nodes, []Edge{
		{From: testFn, To: ext("testing"), Kind: KindCalls, Derivation: Resolved},
		{From: testFn, To: ext("crypto/tls"), Kind: KindCalls, Derivation: Resolved},
		{From: prodFn, To: ext("crypto/tls"), Kind: KindCalls, Derivation: Resolved},
		{From: testFn, To: ext("github.com/stretchr/testify/require"), Kind: KindCalls, Derivation: Resolved},
		{From: doc, To: ext("github.com/stretchr/testify/require"), Kind: KindDocuments, Derivation: Inferred},
	})
	g := b.Build()
	for p, want := range map[string]bool{"testing": true, "crypto/tls": false, "github.com/stretchr/testify/require": true, "unused": false} {
		if n, _ := g.Node(ext(p)); n.Test != want {
			t.Errorf("%s: Test = %v, want %v", p, n.Test, want)
		}
	}
	docEdgeIsTest(t, g, doc, ext("github.com/stretchr/testify/require"))
}

// TestExternalTestMarkReachesDroppedEdges pins the dropped half of the mark
// above: a dropped edge into a node #128's rule marked test is a test edge
// too. The PXF document writes test on a dropped block as it does on a kept
// one (edgeBlock), so an edge left production into a node the same document
// calls test is a contradiction a reader can see; and the exact-tests build
// reads that flag to decide which dropped edges are a test's (#133).
func TestExternalTestMarkReachesDroppedEdges(t *testing.T) {
	m := NewMinter(testRoot)
	testFn := mustID(m.GoID("example.com/m/p", "TestX"))
	ghost := mustID(m.GoID("example.com/m/gone", "Vanished"))
	ext := mustID(m.GoID("testing", ""))

	b := NewBuilder(testRoot)
	addAll(t, b,
		[]Node{
			{ID: testFn, Kind: KindFunc, Name: "TestX", Test: true},
			{ID: ext, Kind: KindPackage, Name: "testing", Attrs: map[string]string{"external": "true"}},
		},
		[]Edge{
			{From: testFn, To: ext, Kind: KindCalls, Derivation: Resolved},
			{From: ghost, To: ext, Kind: KindCalls, Derivation: Resolved},
		},
	)
	g := b.Build()

	if n, _ := g.Node(ext); !n.Test {
		t.Fatalf("the external node is not a test node, so the case this pins does not arise")
	}
	dropped := g.Dropped()
	if len(dropped) != 1 || dropped[0].Reason != DropUnknownSource {
		t.Fatalf("Dropped() = %v, want one unknown_source", dropped)
	}
	if !dropped[0].Edge.Test {
		t.Errorf("the dropped edge into a test node is a production edge: %+v", dropped[0].Edge)
	}
}
