// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package proto is the .proto layer adapter: it compiles a repository's
// .proto files through github.com/trendvidia/protocompile and emits the
// schema nodes and edges of the graph.
//
// The adapter owns no parser (ADR 000). Files are compiled to descriptors
// by protocompile; Go type names for the generates edge come from
// google.golang.org/protobuf/compiler/protogen, the code protoc-gen-go
// itself uses, rather than from a naming rule written here.
//
// What comes out, for every .proto file under the repository root:
//
//   - proto_file, message, field, enum, enum_value, service and rpc nodes,
//     keyed by descriptor full name (ADR 002), the file by its
//     repo-relative path (rule 7);
//   - contains (EXTRACTED): file → declaration, message → field and nested
//     declaration, enum → value, service → rpc;
//   - imports (EXTRACTED): file → file;
//   - references (RESOLVED): field → the message or enum it is typed as,
//     rpc → its input and output;
//   - generates (DECLARED): message and enum → the Go type protoc-gen-go
//     emits for it, when the file names a go_package. Determined by the
//     generator's documented rule applied to a written option (ADR 006);
//     the edge resolves only when the Go layer saw that type, and drops
//     otherwise.
//
// Import paths follow buf: a file under a buf.yaml module is addressed
// relative to that module, and any other file relative to the repository
// root. An import that no file in the repository answers — a well-known
// type, a registry module, a shared schema pxf build bundles — lands on an
// external file node marked external=true, the way the Go layer lands
// boundary calls on a package node, so the reference is kept without
// inventing a declaration. The bundled schemas (github.com/trendvidia/protos)
// compile, so a file importing one does; nothing of theirs becomes a node.
//
// A file that does not compile still has a node, marked with its first
// error, and nothing else: a schema the compiler rejects is not one to
// guess declarations for.
package proto
