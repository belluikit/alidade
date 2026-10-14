# ADR 007: Frontend Layer — TypeScript and Svelte at File Granularity, Joined to `.proto` Through protoc-gen-es

**Status:** Accepted 2026-09-11 (#45); implemented by `internal/adapter/ts`
(#46), which added ADR 002 rule 11. One correction from implementation, under
"Why esbuild's resolver and not its bundler" below.
**Date:** 2026-09-11
**Relates to:** [000](000-composition-boundary.md),
[003](003-corpus-scope.md), [004](004-generated-code.md),
[006](006-edge-provenance.md)

## Context

ADR 003 names the frontend as its honest gap — "two repos are majority
TypeScript and alidade will cover a third of them" — and sets the bar for
admitting it: a new adapter and its own ADR, and the format must participate in
a relationship that crosses a layer boundary. This ADR measures whether it does,
and what an adapter can be built from.

### The corpus

Counted 2026-09-11, excluding `node_modules`, `.svelte-kit`, `dist`, `build`,
`site` and `.astro` — build output, which ADR 003's earlier count did not
exclude (steward's "427 `.js`" were 347 files of SvelteKit output and 78 of a
deployed site; the hand-written `.js` count is 2).

| repo | `.ts` | `.tsx` | `.svelte` | `.js` | `.go` | `.proto` | proto-generated TS |
|---|---|---|---|---|---|---|---|
| pxfed | 78 | 34 | 0 | 8 | 0 | 1 | 8 (`*_pb.ts`) |
| steward | 84 | 0 | 103 | 2 | 565 | 29 | 19 (`*_pb.ts`) |

pxfed is an Electron application (React, `packages/renderer` and
`packages/pxfed-shell`); its Go is elsewhere (protolsp), reached over
JSON-RPC and Connect. steward is a Go service with a SvelteKit application
in `app/` (Svelte 5, every component `<script lang="ts">`).

### The relationship

Both repositories generate TypeScript from `.proto` with protoc-gen-es v2
(`buf.gen.ts.yaml` in pxfed, `internal/api/web/buf.gen.yaml` in steward;
`@bufbuild/protobuf` ^2). That is the ADR 004 bridge a third time:

```
.proto (message AuditServiceListRequest)
    → generated TS (export type AuditServiceListRequest, export const
      AuditServiceListRequestSchema)                              [audit_pb.ts]
    → hand-written component that imports it                    [+page.svelte]
```

How much of the hand-written side touches it, by import statement:

| repo | hand-written files importing a `*_pb` module | distinct generated symbols imported | import edges into generated files |
|---|---|---|---|
| pxfed | 3–4 | 8 | 10 |
| steward | 26–27 | 25 | 41 |

(Two counts each: an import-statement scan of the source, and esbuild's resolved
metafile — below — which differ by one file per repo. Both are quoted rather
than reconciled to one because they measure slightly different things: a
statement and a resolved import.)

The question this answers is "which screens change if this message changes",
which nothing answers today; steward's Go side of the same messages is already
in the graph through the `.proto` layer. So the format passes ADR 003's test:
`.proto ↔ TypeScript` via generated code, and the Go and TypeScript consumers
of one message meet at one `proto://` node.

### What could parse it

ADR 000 admits no parser of alidade's own. The candidates, each checked rather
than assumed:

| candidate | checked | verdict |
|---|---|---|
| `github.com/microsoft/typescript-go` (TypeScript 7's Go port) | `go list` of the module at `v0.0.0-20260820064610`: 104 packages under `internal/`, one importable (`cmd/tsgo`) | not a library; a subprocess at best |
| tree-sitter (`smacker/go-tree-sitter`, `tree-sitter/**`) | `.steward/governance.pxf` `blocked_module_globs` | blocked, and cgo |
| `github.com/tdewolff/parse/v2/js` | `go doc`: `Options{WhileToFor, Inline}`; ES2020 only | no TypeScript syntax |
| `tsc` / `svelte-check` as a subprocess | needs a Node toolchain on the machine; ADR 000 calls an analysis reachable only over a wire an exception needing its own ADR | out, for the same reason the Go layer does not shell out to `go` |
| an import-statement regex, no resolver | 42% of steward's imports are alias-based (`$gen`, `$lib`: 258 of 617), 16% of pxfed's are extensionless or `index` | resolution is the hard part; a scan gets the specifier and not the file |
| `github.com/evanw/esbuild/pkg/api` v0.28.2 | pure Go, no cgo, MIT. `Build` with `Metafile: true`, `Bundle: true`, `Packages: External`, `Format: ESModule` and hashed `EntryNames`; Svelte through an `OnLoad` plugin that hands esbuild the `<script>` blocks as TS | parses `.ts`/`.tsx`/`.js` natively, resolves relative, extension-less, `index` and aliased imports (aliases from `svelte.config.js` / `tsconfig` `paths`), and reports every resolved import per input file. **No AST**: the metafile is files and imports. |

Measured with esbuild over every hand-written and generated `.ts`, `.tsx` and
`.svelte` file (`.d.ts` excluded):

| repo | inputs | resolved import edges | package imports (external) | errors | time |
|---|---|---|---|---|---|
| pxfed | 111 | 175 | 192 | 0 | 23 ms |
| steward | 188 | 262 | 220 | 0 | 44 ms |

Against ADR 005's budget that is noise. The cost is binary size: a stripped
`CGO_ENABLED=0` binary linking `pkg/api` is 8.3 MB against a 19.2 MB alidade,
of which roughly 2 MB is the Go runtime both carry, so about +6 MB (+30%). The
same class of cost as chameleon and protocompile, accepted in #22.

## Decision

**A sixth adapter, `ts`, at file granularity for hand-written code and symbol
granularity for generated code, over esbuild's resolver. No parser, no AST.**

1. **Nodes.** Every `.ts`, `.tsx`, `.js`, `.jsx` and `.svelte` file outside
   build output is a node, keyed by its repo-relative path:
   `ts://app/src/routes/app/+page.svelte`. Kind `module` for scripts,
   `component` for `.svelte`. The scheme names the layer, as `md` does, not
   the extension. A package imported by bare specifier is one external node
   per package, `ts://@pkg/@bufbuild/protobuf` (`ts://pkg/` until #124), as the Go layer keeps external
   packages — nothing under `node_modules` is read.

   *Amended 2026-10-06 (#115, owner's decision):* `.mjs`, `.cjs`, `.mts` and
   `.cts` are read too, and their `.d.mts`/`.d.cts` declaration files are
   not, as `.d.ts` is not. Measured on steward: its five `.mjs` build scripts
   (`app/scripts/*.mjs`, `docs-site/astro.config.mjs`) become nodes, and so
   become targets for Markdown links and mentions; their imports are all
   Node built-ins and `astro`, so no edge into application code was gained.
   The recommendation that preceded the decision said they import
   application modules; that was not measured and is not so. The cost is build output in
   those forms, so `out/` (electron-vite's, pxfed's
   `packages/pxfed-shell/out/`) joins the skipped directories. The list
   lives once, in `ts.Extensions`, which the Markdown layer's link targets
   and the build's signature read.

2. **Generated files carry their exports as nodes.** A file protoc-gen-es
   wrote (`@generated by protoc-gen-es` in its header) also gets one node per
   export, `ts://…/audit_pb.ts#AuditServiceListRequestSchema`, keyed by the
   export name. The export list of a generated file is the generator's
   documented output — `export type M`, `export const MSchema`, `export enum
   E` and `ESchema`, `export const S: GenService` — read from the `export`
   lines, the way the SQL layer reads `-- name:` and the goasm layer reads
   `# TEMPLATE`. Hand-written files get no symbol nodes: their declarations
   would need a parser.

3. **Edges.**
   * `imports` (RESOLVED): file → file, from esbuild's metafile. The
     specifier is stated in the source; the file it names is what esbuild's
     resolver, given the repository's aliases, decided — a tool's
     resolution, so RESOLVED rather than EXTRACTED. File → external package,
     EXTRACTED.
   * `references` (EXTRACTED): importing file → generated export, from the
     `import { A, B } from "…_pb"` specifiers. This is the one place the
     adapter reads syntax beyond a marker line, and it reads exactly the
     import-clause grammar (`{ a, b as c, type d }`, `* as ns`, default).
     A clause it cannot read gives the file-level `imports` edge and
     nothing more; the count is reported.
   * `generates` (DECLARED, `via=protoc-gen-es`): `.proto` file → generated
     file by protoc-gen-es's naming (`a/b.proto` → `<out>/a/b_pb.ts`, `out`
     from the es plugin's entry in `buf.gen.yaml`, `import_extension` and
     `target` honoured); `proto://pkg.M` → the `M` and `MSchema` exports, and
     likewise enums and services. The generated file also states its source
     (`@generated from file a/b.proto (package pkg …)` and
     `Message<"pkg.M">` literals); the adapter checks the header agrees with
     the naming rule and reports a file where it does not, rather than
     guessing (ADR 006 rule 6).

4. **Scope.** Directories skipped: `node_modules`, `.svelte-kit`, `dist`,
   `build`, `.astro`, and any directory a repository's `.gitignore` ignores
   — build output is where the false volume is (steward's 347 generated `.js`).
   `.d.ts` files are skipped. Svelte components contribute their `<script>`
   blocks; markup is not read, and a component used in markup is reached
   through the import that brought it into scope, which is in the script.

5. **Identity** (ADR 002 rule 11, to be added with the adapter): the
   repo-relative path, and for a generated export the export name after
   `#`. An export name is one segment.

### Why file-level, and not a parser

The alternative was symbol-level for everything: functions, components, the
RPC call sites. No importable TypeScript parser exists in Go today
(typescript-go keeps its compiler under `internal/`), tree-sitter is blocked,
and shelling out to Node is a wire. Waiting for one would keep the gap open
indefinitely; file-level with symbol-level on the generated boundary answers
the motivating question now — a message's `proto://` node reaches the screens
that import its generated symbol in two hops — at a cost of one pure-Go
dependency and 44 ms.

What file-level cannot answer, stated so nobody expects it: which *function*
in a module makes an RPC call; whether an imported symbol is used or merely
imported; anything about the markup of a component. Those wait on a parser and
are the reopen trigger below.

### Why esbuild's resolver and not its bundler

The measurement above used esbuild's bundler metafile. The adapter does not:
a bundler applies TypeScript's import elision, so an `import type { User }`
and an import a Svelte component uses only in its markup (which the
script-only reading never sees used) vanish from the metafile — and both are
dependencies here. So the adapter reads the import clauses itself (the one
grammar it reads: `import … from`, `export … from`, `import()`) and asks
esbuild's resolver, through `PluginBuild.Resolve` inside one empty build,
where each specifier leads. Same resolver, same aliases, no elision, and
nothing is parsed or bundled. The numbers below are from the adapter.

### Why esbuild and not an import regex

Resolution. Steward's imports are 42% alias-based and both repositories use
extension-less specifiers; SvelteKit adds `$lib`, `$gen`, `$docs`. Reproducing
the resolver — `tsconfig` `paths`, `index` files, the `.js`-means-`.ts`
convention protoc-gen-es's `import_extension=js` relies on — is writing a
parser's worth of rules for a resolver's job. esbuild's is the one the
repositories' own builds use.

## Alternatives considered

* **Wait for typescript-go to expose an API.** Would give a real type-resolved
  graph, the TypeScript equivalent of caliper. Nothing importable at the
  checked revision; reopen when there is.
* **Vendor a TypeScript grammar and write the parser.** ADR 000 rule 3 says
  no: a new format is a new adapter, never a new parser.
* **Treat generated TS files only** (proto → generated file, stop). Loses the
  screens, which are the point.
* **Run under Node** (`tsc --listFilesOnly`, `svelte-check`). A toolchain
  requirement on every machine that runs alidade, for a layer that esbuild's
  Go API reaches without one.

## When to reopen this

* **A TypeScript parser becomes importable in Go** — typescript-go publishing
  a stable API is the likely one. That is the day hand-written symbols enter
  the graph, as a new derivation of the same nodes.
* **A question needs the markup**: a component referenced in a Svelte template
  by name without an import (Svelte 5 does not allow it; if that changes, the
  script-only reading misses it).
* **A third frontend arrives with a different generator** (protoc-gen-connect-es
  service stubs, tRPC, GraphQL codegen). Rule 3's `generates` is protoc-gen-es
  specific by design; a second generator is a second `via`, not a change to
  the rule.
* **esbuild's metafile changes shape or the build cost crosses ADR 005's
  line.** 44 ms today.

## Amended 2026-10-08: declarations and calls through TypeScript 7's compiler (#215, the owner's decision)

**The first reopen trigger fired, in a different envelope.** typescript-go —
TypeScript 7's compiler, GA since 2026-08-20 — still publishes no Go API (its
maintainers: "this module does not have a public Go API", microsoft/typescript-go#482),
and every package of it is under `internal/`. Go lets only a package under
`github.com/microsoft/typescript-go` import those, so alidade reaches them the
way oxc's tsgolint does: typescript-go is a submodule pinned to a release
(`third_party/typescript-go`, tag `typescript/v7.0.2`, no patches), and a shim
module named `github.com/microsoft/typescript-go/shim` (`third_party/tsgo-shim`)
re-declares the types, constants and functions the layer uses. alidade's
`go.mod` replaces both with the checkouts. The owner chose this over copying
the packages into alidade's tree and over a helper binary; it costs
`go install …@latest`, which a module with `replace` directives cannot be, and
which the README no longer offers.

The layer keeps everything above — esbuild's resolver for imports, the export
lines of generated files — and adds a pass of the type checker:

1. **Declarations.** A hand-written file's module-level functions, classes,
   interfaces, type aliases, enums and variables are nodes, and one level of
   members: a class's or interface's, a type literal's under a type alias, and
   the function-valued properties of an object literal a module-level variable
   holds. `ts://<file>#<name>` and `#<holder>.<member>` (ADR 002 rule 11,
   amended); `contains` (EXTRACTED) from the file or the holder. Each carries
   `end_line`, so `search_text` places a hit in a TypeScript declaration
   exactly (#212). Kinds: `func` (a function, or a variable holding an arrow or
   function expression), `type`, `enum`, `const`, `var`, `method`, `field`.
   Named functions nested in a body are nodes too (amended below, #235).
2. **Calls** (RESOLVED): from the innermost declaration holding a call
   expression, a `new` or a JSX element — the file, at module level — to the
   declaration the checker resolves the callee to, through imports, aliases and
   re-exports. A JSX element is a call: rendering a component is calling it. A
   callee outside the tree's sources (a package, a `lib.*.d.ts`, a declaration
   file), or a declaration of the tree with no node (a local function, a
   parameter, a destructured binding, a member of an anonymous type), is no
   edge, and each is counted.
3. **Projects.** Every `tsconfig.json` and `jsconfig.json` is a program; a
   solution config (no files, only references, as Vite and electron-vite write
   one) stands for the configs it references. A file two projects include is
   walked by the deepest, the one its editor uses; a file none includes, by one
   project inferred for the leftovers. A declaration's ID is derived from its
   place in the source, so each program is checked, walked and released alone.
4. **Svelte is not checked.** The compiler reads no `.svelte` file, so a call
   made in a component is no edge. A component node says so
   (`calls_unavailable`), as a Go package whose calls are missing does, and the
   MCP instructions tell the agent; `search_text` still finds the text.
5. **Markdown links them last.** A hand-written declaration answers a doc's
   mention only when no other layer's declaration does — by name for a
   module-level one, as `Holder.member` for a member — and never a single
   lowercase word (`main`). Indexed with the rest, the type checker's
   thousands of members made mentions that resolved before ambiguous: the full
   oracle lost three right links (steward's `behavioral`, a proto field, met a
   TypeScript interface's; pxfed's `registry` met four), and with the rule its
   samples read as main's again.
6. **Generated files** keep the nodes their export lines give. Their
   declarations are not walked again, and a call into one lands on the export
   node of that name, which is the same ID.

**Measured, 2026-10-08.** The calls oracle (`internal/oracle/ts_calls.go`) runs
the repository's own `typescript` package under Node — TypeScript 5.9 in pxfed,
6.0 in steward — with the declaration rules written a second time in
JavaScript and each file under the nearest tsconfig above it, and compares edge
by edge: pxfed 472 of 472 distinct calls edges and none extra (4,960 call
sites, 4,908 resolved, 830 to a declaration of the tree), steward 94 of 94 and
none extra (1,572, 1,518, 173). The layer's own count of call sites, a third
walk, agrees: 4,961 and 1,572. Of pxfed's call sites into the tree that land on
no node (565), the twenty commonest shapes cover 562: 268 array bindings
(`useState` setters above all), 97 local variables, 123 destructured locals and
props, 31 parameters, 36 members of anonymous types and 7 `for…of` variables —
local or anonymous every one.

Cost: the stripped darwin/arm64 binary is 40.0 MB against 24.7 MB (+15.3 MB:
the compiler and its bundled `lib.*.d.ts`); pxfed's build 0.56 s against
0.30 s, peak RSS 246 MB against 39 MB; steward's 3.38 s against 3.25 s, its
peak set by the Go layer either way.

**What would break it, and what says so.** A typescript-go release moves an
`internal/` package or a name the shim re-declares: the shim stops compiling,
in CI, before anything ships; `third_party/tsgo-shim/gen.go` regenerates the
constants. A rule the two implementations share and both get wrong is not
caught by the oracle — the fixture (`testdata/checker`, `TestCheckerCalls`,
`TestCheckerDeclarations`) pins each rule by hand, and mutations of the
solution-config, alias and object-literal rules fail it.

**Reopen when:** typescript-go publishes a Go API (the shim goes, the rules
stay); a Svelte component's calls are wanted (the compiler would need
svelte2tsx's translation, which is JavaScript); or a TypeScript release changes
how the checker names a declaration.

The parts of this ADR it supersedes: Decision 2's "Hand-written files get no
symbol nodes: their declarations would need a parser", "Why file-level, and not
a parser", and the first alternative considered.

## Amended 2026-10-09: Svelte components through svelte2tsx in V8 (#221, the owner's decision)

**A component's calls are edges.** The checker reads no `.svelte` file, so
#215 left steward's 103 components, most of its frontend, as files whose calls
were missing. Each component is now translated by svelte2tsx (Svelte's own
translator, the one svelte-check and the Svelte editor extension run), and the
checker reads the translation beside the component as `Card.svelte.tsx`,
through an overlay of its file system, so an import of `./Card.svelte`
resolves to it the way TypeScript tries an import's extensions. A component
joins the program of the deepest tsconfig whose directory holds it, under that
project's options and aliases.

1. **Where svelte2tsx runs.** In V8, embedded through cgo (tommie/v8go), from a
   bundle of svelte2tsx and its dependencies that `scripts/svelte2tsx.sh`
   generates and commits, pinned in `internal/adapter/ts/svelte/VERSIONS`. No
   Node at build or run time. The owner chose it over two others the spike
   measured: svelte2tsx in goja, a JavaScript engine in Go (identical output,
   14.7 s for steward's components against V8's 0.42 s), and the script blocks
   alone without a translator (79% of a component's calls: none of its
   markup's).
2. **What is a node in a translation.** Its module-level declarations, as in
   any file, and the statements of the generated `$$render` function's body,
   which are the instance script's declarations, `ts://…/Card.svelte#label`.
   svelte2tsx's own names never are (`$$render`, `$$_…`, `__sveltets_…`,
   `X__SvelteComponent_`). Positions are mapped back to the component through
   the translation's source map.
3. **Calls.** A call in a component's script is an edge from the declaration
   holding it, and one in its markup, an event handler's included, is an edge
   from the component. A component used in markup is a call to the component's
   file node: svelte2tsx writes it as `new` of a constant made by
   `__sveltets_2_ensureComponent(Card)`, which is resolved to `Card`, and the
   component's default export is the component. The calls svelte2tsx writes for
   itself (`svelteHTML.createElement`, its helpers) are no call sites.
4. **A build without V8.** V8 needs cgo and `CGO_CXXFLAGS=-nostdinc++`, so it is
   behind the `v8` build tag. A build without it translates nothing, its
   components say so (`calls_unavailable`), and so do the MCP instructions. The
   release is built with it.

**Measured, 2026-10-09:**
- **The translation in V8 is byte-identical to svelte2tsx under Node** for all
  103 of steward's components.
- **steward's TypeScript calls edges go from 94 to 658:** 564 from components,
  49 of them renders of a component.
- **The calls oracle,** now translating components with the same bundle under
  Node and applying these rules written again in JavaScript, agrees edge by
  edge: 658 of 658, none extra. Its call-site count, 4,355, is the layer's.
- **pxfed,** with no components, reads as before: 472 of 472.
- **steward's build** takes 5.0 s against 4.1 s.
- **The release binaries** grow from 40.0 MB to 94.5 MB (darwin/arm64);
  darwin/amd64 is 100.5 MB, linux/arm64 107.2 MB, linux/amd64 111.9 MB.
- **The two darwin binaries ran:** arm64 natively on steward, amd64 under
  Rosetta on the fixture.
- **Not run:** the linux binaries. They are cross-compiled with zig, and need
  glibc 2.29 or newer.

**What would break it.** svelte2tsx's output changing shape, which a bundle
regeneration would show: the fixture's component cases
(`TestCheckerCalls`, `TestCheckerComponentDeclarations`) fail when the render,
instance-script or default-export rule stops matching, and mutations of each
were seen to fail them. A V8 update that tommie/v8go's prebuilt libraries no
longer support on a target, which the release's cross builds catch.

## Amended 2026-10-09: a call through an interface reaches its declared implementations (#229, the owner's decision)

A call the checker resolves to an interface's member (`services.workspace.open()`
to `WorkspaceService.open`) stopped there. Nothing joined the interface to the
classes implementing it, so "who calls `ElectronWorkspace.open`?" had no answer.
Measured on pxfed: 88 of 472 calls edges landed on an interface member, and the
50 methods of the 8 classes that declare `implements` had a caller in 2 cases.

1. **`implements` (RESOLVED):** from each class to each interface its
   `implements` clause names, and to each interface those extend, as the checker
   resolves the names. Declared, not structural. An object literal or a class
   that merely has the members is not an implementation; pxfed has 146
   interfaces, most of them data shapes, and structural matching would join
   every object with the same fields. An interface from a package has no node,
   so it gets no edge.
2. **calls (RESOLVED, `via=interface`):** a call to an interface member is also
   an edge to each implementing class's member of that name, its own or one it
   inherits, through the interface's inherited members too. The edge to the
   interface member stays, as Go keeps one beside its dispatch edges. An
   implementation the caller also calls directly keeps the direct edge,
   unmarked.

**Measured, 2026-10-09:**
- **pxfed:** 6 `implements` edges (two of the 8 classes implement a package's
  interfaces) and 50 dispatch edges. The implementing methods with a caller go
  from 2 of 50 to 39 of 50, and `ElectronWorkspace.open`'s callers now include
  `WorkspaceService.open`'s.
- **The calls oracle,** with the rule written again in JavaScript over the
  repository's TypeScript (`implements` clauses, the interface's properties and
  bases, the class's own or inherited property), agrees edge by edge: 522 of
  522, against 472 before. steward has no `implements` clause and reads as
  before.

**What would change it:** a repository whose implementations are mostly object
literals or factory returns, which declared `implements` does not see. That is
the measured reason to reopen it as structural, limited to interfaces that have
methods.

## Amended 2026-10-09: a SvelteKit app never synced is read under the config SvelteKit would generate (#232, the owner's decision)

A SvelteKit app's `tsconfig.json` extends `./.svelte-kit/tsconfig.json`, which
`svelte-kit sync` generates and git ignores. In a fresh clone it is missing,
the checker loads the app without SvelteKit's aliases (`$lib`, the app's
`kit.alias`), and every import through them resolves to nothing. On steward,
35% of the TypeScript calls edges were gone (658 → 426) with no error anywhere.

When that file is missing beside a `svelte.config.js`, the layer serves the
config SvelteKit would generate, through the overlay the checker reads
components through. It contains:
- `paths` for each alias the layer reads from `svelte.config.js` (the table
  esbuild resolves with) and `$lib`;
- SvelteKit's compiler options;
- its include and exclude patterns.

It leaves out what SvelteKit generates beside it: the `$app` and `$env` ambient
declarations and each route's `./$types`. Nothing is written to the tree. The
synthesis is recorded as a skipped input naming the file and telling the reader
to run `svelte-kit sync` for the real one. That's the owner's option 3:
synthesize, and say so.

**Measured, 2026-10-09:** on a fresh clone of steward, the TypeScript layer is
identical to the built checkout's, node for node and edge for edge, with all
658 calls edges the same set. On the built checkout nothing is synthesized and
nothing moves. The calls oracle, run over the fresh clone, sees what an unsynced
TypeScript sees (426), so for this case the reference is the built checkout, not
the oracle.

**What would change it:** a SvelteKit app whose generated config carries
something this does not, `kit.typescript.config` rewriting it or route `$types`
that a call resolves through, and whose edges depend on it.
`TestSvelteKitAppNeverSynced` pins the rule, and fails with the synthesis
removed.

## Amended 2026-10-09: a named function nested in a body is a node (#235, the owner's decision)

A call made in a function nested inside another was attributed to the outer
declaration. On the TypeScript benchmark that cost a question: asked who calls
`getCurrentRanges`, the graph answered `Editor` and `attachSecretEditGuard`,
where TypeScript's own "find all references" names `protectedAt` and `save`,
two functions declared inside them.

Now a named function inside a declaration that has a node is a node itself,
`kind` func or method, contained by that declaration. Its calls come from it
and calls to it land on it. It counts as named when it is:
- a function declaration;
- a variable holding an arrow or function expression, or React's `useCallback`
  on one (its result is that function);
- an object literal's named method, accessor or function-valued property.

Its ID is the enclosing declaration's path, then the name of each object
literal holding it (a variable's or a property's), then its own name:
`#attachSecretEditGuard.protectedAt`, `#Editor.save` for a method of the object
`useImperativeHandle`'s callback returns, `#api.fs.readDir`. In a Svelte
component the enclosing path is the component's own rule, so a function inside
a script-level function is `#label.inner`.

What stays out:
- an anonymous callback: its calls are the enclosing function's;
- a variable holding a value, `useMemo`'s included, since its result is what
  the function returns;
- `memo` and `forwardRef`, which at module level would turn a component's const
  into a func.

Two nested functions of one name in one declaration (in two branches, say) are
one node, at the first.

**Measured, 2026-10-09:**
- **pxfed:** TypeScript nodes go from 1,347 to 1,523 (+176: 87 func, 89
  method), calls edges from 522 to 597. No existing node changes kind,
  position or attributes. `getCurrentRanges` gains the callers
  `attachSecretEditGuard.protectedAt` and `Editor.save`.
- **steward:** +3 nodes. Its 399 indented functions are the top-level
  statements of Svelte script blocks, which were nodes already.
- **The calls oracle,** which applies the same rule written again in
  JavaScript over the repository's own TypeScript, agrees edge for edge:
  pxfed 597 of 597, steward 658 of 658 (with V8), no extra edge either way.
  With nested nodes turned off in the layer alone, it reports 438 of 597 and
  84 extra.

**What would change it:** answers too large to read on a repository dense
with handlers, which the counts above do not show for pxfed or steward; or a
wrapper other than `useCallback` that returns its function and is common
nested. `TestCheckerDeclarations` and `TestCheckerCalls` pin the rule over
`testdata/checker/src/nested.ts`, and each sub-rule fails them when removed.

## Amended 2026-10-10: a class implements the class it extends (#249, #240 decision 2, the owner's decision)

A class's `extends` clause produced no edge, so "what derives from `BaseImpl`?"
had no answer in the graph, in TypeScript or in Python, while graphify's
`inherits` edges answer it.

- **`implements` (RESOLVED):** from a class to the class its `extends` clause
  names, as the checker resolves the name: its direct base only, whose own edge
  reaches the next. The same kind as class → interface: both say what the class
  can stand for. Python's bases get the same edges (ADR 008).
- **No edge** for a base outside the tree's sources (`Error`, `EventEmitter`),
  or for an `extends` whose expression the checker names no class by (a mixin
  call, `extends mix(Base)`).
- **Unchanged:** dispatch through interfaces reads the `implements` clause, not
  the edges, and a call to a base class's method already resolves to it.
  Interface → interface `extends` gets no edge: the decision names classes, and
  a class's edges already reach the interfaces its interfaces extend.

**Measured, 2026-10-10:** the fixture's `DiskStore extends BaseImpl` is an
edge and `Oops extends Error` none. On real repositories it changes nothing:
pxfed, steward and protowire-typescript have the same edges before and after,
and their only `extends` clauses name `EventEmitter` (one) and `Error` (two),
both outside. No local repository has a class extending one of its own, so a
positive case on a real repository is not measured yet.
