// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package oracle

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/belluikit/alidade/internal/build"
	"github.com/belluikit/alidade/internal/graph"
)

// A _pb.ts's exports map to the declarations its embedded descriptor
// says they are: a message's schema and type, a nested message, an enum's
// schema and TypeScript enum, a service — whose type parameter's comments
// hold "=" — and the file to its .proto.
func TestPBFileExports(t *testing.T) {
	fd := &descriptorpb.FileDescriptorProto{
		Name: proto.String("x/v1/a.proto"), Package: proto.String("x.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("A"), NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("Inner")}}},
			{Name: proto.String("B")},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Kind")}},
		Service:  []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("S")}},
	}
	raw, err := proto.Marshal(fd)
	if err != nil {
		t.Fatal(err)
	}
	src := `export const file_x_v1_a: GenFile = /*@__PURE__*/
  fileDesc("` + base64.StdEncoding.EncodeToString(raw) + `");
export type A = Message<"x.v1.A"> & { };
export const ASchema: GenMessage<A> = /*@__PURE__*/
  messageDesc(file_x_v1_a, 0);
export const A_InnerSchema: GenMessage<A_Inner> = /*@__PURE__*/
  messageDesc(file_x_v1_a, 0, 0);
export const BSchema: GenMessage<B> = /*@__PURE__*/
  messageDesc(file_x_v1_a, 1);
export enum Kind { K0 = 0 }
export const KindSchema: GenEnum<Kind> = /*@__PURE__*/
  enumDesc(file_x_v1_a, 0);
export const S: GenService<{
  /** a = b, as a doc comment may say */
  get: { methodKind: "unary"; input: typeof ASchema; output: typeof BSchema; },
}> = /*@__PURE__*/
  serviceDesc(file_x_v1_a, 0);
`
	dir := t.TempDir()
	for name, body := range map[string]string{"gen/x/v1/a_pb.ts": src, "proto/x/v1/a.proto": "syntax = \"proto3\";\n"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := &TS{Files: map[graph.ID]bool{}}
	if err := ts.pbFiles(graph.NewMinter(dir), dir); err != nil {
		t.Fatal(err)
	}
	got := map[[2]graph.ID]bool{}
	for _, e := range ts.Generates {
		got[e] = true
	}
	f := "ts://gen/x/v1/a_pb.ts"
	for _, want := range [][2]graph.ID{
		{"proto://x.v1.A", graph.ID(f + "#A")}, {"proto://x.v1.A", graph.ID(f + "#ASchema")},
		{"proto://x.v1.A.Inner", graph.ID(f + "#A_InnerSchema")}, {"proto://x.v1.B", graph.ID(f + "#BSchema")},
		{"proto://x.v1.Kind", graph.ID(f + "#KindSchema")}, {"proto://x.v1.Kind", graph.ID(f + "#Kind")},
		{"proto://x.v1.S", graph.ID(f + "#S")}, {"proto://file/proto/x/v1/a.proto", graph.ID(f)},
	} {
		if !got[want] {
			t.Errorf("missing %v; have %v", want, ts.Generates)
		}
	}
	if len(ts.Generates) != 8 || ts.PBFiles != 1 {
		t.Errorf("%d generates, %d files", len(ts.Generates), ts.PBFiles)
	}
}

func TestPackageNameAndAccounting(t *testing.T) {
	for spec, want := range map[string]string{"@bufbuild/protobuf/codegenv2": "@bufbuild/protobuf", "node:fs": "node:fs", "$app/stores": "$app", "react": "react"} {
		if got := packageName(spec); got != want {
			t.Errorf("packageName(%q) = %q, want %q", spec, got, want)
		}
	}
	specs := []string{"./Row.svelte", "$lib/Form.svelte", "$app/navigation"}
	for to, want := range map[graph.ID]bool{
		"ts://app/src/lib/Row.svelte": true, "ts://app/src/lib/Form.svelte": true, "ts://@pkg/$app": true,
		"ts://app/src/lib/Other.svelte": false, "ts://@pkg/react": false,
	} {
		if got := accountedFor(to, specs); got != want {
			t.Errorf("accountedFor(%s) = %v, want %v", to, got, want)
		}
	}
}

