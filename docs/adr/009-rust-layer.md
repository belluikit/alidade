# ADR 009: Rust — the User's rust-analyzer, Through Its SCIP Index and LSP Lookups

**Status:** Accepted 2026-11-24 (#71, #72). The owner decided these first: that Rust
is read, which engine reads it, how alidade reaches that engine, and that Cargo's
build runs. Then, on #72, the same day: the layer's IDs, its call rule, where trait
implementations come from (revising #71's answer after a measurement), a machine
without the toolchain, when `serve` answers, and the oracle ("The layer" below).
Built so far: file nodes, declarations, calls, references and implementations,
a machine without the toolchain, and the code Cargo's build generates, joined to
its `.proto` messages (#72, #109); the items, calls and implementations of a
hand-written file an `include!` reads (#84, #88); checked-in prost-build output
joined to its messages (#89); failing build scripts reported (#85); test code
marked (#106, #112); and the public paths of a library's items (#116).
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

*Built 2026-11-25 (#85), the owner's decisions recorded there.* The index says
nothing when a script fails: `rust-analyzer scip` exits 0 and logs nothing of it,
and Cargo keeps no record under `target/`. The server does, so it is started on
every workspace with a build script. A server costs about 3.4 s on prost-reflect,
paid only where no other step starts one. Its log, at `RA_LOG=error`, names each
package (failed to run custom build command for `a v0.1.0 (…)`) and holds what
the script printed. Each failed package is reported at its `Cargo.toml` with its exit
and the last lines of its output, backtraces left out. One log entry can name
several packages, in an order that varies between runs, so they are sorted. The
server can run the build scripts twice, logging a package twice; it is reported once,
as the first entry says (macOS CI, PR #134). That
line is log text, not an API: when it is missing, the server's status message is
reported alone. On prost-reflect: `the build script of
prost-reflect-conformance-tests v0.0.0 failed (exit status: 1)`, with the message
above.
A dependency from outside the repository has no `Cargo.toml` in the tree, so its
failure is reported at the workspace's, naming it (#117): `the build script of
dependency regdep v0.1.0 failed`. Cargo's home is written `$CARGO_HOME` in what it
printed. A dependency's failure is reported in every workspace a step starts the
server on (#127, the owner's pick of option b): one with a build script of its own,
one that declares a trait (the implementations step), and one with an `include!` or
a call into generated code. A workspace with none of these starts no server, and a
dependency's failure there is not reported. Measured 2026-11-26: that is 2 of
tokio's 3 workspaces (its fuzz crates, whose one outside dependency, libfuzzer-sys,
writes no Rust) and none of jj's, and a server on every workspace would cost tokio
5.9 s (+17%) on every build. Failing dependencies changed no edge on tokio (serde)
and 8 of jj's 45,817 (thiserror's `source()` methods), all in workspaces that report
them. `TestDependencyFailureWhereServerRuns` pins both sides.

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
- **An implementing method's symbol names only the trait's simple name.** A
  workspace trait can share it with a foreign one: protowire-rust has its own
  `Message`, as prost does.

  *Corrected 2026-11-24 (#72):* this said no SCIP occurrence on an `impl … for`
  line names the trait's full symbol. That came from a probe that read only the
  last of the lines it was given. The header does carry both, even inside a
  function body: `impl Message for Holder` in `tests/wire_type.rs` has
  `codec/Message#` and `Holder#`.

  *Amended 2026-11-24 (#72, the owner's decision):* the layer reads that header
  for an implementation inside a function body, where it had joined by name:
  the exact trait, at the cost of reading the header's lines, which ADR 006
  rule 9 now allows. The nearest header above a method that names its trait is
  its impl's; one nested in an earlier method of the same impl names another
  trait and is passed over. The fixture pins a header whose trait is on its
  third line, a header nested in a method, and fmt::Display beside a workspace
  Display.

So the layer asks LSP for the implementations of each workspace trait, one request
per trait (5 on protowire-rust). That gives the exact trait, including
macro-generated implementations. SCIP's names then add the implementations LSP
missed, those inside a function body, and only those: elsewhere LSP has every
implementation. Such an implementation's trait is the one its header names, read
from the index's reference there (amended above): `impl fmt::Display for Shown`
inside a body is fmt::Display, not a workspace trait named Display that declares
`fmt` too, which a join by name would take. Together the two cover all 55. The 55
were reconciled two ways:
- **By source text:** 42 `impl … Message for` lines, less 2 that aren't
  implementations (a different trait, and the macro's own definition), leave 40
  written by hand.
- **By LSP:** 50, which is 35 of those 40 plus the 15 macro expansions.

**Built** (#72), and measured with `alidade graph`:
- **protowire-rust:** 63 `implements` edges. `Message` has 55, the 55 above.
  `Validator` has 5: LSP finds 6, and the sixth is `impl<F> Validator for F`,
  whose type is a parameter with no node. `TypeResolver` has 2 and `SaxHandler`
  1, the text counts. `MapEntryField` has none: its 10 implementations are on
  `str`, `[u8]`, eight primitives and a blanket `M`, none with a node.
- **prost-reflect:** 6 edges. 27 `impl … Visitor … for` lines are in its source,
  and 24 of them implement serde's `Visitor<'de>`, another trait of that name; the
  workspace's `Visitor` has the other 3, which are its edges. A join by name
  would have made the 24 too.
- **Cost:** the server loads the workspace a second time, after the index: the
  whole graph took 10.7 s against 7.2 s on protowire-rust and 17.5 s against
  14.2 s on prost-reflect, at the same peak memory (1.28 GB and 1.31 GB), since
  the two run one after the other.
- **Readiness:** the client waits for rust-analyzer's `experimental/serverStatus`
  to say `quiescent` before it asks: a trait with no implementations answers
  empty either way, so asking again until an answer comes cannot tell a server
  still loading from an answer. A fixture test fails without the wait.
- **Columns:** the client asks for UTF-8 positions, the index's, and converts
  when a server answers in UTF-16; a fixture test forces UTF-16.

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

**Built 2026-11-25** (#72), with two decisions of the owner's: every item of a
generated file gets a node, and a `.proto` edit reruns the Rust layer.
- **Which file an include reads.** rust-analyzer's server resolves it: its
  definition of a string literal in an `include!` call is the file the call reads,
  `target/debug/build/prost-reflect-tests/<hash>/out/test.rs`. An include the
  index shows only inside a macro's definition (tonic's `include_proto!`) is
  found from the other side instead. A reference to a symbol the index lists
  without a definition has the generated file as its definition, and the file is
  placed under the innermost workspace module the symbol is inside. Only a file
  whose items workspace code names is found that way.
- **What it declares.** The server's outline of the file (`documentSymbol`): every
  struct, enum, field, variant, module, function and constant, and each impl's
  items under its type. Each is a node at the `include!` (ADR 002 rule 16),
  marked generated, with `generated_in` naming the file. The index's symbols for
  them, `proto/Scalars#double.`, are registered as their definitions, so a call
  into generated code is an edge, and a method a derive writes on a generated type
  has a node, as on any type.
- **Which items are messages.** The generator's own derive says so:
  `::prost::Message` on a struct, `::prost::Enumeration` on an enum. A oneof's
  enum (`::prost::Oneof`) is neither, and gets no edge. rustfmt spreads a long
  derive over several lines, so the derive is read over the item's whole range.
- **Nesting.** prost-build writes a message's nested types in a module named
  after it in snake case, beside its struct. The module beside struct
  `ComplexType` is `complex_type`, so `complex_type::MyEnum` is
  `test.ComplexType.MyEnum`.
- **Stated names.** A struct whose stated names (every `message_name`, since a
  build can stack two, and `impl Name`'s `PACKAGE` and `NAME`) do not include the
  rule's name gets no edge, and is reported.

Measured on prost-reflect, against main (25598e8):
- 3 generated files are included, by `prost-reflect-tests`, and 160 items get
  nodes. Its proto messages and enums in their packages number 22, and each has
  its `generates` edge. Counting prost's derives in the three files' text also
  gives 22.
- All 24 stated `message_name` attributes agree with the rule. The 3 structs
  with a second, conflicting one have a first that agrees.
- 42 new call edges, all to methods a derive wrote on a generated type
  (`proto.Scalars[Default].default()`). No call edge is lost.
- The time is unchanged: 18.1 s on main, 17.9 s here. protowire-rust, which
  generates no code, gives the same graph as on main.

## The layer (decided 2026-11-24, the owner's calls on #72)

- **IDs** (ADR 002 rule 16): the file, then the item's path inside it, as for Python,
  a function's segment ending in `()`.
  `rust://crates/protowire-pb/src/wire.rs#Reader.skip()`.
  - **Why not SCIP's symbols, or the crate's module path.** SCIP's symbols are per
    Cargo package, not per compiled crate. Integration tests, benches, `build.rs` and
    `main.rs` all share one root, so 76 of protowire-rust's 3,558 defined symbols
    collide (`desc()` is defined in 10 test files), and 5 of prost-reflect's 1,959.
    A crate-and-module ID would need a segment for each test, bench, bin and build
    script. It would also make one declaration several nodes: 6 of protowire-pxf's
    test crates compile the same `tests/common/mod.rs`.
  - **A trait's method carries the trait:** `Packed[Message].encode_to()`. prost-reflect's
    `DescriptorError` has `fmt` for both `Debug` and `Display`, the one type and
    method name implemented for two traits in either repository.
  - **A generated type anchors at the file holding its `include!`:**
    `rust://prost-reflect-tests/src/lib.rs#proto.Scalars`. The generated file's own
    path holds a build hash, which ADR 002 forbids in an ID.
- **Calls** (ADR 006 rule 9): a reference to a workspace function, inside a function
  body, followed by `(`, or by a turbofish and then `(`, is a `calls` edge from the
  innermost function holding it. (*Amended 2026-11-25, #100, the owner's
  decision:* the rule read "followed by `(` or `::<`", so a function named as a
  value with a turbofish was a call. On tokio @854013a that was 11 edges, every
  one an entry of a vtable: `drop_arc_raw::<W>` in `waker_vtable`, and
  `poll::<T, S>` and its like in `raw::vtable`.) A reference to a function used as a value is a `references` edge, as in
  Python. Measured on protowire-rust's 7,306 references to its own functions: 6,855
  are followed by `(` or `::<`, 428 are imports, and 23 are functions used as values
  (`.map(row)`, `Some(&deny_all)`, `LazyLock::new(ValidationCache::new)`). The rule
  reads one token of source text after a reference, a read as narrow as the
  TypeScript layer's import clauses (ADR 007).
- **Implementations:** from LSP, and from the impl's header inside a function
  body, as above. RESOLVED (ADR 006 rule 9).
- **Test code is marked** (*added 2026-11-25, #106, the owner's decision on #99*).
  rust-analyzer indexes a crate's test build, with `cfg(test)` on, so items that
  exist only there have nodes. Each is marked test, as ADR 004 rule 4 marks Go's,
  and so is every edge touching one (that rule's derivation). An item is test code
  when:
  - its own attributes need `test`: `#[cfg(test)]`, `#[cfg(all(test, unix))]`, a
    `#[test]` or a framework's `#[tokio::test]`;
  - so do its impl block's, read from the block's header in the source, since the
    index has no symbol of an impl;
  - what holds it is test code: a function, a type, an inline module;
  - a module on its symbol's path is declared only as `#[cfg(test)] mod x;`, or
    opens with `#![cfg(test)]`: at the head of its file, or of an inline
    module's body, past comments and other inner attributes; one further in
    is an inner module's, not the file's (*#112*). A file two crates compile
    through `#[path]` is test code only when every declaration of it is;
  - or its file is below its package's `tests/` directory, an integration test.
    Its package is the nearest `Cargo.toml` above it, so a package inside
    another's `tests/` is its own (*#112*).

  An item of a hand-written file an `include!` reads, whose node is at that
  file (ADR 002 rule 16), is marked by the same rules: its attributes, its impl
  block's, its holder's, and its file's. The file is test code when it is below
  its package's `tests/`, or when every `include!` of it is in test code, as a
  `#[path]` file is when every declaration of it is (*#112*). An item of
  Cargo's output is at its `include!`, so the module holding that marks it.

  An item a `cfg(test)` marks is not test code when its file also declares
  that name behind `not(test)`. The two variants (`#[cfg(not(test))] const
  MAX_IMAGE: usize = 32 << 20;` beside `#[cfg(test)] const MAX_IMAGE: usize = 512;`)
  share one ID, so the node is the library build's too. Marked test, production
  code's edges to it would become test edges and leave the production view.

  `any(test, X)` is not only a test's, whatever the host says of X. A platform's
  cfg (`any(target_os = "linux", test)`) would otherwise make the graph differ
  from one machine to another, which ADR 004 rule 4 rules out for Go's files.

  Measured against rust-analyzer's own index with `cfg(test)` off, plus the
  integration tests' files, which it still reads:
  - **tokio @854013a:** 3,099 of its 3,115 test items are marked, and nothing
    else. Of the 16 left, 12 are `#[cfg(any(test, fuzzing))]` and 4 have no
    `cfg(test)` at all.
  - **pdfcraft @6e91067:** 14,286 of 14,385 are marked. Most of the rest are
    `any(target_os = "linux", test)` (23 in `desktop_theme.rs`), or depend on
    features the test build turns on.
  - **Marked and not test code, 13 on pdfcraft:** all are items of
    `fonts/src/cjk_scripts.rs`. The library declares it `#[cfg(test)] mod
    cjk_scripts;`, and the build script compiles it through `#[path]`. In the
    test build rust-analyzer gives the file to the library alone, so the build
    script's declaration and its calls into the file are not in the index.

  **What this leaves, the limit #99 chose:** a call the library build makes
  through code behind `cfg(not(test))` is not read, since the index is of the test
  build. tokio's `File::poll_write` calls `fs::mocks::spawn_mandatory_blocking`, in
  a test edge, and its call to `blocking::spawn_mandatory_blocking` is not in the
  graph. That is 14 edges on tokio and 4 on pdfcraft. The test edges it gets in
  their place are 19 and 22, which `exclude_tests` leaves out. A second index run
  without `cfg(test)` would read them, at +70% (tokio) and +82% (pdfcraft) of the
  build's time. `TestTestCode` pins the rule and this limit.
- **A machine without the toolchain:** if `cargo`, `rust-analyzer` or the standard
  library's source is missing, each Rust file is a node marked `calls_unavailable`,
  and the report names what is missing and the `rustup` command that adds it. The
  standard library's source counts: without it rust-analyzer silently loses 26% of
  expression types.
- **`serve` waits for the index.** The first answer, and the first after a Rust edit,
  wait for SCIP to finish: 6.5 s on protowire-rust and 13.9 s on prost-reflect, for
  every workspace each time. An answer is then never stale and never partial
  (ADR 005). Since the layer also asks rust-analyzer's server (implementations,
  included files, build scripts), the wait is the whole graph's: 10.7 s on
  protowire-rust and 17.9 s on prost-reflect (both above), and 34 s on tokio
  @854013a's 192,000 lines (measured 2026-11-26, twice: 34.5 s and 34.3 s).
- **The oracle is rustc.** Each crate is compiled to LLVM IR at opt-level 0, whose
  direct calls are rustc's own resolution, and compared with the layer's call
  edges. rustc is an engine independent of rust-analyzer.

  Its limits:
  - it sees only the functions rustc compiles as they are: a generic function only
    where it is instantiated, and a closure in a returned `impl Iterator` only where
    the iterator is used (*since #98*, `-C link-dead-code` compiles the closure,
    and an `async fn`'s body, in its own crate; see the last correction below);
  - a call through `dyn` is an indirect call it cannot name;
  - rustc 1.99's default symbol format needs a decoder. The comparison below used
    the legacy format, a nightly-only flag, through `RUSTC_BOOTSTRAP=1`.
    *Since #86 (2026-11-25)*, `scripts/oracle.sh` runs it, decoding the default
    v0 symbols with `github.com/ianlancetaylor/demangle` (the owner's choice,
    a dependency of `internal/oracle` alone, so not of the release). See the
    correction after the table.

  *Corrected 2026-11-24, by the layer's own edges.* The probe this bullet first
  reported (65 edges agree on 43 functions, one edge "not explained yet") came
  from a comparison that could not read three of rustc's name forms: a quoted
  symbol, a closure (`_{{closure}}`), and a method of an `impl` that sits in
  another module than its type (`api::<impl FieldDescriptor>::is_list`). The
  unexplained edge, `Reader::new` to `Limits::default`, is a real call rustc has
  too. With those forms read, against the layer's edges:

  | crate | functions both have | agree | rustc only | layer only |
  |---|---|---|---|---|
  | protowire-pb | 56 | 109 | 3 | 1 |
  | prost-reflect | 364 | 858 | 94 | 67 |

  (Measured with the IDs of rule 16 as amended, after a function's `()` and the
  nodes of macro-written methods: before them prost-reflect had 756 agreeing on
  355 functions, and 107 rustc-only edges to functions with no node, now 0.)

  Every difference has a cause other than a wrong edge:
  - **A call through a generic trait bound:** protowire-pb's 3 and 1, and
    prost-reflect's 78 and 48. The layer's edge goes to the trait's method,
    `write_message` to `Message::encode_to`, where rustc names each
    instantiation, `<BigInt as Message>::encode_to`. Reaching the
    implementations is not in this version (below).
  - **A call an operator makes:** 14 of prost-reflect's rustc-only, `==` calling
    a derived `PartialEq::eq`. Not in this version (below).
  - **A closure rustc never compiles:** 18 of prost-reflect's layer-only, in
    the iterators `FileDescriptor::messages`, `MessageDescriptor::extension_ranges`
    and their like return.
  - **The configuration:** one each side, `option_to_message`, whose two
    definitions are behind `cfg(feature = "text-format")` and its negation.
    rust-analyzer reads the workspace's unified features, where another member
    turns it on; the rustc build was of the one package.
  - **The comparison's own reading:** 1, a closure attributed to the function
    defining it.

  *Corrected 2026-11-25 (#86), by the oracle as a script.* The hand-run
  comparison misread one more legacy name form: a trait's impl written
  `api::<impl core::fmt::Debug for DescriptorPool>::fmt` was named
  `DescriptorPool::fmt`, the trait dropped. So a method of a trait impl in that
  form never matched the layer's `DescriptorPool[Debug].fmt()`, as a caller or
  as a callee. v0 symbols name the trait (`<DescriptorPool as Debug>::fmt`).
  The same script with that one reading fixed, run on legacy IR, and the oracle
  on v0 IR agree on every number below. Both were compiled by rustc 1.100 at the
  commits the table above was measured at, protowire-rust @91f60b5 and
  prost-reflect @2d0e184:

  | crate | functions both have | agree | rustc only | layer only |
  |---|---|---|---|---|
  | protowire-pb | 56 | 109 | 3 | 1 |
  | prost-reflect | 379 | 939 | 94 | 67 |

  protowire-pb is unchanged. prost-reflect's causes, now each counted by the
  oracle and the rest listed by it:
  - rustc only: 78 through a generic trait bound, 14 an operator's, 1 the
    configuration, 1 the comparison's own reading (`Writer::fmt_list`'s closure);
  - layer only: 39 through a generic trait bound, 9 through `dyn`
    (`visit::Context` holds a `&mut dyn Visitor`, an indirect call rustc cannot
    name; counted with the bound above, by hand), 18 in closures rustc never
    compiles, 1 the configuration.

  The oracle leaves out what rustc compiles from code outside the crate for a
  crate type, which the layer has no node for and legacy symbols never named by
  the crate: a blanket impl (`<KeyedError as Into>::into`) or a provided method
  of a trait outside it (`<Cardinality as PartialEq>::ne`); 16 edges on
  prost-reflect. A provided method of a trait inside the crate is named by the
  trait, as the layer names it.

  At their commits of 2026-11-25 (protowire-rust @7832c11, prost-reflect
  @f82c954): protowire-pb 53 functions, 101 agree, 4 rustc only, 1 layer only;
  prost-reflect 391, 969, 96 and 67, every difference of the same causes (16 of
  the rustc-only are operators').

  *Corrected 2026-11-25 (#98), by two flags, the owner's choice over reading
  MIR.* The IR left out two kinds of call, both measured on tokio @854013a with
  `--features full`. A call of an `#[inline(always)]` function was inlined
  even at opt-level 0 (tokio's `UnsafeCell::with_mut`). And the body of an
  `async fn`, `async` block or closure was compiled only where something polls
  or calls it, which is often another crate (tokio's `UdpSocket::peek_from`,
  and the closures above). `-C no-prepopulate-passes` runs no LLVM pass, so
  the inlined call stays a call. `-C link-dead-code` compiles every
  non-generic item, those bodies included. On tokio, the source-written calls
  of the reference went from 2,541 to 2,923, and the layer's edges it lacked
  from 312 to 105. At the table's commits:

  | crate | functions both have | agree | rustc only | layer only |
  |---|---|---|---|---|
  | protowire-pb | 56 | 109 | 3 | 1 |
  | prost-reflect | 387 | 974 | 94 | 49 |

  protowire-pb is unchanged, with one more rustc caller that the layer has no
  edge from. prost-reflect's 18 layer-only edges in closures rustc never
  compiled now agree. Its remaining differences: rustc only, 78 through a
  generic trait bound, 14 an operator's and 2 others; layer only, 39 through a
  generic trait bound, 9 through `dyn`, and 1 the configuration
  (`option_to_message`). A generic function is still compiled only where it is
  instantiated.

Not in this version:
- edges for `use` declarations;
- whether a call to a trait's method also reaches that trait's implementations, as
  a TypeScript call through an interface does (ADR 007). A call through a trait
  method goes to the method as rust-analyzer resolves it;
- a node for a method a derive or a macro writes that nothing calls. One that
  something calls has a node under its type (ADR 002 rule 16, amended);
- a call an operator makes: `==` calls `PartialEq::eq`, `+` `Add::add`, a scope's
  end `Drop::drop`, `?` `From::from`, with no reference in the source to read. On
  prost-reflect, 14 of rustc's call edges are `==`;
- more than one configuration. rust-analyzer reads one: the workspace's features
  as Cargo unifies them, and the host's target;
- a module file (one the index reads) whose header says prost-build wrote it.
  An `include!`d one is joined to its `.proto` messages (ADR 002 rule 16, #89):
  on jj, all 37 of its prost messages and enums, with no conflicting name;
- of what a hand-written file the workspace `include!`s writes (#88, below): a
  call of a method of an impl block the file itself writes (`h.area()`,
  `Hand::area(&h)`), a function it names as a value, and an implementation
  elsewhere of a trait it declares. The server answers none of them;
- a generated file included only through a macro when no workspace code names
  any of its items. Nothing then leads to it.

**Calls and implementations inside an included file** (*added 2026-11-25, #88,
the owner's decision*). rust-analyzer's index has no document for a hand-written
file that an `include!` reads (ADR 002 rule 16), so two more derivations read
what it writes:
- **Calls** come from the server's call hierarchy (`callHierarchy/outgoingCalls`
  on each function of the file), which answers there. It finds the call sites;
  each is then named by the server's definition at the last character of its
  range, as the index would name it. `crate::Area::area(&h)` reaches the impl's
  `Hand[Area].area()`, where the call hierarchy names the trait's `Area.area()`.
  The call hierarchy reports calls alone, so there is no references edge from
  such a file, and it leaves out a call of a method of an impl block the file
  itself writes (measured, rust-analyzer 1.100).
- **Implementations** come from the impl blocks of the file's outline: `impl
  crate::Area for Hand` is an implements edge from the file's `Hand` to the
  workspace trait named `Area`, when exactly one has that name, as the index's
  names are joined inside function bodies. The server's own answer for such an
  implementation points at the `include!`, or into the file, and names no type;
  it is passed over, not counted as unplaced.

On the repositories measured, neither adds an edge: protowire-rust and
prost-reflect include no hand-written file, and jj's included files are its
prost output, whose 4 written functions call no workspace code.

**Measured on the built layer** (`alidade graph`, the whole graph, all layers),
when the layer was first built: before implementations, the methods a macro
writes and the code a build script writes. With implementations the times became
10.7 s and 17.5 s, and with generated code prost-reflect's 17.9 s (above):

| repository | Rust nodes | files not read | calls | references | time | peak memory |
|---|---|---|---|---|---|---|
| protowire-rust | 3,801 | 0 | 4,611 | 16 | 7.2 s | 1.30 GB |
| prost-reflect | 1,949 | 7 | 2,067 | 86 | 14.2 s | 1.35 GB |

prost-reflect's 7 files are compile-fail cases that `prost-reflect-derive`'s tests
feed rustc. No crate includes them, and each says so.

**Public paths** (*added 2026-11-26, #116, the owner's decision*). A library's
items and file modules carry the attribute `paths`: the paths code outside the
crate names them by, shortest first, at most 8, space-separated.
`tokio/src/task/spawn.rs#spawn()` has `tokio::spawn tokio::task::spawn`. The
Markdown layer links a mention written as a crate path by them
(`tokio_util::sync::CancellationToken`). It reads only the graph, so they are on
the node, not handed over beside it: a rebuild that reruns only the Markdown layer
still has them.
- **An item's path is its parent's and its name.** A crate root's path is the
  crate's name, the package's with `-` as `_`. A module's parent is the module
  holding it, and a module has a path only when it is declared `pub mod`. A
  method's parent is its impl block's type: the type of that name in the impl's
  module, else the library's one type of that name. rust-analyzer names a method by
  the impl block's module, which need not be its type's.
- **Every `pub use` adds the name it exports**, `as` renames read, at each path of
  the module holding it. rust-analyzer resolves the name to the item's definition,
  through any uses on the way: `pub use task::spawn` in `lib.rs` names
  `task/spawn/spawn()`. `pub(crate) use` adds nothing.
- **Only the library has paths:** a file below the package's `src/`, other than
  `src/main.rs` and `src/bin/`. The package's tests, benches, examples, binaries and
  build script share its symbols (ADR 002 rule 16), and no other crate names what
  they define. Measured: without this rule, the CHANGELOG's `tokio::run`, tokio
  0.1's removed function, resolved to a helper in `tokio/tests/`.
- **A module's body is read from the text**, its braces matched from its name, and
  a `pub use` inside a function body is not told apart. rust-analyzer gives an item
  that a macro writes with an attribute on it the macro call's whole range: in
  tokio's `cfg_sync! { pub mod futures { … } … pub use mutex::Mutex; }` the
  range of `futures` holds the use, and so does a function's in `cfg_rt!`.

Not read: a glob, `pub use inner::*`, which adds no path; an item's own
visibility, so a `pub(crate)` function in a public module has a path; `[lib] name`
in `Cargo.toml`, so a crate renamed there is named by its package; a library whose
root is not below `src/`; the items a macro writes, and Cargo's generated files.
A macro has no node, so `tokio::select!` links nothing.

Measured on tokio at 854013a, before building, from the index alone: of the 66 code
spans in its Markdown that open with one of its crates' names, 49 resolve to the
item or module they name, against 28 with defining paths alone. The rest name
removed, private or `tokio_unstable` items (11), a macro (3), or one of several
cfg variants (`tokio::main`, 3). Built, the layer adds 45 `documents` edges on
tokio, each read by hand, and removes none. An edge is one per section and item, so
a mention in a section that already links the item adds none: the CHANGELOG's
`tokio::runtime::Runtime` sits beside `Runtime`. jj's Markdown has no such mention.

`get_node` shows the attribute, and an MCP answer that lists nodes leaves it out
(the owner's decision). Listed, it made a `find_nodes` or `neighbors` answer over
tokio's library code 11% to 24% longer, and left 4% to 9% fewer nodes in a
`subgraph_for_question` answer at its default budget. Left out, those answers are
the size they were before.

## Assumptions, each with the test #72 must add

- **rust-analyzer leaves generated files out of its SCIP index, and LSP's definition
  lands in them.** If a release starts indexing them, the LSP lookups become
  unnecessary. If LSP stops landing there, the `.proto` link is lost.
  `TestGeneratedCode` pins both, with a `build.rs` that writes what prost-build
  does; `TestGeneratedThroughMacroAlone` pins the include found through a macro.
- **Implementations come from both sources.** A fixture with one implementation
  written by a `macro_rules!` macro and one inside a function body must give both
  `implements` edges. It fails the day either source stops supplying its half, or
  the symbol grammar changes.
- **A call is a reference followed by `(`, or by a turbofish and `(`.** A fixture
  calling a function, passing it as a value, calling it with a turbofish, and
  naming it with a turbofish as a value must give two `calls` edges and two
  `references` edges. `TestCallShaped` pins the turbofish's own shapes: nested
  arguments, `->` inside one, and one spanning lines.
- **rust-analyzer resolves a `pub use` to the definition, and names a test
  crate's items by the package** (#116). `TestPublicPaths` pins each path rule on
  the fixture: renamed and multi-line uses, a private module's re-exported items, a
  method through an aliased type and from an impl block in another module, a use
  beside a module and a function a macro writes, `pub(crate) use`, a glob, and no
  paths for the package's tests, binary and build script. It fails the day the
  index names a use by the use, or stops sharing the package's name with tests.
- **A missing toolchain is reported, never silent.** A test with `rust-analyzer`, and
  separately `rust-src`, absent from the path must give file nodes marked
  `calls_unavailable`, and a report naming what is missing.
- **Indexing writes nothing into the repository outside `target/`** (#81, the
  owner's pick of option B). rust-analyzer 1.100's full `cargo metadata` writes a
  `Cargo.lock` when there is none, which is a Rust input, so the next build reran
  the layer with nothing edited. Both rust-analyzer processes therefore run with
  `CARGO_RESOLVER_LOCKFILE_PATH` (Cargo's `resolver.lockfile-path`, stable since
  1.97) pointing at a temporary copy of the workspace's `Cargo.lock`. A path the
  environment already sets is kept. `TestRustWritesNothing` builds a package with
  no lock file twice, and fails if a `Cargo.lock` appears or the second build
  reruns anything. A path set in `.cargo/config.toml` instead is overridden: not
  measured.
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
