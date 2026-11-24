# ADR 009: Rust — the User's rust-analyzer, Through Its SCIP Index and LSP Lookups

**Status:** Accepted 2026-11-24 (#71). The owner decided these: that Rust is read,
which engine reads it, how alidade reaches that engine, that Cargo's build runs, and
that trait implementations come from rust-analyzer's symbol names. The layer's IDs,
its rule for what counts as a call, and the questions listed under "Open" are
#72's, and nothing is built yet.
**Date:** 2026-11-24
**Relates to:** [000](000-composition-boundary.md), [002](002-node-identity.md),
[003](003-corpus-scope.md), [004](004-generated-code.md),
[006](006-edge-provenance.md), [008](008-python-engine.md)

## Context

The owner asked for Rust (#71). Every layer so far links an existing analyzer into
the binary (ADR 000). For Rust, the only analyzer that resolves types is
rust-analyzer, and it is written in Rust.

Rust also adds a problem no other layer has: **part of the program exists only
after Cargo has built it.**
- **Build scripts** (`build.rs`) run before their crate compiles and write code into
  Cargo's output directory. prost-build turns `.proto` files into Rust this way, and
  the source pulls the result in with `include!(concat!(env!("OUT_DIR"), "/test.rs"))`.
- **Procedural macros** are compiled plugins that write code inside the compiler.
  `#[derive(thiserror::Error)]` writes a type's `Display` that way, and
  `#[derive(prost::Message)]` writes its encoding.

rust-analyzer sees both only when it runs Cargo first. By default it does: it "runs
build scripts [and] loads `OUT_DIR` values by running `cargo check` before
analysis" (its `analysis-stats --help`). The proc-macro server it uses ships with
the toolchain (`<sysroot>/libexec/rust-analyzer-proc-macro-srv`), because a
compiled macro must be loaded by code matching the compiler that built it.

**Measured 2026-11-24**, rust-analyzer 1.99.1 from rustup, macOS arm64, on two
repositories:
- **trendvidia/protowire-rust @ 91f60b5:** 10 crates, 48,896 lines, no `build.rs`.
- **trendvidia/prost-reflect @ 2d0e184:** 5 crates, 26,465 lines.
  `prost-reflect-tests` generates Rust from 8 `.proto` files in its `build.rs`.

Expressions whose type rust-analyzer could not infer (`analysis-stats`):

| repository | with Cargo's build | without it | without the standard library's source |
|---|---|---|---|
| protowire-rust | 10 of 174,562 | 58 of 174,235 | 17,283 of 64,137 (26%) |
| prost-reflect | 97 of 82,735 | 1,812 of 71,251 (2%) | not run |

- **What protowire-rust loses without the build:** almost entirely `err.to_string()`
  on error types whose `Display` the `thiserror` derive writes.
- **What prost-reflect loses:** the losses cluster in the files that use the
  generated types (`prost-reflect-tests/src/decode.rs` 358, `json.rs` 317).
- **The standard library's source is required.** It is rustup's `rust-src`
  component.

## Decision

**alidade reads Rust through the user's own rust-analyzer, as a separate process.**
It runs rust-analyzer's SCIP export for the whole workspace, then asks rust-analyzer's
LSP server only about the symbols the index leaves without a location. Cargo's build
runs as part of that.

### Why the user's copy, and not an embedded engine

Embedding (rust-analyzer's `ra_ap_*` crates through cgo, as CPython is in ADR 008)
would still need the user's Cargo:
- to run build scripts;
- to compile proc macros;
- and, for the macros, to provide the matching server that loads them.

Once the user's toolchain is required anyway, an embedded engine buys little. Its
cost would be a Rust build of our own for four targets, against libraries that
publish weekly with no stable API.

rust-analyzer and the standard library's source are rustup components
(`rustup component add rust-analyzer rust-src`), not part of every Cargo install.
The machine measured here had Cargo without either, so the layer has to say when
they are missing (#72).

This is the layer ADR 000 rule 2 says needs an ADR of its own: one reached over a
process boundary. ADR 000 is amended with it.

### Why SCIP, with LSP lookups

SCIP is rust-analyzer's one-shot index: a protobuf file
(`rust-analyzer scip <workspace>`) listing every definition and every reference.
- **It is one pass, and its output is stable.** On protowire-rust it took 6.5 s and
  6.8 s and 1.26 GB, and the two outputs were identical byte for byte. On
  prost-reflect it took 13.9 s and 1.33 GB.
- **Calls can be placed.** Every definition carries its full source range (14,043
  of 14,043 on protowire-rust), so each reference is attributed to the function
  holding it.
- **An edit's effect shows in the index.** On a copy of protowire-rust, a blank line
  added to one file changed that file's entry only (1 of 140). Renaming
  `unmarshal_full` at its definition changed 35 of 140: its file and the 34 that
  reference it (36 files name it as text).
- **Indexing one crate costs the same as the whole workspace.** Pointing it at
  `crates/protowire-envelope` took 6.7 s and 1.26 GB, and only the output shrank.

SCIP leaves out one thing the layer needs: **Cargo's generated files**. They are not
documents in the index. A reference to a generated type points at a symbol with no
location: `Scalars` at `prost-reflect-tests/src/decode.rs:22` resolves to
`rust-analyzer cargo prost-reflect-tests 0.0.0 proto/Scalars#`. The symbol doesn't
say which generated file the type came from either, since `lib.rs` includes both
`test.rs` and `test2.rs` into the same `mod proto`.

**LSP fills that gap.** `textDocument/definition` on that same `Scalars`, and at
`:67`, returned `target/debug/build/prost-reflect-tests-*/out/test.rs` line 7, which
is `pub struct Scalars`.
- **The layer asks once per symbol, not once per use.** It asks only about symbols
  the index lists without a definition, from one place each is referenced.
- **The number of such symbols, measured:** prost-reflect's index lists 93, and
  protowire-rust's lists 0.

LSP alone was the other option. It updates incrementally, but rebuilding what SCIP
gives in one pass would take many requests per file. That cost was not measured, and
it is what would reopen the choice (below).

### Cargo's build runs

Indexing runs the build scripts of the repository and of its dependencies, and
compiles their proc macros, so the layer sees what they generate. This executes their
code on the machine, as `cargo build` does. It can also need tools the build needs:
prost-build needs `protoc`.

A build script that fails leaves its crate's generated code unread, and the report
says which crate. prost-reflect's conformance crate fails without its `protobuf` git
submodule ("protobuf sources are not checked out"). Its `main.rs` keeps 85 unknown
expressions with the build or without it.

### Trait implementations come from rust-analyzer's symbol names

The SCIP index carries no relationships at all: 0 on protowire-rust, whose source has
61 implementations of the workspace's own traits (counted by text). What it does have
is the symbol rust-analyzer gives an implementing method, which names the type and
the trait: `impl#[Fake][Validator]validate().`. SCIP's published symbol grammar
splits that name into its parts. Which part is the type and which the trait is
rust-analyzer's own convention, which is why a test pins it (below).

The trait is rust-analyzer's resolution; the name only carries it. Which derivation
the edge gets is #72's (ADR 006).

### Generated types join their `.proto` messages

A prost-generated struct joins its `proto://` message by prost-build's documented
naming. This is the ADR 004 bridge a fourth time, after Go from SQL (sqlc), Go from
`.proto` (`gonames.go`) and TypeScript from `.proto` (protoc-gen-es, ADR 007).
- **The package comes from the file.** prost-build writes one file per `.proto`
  package and names it after the package: `test.rs` holds package `test`, and
  `my.package.rs` holds `my.package`.
- **The message comes from the type.** prost's naming rule gives the message for the
  struct. A nested message or enum sits in a module named after its parent:
  `complex_type::MyEnum` is `ComplexType.MyEnum`.
- **The proto side is already in the graph.** The proto layer's node ID is the
  message's full name (`proto://test.Scalars`), so no further lookup is needed.

The edge is `DECLARED`: a generator's documented rule, as for `.pb.go` (ADR 006).
Where a generated struct also states its full name, the layer checks the two agree and
reports a struct where they don't, rather than guessing (ADR 007 does the same with a
generated file's header). prost-reflect-build writes the full name next to every
struct (24 of 24 in prost-reflect: `#[prost_reflect(message_name = "test.Scalars")]`), and prost writes
`impl ::prost::Name` when `enable_type_names()` is set. None of prost-reflect's
generated files has the latter.

## Assumptions, each with the test #72 must add

- **rust-analyzer leaves generated files out of its SCIP index, and LSP's definition
  lands in them.** If a release starts indexing them, the LSP lookups become
  unnecessary. If LSP stops landing there, the `.proto` link is lost. A fixture with a
  `build.rs` that generates one type pins both.
- **An implementing method's symbol names its trait.** A fixture's `impl Trait for
  Type` must produce its `implements` edge, and fails the day the symbol grammar
  changes.
- **The answers depend on the user's rust-analyzer version.** The report records the
  version from the index's metadata (`tool_info`). A cache of rust-analyzer's answers,
  if one is ever kept, is keyed on it, as Python's cache is keyed on its engine (ADR
  008).

## Open (#72)

Node IDs (a new ADR 002 rule), with two cases to cover:
- generated types, whose files sit under a path holding a build hash;
- methods in impl blocks.

Also open:
- which references are calls, since SCIP has no call role;
- the derivation of `implements` edges taken from symbol names;
- what a machine without `rust-analyzer` or `cargo` gets;
- when `serve` answers;
- the oracle the edges are checked against.

## What would change it

- **A large workspace whose full SCIP run is too slow to repeat on every edit.** LSP's
  incremental updates would then weigh more than the cost of rebuilding SCIP's
  answers through it.
- **The symbols without a location turning out mostly not generated.** If most of
  prost-reflect's 93, or another repository's, are something else, the LSP lookups
  target the wrong thing. They were not classified.
- **rust-analyzer gaining a stable library API.** That would bring back embedding,
  and the pinned answers it gives.
