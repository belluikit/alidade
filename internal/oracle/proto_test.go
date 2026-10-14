// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/trendvidia/alidade/internal/build"
	"github.com/trendvidia/alidade/internal/graph"
)

// protoFixture is a buf module of two files: one importing the other and
// a well-known type, with a map field, an enum, a service and an
// extension, and protoc-gen-go's type table for it (the file is ignored
// by the Go build; the oracle reads its text).
var protoFixture = map[string]string{
	"go.mod":   "module example.com/m\n\ngo 1.26\n",
	"buf.yaml": "version: v2\nmodules:\n  - path: proto\n",
	"proto/a/v1/a.proto": `syntax = "proto3";
package a.v1;
option go_package = "example.com/m/gen/a/v1;av1";
import "b/v1/b.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/descriptor.proto";
message A {
  map<string, b.v1.B> m = 1;
  google.protobuf.Timestamp t = 2;
  Kind k = 3;
}
enum Kind { K0 = 0; }
service S { rpc Get(A) returns (b.v1.B); }
extend google.protobuf.FieldOptions { string note = 50001; }
`,
	"proto/b/v1/b.proto": "syntax = \"proto3\";\npackage b.v1;\nmessage B { string x = 1; }\n",
	"gen/a/v1/a.pb.go": `//go:build ignore

package av1

var file_a_v1_a_proto_goTypes = []any{
	(Kind)(0),                     // 0: a.v1.Kind
	(*A)(nil),                     // 1: a.v1.A
	nil,                           // 2: a.v1.A.MEntry
	(*timestamppb.Timestamp)(nil), // 3: google.protobuf.Timestamp
}
`,
}

func buildProtoFixture(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc is not on PATH: the proto oracle is protoc")
	}
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for name, body := range protoFixture {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return g, dir
}

func pid(s string) graph.ID { return graph.ID("proto://" + s) }

// TestProtoOracleAgrees: protoc's view of the fixture agrees with the
// graph in every count: a map field refers to its value type and its
// entry message is not compared, a well-known type lands on its file's
// external node, an enum value is its enum's sibling, an extension
// refers to what it extends, and protoc-gen-go's table names the Go type.
func TestProtoOracleAgrees(t *testing.T) {
	g, dir := buildProtoFixture(t)
	p, err := Protoc(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 || len(p.Failed) != 0 || p.MapEntries != 1 || p.GoFiles != 1 {
		t.Fatalf("files %v, failed %v, map entries %d, go files %d", p.Files, p.Failed, p.MapEntries, p.GoFiles)
	}
	checkProtoFacts(t, p)
	r := CompareProto(g, p)
	if r.Decls[0] != r.Decls[1] || r.Imports[0] != r.Imports[1] || r.Refs[0] != r.Refs[1] || r.Generates[0] != r.Generates[1] ||
		len(r.ExtraDecls)+len(r.ExtraImports)+len(r.ExtraRefs)+len(r.ExtraKinds)+len(r.WrongGenerates) != 0 {
		t.Errorf("not exact:\n%s", r)
	}
	if r.Imports[0] != 3 || r.Generates[0] != 2 {
		t.Errorf("imports %v, generates %v: want 3 and 2 compared", r.Imports, r.Generates)
	}
}

// checkProtoFacts checks what the oracle read from the fixture.
func checkProtoFacts(t *testing.T, p *Proto) {
	t.Helper()
	for id, kind := range map[graph.ID]graph.Kind{
		pid("a.v1.A.m"): graph.KindField, pid("a.v1.K0"): graph.KindEnumValue,
		pid("a.v1.S.Get"): graph.KindRPC, pid("a.v1.note"): graph.KindField,
	} {
		if p.Decls[id] != kind {
			t.Errorf("decl %s = %q, want %s", id, p.Decls[id], kind)
		}
	}
	for _, want := range [][2]graph.ID{
		{pid("a.v1.A.m"), pid("b.v1.B")},
		{pid("a.v1.A.t"), pid("file/google/protobuf/timestamp.proto")},
		{pid("a.v1.note"), pid("file/google/protobuf/descriptor.proto")},
		{pid("a.v1.S.Get"), pid("b.v1.B")},
	} {
		if !containsEdge(p.Refs, want) {
			t.Errorf("no ref %v in %v", want, p.Refs)
		}
	}
	if !containsEdge(p.Generates, [2]graph.ID{pid("a.v1.A"), "go://example.com/m/gen/a/v1#A"}) || len(p.Generates) != 2 {
		t.Errorf("generates %v", p.Generates)
	}
}

// TestProtoOracleCatchesDisagreement: an edge the graph lacks is missing,
// one the oracle lacks is extra, and a Go type other than the graph's is
// wrong — the comparison is not agreeing by construction.
func TestProtoOracleCatchesDisagreement(t *testing.T) {
	g, dir := buildProtoFixture(t)
	p, err := Protoc(graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	dropped := p.Refs[0]
	p.Refs = append(p.Refs[1:], [2]graph.ID{pid("a.v1.A.k"), pid("b.v1.B")})
	p.Decls[pid("a.v1.Ghost")] = graph.KindMessage
	for i, e := range p.Generates {
		if e[0] == pid("a.v1.A") {
			p.Generates[i][1] = "go://example.com/m/gen/a/v1#Other"
		}
	}
	r := CompareProto(g, p)
	if len(r.MissingRefs) != 1 || len(r.ExtraRefs) != 1 || r.ExtraRefs[0] != string(dropped[0])+" -> "+string(dropped[1]) {
		t.Errorf("missing refs %v, extra refs %v; want the bogus one missing and %v extra", r.MissingRefs, r.ExtraRefs, dropped)
	}
	if len(r.MissingDecls) != 1 || len(r.WrongGenerates) != 1 {
		t.Errorf("missing decls %v, wrong generates %v", r.MissingDecls, r.WrongGenerates)
	}
}

func containsEdge(edges [][2]graph.ID, e [2]graph.ID) bool {
	for _, x := range edges {
		if x == e {
			return true
		}
	}
	return false
}
