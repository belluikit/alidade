# ADR 002: Node Identity — Declaration-Anchored, Never Positional

**Status:** Accepted
**Date:** 2026-08-29 (proposed and accepted the same day; the two sub-decisions
below were the owner's call and were taken as recommended). Amended 2026-09-10
with rules 7 and 8, the two grammar points PR #12 implemented ahead of the ADR;
amended 2026-09-10 with rule 9, the `.pxf` layer's keys for what has no name;
amended 2026-10-06 in rules 11 and 12, the escape and `@pkg` (#124);
amended 2026-10-08 with rule 14, Go struct fields (#193).
**Relates to:** [001](001-core-architecture.md), [004](004-generated-code.md), [005](005-on-demand-construction.md)

## Context

This is the only decision in the plan that costs a migration rather than a revert
to get wrong, and it is the reason `#1` sequences before every adapter.

Each layer arrives with its own notion of "a thing":

| layer | native identity |
|---|---|
| Go | `*ssa.Function` — package path, receiver, name |
| `.proto` | fully-qualified descriptor path |
| `.pxf` | file plus block path |
| SQL | table, or named query within its sqlc package |
| Markdown | file plus heading path |
| goasm | file plus template name (added 2026-09-11 with ADR 003's amendment) |
| frontend | file, or file plus export of a generated file (added 2026-09-11 with ADR 007) |

The core must give all five one key that (a) survives a rebuild after unrelated
edits, and (b) lets an edge minted by one layer land on a node minted by another.

An unstable key fails silently, which is what makes this dangerous. A query
returns nothing and reads as *absence* — "nothing calls this" — rather than as a
broken join. Nobody notices until a conclusion drawn from the graph turns out to
be wrong.

### Prior art worth copying

`protolsp` solved exactly this for runtime violations, in RFC-001 §8.3.1. Its
`sourceRef {file, descriptorPath}` is a **declaration identity that takes
precedence over positional identity** — "when present it is the join key (the
positional `descriptorPath` is ignored)" — with path-boundary-aware suffix
matching to reconcile module-relative producer paths against absolute compiler
paths, and an explicit policy of **dropping** a join rather than attaching it to
the wrong file.

That last rule is the important one and it is adopted here: a wrong edge is worse
than a missing one, because a missing edge looks like missing data while a wrong
edge looks like a fact.

## Decision

**A node's identity is the fully-qualified path of its declaration, prefixed by
its layer scheme. Source position is an attribute of a node, never part of its
identity.**

```
go://github.com/trendvidia/protolsp/analysis#Workspace.ReferencesFor
proto://protolsp.v1.WorkspaceConfig.registry
pxf://config/chain.pxf#registry.address
sql://table/users
sql://query/github.com/trendvidia/steward/internal/db#GetUser
proto://file/proto/protolsp/v1/config.proto
md://docs/adr/002-node-identity.md#decision
md://CHANGELOG.md#changelog.0-21-0-2026-09-07.added
```

Rules that follow, and are not open:

1. **Positions never key anything.** A node carries `file`, `line`, `column` for
   navigation. Two builds of an unchanged tree must produce byte-identical IDs;
   a build after an unrelated edit in the same file must not change a node's ID.

2. **File-anchored IDs use repo-relative, forward-slash paths.** Absolute paths
   embed the machine that built the graph, so a CI-built graph and a local one
   would share no nodes.

3. **An unresolvable edge is dropped with a recorded reason** — never attached to
   the nearest plausible node. The dropped-edge count is part of the graph's
   output, so silent loss is visible rather than inferred.

   *Amended 2026-10-05 (#92):* the same holds one step earlier, for an input
   no ID can name — a file whose path holds a space, a reference with an empty
   segment, a column named `rate/sec` — and for one the layer cannot read. It
   is left out on its own and recorded with its reason in the build's
   `skipped` list; it never fails the build, which until then one such name
   anywhere in the tree did. A position is not an ID (rule 1) and may name any
   file inside the root, so a SQL table declared under `q dir/` keeps its
   node. Whether the ID grammar should admit whitespace instead of skipping
   is still open on #92; skipping is what both answers need for the names
   that stay invalid.

4. **A node that disappears between builds is gone, not tombstoned.** alidade
   builds on demand (ADR 005) and keeps no history; tombstones are a persisted-
   graph concept and would be dead weight here.

5. **A Go node's ID carries its module path.**
   `go://github.com/trendvidia/protolsp/analysis#Workspace`, not
   `go://analysis#Workspace`. IDs are unambiguous when two modules meet in one
   graph, and vendored copies of a package are distinguishable.

   *Amended 2026-10-06 (#128, the owner's decision):* a package's in-package
   test files have a node of their own, `go://<pkg>#(test)`, kind
   `package_tests`: the go tool's test variant of the package, `p [p.test]`.
   A Go identifier can't contain `(`, so no declaration shares it. ADR 004
   rule 4 says what it carries.

6. **A generated artifact is a distinct node from the schema that produced it,
   joined by a `generates` edge.** `protolsp.v1.WorkspaceConfig` (the `.proto`
   message) and the Go type in `.pb.go` are two nodes, not one.

7. **A file node belongs to the layer that parses the file.** A layer whose
   declarations are already file-anchored names the file by its bare
   repo-relative path: `pxf://config/chain.pxf`, `md://README.md`. A layer whose
   declarations are not file-anchored writes the file under a `file/` segment:
   `proto://file/proto/protolsp/v1/config.proto`. SQL has no file nodes (a
   migration is where a table is declared, not a thing the graph joins on) and
   Go's unit is the package. A reference to a file with no owning node is
   dropped under rule 3, not attached to the nearest layer.

8. **Markdown sections are identified by their heading path, dotted.** The
   fragment after `#` is the slug of every enclosing heading down to the
   section's own, joined with `.`:
   `md://CHANGELOG.md#changelog.0-21-0-2026-09-07.added`. A slug is lowercase
   letters and digits with every other run of characters collapsed to one
   `-`, so it never contains the separator. The fragment is therefore a dotted
   path like every other fragment in this grammar — `go://…#Type.Method`,
   `pxf://…#block.field` — and one validator covers all of them.

9. **A `.pxf` entry with no name of its own is keyed by the name it carries,
   else by its position.** A block in a list — `layers = [ { id = "base" … } ]`
   — takes its `id` or `name` field as its segment (`layers.base`), and its
   index when it has neither (`regions.2`). A directive is keyed by `@` and its
   name (`@header`, `@dataset`), a second of the same name by an ordinal. The
   index case is the one positional key this ADR permits, and only because the
   format itself gives such an element nothing else: reordering a list of
   unnamed blocks does re-key them, and a layer that wants stable identity
   for its list elements names them.

10. **A goasm template is identified by its file and its name.**
    `goasm://jit/templates/arm64.goasm#AddLocalsInt`; the file itself by its
    bare repo-relative path under rule 7, `goasm://jit/templates/arm64.goasm`.
    The name is one segment — it becomes part of the generated Go method's
    name, `Emitter.EmitAddLocalsInt` — so a `.` in it is an error, not a
    level. The same name in the arm64 and the amd64 file is two template
    nodes, since they are two declarations in two files; both carry a
    `generates` edge to the one Go method node, since that is one
    declaration path in one package (rule 5), whichever build-tagged file
    holds it. Added 2026-09-11 with ADR 003's goasm amendment.

11. **A frontend file is identified by its path; a generated export by its
    file and name.** `ts://web/src/routes/+page.svelte`, and for a file
    protoc-gen-es wrote, `ts://web/gen/app/v1/user_pb.ts#UserSchema`. The
    scheme names the layer, as `md` does, not the extension: a `.svelte`
    component and a `.tsx` module are both `ts://`. Hand-written files carry
    no export nodes (ADR 007: that needs a parser). A package imported by
    bare specifier and never read is `ts://pkg/@bufbuild/protobuf`, the
    package name with any subpath dropped, under a `pkg/` segment as rule 7
    puts `.proto` files under `file/`. Added 2026-09-11 with ADR 007.

    *Amended 2026-10-06 (#124, the owner's decision):* the package is
    `ts://@pkg/@bufbuild/protobuf`. Under `pkg/`, the file `pkg/chart.js`,
    in a directory wasm-pack and many Go repositories have, and the package
    `chart.js` were one ID. `@pkg` is a single-`@` first segment, which rule
    12's escape keeps every file path out of, so no file can share it, and
    no root may be labelled `pkg`. Every `ts://pkg/` ID changed.

    *Amended 2026-10-08 (#215, the owner's decision, with ADR 007's
    amendment):* a hand-written file's declarations are nodes, by file and
    declaration path: `ts://app/src/api.ts#fetchUser` for a module-level
    declaration, `ts://app/src/api.ts#Client.fetch` for a member of one, a
    default export without a name `#default`. Each name is one segment, so
    a member is always two. A module-level declaration of a generated file
    and that file's export of the same name are one ID, which is the point:
    a call into a generated file lands on the export node. A name no ID can
    carry — a computed name, one holding a `.` or whitespace — is no node,
    and counted. Pinned in `TestMintedIDsAreStable`.

    *Amended 2026-10-09 (#235, the owner's decision, with ADR 007's
    amendment):* a member is no longer always two segments. A named
    function nested in a declaration's body is a node under that
    declaration's path, at any depth, with the name of each object literal
    holding it between: `#attachSecretEditGuard.protectedAt`,
    `#Editor.handle.save`. The path comes from the declaration's place in
    the source alone, as before. Two nested functions of one name in one
    declaration are one ID. Pinned in `TestMintedIDsAreStable`.

12. **A second repository in one graph is a secondary root, and its
    file-anchored IDs carry the root's label.** A schema root — a repository
    whose `.proto` files another repository's generated code comes from
    (#47) — enters the graph with its file nodes and positions under
    `@<label>/`: `proto://file/@protolsp/proto/protolsp/v1/rpc/registry.proto`,
    the label being the root's directory name. Declaration-keyed IDs are
    unchanged: `proto://protolsp.v1.rpc.RegistryService` is the same node
    whichever root declared it, which is the point — the primary root's
    generated code names it, and now the edge lands. Rule 5 already did this
    for Go by keying on the module path; SQL names have no path to begin
    with. The `@` is what keeps a label from reading as a directory of the
    primary root; a label used twice in one graph is an error. A schema
    root contributes its `.proto` layer only, without the `generates` edges
    to Go types that are in its own repository and not in the graph.
    File-anchored links do not cross roots: the frontend layer's file-level
    `generates` matches the header's path among the primary root's `.proto`
    files, and reports rather than guesses when it is not there. Added
    2026-09-12 with #47.

    *Amended 2026-10-06 (#124, the owner's decision):* "the `@` keeps a label
    from reading as a directory" assumed no real path starts with `@`, and
    nothing enforced it. A primary-root directory `@protolsp/` and the root
    labelled `protolsp` minted the same IDs. A secondary root's own
    `@protolsp/` directory collided with its other files too, because the
    minter took a path already starting with its label to be labelled
    already. Two changes fix it:
    - **The escape.** A real path whose first segment starts with `@` is
      written with one more `@`, in IDs and positions both. A single leading
      `@` always starts a label, and two or more an escaped path:
      `@types/x.d.ts` is `ts://@@types/x.d.ts`. `graph.FilePath` reads the
      file system's path back.
    - **The typed path.** Inside alidade a minted path is a `graph.Path`,
      which only the `Minter` makes and which does not convert to a
      `string` silently, so a minted path is never minted again. Making the
      type surfaced every place that relied on the guess, and one bug it
      hid: a schema root's `buf.gen.yaml` inputs were matched against its
      labelled paths, which no input directory is, so its managed-mode
      `go_package` was never derived. None of 75 local repositories has a
      root-level `@` directory, so no existing ID changed.

13. **A SQL query is identified by its package and its name.**
    `sql://query/<package>#<Name>`, the package written as Go writes it: the
    import path of the package sqlc generates the query into
    (`sql://query/github.com/trendvidia/steward/internal/db#GetUser`, whose
    method is `go://github.com/trendvidia/steward/internal/db#Queries.GetUser`).
    A query in no package that generates Go is keyed by its file's
    repo-relative path instead. A query file two packages list is a query of
    each. Tables and columns stay keyed by name alone: a table is the
    database's, whichever package reads it, and a shared schema's table is
    linked to each package's struct. Added 2026-10-06 with #118, the owner's
    decision.

    Measured, not assumed: sqlc v1.31.1 builds one compiler per `sql:`
    entry (`internal/cmd/generate.go`), and its "duplicate query name" check
    (`internal/compiler/compile.go`) keeps its set inside that compiler, so
    a name repeats freely across packages and never within one. Keying a
    query by name alone folded two packages' `GetUser` into one node with
    both queries' edges. Every query ID in every repository changed with
    this rule. Still open: sqlc's catalog is per package as well, and two
    packages may target different databases, which a single table catalog
    merges; no repository does that yet.

14. **A Go struct field is identified by its type and its name.**
    `go://<package>#<Type>.<field>`, the form a method takes
    (`go://github.com/trendvidia/protoregistry/client#schemaSnapshot.rawDescriptorSet`).
    Go forbids a field and a method of one name on one type, so the two
    never share an ID. Only a type declared by a struct type literal has
    field nodes: `type X Y` over a struct `Y` has `Y`'s fields, declared
    at `Y`, and they are `Y`'s nodes. A blank field names nothing, and the
    fields of an anonymous struct nested in a field have no declaration
    path of their own; neither is a node. Added 2026-10-08 with #193, the
    owner's decision, which also deferred the edges from the functions
    that read or write a field.

    Measured, not assumed: on the 15 repositories of the oracle, 22,149
    fields, every one matching go/types' own record and none extra
    (`scripts/oracle.sh`). Before this rule, a doc naming a Go field — 3
    of the 34 declarations the labelled docs sample names (#189) — had no
    node to link to.

15. **A Python file is identified by its path; a declaration in it by its
    qualified name.** `py://bin/autofix/land.py`, and
    `py://bin/autofix/land.py#resolve_conflict`, `py://pkg/shapes.py#Square.area`,
    `py://app.py#main.inner`: each class or function holding the
    declaration is a segment, as in TypeScript (rule 11 and #235). Added
    2026-10-09 with ADR 008, the owner's decision (#237).

    The file anchors the ID, not the dotted module name Python itself uses.
    A module's name depends on the import path it is reached through, not
    on the file. agent-config's `bin/autofix/autofix.py` is the module
    `autofix` only because its tests put `bin/autofix` on `sys.path`. A
    `src/` layout, or a script reachable two ways, gives one file two
    module names, or none.

    The declarations with IDs are classes, functions, methods, a module's
    variables and a class body's attributes. A function's locals are not
    declarations; a function nested in one is. Two declarations of one
    qualified name in one file (a function defined in both branches of an
    `if`) are one ID, at the first. A name reached only through a lambda or
    a comprehension has no ID. Pinned in `TestMintedIDsAreStable`.

## Why 5 and 6 went that way

Both were open when this ADR was first written, because both cost a migration
rather than a revert to change. The reasoning, recorded so the trade-off is not
relitigated from memory:

**Module path (rule 5).** Omitting it is shorter and survives a module rename,
but collides the moment two modules share a graph — and there is no migration
from omitted to included that does not rewrite every Go ID. Renames are rare and
mechanical; cross-repo graphs are a stated direction. The asymmetry decided it:
the cost of including is a rewrite on a rare event, the cost of omitting is a
rewrite on a planned one.

**Generated nodes (rule 6).** One node is tidier and makes "what uses this
message" a single lookup. It also erases the distinction between a schema and its
implementation — "where is the generated Go for this message" becomes
unanswerable, and a schema generating into several languages has nowhere to put
the others. The `.proto` → `.pb.go` and `.sql` → sqlc links are the highest-value
edges in the plan (ADR 004); collapsing them deletes the information that
motivated those layers.

## Why 7 and 8 went that way

Both were implemented in PR #12 ahead of this ADR and pinned byte-for-byte in
`TestMintedIDsAreStable`; this amendment records them and, for rule 8, corrects
the reasoning the PR gave.

**Proto file nodes (rule 7).** #3 needs file nodes for `imports` edges, which
ADR 006 names as `EXTRACTED`, and a `.proto` file has no descriptor path. The
`file/` segment cannot collide with a descriptor path because that is a dotted
identifier and never contains a `/`; a package literally named `file` mints
`proto://file` or `proto://file.Foo`, neither of which has one. The segment is
not decoration: without it a root-level `api.proto` is indistinguishable from a
message `proto` in package `api`. The path is repo-relative (rule 2) rather than
the protobuf import string, because import strings are relative to include
roots — build configuration, not declaration — and two roots can make one
string mean two files. The consequence for #3: an `imports` edge is extracted
from the source, but its target is located through the include roots, and an
import that resolves to nothing on disk (a well-known type, a registry-only
descriptor) is dropped with a reason under rule 3.

A shared `file://` scheme for every layer's files was considered and rejected.
It would break "prefixed by its layer scheme" for one node kind, re-key the
`pxf://` and `md://` document forms this ADR already shows, and borrow a real
URI scheme whose absolute-path semantics are the opposite of rule 2.

**Heading paths (rule 8).** The real question was whether sections nest at
all, since every Markdown tool uses flat anchors. Measured on 2026-09-10 across
the nineteen in-house repositories, changelog-fragment directories excluded:

| | count |
|---|---|
| Markdown files with headings | 309 |
| headings | 4,861 |
| files with a duplicate flat slug | 14 |
| collisions under flat anchors | 400 |
| collisions under nested paths | 3 |

The collisions are not only changelogs. One pxfcloud file holds seventeen ADRs
each with a context, decision and rationale section; steward's governance
guide repeats "what it does" thirteen times; a protowire RFC repeats
"acceptance criteria" sixteen times. Those are the documents #5 values most,
and flat anchors would fold each of them onto one node. The three residual
collisions are changelogs with two "Changed" headings under one version, which
#5 handles as a documented collision policy rather than a grammar rule.

PR #12 joined heading paths with `/` and gave as its reason that dots survive
slugging. They do not — the slugger collapses a dot to `-` — so either
separator was safe and the choice fell to uniformity: dotted, like every other
fragment. `:` was also considered and rejected: it would be a third fragment
convention, and `file:line` is the one notation this ADR exists to reject.

A consequence for #5 to own: a Markdown link such as `other.md#decision` is a
flat anchor and resolves to a nested ID only when that slug is unique in the
target document. When it is not, GitHub's numeric-suffix convention is
positional, and rule 3 says drop with a reason.

*Implemented 2026-10-06 (#98):* a link's anchor is matched against the anchor
GitHub renders for each heading — github-slugger's rules, not this rule's
slug, which a link is never written against — and a heading whose text
another heading of the document repeats has no resolvable anchor: the link
drops as `#@ambiguous.…`, and one naming no heading as `#@anchor.…`. The
collision policy's `-N` suffix skips any path a real heading of the document
holds, and fenced blocks number within their section, which is what rule 1
requires of an ID.

## Alternatives considered

**Positional identity (`file:line:col`).** Rejected: every edit above a
declaration changes its ID, so a graph is valid only against the commit that
built it. This is what makes caliper's own finding shape (`{loc, sev, msg}`)
unsuitable as a graph key, and why this ADR exists.

**Content addressing (hash of the declaration).** Survives moves and renames,
which sounds attractive, but changes on every edit to the body — so a function's
identity changes when its implementation changes, which is backwards for a graph
whose purpose is tracking relationships between stable things.

**Opaque sequential IDs with a side table.** Stable within a build and meaningless
across builds; pushes the join problem into the side table without solving it.

## When to reopen this

* **A second repository enters one graph.** If (a) was answered "omit", this is
  the day it breaks, and the ADR should be amended before the collision is
  discovered in query results. *Reached 2026-09-12 (#47), by rule 12: the
  label on file-anchored IDs, the module path on Go IDs, and nothing on
  declaration-keyed ones.*
* **A layer arrives with no stable declaration path** — a format identified only
  by position. That layer needs its own scheme rule, not a weakening of this one.
* **The dropped-edge count stops being small.** Rule 3 makes it visible; if a
  meaningful fraction of edges fail to resolve, the identity scheme is not
  matching reality and the cause belongs here rather than in an adapter.
