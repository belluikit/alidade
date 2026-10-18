# ADR 005: Graph Construction — On Demand, Not Persisted

**Status:** Accepted
**Date:** 2026-08-29
**Relates to:** [001](001-core-architecture.md), [002](002-node-identity.md)

## Context

A code graph invites a store: build once, keep it fresh, serve queries from it.
That design brings an invalidation model, a staleness policy, a migration story
for the schema, and a class of bug where the graph disagrees with the working
tree.

Whether any of that is warranted is an empirical question, and it was measured.
Full cold build — `packages.Load` → SSA → CHA call graph — with a warm Go module
cache:

| repo | .go files | load | SSA | CHA | total |
|---|---|---|---|---|---|
| protocompile | 393 | 0.49s | 0.35s | 0.25s | **1.09s** |
| govm | 963 | 0.64s | 0.47s | 0.29s | **1.40s** |
| protolsp | 299 | 0.56s | 0.57s | 0.42s | **1.55s** |
| gonum | 1,369 | 0.58s | 15.39s | 0.19s | **16.17s** |

For comparison, the incremental path of the tree-sitter tool alidade replaces —
per-file content-hashed cache, one file changed — costs **1.25s** on protolsp.
Its cold build is 1.84s. So caching buys it 0.6s, because the cross-file
resolution and graph rebuild are O(corpus) regardless of how little changed.

A full type-resolved build is the same order as that tool's *incremental* path.

## Decision

**The graph is built on demand and held in memory for the session. There is no
persisted graph, no invalidation model, and no staleness policy.**