// TypeScript's resolution of a small project agrees with the graph: a
// relative import, an alias from tsconfig paths and a package; a .svelte
// component (#171), a Node builtin and an ambient module; an import of
// nothing is reported and not compared, and a .json import is outside
// the layer. A dropped and a bogus oracle edge are caught.
func TestTSImportsAgree(t *testing.T) {
	tsPkg := os.Getenv("ALIDADE_TYPESCRIPT")
	if tsPkg == "" {
		// Any repository checked out beside alidade that installs one.
		found, _ := filepath.Glob("../../../*/node_modules/typescript/package.json")
		if len(found) == 0 {
			t.Skip("ALIDADE_TYPESCRIPT is unset and no repository beside alidade installs typescript: the import oracle is TypeScript")
		}
		tsPkg, _ = filepath.Abs(filepath.Dir(found[0]))
	}
	t.Setenv("ALIDADE_TYPESCRIPT", tsPkg)
	dir := write(t, tsProject)
	g, _, err := build.Build(dir, build.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	ts, err := Frontend(g, graph.NewMinter(dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	// react too: the scratch project installs no packages.
	if len(ts.OutOfScope) != 1 || !slices.Equal(ts.Unresolved, []string{"src/a.ts: react", "src/a.ts: ./gone"}) {
		t.Errorf("out of scope %v, unresolved %v", ts.OutOfScope, ts.Unresolved)
	}
	checkNamedImports(t, ts)
	r := CompareTS(g, ts)
	if r.Imports[0] == 0 || r.Imports[0] != r.Imports[1] || len(r.ExtraImports) != 0 {
		t.Fatalf("not exact:\n%s", r)
	}
	ts.Imports = append(ts.Imports[1:], [2]graph.ID{"ts://src/a.ts", "ts://src/nowhere.ts"})
	r = CompareTS(g, ts)
	if len(r.MissingImports) != 1 || len(r.ExtraImports) != 1 {
		t.Errorf("missing %v, extra %v; want one of each", r.MissingImports, r.ExtraImports)
	}
}

// tsProject is TestTSImportsAgree's scratch project.
var tsProject = map[string]string{
	"tsconfig.json":    `{"compilerOptions": {"module": "esnext", "moduleResolution": "bundler", "allowJs": true, "resolveJsonModule": true, "baseUrl": ".", "paths": {"$lib/*": ["src/lib/*"]}}}`,
	"src/a.ts":         "import { b } from './b';\nimport { c } from '$lib/c';\nimport React from 'react';\nimport Row from './Row.svelte';\nimport data from './data.json';\nimport fs from 'node:fs';\nimport { page } from '$app/state';\nimport gone from './gone';\n",
	"src/ambient.d.ts": "declare module '$app/state' { export const page: unknown; }\n",
	"src/b.ts":         "export const b = 1;\n",
	"src/lib/c.ts":     "export const c = 2;\n",
	"src/Row.svelte":   "<script lang=\"ts\">import { b } from './b';</script>\n",
	"src/data.json":    "{}\n",
}

// checkNamedImports checks the imports TypeScript's resolver does not
// answer alone (#171): a .svelte component, a Node builtin and an ambient
// module.
func checkNamedImports(t *testing.T, ts *TS) {
	t.Helper()
	want := map[[2]graph.ID]bool{
		{"ts://src/a.ts", "ts://src/Row.svelte"}: true, {"ts://src/a.ts", "ts://@pkg/node:fs"}: true, {"ts://src/a.ts", "ts://@pkg/$app"}: true,
	}
	for _, e := range ts.Imports {
		delete(want, e)
	}
	if len(want) != 0 || ts.Named["builtin"] != 1 || ts.Named["ambient"] != 1 {
		t.Errorf("TypeScript did not resolve %v; named %v", want, ts.Named)
	}
}
