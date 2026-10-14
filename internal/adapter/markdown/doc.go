// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package markdown is the Markdown layer adapter: documents and their
// heading hierarchy, fenced code, links, and the mentions that bind prose
// to the code graph.
//
// The adapter owns no parser (ADR 000): every file is parsed by
// github.com/yuin/goldmark and the graph is a walk of its AST. Front
// matter — a leading YAML block between "---" lines, which CommonMark does
// not know — is read for its keys before goldmark sees the rest.
//
// What comes out:
//
//   - document nodes keyed by repo-relative path (ADR 002 rule 7) and
//     section nodes keyed by their dotted heading path (rule 8), with the
//     collision policy rule 8 left to this layer: a second identical path
//     in one document gets "-2", a third "-3";
//   - block nodes for fenced code, "@fence" under their section, with the
//     language tag and line count;
//   - contains (EXTRACTED): document → section → subsection → fence;
//   - references (EXTRACTED): a link to a repo-relative path lands on that
//     file's node — a Markdown document, a .proto or .pxf file — or, for
//     a Go file or a directory, on the Go package there, minted from the
//     nearest go.mod (#68); the anchor, if any, is in attrs. A path that
//     is gone from a place that holds Go — a package directory or a
//     module root — is minted too, as the package it would be, so the
//     link is a dropped edge with reason unknown_target and the raw
//     link, which is how a stale link to code becomes visible. A path
//     missing from a directory of documents is a route of the site they
//     are published on and is left alone, as is a root-absolute link and
//     a directory that exists and holds no Go. A link to a heading of the
//     same document lands on the section when its flat anchor is unique
//     there;
//   - documents (INFERRED): from a section to a declaration a code span in
//     it names. This edge is not emitted during extraction — an adapter
//     sees no other layer — but by [Link], which the build runs over the
//     merged graph and which accepts a mention only when exactly one node
//     answers it. Prose is never scanned: an ADR that says Config in a
//     sentence does not reference the Config type; one that writes
//     `synth.Config` does, if there is one such thing. A fenced block is
//     read for one thing only, an RPC procedure path such as
//     "POST /api/pkg.v1.Service/Method", which names that rpc (#158).
//
// Changelog fragments are out by default: a directory named changelog.d
// or .changes (towncrier's and changie's conventions) holds one paragraph
// per change, which measured across the in-house repositories is most of
// the Markdown by count and little of it by content. Options.Changelogs
// puts them in; the report counts what was skipped either way.
package markdown