1. **Freshness is free.** The graph is built from the working tree at query time,
   so it cannot disagree with what is on disk. The entire class of "the index is
   stale" bugs does not exist.

   *Amended 2026-10-08 (#204, the owner's decision):* that held for the CLI and
   not for the MCP server, which holds one graph for the session and rebuilt
   only when an agent called `rebuild`. An agent that edited code or switched
   branches and then asked got an answer about the old code — the failure this
   ADR exists to rule out, which "Rebuild triggers are an implementation detail
   of the surface" below had left unchosen. The trigger is now every tool call:
   before a tool reads the graph, the server fingerprints the size and
   modification time of every file the build reads, under the root and each
   schema root, and when the fingerprint differs from the one taken before the
   last build, it rebuilds first; the answer carries `rebuilt` with the layers
   that reran. The fingerprint only decides whether to ask: what reruns is still
   decided by the layers' content signatures (#56), which a modification time
   cannot fool. Measured on steward, govm and protocompile (492–1,866 files):
   the fingerprint 3–20 ms per call, the content check 90–210 ms when it finds
   nothing, a rebuild after one Go file changed 1.2–5.6 s. The one edit it
   misses is a file rewritten to the same size with its old modification time
   restored, which no editor, git or agent does. Nothing is persisted, so the
   rest of this decision stands; the point is now true of the server as built,
   not only of the CLI. `TestServeAnswersFromTheFilesOnDisk` fails the day a
   call answers from files that changed since the build.

   *Amended 2026-10-11 (#269, the owner's decision):* an answer may come
   before the build is finished. Waiting is not always possible: jedi takes
   about 14 minutes on openpilot's 1,644 Python files, and the first answer
   of a session waited for all of it. The server now answers from the
   Python layer's warm-up pass and runs jedi in the background (ADR 008).
   - **What an answer may hold:** its nodes, and every edge outside the
     Python calls, are the current tree's, as before. A Python call jedi
     has not reached yet has an edge guessed by name, marked INFERRED
     (ADR 006).
   - **What the answer says:** while jedi runs, it carries `pending`, for
     example "jedi has resolved the calls of 1,518 of 1,644 Python files;
     calls from the rest are INFERRED by name until it reaches them".
   - **How it catches up:** at each call, the server folds in what jedi has
     finished since the last one, as it rebuilds for changed files. It
     rebuilds only the Python layer.
   - **What it never is:** stale. A file edited since the build is still
     rebuilt first, and the guessed edges are the current files' too.
   - **Once jedi is done,** the graph is the one a build that waits gives.
     Measured on openpilot: written out after jedi finished, it equals
     main's line for line apart from build time.

   The `graph` command still waits for jedi. Measured on openpilot:
   - the first answer came in 23 s, against about 14 minutes;
   - jedi had resolved 1,518 of 1,644 files after about 1.5 minutes, and all
     but one after about 8;
   - the guessed edges fell from 33,937 to 1,285 within 2.5 minutes;
   - a call that folds in new answers took 1.6–2.5 s, and one with nothing
     new 0.1 s.

   `TestToolsFoldInBackgroundProgress` fails the day a call misses what jedi
   has finished, or answers without saying what is pending.

   *Amended 2026-10-12 (#269):* after a Python edit, jedi reruns only the
   files whose answers the edit can change, and the rest keep jedi's
   answers from the build before (ADR 008 says how that is decided). This
   is the rule of #56 applied per file inside one layer: a file keeps its
   answers only when every file jedi parsed for them is byte-identical and
   no file it searched or looked up by name could now answer differently,
   and nothing leaves the process. Until jedi has reread a file, its calls are
   guessed by name and INFERRED, as on a first build.

   *Amended 2026-10-12 (#279, the owner's decision on #271):* jedi's
   answers are kept on disk between server sessions. This is the first
   thing alidade persists, and it is not the graph: it is each Python
   file's answers from jedi, with the content hashes of every file jedi
   read for them and the identity of the engine that gave them (ADR 008
   says how). A session loads them as the previous build and keeps an
   answer by the per-file rule above, so an answer kept from disk is one
   the tree as it is now would give; the rest of the graph is built as
   before. Why: on openpilot, jedi took about 17 minutes in every new
   session; with the answers kept, a session on an unchanged tree had jedi
   done in 1.7 s, and its graph equals a fresh full build. The file can be
   deleted at any time, and the next session rebuilds it.

   *Amended 2026-10-17 (#25, the owner's decision):* the graph tools
   answer for another `git worktree` of the repository, named by `root`,
   and the server holds that worktree's graph beside its own checkout's.
   It is not built from nothing: it is the base graph with the worktree's
   differences laid over it, as a union file system lays one directory
   over another.
   - **A layer whose files are the base's,** byte for byte by its
     signature (#56), takes the base's contributions as they are.
   - **A layer whose files differ** is extracted from the worktree, as a
     build of it would be, except the Go layer.
   - **The Go layer** loads again only the packages a differing Go file
     is in, and takes every other package from the base. A go.mod, go.sum
     or go.work that differs builds the layer whole.
     *Amended 2026-10-17 (#35, the owner's decision):* a differing
     package the host's configuration builds no file of (only another
     platform or a build tag brings it in) no longer builds the layer
     whole. A load naming it fails its module, so the host's load leaves
     it out. It is loaded under the further configurations the base's
     build chose, those of them that bring in a file of it, so it is read
     under the configuration a full build reads it under, or skipped as a
     full build skips it. With `-tests-exact` the load with tests takes
     those the base's load with tests chose, which bring in test files
     the load without tests has no configuration for. On trendvidia/go,
     a worktree changing internal/syscall/windows/sysdll took 3.9 s
     against 22.5 s for a full build, and 11.1 s against 2 min 4 s with
     `-go-configs auto`. `TestOverlayOfAnotherPlatformsPackage` fails the
     day such a package is read under another configuration than a full
     build's, and its case "the same choice, with tests" of
     `TestOverlayBuildsWholeWhenAutoWouldChooseOtherwise` the day a test
     file is.
     *Amended 2026-10-17 (#39 and #40, the owner's decisions):* the base's
     Go layer rests on two facts about whole modules. A load of only the
     differing packages cannot check either, so the layer is built whole
     when either may not hold for the worktree.
     - **Which further configurations `-go-configs auto` chooses, in
       what order.** The choice is made from every file the host's load
       leaves out. The base's report keeps that list, and the
       configurations its build chose from it. The worktree's list is
       the base's, with the directories loaded again replaced by that
       load's own. If auto would choose from it otherwise than the
       base's build chose, the layer is built whole (#39). The base's
       checkout is not read again: it may have moved on since the build
       its graph is of. Across the last 300 Go commits of trendvidia/go
       and of trendvidia/govm, no commit changed the choice.
     - **Whether each module loads for each configuration.** caliper
       refuses a load only when none of its packages type-checks. So the
       layer is built whole when the worktree changes a module the base
       could not load for some configuration. A module skipped for
       another reason is skipped by a full build of the worktree too,
       and is laid over: std and cmd of an unbuilt checkout of the Go
       repository (#219). It is also built whole when the load of the
       differing packages fails where the whole module's load would not,
       which includes a package with a type error loaded alone for the
       host (#40). The load of the differing packages runs module by
       module, so one module is enough: a worktree mid-edit is built
       whole while the only differing package of some module has a type
       error, even when differing packages of other modules compile. On
       trendvidia/go that took 22.0 s against 4.7 s laid over the base
       (#41 is the way back to the shorter time). Whether a module loads
       for another configuration also depends on the environment:
       `CGO_ENABLED` decides whether a file importing C is read there,
       and trendvidia/go's misc module fails for windows only when it is
       set. The base and the worktree are built in one process, under
       one environment, so the comparison holds either way.
     `TestOverlayBuildsWholeWhenAutoWouldChooseOtherwise`,
     `TestOverlayBuildsWholeWhenAModuleDoesNotLoad` and
     `TestOverlayChoosesAgainstTheBaseAsBuilt` fail the day such a
     worktree is laid over the base instead, and
     `TestOverlayOfAnUnbuiltGoRepository` the day a skipped module builds
     it whole.
   - **What a load of fewer packages cannot see** is a package's
     whole-program edges: every implementation an interface call reaches,
     every function value reaching a call, an implements edge to an
     interface outside the load. The base's such edges from a declaration
     the worktree still has, into a package not loaded again, are kept,
     INFERRED with carried=base (ADR 006: the ends are real nodes, the
     relationship may be stale).
   - **Freshness is the base's rule:** a worktree's graph is made again
     when its files change or when the base's graph does. Nothing is
     persisted. The server holds at most four worktrees' graphs and lets
     the one used longest ago go.

   The assumption this rests on: a package the worktree did not change
   has the base's declarations and edges, apart from whole-program edges
   into changed code. Measured on #25 against a full build of each
   worktree: on six govm worktrees the Go layer missed at most 0.06% of
   the changed packages' edges and had at most 0.07% wrong. On
   trendvidia/go, through the server's own path, go-notiny's graph equals
   a full build of it but for 2 stale carried edges of 533,866, in 3.9 s
   against 21.7 s. go-fastleaf's differs by 47 nodes in cgo test packages
   whose source is the base's and which each checkout's compiled toolchain
   resolves differently: an unchanged package is as the base's build saw
   it. The peak memory is the base build's: 7.4–8.0 GB with a worktree's
   graph against 7.8 GB without. Not measured: a worktree far from the
   base, where carried edges go stale as it diverges.
   `TestOverlayIsAWorktreeBuild` fails the day a worktree's graph differs
   from a full build of it by more than its carried edges. The one
   difference the assumption admits is pinned rather than counted: a call
   through an interface from a package the worktree did not change does
   not reach an implementation the worktree added
   (`TestOverlayMissesAnImplementationCalledFromUnchangedCode`).

   This is the reopen named below, "the MCP surface starts holding graphs
   across checkouts": the invalidation model is the layer signatures
   already in use, applied between two trees instead of two builds.

2. **Caching is delegated, not reimplemented.** Caliper's result cache
   (`pkg/mcp/resultcache.go`) already keys on a source fingerprint —
   `(relpath, mtime, size)` per file. alidade reuses whatever each layer provides
   and adds no cache of its own.

3. **No history.** A node that disappears between builds is gone (ADR 002, rule
   4). Tombstones, versioning and diffing are persisted-graph concepts.

4. **The session is the lifetime.** An MCP server builds on first query and holds
   the result; a CLI invocation builds and exits.

## What we are accepting

**gonum is the counter-example, and it is real.** 16.17s, essentially all in SSA
construction, for a 1,369-file numeric library. A repo of that shape would make
on-demand building painful, and nothing here pretends otherwise.

The bet is that the repos alidade targets look like protolsp, protocompile and
govm — 1–1.6s — rather than like gonum. ADR 003's corpus table supports that: the
in-house repos are in the 19–963 `.go` file range.

## Alternatives considered

**Persist the graph, invalidate on file change.** This is what the tool alidade
replaces does, and its own numbers argue against it: 0.6s saved, a 9.4 MB
on-disk cache of 300 files for a 299-file repo, and a full graph rewrite on every
single-file edit because the cross-file passes are O(corpus) anyway.

**Persist, and accept staleness with a background refresh.** Rejected: it trades
a 1.5s cost for a correctness risk. An agent reasoning from a stale graph reaches
a wrong conclusion confidently, which is the failure mode this whole tool exists
to avoid.

**Build once per process, never rebuild.** Effectively what "session lifetime"
means, with the caveat that a long-lived server holding a graph across a `git
checkout` is exactly the staleness bug in miniature. Rebuild triggers are an
implementation detail of the surface, not of the core.

## When to reopen this

* **A target repo's build exceeds roughly 5 seconds.** gonum already does. The
  moment a repo people actually query behaves like that, persistence stops being
  premature.

  *Reached, 2026-09-10, and accepted for now (#18):* with all five layers,
  steward builds in 7–11s (117 packages; 6s of it caliper's package load) and
  gameflow in 6.9s (3.4s in SSA and the call graph); every other in-house repo
  is 2–3s. The decision was to keep on-demand construction and record these two
  as known exceptions: the cost is paid once per session, alidade's own passes
  are a minority of it, and persistence would bring back the staleness this
  ADR rejects. The upstream fix — a caliper load mode that type-checks only the
  module's own packages from export data — is the next thing to try if a third
  repository joins them.

  *Narrowed, 2026-09-12 (#56):* the server holds one build Session, and a
  `rebuild` reruns only the layers whose files' contents changed — a
  content signature over what each layer reads costs about 200 ms on
  steward against 6 s for the Go layer — keeping the other layers'
  contributions and folding again. Nothing leaves the process, and a kept
  layer is one whose inputs are byte-identical to the build that produced
  it, which is not the staleness this ADR rejects. `graph_stats` says which
  layers were rebuilt and which kept.

  *Reached again, 2026-09-11, by an opt-in (#43):* `-go-configs auto` loads
  the Go module once more per further build configuration, and on govm that
  is three loads — host, linux/amd64, windows/amd64 — for 8.75s against 3.0s
  for the host alone. The default is unchanged (host only, 3.0s); the cost
  is the price of a graph that does not depend on the machine it was built
  on, paid only by a session that asks for it.
  *Widened, 2026-10-18 (#48, the owner's decision):* `auto` also reads
  what only a custom build tag or a platform beyond the desktop six brings
  in. A configuration is a platform with build tags, and auto chooses
  among ten platforms (adding js/wasm, wasip1/wasm, android/arm64 and
  ios/arm64), each with or without every tag the excluded files name. A
  node or edge such a load added says so in full,
  `build=windows/amd64+gl+mobile`. Measured with auto, against auto
  before: belluikit/bellui 74 s against 37 s (11 loads against 3), and
  every declaration of the 126 files only a tag or another platform
  brought in is now in the graph; trendvidia/govm 12 s, 4 loads against
  3; trendvidia/go 162 s against 1 min 52 s, at the same 12.9 GB peak.
  The default stays the host's alone.
  *Reached in another narrow form, 2026-09-12 (#67):* a repository of
  several Go modules is several loads, one per module, since a nested
  module with no go.work is what the go tool builds on its own. Measured
  warm: drover (two modules, 26,000 non-test lines) 0.4 s → 0.8 s;
  chameleon (three modules, two of them one package each) 0.3 s → 1.4 s;
  voya (seven) 0.3 s → 1.4 s; protocompile (two) 1.0 s → 1.6 s. A load
  costs 0.2–0.5 s before it reads a line of the module, so the cost is
  linear in modules rather than in code, and small modules are the
  expensive ones per line. A repository whose modules add up to the 5 s
  above is the same reopen as a single module that does.
* **The MCP surface starts holding graphs across checkouts** — at that point
  there is a cache with an invalidation problem whether or not it is called one.

  *Reached, 2026-10-17 (#25):* see decision 1's amendment of that date.
* **Cross-repo graphs arrive.** Building N repos on demand does not scale the way
  building one does, and that is the most likely trigger of the three.

  *Reached in a narrow form, 2026-09-12 (#47):* a schema root adds one
  repository's `.proto` layer, not its Go, so its cost is the `.proto` layer's
  — protolsp's is under a second on top of pxfed's build. On demand still
  holds; a second root's Go layer would be the day this reopens.
