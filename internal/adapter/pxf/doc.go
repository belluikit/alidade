// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package pxf is the .pxf layer adapter: documents, blocks and fields of
// Proto eXpressive Format files, and the edges that bind configuration to
// schema.
//
// The adapter owns no parser (ADR 000): every file goes through
// github.com/trendvidia/protowire-go/encoding/pxf, and the graph is a walk
// of the AST it returns. What comes out:
//
//   - pxf_file nodes, keyed by repo-relative path (ADR 002 rule 7); block
//     and field nodes keyed by the document's block path — nested names
//     joined with ".", a list element by its id or name field when it has
//     one and by its index otherwise, a directive by "@" and its name;
//   - contains (EXTRACTED): file → entry, block → entry;
//   - references (EXTRACTED): file → the message its @type names,
//     dataset → its row type, directive → the type it names — the edge
//     that joins configuration to the .proto layer; and field → the field
//     of the same document a "${path}" in its value names;
//   - references from each field and block to the .proto field it sets,
//     made by [LinkSchema] over the merged graph (#190): RESOLVED through
//     the document's @type or a typed directive's, DECLARED with
//     via=chain for a chameleon layer with no @type whose chain's other
//     layers declare one type;
//   - references (DECLARED) from a chameleon chain specification to each
//     layer file it loads, expanded per declared environment by
//     chameleon's own chain package (ONCE for the environment itself,
//     PER_ENV for every ancestor root-first, "${env}" substituted);
//     entries with an unresolvable context variable or a non-file source
//     load nothing. A literal path keeps EXTRACTED; a templated one is
//     DECLARED, since the environments it stands for are the expansion's.
//
// No value ever reaches the graph. A field node says what kind of value
// it holds (string, int, list, …), never the value, so a pxf.Secret is
// indistinguishable from any other string and nothing needs redacting
// (#4's proposed default). A @dataset is one node with its row count and
// column names; rows are not expanded. A document with a "${env}" or
// "${ctx.*}" reference records it as an attribute rather than an edge:
// those name a context, not a field.
package pxf
