# ADR 009: Rust — the User's rust-analyzer, Through Its SCIP Index and LSP Lookups

**Status:** Accepted 2026-11-24 (#71, #72). The owner decided these first: that Rust
is read, which engine reads it, how alidade reaches that engine, and that Cargo's
build runs. Then, on #72, the same day: the layer's IDs, its call rule, where trait
implementations come from (revising #71's answer after a measurement), a machine
without the toolchain, when `serve` answers, and the oracle ("The layer" below).
Nothing is built yet (#72).
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

### Trait implementations come from LSP and SCIP together

The SCIP index carries no relationships at all: 0 on protowire-rust, whose source has
61 implementations of the workspace's own traits (counted by text). What it does have
is the symbol rust-analyzer gives an implementing method, which names the type and
the trait: `impl#[Fake][Validator]validate().`. SCIP's published symbol grammar
splits that name into its parts. Which part is the type and which the trait is
rust-analyzer's own convention, which is why a test pins it (below).

The owner's first answer (#71) was to take implementations from those names alone. A
measurement on #72 changed it: neither source is complete. On protowire-pb's
`Message` trait, which has 55 implementations:
- **SCIP has no symbols for the 15 a `macro_rules!` macro generates.**
  `write_only!(Leaf, …)` in `tests/values_table.rs` is one of them.
- **LSP's `textDocument/implementation` has those 15, but misses the 5 written
  inside a function body,** all in tests (`impl Message for Holder` in
  `tests/wire_type.rs`, inside `fn mismatch_leaves_field_alone`).
- **SCIP names only the trait's simple name.** A workspace trait can share it with a
  foreign one: protowire-rust has its own `Message`, as prost does. No SCIP
  occurrence on an `impl … for` line names the trait's full symbol.

So the layer asks LSP for the implementations of each workspace trait, one request
per trait (5 on protowire-rust). That gives the exact trait, including
macro-generated implementations. SCIP's names then add the implementations LSP
missed. Such an implementation is joined only when exactly one workspace trait has
that simple name; otherwise it gets no edge. Together the two cover all 55. The 55
were reconciled two ways:
- **By source text:** 42 `impl … Message for` lines, less 2 that aren't
  implementations (a different trait, and the macro's own definition), leave 40
  written by hand.
- **By LSP:** 50, which is 35 of those 40 plus the 15 macro expansions.

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

## The layer (decided 2026-11-24, the owner's calls on #72)

- **IDs** (ADR 002 rule 16): the file, then the item's path inside it, as for Python.
  `rust://crates/protowire-pb/src/wire.rs#Reader.skip`.
  - **Why not SCIP's symbols, or the crate's module path.** SCIP's symbols are per
    Cargo package, not per compiled crate. Integration tests, benches, `build.rs` and
    `main.rs` all share one root, so 76 of protowire-rust's 3,558 defined symbols
    collide (`desc()` is defined in 10 test files), and 5 of prost-reflect's 1,959.
    A crate-and-module ID would need a segment for each test, bench, bin and build
    script. It would also make one declaration several nodes: 6 of protowire-pxf's
    test crates compile the same `tests/common/mod.rs`.
  - **A trait's method carries the trait:** `Packed[Message].encode_to`. prost-reflect's
    `DescriptorError` has `fmt` for both `Debug` and `Display`, the one type and
    method name implemented for two traits in either repository.
  - **A generated type anchors at the file holding its `include!`:**
    `rust://prost-reflect-tests/src/lib.rs#proto.Scalars`. The generated file's own
    path holds a build hash, which ADR 002 forbids in an ID.
- **Calls** (ADR 006 rule 9): a reference to a workspace function, inside a function
  body, followed by `(` or `::<`, is a `calls` edge from the innermost function
  holding it. A reference to a function used as a value is a `references` edge, as in
  Python. Measured on protowire-rust's 7,306 references to its own functions: 6,855
  are followed by `(` or `::<`, 428 are imports, and 23 are functions used as values
  (`.map(row)`, `Some(&deny_all)`, `LazyLock::new(ValidationCache::new)`). The rule
  reads one token of source text after a reference, a read as narrow as the
  TypeScript layer's import clauses (ADR 007).
- **Implementations:** from LSP and SCIP together, as above. RESOLVED (ADR 006
  rule 9).
- **A machine without the toolchain:** if `cargo`, `rust-analyzer` or the standard
  library's source is missing, each Rust file is a node marked `calls_unavailable`,
  and the report names what is missing and the `rustup` command that adds it. The
  standard library's source counts: without it rust-analyzer silently loses 26% of
  expression types.
- **`serve` waits for the index.** The first answer, and the first after a Rust edit,
  wait for SCIP to finish: 6.5 s on protowire-rust and 13.9 s on prost-reflect, for
  the whole workspace each time. An answer is then never stale and never partial
  (ADR 005).
- **The oracle is rustc.** Each crate is compiled to LLVM IR at opt-level 0, whose
  direct calls are rustc's own resolution, and compared with the layer's call
  edges. rustc is an engine independent of rust-analyzer. Probed on protowire-pb
  (2.7 s): on the 43 functions both sides had, 65 edges agree, rustc had 0 the rule
  lacked, and the rule had 2 rustc lacked.
  - One of the 2 is a call through a generic trait bound (`write_message` to
    `Message::encode_to`), which rustc compiles only once instantiated.
  - The other, `Reader::new` to `Limits::default`, is not explained yet.

  Its limits:
  - it sees only the functions rustc compiles as they are (43 of 81 callers here;
    generic ones only when instantiated);
  - a call through `dyn` is an indirect call it cannot name;
  - rustc 1.99's default symbol format needs a decoder. The probe used the legacy
    format, a nightly-only flag, through `RUSTC_BOOTSTRAP=1`.

Not in this version: edges for `use` declarations, and whether a call to a trait's
method also reaches that trait's implementations, as a TypeScript call through an
interface does (ADR 007). A call through a trait method goes to the method as
rust-analyzer resolves it.

## Assumptions, each with the test #72 must add

- **rust-analyzer leaves generated files out of its SCIP index, and LSP's definition
  lands in them.** If a release starts indexing them, the LSP lookups become
  unnecessary. If LSP stops landing there, the `.proto` link is lost. A fixture with a
  `build.rs` that generates one type pins both.
- **Implementations come from both sources.** A fixture with one implementation
  written by a `macro_rules!` macro and one inside a function body must give both
  `implements` edges. It fails the day either source stops supplying its half, or
  the symbol grammar changes.
- **A call is a reference followed by `(` or `::<`.** A fixture calling a function,
  passing it as a value, and calling it with a turbofish must give two `calls` edges
  and one `references` edge.
- **A missing toolchain is reported, never silent.** A test with `rust-analyzer`, and
  separately `rust-src`, absent from the path must give file nodes marked
  `calls_unavailable`, and a report naming what is missing.
- **The answers depend on the user's rust-analyzer version.** The report records the
  version from the index's metadata (`tool_info`). A cache of rust-analyzer's answers,
  if one is ever kept, is keyed on it, as Python's cache is keyed on its engine (ADR
  008).

## What would change it

- **A large workspace whose full SCIP run is too slow to repeat on every edit.** LSP's
  incremental updates would then weigh more than the cost of rebuilding SCIP's
  answers through it. A wait of minutes would also turn `serve` towards answering
  in the background, with Rust `pending`.
- **The symbols without a location turning out mostly not generated.** If most of
  prost-reflect's 93, or another repository's, are something else, the LSP lookups
  target the wrong thing. They were not classified.
- **rust-analyzer gaining a stable library API.** That would bring back embedding,
  and the pinned answers it gives.
