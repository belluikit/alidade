// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package ts is the frontend layer adapter of ADR 007: TypeScript,
// JavaScript and Svelte files as nodes at file granularity, the exports of
// protoc-gen-es output as nodes, and the edges that join a .proto message
// to the screens that use it.
//
// The adapter owns no parser (ADR 000). It reads the import clauses of
// each file — the one grammar it reads, import/export … from and
// import() — and esbuild's Go API resolves every specifier the way the
// repository's own build would: tsconfig paths, extension-less
// specifiers, index files, and SvelteKit's aliases read from
// svelte.config.js. The resolver rather than the bundler, deliberately:
// a bundler erases a type-only import and an import a component uses in
// markup alone, and both are dependencies here. Svelte components
// contribute their <script> blocks; markup is not read. What comes out:
//
//   - module nodes (.ts, .tsx, .js, .jsx) and component nodes (.svelte),
//     keyed by repo-relative path (ADR 002 rule 11); package nodes for
//     bare-specifier imports, never read, under ts://@pkg/ (#124);
//   - for a file protoc-gen-es wrote, one node per export (type, const,
//     enum), keyed by the export name, all marked generated;
//   - imports (RESOLVED): file → the file esbuild resolved the specifier
//     to; imports (EXTRACTED): file → package;
//   - contains (EXTRACTED): generated file → export;
//   - references (EXTRACTED): importing file → generated export, from
//     the names in the import clause. A clause the adapter cannot read
//     to names (a namespace or default import) leaves the file-level
//     edge and is counted;
//   - generates (DECLARED, via=protoc-gen-es): .proto file → generated
//     file, and proto message, enum or service → the exports written for
//     it, each stated by the generator's own "@generated from" markers.
//     The .proto file is found by the header's path as a unique suffix
//     among the repository's .proto files; no match, or two, is reported
//     and the file-level edge is not guessed.
//
// Skipped: node_modules, .svelte-kit, dist, build, .astro, testdata, the
// directories the root .gitignore names, and .d.ts files. A specifier
// esbuild resolves to neither a file nor a package is counted and gives
// no edge.
package ts
