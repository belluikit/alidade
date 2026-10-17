# Changelog

Entries up to v0.11.0 cite issue and pull request numbers (#281, #206)
from the tracker alidade had before it was published, which is not
public. v0.12.0 is the first public release, and its entries cite both:
#2 and #7 are this repository's issues, every other number the earlier
tracker's. From v0.12.1 on, every number is this repository's.

## Unreleased

- **Fixed: a graph built while jedi is still reading the Python files
  no longer keeps its INFERRED Python calls after jedi is done** (#34).
  A rebuild for an edit to a non-Python file kept the Python layer as it
  was last made, which could be older than what jedi had read; the
  server took that graph for current, and its calls stayed INFERRED by
  name until the next Python edit. A build that keeps the Python layer
  now folds in what jedi has read since, and so does a worktree's build
  that takes the Python layer from the session's.
- **Added: the graph tools answer for another worktree** (#25; ADR 005
  amended). `find_nodes`, `get_node`, `neighbors`, `shortest_path`,
  `subgraph_for_question`, `graph_stats`, `graph_issues` and `rebuild`
  take `root`, another `git worktree` of the repository, and the answer
  carries it. The worktree's graph is the session's with the worktree's
  differences laid over it: layers whose files are the same are reused,
  and only the Go packages whose files differ are loaded again. A call
  through an interface or a function value from those packages is kept
  from the session's graph, INFERRED with `carried=base`. A package the
  worktree did not change keeps the session's edges, so its calls through
  an interface miss an implementation only the worktree has. On
  trendvidia/go a worktree's graph took 3.9–4.4 s against 21.7–22.4 s for
  a full build. `graph_stats` with `root` says what was reused under
  `overlay`. The server holds four worktrees' graphs at most. The grep
  hook's refusal in a worktree names the graph tools too. `search_text`
  with `root` still never waits for a build: its hits name their
  declarations when that worktree's graph is already built and current,
  and otherwise carry a `note` saying why they name none.
- **Fixed: a `git worktree` nested inside the repository is not read as
  part of it** (#24; ADR 003 amended). One under `.claude/worktrees/`,
  where Claude Code puts them, had its files read under their nested
  paths, and every `search_text` hit came back once per worktree. One
  under an ordinary directory, not a dot-directory, also loaded the
  repository's Go module a second time. A submodule, which has a git
  directory of its own, is still read.
  A grep of a directory holding nested worktrees is refused with each
  one's `root` named (#31).
- **Fixed: a `search_text` answer whose first file or hit is bigger than
  the whole budget can be continued** (#32). It was cut with no `next`;
  that item is now listed alone, over the budget.
- **Added: a cut `files_only` answer says where its hits are** (#26).
  Its first page also carries `dirs`: hit and matched-file counts per
  directory, two levels below `path`, most hits first, with `dirs_omitted`
  when not all fit. A page continued with `after` lists files only. On trendvidia/go, `TODO`
  matched 1,033 files and the file list stopped after 221. The rollup
  shows `src/cmd` holding 1,277 of the 2,780 hits on the first call.
- **Added: `search_text` answers for another worktree, a commit, or all
  but some paths** (#23). `root` searches another `git worktree` of the
  repository, by its top directory, and `ref` searches a commit's files
  (`origin/main`, a pull request's head) without checking it out. Their
  hits name no declaration, since the graph is built from the session's
  own checkout. `exclude` leaves out every file under any of several path
  prefixes, such as `api/`.
- **Changed: the grep hook covers every worktree of the repository**
  (#23). A search in another worktree of the session's repository is
  refused as one in the repository is, and the refusal names the `root`
  to pass, a worktree inside the checkout (`.claude/worktrees/<name>`)
  included. `git -C <dir> grep`, `git --work-tree <dir> grep`,
  `git --no-pager grep` and `git -c key=value grep` are recognised as
  searches. The session's repository is the one it started in
  (`CLAUDE_PROJECT_DIR`), which alidade serves, wherever `cd` has moved
  the Bash tool since: a search after `cd` into a worktree is told its
  `root`, and one after `cd` into another repository is let through.
- **Fixed: continuing a cut `search_text` answer could skip files**
  (#23). Files were searched in directory order, `a/` before `a-c/`,
  while `after` compares paths as strings, so a page that ended in `a/`
  lost `a-c/` and `a.md`. Files are now searched in string order.
- **Changed: Homebrew installs from `belluikit/tap`.** The tap moved with
  alidade: `brew install belluikit/tap/alidade`. A tap already added as
  `trendvidia/tap` keeps updating through GitHub's redirect (#16).

## v0.13.0 — 2026-10-14

- **Changed: alidade lives at `github.com/belluikit/alidade`.** The
  repository moved from `trendvidia`, and the module path moved with it,
  so `go install` and imports use the new path. Issue and pull request
  numbers are unchanged. The old URL redirects to the new one for the
  repository, git, release downloads and pull requests, but not for links
  to single issues. The modules alidade builds on (caliper, chameleon,
  goasm, protocompile, protowire-go, pylib) stay in `trendvidia` (#16).

## v0.12.1 — 2026-10-14

- **Fixed: a release binary reports its release.** v0.12.0's binaries
  call themselves `v0.0.0-20261014064831-56503fac9adf`. Stock Go stamps
  a binary with a tag's version only when the tag is in the checkout, and
  the release job's checkout fetches no tags. It now fetches the release
  tag, refuses it unless it names the commit the gate passed, and fails
  when the darwin/arm64 binary does not report it (#11).

## v0.12.0 — 2026-10-14

The first public release. alidade is MIT-licensed, builds with stock Go,
and is developed in the open from here.

- **Changed: docs link to declarations written as a call, a package
  path's end, or a command's name** (#7; ADR 001 amended). A span that
  answers nothing whole is read as the name it calls: `Engine.Failed()`,
  `Validate(msg, present) error`, `Options{...}.Apply`, `(*T).M(x)`. A Go
  package answers the end of its import path (`chameleon/sops`), and a
  main package the name of its command when the name has a hyphen
  (`protoc-gen-chameleon-go`). A proto message and the Go type generated
  from it are one answer, the message. On nine public repositories this
  adds 243 links and loses none; 71 of 72 sampled were right. The recall
  sample's linked declarations go from 12 of 24 to 18; the precision
  samples do not change. Left unlinked on purpose: a name several
  declarations share that only the prose can settle (`Outside`, `Chain`,
  `source`), and a function against methods of its name, where
  preferring the function made 36 links of which 5 were wrong.

- **Changed: the Markdown layer's hand-labelled samples are drawn from
  public repositories** (#2). The samples that measured `documents` edges
  came mostly from private repositories and could not be published. They
  are drawn again, by rule, from nine public ones, and labelled by
  reading: 54 of 58 edges right (0.931, where the earlier sample gave
  0.914); 32 of 40 right for a bare lowercase word naming a nested
  declaration; and 12 of the 24 code spans that name a declaration are
  linked (0.50, where the earlier sample gave 27 of 34). The 12 missed
  are in #7. `TestDrawSamples` draws the items.

- **Fixed: adding or removing a Python file reran jedi on hundreds of
  files that could not have changed** (#281). A file kept jedi's answers
  only if no file had appeared or gone away under any directory name it
  had looked up, and on openpilot 400 files had looked up `openpilot`. A
  lookup of a directory's name finds the same package whatever files the
  directory holds, so a new file now counts under its own name, and a
  directory's name only when the directory itself is new or gone. On
  openpilot's last 20 Python commits, the 6 that add or remove a file
  rerun 3 to 53 files (jedi done in 0.7 to 10 s), where they reran 390 to
  560 (19 to 38 s). The finished graph equals a fresh full build at each.

- **Changed: jedi no longer searches other files for a parameter's
  callers** (#265; ADR 008 amended). It searched every file of the project
  to guess what a parameter holds, for calls the layer leaves unplaced
  anyway. graphify's Python layer is about 2.7× faster on a full build, and
  on openpilot jedi reruns 3–10× fewer files after an edit and about half as
  many after a file is added or removed. On openpilot, 18 edges guessed
  through a parameter are gone, 2 of them real calls; on agent-config, 5
  calls seen at run time, all through a parameter.

- **New: the server keeps jedi's answers between sessions** (#279; ADRs
  005 and 008 amended). Every new session ran jedi over the whole tree
  again: on openpilot, about 17 minutes before every Python call was
  jedi's. The answers now go to a file per tree under the user's cache
  directory (`alidade/py/`), and the next session reuses each one that
  nothing since can have changed, by the rules a rebuild after an edit
  uses.
  - **On openpilot,** a new session on an unchanged tree had jedi done in
    1.7 s; after an edit to tinygrad's `helpers.py` between sessions, 576
    files reran and jedi was done in 125 s. Both graphs equal a fresh full
    build.
  - The file is about 48 MB for openpilot and can be deleted at any time.
    `alidade graph` keeps nothing.

- **Fixed: Python imports could resolve to nothing at random** (#279). jedi
  workers shared parso's cache of parsed files on disk, and one reading a
  file another was writing lost the import it was resolving. On openpilot,
  after an edit to `helpers.py`, two files lost their import of it in some
  builds. Workers now keep parsed files in memory only, and alidade no
  longer writes to `~/Library/Caches/Jedi` (`~/.cache/jedi` on Linux), which
  can be deleted.

- **New: after a Python edit, the server reruns jedi only where answers can
  change** (#269; ADRs 005 and 008 amended). Before, one edited `.py` file
  sent jedi back over every file: on openpilot, about 14 minutes before
  every call was jedi's again. jedi now records what it reads for each
  file (the files it parses, the module names it looks up, the names it
  searches the project for), and a file keeps jedi's answers unless one of
  those changed. Until jedi has reread a file, its calls are INFERRED by
  name, as on a first build.
  - **On openpilot's last 20 Python commits,** replayed one after
    another: jedi was done 4.6 to 16 s after a commit that only edits
    files (37 to 152 of about 1,640 files rerun), and 64 to 75 s after one
    that adds or removes a file, against 845 to 985 s for a full build.
  - **The finished graph is still exactly a fresh build's,** at all 20.

- **Fixed: `search_text` did not search directories named `build`, `dist` or
  `out`** (#275). They are build output to the TypeScript and Python layers,
  but the Go layer reads Go packages with those names, so `internal/build`
  had no hits at all. `search_text` now leaves out a directory by name only
  when every layer skips it, and the grep hook lets grep in only where
  `search_text` does not search.

- **Fixed: every layer leaves out installed packages** (#266). Only the
  Python layer did, and on openpilot the Markdown layer read 156 documents
  from a virtual environment's `site-packages`. A directory holding a
  `pyvenv.cfg`, or named `.venv`, `venv` or `site-packages`, is now left out
  of every layer, `search_text` and the build's fingerprint, whatever
  `.alidade.pxf` says. The Python layer also leaves out `.tmp`, as the others
  do.

- **New: Python is a layer of the graph** (#237, the owner's decisions; ADRs
  002, 003, 006 and 008). Built with the `python` tag, every `.py` file's
  classes, functions, methods, module-level variables and class attributes
  are nodes, `py://<file>#<qualified name>`, and imports and calls are edges
  that jedi's type inference resolves (RESOLVED). A call through a parameter
  is counted, not guessed. A tree over 256 KB is shared between worker
  processes. On agent-config: 2,100 Python nodes, 1,166 calls edges, about
  14 s; of the 1,192 calls its tests make at run time, 995 are edges and 174
  go to a stub a test installed.

- **New: more Python calls are edges** (#242, #244; ADR 008 amended).
  - A callee that is an expression, `make()(x)` or `TABLE[k](x)`, is resolved
    by its value.
  - A `with` statement calls its value's `__enter__` and `__exit__`, its own
    or inherited.
  - A function named as a value without being called (a stub installed, a
    callback passed, a function returned) has a `references` edge: 130 on
    agent-config, 415 on graphify.
  - Together, agent-config's tests' calls found as edges go from 995 to 999,
    and graphify's from 10,175 to 10,180.

- **Fixed: alidade did not build from source on Linux** (#246). pylib's
  static libraries were listed in an order GNU ld cannot link, and the `v8`
  tag needs clang: the README now says to build with `CC=clang CXX=clang++`.
  Two tests assumed the developer's machine, and the SQL oracle failed with
  every configuration "missing" when sqlc is a snap, which cannot read
  `/tmp`; it now says so and how to fix it (`TMPDIR` under your home).
  
- **New: a class implements the class it inherits from** (#249, the owner's
  decision on #240; ADRs 006, 007 and 008 amended). A Python class's bases and
  a TypeScript class's `extends` clause are now `implements` edges to the base
  class, direct bases only, so "what derives from `Harness`?" has an answer.
  A base from a package (`Exception`, `Error`) has no node and no edge. On
  agent-config 4 edges, on graphify 27; the Python execution oracle now
  credits a subclass's inherited `__init__` (agent-config 999 → 1,000 calls
  found).

- **New: reading a Python property calls its function** (#253; ADR 008
  amended). `r.b64` runs `ImageRef.b64`, so it is a `calls` edge, including
  a read that is itself the object of another attribute
  (`r.b64.decode()`). On graphify, calls seen at run time that are edges go
  from 10,180 to 10,194; agent-config is unchanged.

- **Changed: the Python oracle counts import-system calls apart** (#254, the
  owner's decision on #240). `from m import x` makes the import system call a
  module's own `__getattr__`, which no line of the code does. Those calls are
  now counted as import probes rather than missing: 95 on graphify, whose
  missing calls go from 264 to 169. The graph is unchanged.

- **New: Python calls through a value annotated `Callable` are edges**
  (#258; ADR 008 amended).
  - A factory's closure: a function whose return annotation names
    `Callable` now has its return statements inferred too, so
    `inc = adder(1); inc(x)` is a call of the closure `adder` returns.
  - A dataclass field: `resolver.resolve(...)`, where `resolve: Callable` is
    a field, is a call of each function a construction of the class passes
    for it, by keyword or by position.
  - On graphify, calls seen at run time that are edges go from 10,194 to
    10,231, and its missing calls from 169 to 135; agent-config is
    unchanged.

- **Fixed: Python lookups late in a long file could answer nothing** (#255).
  jedi caps how often it infers one node, per parsed file, and never resets
  that count between lookups, so on a long file popular nodes (typeshed's
  `list`, `dict`) ran out and later lookups through them came back empty.
  The count is now reset per lookup, and the retries that half made up for
  it are gone. On agent-config 25 more `references` edges, on graphify 7 more
  `calls` edges, nothing lost; building the Python layer takes 45% less CPU
  on agent-config and about 70% less on graphify.

- **New: the server answers a Python project in seconds, not after jedi**
  (#269, the owner's decision; ADRs 005 and 008 amended). `alidade serve`
  answers first from a pass that resolves calls by name alone, and runs
  jedi behind it. Each call folds in what jedi has finished, and says how
  far it is under `pending`. A call jedi has not reached is marked
  INFERRED; once jedi is done, the graph is exactly the one it gives today.
  - **On openpilot:** the first answer in 23 s instead of about 14
    minutes; 1,518 of 1,644 files resolved by jedi after about 1.5 minutes;
    each later answer in about 2 s.
  - **Accuracy of the guesses:** they place 69% of the calls jedi places on
    openpilot and 99% on agent-config and graphify. At most 0.2% of them
    could be wrong.
  - **Also:** workers read their smallest files first. `alidade graph`
    still waits for jedi, and its output is unchanged.

- **Changed: Python workers hand on each file as they finish it** (#269;
  ADR 008 amended). The first step towards answering before a large
  project's Python layer is fully built. A worker that fails now has only
  the files it had not finished read again, not all of its files; one whose
  output ends before its last job counts as failed even if it exits
  cleanly. The graph is unchanged.

- **Fixed: a Python file in a large project could lose every call** (#272;
  ADR 008 amended). jedi's parser dropped cached files it was still reading,
  once a worker had parsed 600 files, unless the file was edited in the last
  10 minutes. Every later lookup in such a file then failed. On openpilot
  that emptied 5 files in a full build and cut a sixth from 24 call edges to
  15; whether a file was hit depended on which files its worker read first
  and on the clock. Workers now keep every
  parsed file: openpilot gains 74 call edges and loses none, and each worker
  peaks about 10% higher in memory (1.04 GB against 0.94 GB). agent-config
  and graphify are unchanged.

- **Fixed: two builds of a large Python project could differ** (#267; ADR
  008 amended). jedi tried an expression's possible values in an order set
  by memory addresses, and where it stopped early, that order decided which
  calls it found. Two builds of openpilot differed by 8 call edges. jedi now
  tries them in a fixed order, and two builds of openpilot are identical.
  agent-config and graphify are unchanged, and no build is slower.

- **New: a call to a decorated Python function reaches the decorator's
  wrapper** (#261; ADR 008 amended). `fix_red_ci(...)`, where `fix_red_ci`
  is decorated with `@reclaiming`, is now also a call of the wrapper
  `reclaiming` returns, which is what runs first. This includes wrappers made
  with `functools.wraps`. On agent-config 2 more `calls` edges, both seen at
  run time; graphify is unchanged.

- **New: a `python` build tag links CPython** (#237, the owner's decisions:
  CPython embedded, from a separate module, behind a tag; ADR 008). It
  carries CPython 3.14.8 as static libraries from the new private module
  `github.com/trendvidia/pylib`, plus jedi 0.20.0, which resolves a Python
  file's calls in process. Release binaries are built with the tag, and
  `alidade version` now names the runtimes a binary links
  (`; v8, cpython 3.14.8`).

- **New: a named function nested inside another is a TypeScript node**
  (#235, the owner's decision; ADRs 007 and 002 amended). A component's
  handlers, a hook's `useCallback`s and the methods of an object a function
  returns now have their own nodes, `#Outer.inner`, and their calls come from
  them rather than from the outer function. On pxfed, `getCurrentRanges`'s
  callers now include `attachSecretEditGuard.protectedAt` and `Editor.save`,
  which the benchmark expects; pxfed gains 176 nodes, steward 3.

- **Fixed: a SvelteKit app never synced lost a third of its TypeScript calls,
  silently** (#232, the owner's decision; ADR 007 amended). Its `tsconfig.json`
  extends `.svelte-kit/tsconfig.json`, which only `svelte-kit sync` or a build
  writes, so in a fresh clone the `$lib` and `kit.alias` imports resolved to
  nothing (steward: 658 → 426 calls edges). alidade now synthesizes that config
  from `svelte.config.js`, in memory, and records that it did. A fresh clone of
  steward gives the built checkout's TypeScript layer exactly.

## v0.11.0 — 2026-10-09

- **New: a TypeScript call through an interface reaches its implementations**
  (#229, the owner's decision; ADR 007 amended). A class's `implements` clause
  is an `implements` edge to each interface it names and those they extend,
  and a call to an interface member is also a calls edge, marked
  `via=interface`, to each implementing class's member. On pxfed the methods
  of its implementing classes with a caller go from 2 of 50 to 39 of 50, and
  the oracle agrees edge by edge (522 of 522).
- **Changed: the grep hook says how grep comes back** (found in its own
  log). An agent read "a session that truly needs grep runs with
  `ALIDADE_ALLOW_GREP=1`" as a prefix to add, wrote
  `ALIDADE_ALLOW_GREP=1 grep …`, and was refused without being told why.
  The refusal now says the variable is read from the environment a session
  starts with (`ALIDADE_ALLOW_GREP=1 claude`), and a command that sets it
  itself, inline or by `export`, is told that is not honoured — honouring
  it would let any agent lift the refusal at will.

## v0.10.0 — 2026-10-09

- **New: calls in Svelte components are edges** (#221, the owner's decision;
  ADR 007 and ADR 000 amended). Each component is translated by svelte2tsx,
  Svelte's own translator, run in V8 embedded in the binary (no Node), and the
  TypeScript checker reads the translation. A call in a component's script is
  an edge from the function holding it, one in its markup from the component,
  and a component used in markup is a call to it. On steward, TypeScript calls
  edges go from 94 to 658, and the oracle agrees edge by edge. **The binary
  links V8**: about 95 MB to 112 MB per platform, against 40 MB. Building from
  source needs `CGO_CXXFLAGS=-nostdinc++ go build -tags v8`. Without the tag,
  components say their calls are not in the graph.

- **Fixed: an unbuilt checkout of the Go repository was read against the
  installed toolchain** (#219, the owner's decision). Without its own
  compiler the checkout cannot be its GOROOT, and the graph held the
  installed toolchain's `cmd` and test files under the checkout's paths,
  or nothing, unsaid. Its `std` and `cmd` modules are skipped now, each
  saying to run `./make.bash` in `src/`; after it, the checkout reads in
  full.

- **New: TypeScript declarations and a type-resolved call graph** (#215,
  the owner's decision; ADR 007, 002, 000 and 003 amended). TypeScript 7's
  own compiler, typescript-go, runs in process — a submodule pinned at
  `typescript/v7.0.2`, reached through a shim module, as oxc's tsgolint
  does, since it publishes no Go API. A hand-written file's module-level
  declarations and their members are nodes (`ts://app/src/api.ts#Client.fetch`),
  and calls, `new` and JSX elements are RESOLVED `calls` edges to the
  declaration the checker resolves, through imports and re-exports. Against
  the repository's own TypeScript under Node, edge by edge: pxfed 472 of
  472, steward 94 of 94, none extra either way. Svelte components are not
  checked, and their nodes say so. Costs +15.3 MB of binary (40.0 MB
  stripped) and, on pxfed, 0.26 s and 207 MB of peak memory a build.
  **Building from source needs the submodule** (`git clone
  --recurse-submodules`); `go install …@latest` no longer works.
- **Fixed: `search_text` placed a TypeScript hit by position** (#212): the
  hit #211 labelled `App` was in `AppContent`. A hit in a TypeScript
  declaration is in it exactly now, and one at module level is in none.
- **New: `.alidade.pxf` says which directories alidade leaves out** (ADR
  003, amended). `vendor` and `third_party` are left out by default, by
  every layer, `search_text`, the fingerprint and the search hook alike,
  and `skip = ["vendor", "generated", "web/legacy"]` in `.alidade.pxf` at
  the root replaces that list: a bare name is skipped anywhere, a path
  from the root. Submodules are read like any other directory.
- **Fixed: in a checkout of the Go repository the standard library was
  not in the graph, and nothing said so** (#217). Its packages are
  `bytes` and `net/http`, not under their module's path `std`, so the Go
  layer took every one for external; `search_text` hits in `src/` named
  no declaration. They are the tree's now, and such a checkout's
  dependencies are read from source: export data would be compiled by
  the checkout's own compiler, which refuses a go command of another
  version. On trendvidia/go: 103,315 nodes against 52,023, 287,956 calls
  edges against 91,983, one failed package against 30.

- **Fixed: the graph build panicked on a method with type parameters of
  its own** (#216). Go 1.27 admits them, and the standard library has one,
  `math/rand/v2`'s `Rand.N`; x/tools v0.44.0's call graph panicked walking
  it (`panic: Int`), and every tool answered `internal error: Int` in a
  repository whose code reached it — trendvidia/go among them. x/tools is
  v0.51.0 now, which skips such methods where it walks runtime types, and
  calls to them are edges. A panic inside the call graph's build no longer
  fails the build: the module's packages are named in
  `go_packages_without_calls`, the panic as their cause, and their
  declarations stay.

- **Fixed:** the grep hook refused a search after `cd $D` (#208's hook,
  found in use): the unexpanded variable was taken for a path inside the
  repository. A `cd` to a variable or a command's output now leaves the
  search unplaced, and an unplaced search is let through.

- **Fixed:** the grep hook judged a search by the session's directory,
  not the files it names (#214): `grep ERROR /tmp/server.log` run from
  inside a repository was refused. The search's own words are read now —
  options and their values, the pattern, the files, an input redirect —
  and it is refused only when a file it names is in the repository and
  outside what `search_text` skips. A here-document's body (a commit
  message through `<<'EOF'`) is no longer read as commands. A search of a
  pipe's output was never refused, and still is not.

## v0.9.0 — 2026-10-08

- **New: `alidade setup -enforce` and `alidade hook`** (#208, the owner's
  decision). A Claude Code PreToolUse hook refuses the Grep tool and
  Bash's grep, rg, ag, ack and git grep in the repository a session runs
  in, and tells the model to use `search_text` or `find_nodes` /
  `neighbors` instead; a search elsewhere, under a directory
  `search_text` skips, or of a pipe's output runs as before.
  `ALIDADE_ALLOW_GREP=1` lets a session through, `-no-enforce` removes
  the hook, refused calls are logged to `~/.local/state/alidade/hook.log`.
  Checked in a real headless session: the model received the hook's
  reason as the tool result.

- **Measured: alidade without grep** (#206). Every benchmark question
  (43, over steward, govm and protocompile), twice, with alidade and Read
  but no Grep against Grep and no alidade: 86 of 86 correct at $0.032 a
  correct answer against 84 of 86 at $0.042. Equal or better on every
  question shape; cheaper on call-graph (−33%) and text (−14%) questions,
  dearer on schema-to-code (+6%) and the one doc question.

- **Answers are always about the files on disk** (#204, the owner's
  decision; ADR 005 amended). Before every tool call the server checks
  whether any file's size or modification time changed since the build,
  and if one did, rebuilds the changed layers first and says so in the
  answer's `rebuilt`. An agent no longer has to call `rebuild` after
  editing or checking out, and an answer about code that has since
  changed is no longer possible. Measured: 3–20 ms per call when nothing
  changed; 1.2–5.6 s on the first call after a Go file changed.

- The benchmark gains seven questions where a text search should
  struggle (#202), over govm and protocompile (new sets) and steward:
  callers of a method many types share, and three-hop call chains. First
  run ($1.38, 28 sessions): on the shared-name callers, alidade 8 of 8 at
  $0.030 a correct answer, grep alone 5 of 8 at $0.145 (it missed callers
  on protocompile, saying it had not checked the receivers); on the
  chains, both 6 of 6 at the same cost. Agents used alidade in every
  session of its arm. The README says where it beats a text search.

- **A doc's SQL clause links to the table or column it names** (#192):
  `app.org_subscriptions.seat_limit IS NOT NULL`, `DELETE FROM
  trader.health_canary`. When a span with spaces answers nothing whole,
  its qualified names are tried against the SQL layer, and the one table
  or column they name is the link; two are none. On the corpus: 11 links,
  11 right. A method called on a variable (`v.AsString`) stays unlinked:
  resolving it by the method name and the section's words linked 14,
  5 of them wrong — `t.Run`, `t.C`, `wg.Done` are the standard library's,
  which a doc never says.

- **A bare `.pxf` file name no longer links to the repository's own file**
  (#191). `governance.pxf`, `chain.pxf`, `.protolsp.pxf` name a
  configuration convention, and the docs that write them mostly describe
  the format: of 30 such links sampled, 25 meant any repository's file.
  A bare file name now links to the file of that name in the doc's own
  directory when there is one (an example's README to its own
  `governance.pxf`, which used to reach the root file), and otherwise, for
  a `.pxf` file, not at all. Code files keep their links (13 of 13
  sampled bare `.ts` and `.proto` names were right). On the corpus: 131
  links removed, 5 sibling links added; the precision sample loses its one
  right bare `.pxf` link.

- A doc naming one of several same-named declarations stays unlinked
  (#190, the owner's decision, option E), and the server instructions
  and README now say so and point to `search_text`, which finds every
  such mention (gameflow's four `SettleMarket` mentions in ADR 0014,
  each placed in its section).

- **New edges: a `.pxf` entry references the `.proto` field it sets**
  (#190, the owner's decision; ADR 001 amended). The path is walked
  through the document's `@type`, or a typed directive's (`@header`); an
  element of a list or map sets its collection's field. A chameleon
  layer with no `@type` takes the one type the other layers of its chain
  declare (DECLARED, `via=chain`); a chain with no declared type, or two,
  gives none — gameflow's layers declare only `@header`, so they stay
  unlinked. On the corpus: 1,434 entries linked; protowire-go's decoder,
  against protoc's descriptors, confirms 826 of 826 distinct fields in
  23 documents, none extra (`scripts/oracle.sh`'s new schema row).
- **Docs link to a schema field its documents also name** (#190): a
  mention answered by a proto field and by the `.pxf` entries that set it
  now names the proto field, where it was ambiguous. 225 more documents
  edges, none lost; 40 of 40 sampled right. The recall sample's
  `blocked_module_globs` now links (27 of 34).

- Field uses stay out of the graph (#195, the owner's decision): edges
  from functions to the struct fields they read or write would add about
  37,000 edges, and on the benchmark's three field-setter questions a
  text search answered every session correctly (6 of 6) without them.
  The README says so.

- **New nodes: Go struct fields** (#193, the owner's decision; ADR 002
  rule 14). Each field of a struct type literal is a node,
  `go://pkg#Type.field`, contained by its type and referencing the named
  types its type writes. Generated fields are in, marked generated. On
  the 15 Go repositories of the oracle: 22,149 fields and 5,134 field
  references, every one matching go/types and none extra. Docs now link
  to fields: 362 more documents edges, none lost; a field written as
  `Type.Field` links as a method does, and a bare field name only when
  nothing else answers, its type is named in the section, and it is not
  one lowercase word (`timeout`, `unknown` are values in prose). Of 50
  new edges sampled, 50 are right. Edges from the functions that read
  or write a field are not in yet.

- **More docs link to the code they name** (#189). A code span now
  links when written as a method qualified by its package
  (`tennis.Parse` for `tennis.Adapter.Parse`), as a Go package's import
  path or directory (`go/ast`, `internal/doctor/`), or as a hyphenated
  name (`wrong-audience`, which, like a bare lowercase word, needs its
  parent named in the section). Each form is tried only when the
  existing ones answer nothing, so no link that resolved before is lost:
  on the 16 repositories, 550 documents edges are added and none
  removed, and 56 of a sample of 57 new ones are right.
- `scripts/oracle.sh` measures Markdown recall (#189): 120 code spans
  drawn from the docs, not from the edges, each labelled by reading.
  Of the 31 that name a declaration with a node, 21 were linked from
  their section (0.68); with the forms above, 25 (0.81). The six left
  are four ambiguous names and two spellings a name lookup cannot
  answer.

- `scripts/oracle.sh` checks the goasm and PXF layers (#187):
  - goasm: each template through the generator its `//go:generate` line
    pins, one at a time. govm: 88 / 88 links to Go methods, none extra.
  - PXF type references: a reader written from the protowire
    specification, over the raw text, sharing no code with the parser
    the layer uses; protoc confirms each name the repository declares.
    35 / 35 on 68 files, none extra. 11 name schemas of other
    repositories (chameleon's, protolsp's).
  - PXF `${path}` references: 3 / 3.
  - PXF chain links: all 26, judged by reading against each chain and
    pinned to each repository's commit. No independent tool expands a
    chain.
  - Not checked: a PXF document's blocks and fields. protowire-go is the
    only PXF parser.

- The release workflow tests the MCP surface on the runner that builds
  the release, with its toolchain, before building: `go test
  ./internal/mcp`, and again for darwin/amd64 under Rosetta where the
  runner has it. A go-plus older than v1.27.1a on that runner would have
  shipped #160 again while CI passed on another one.

## v0.8.0 — 2026-10-08

- **New edges (#180, the owner's decision):** a Go function or method
  now `references` each package-level constant and variable of the
  repository it names in its body, RESOLVED by the type checker. "What
  reads `caliperBinaryEnv`" is `neighbors` in. A local or a field of the
  same name is not a use; a closure's uses are its function's. Uses of a
  dependency's constants and variables, and uses in a package-level
  initializer, are not edges. 15,926 edges on the 15 Go repositories of
  the oracle, every one matching the type checker's own record and none
  extra (`scripts/oracle.sh`'s new uses row). Before this, a whole-word
  text search found exactly a constant's or a variable's uses for only
  41–58% of them.

- The frontend oracle checks the imports it used to leave out (#171):
  `.svelte` components, Node builtins (`node:fs`) and virtual modules
  (`$app/state`, `astro:content`), all from TypeScript and Node rather
  than from alidade's resolver. steward's imports are now compared in
  full, 634 / 634 where 95 edges went unchecked; pxfed's 412 / 412 where
  48 did. One import is still unresolved and named: a CSS file of
  another workspace package.

- **Changed (#156, the owner's decision):** every tool is marked
  `anthropic/alwaysLoad`, so Claude Code loads alidade's tool schemas up
  front instead of hiding them behind tool search. Measured on steward's
  question set, 50 sessions per arm: with the tools deferred, no session
  called alidade; loaded, 21 of 50 did, and those sessions cost 11% less
  in total and 23% less on the call-graph questions. The price is about
  2,300 tokens of context per request.

- **New tool:** `search_text` (#178, the owner's decision; ADR 000
  amended). It searches the working tree's text, a literal or a Go
  regular expression, for what the graph doesn't hold: a string literal,
  a log message, a comment. Each hit comes with the declaration holding
  it, as a node the other tools take: exactly in Go, by position
  elsewhere, marked `guess`. `files_only`, `exclude_tests`, and
  `after`/`next` continue a cut answer. Measured against Grep on ten text
  questions, the same model and the same window: as often correct, 22%
  and 33% less per correct answer, fewer than half the calls. It is
  alidade's one analysis of its own, and it never writes the graph.

- **Changed (the MCP answer format, the owner's decision on #157):**
  a traversal's answer (`find_nodes`, `neighbors`, `shortest_path`,
  `subgraph_for_question`) is shorter and loses nothing. An edge's `f` and
  `t` are indexes into the answer's nodes instead of IDs; a node's `n` is
  left out when its ID ends with it, and its `at` reads `:line` when the
  ID names its file. `get_node` and `graph_issues` are unchanged. On
  steward's real answers: 14–45% smaller with the same nodes and edges,
  and an answer at its budget holds about twice as many.

- `scripts/mcp-e2e.sh` checks the MCP surface end to end (#174): the
  release-built binary over stdio on steward, drover and gameflow, its
  descriptions against the source, every tool's answer, and 20
  declarations' callers per repository against the graph. Run against
  v0.7.0 as shipped, it fails on #160's 30 misdescribed parameters.
  
- The MCP server says what the graph cannot answer (#173): it holds
  declarations and how they relate, not text, so a question about a
  string literal, a log message, a comment or prose needs a search of the
  files. It is in the instructions, which load even when an agent defers
  the tool schemas, and in `find_nodes`. 165 bytes.

- `scripts/oracle.sh` measures the frontend layer (#168): imports against
  TypeScript's own resolution (each file's `tsconfig.json`, the
  repository's `typescript` package), and generated exports against the
  descriptor protoc-gen-es embeds in every `_pb.ts`. Exact on steward
  (imports 534, exports 474) and pxfed (358, 43). Not compared: 143
  imports TypeScript cannot resolve (Svelte components, SvelteKit's
  virtual modules), and 3 imports of `.json` files, which ADR 007's node
  types leave out.
  
- **Fixed:** a doc's everyday word no longer links to the one field,
  method or block that shares it (#167). A bare lowercase word in a code
  span (`done`, `status`, `prod`) naming a nested declaration now links
  only when the section, or its heading path, names the declaration's
  parent. The labelled sample's precision rises from 0.914 to 0.982 with
  no right edge lost. The cost, measured on a second sample of this
  class: about a third of its right links go too (6 of 17); on the corpus
  the rule removes 189 of 4,643 documents edges, most of them wrong.

- A doc's RPC procedure path names that RPC (#158, the owner's decision:
  option b). `/api/pkg.v1.Service/Method` in a code span, or in a fenced
  block such as an HTTP example, now gives a `documents` edge to the RPC;
  nothing else in a fence is read. On steward this adds 8 edges, all
  correct, among them the one the benchmark found missing
  (`using-the-steward-api.md` → `EvaluationsService.Create`).

- `scripts/oracle.sh` reads a hand-labelled sample of Markdown
  `documents` edges (#148): 58 drawn from the 4,632 on 10 repositories,
  each judged by reading the sentence and the declaration, pinned to the
  commit it was judged at. Precision 0.914: the 5 wrong edges are code
  spans naming an everyday word that one declaration shares (#167). A
  repository with no Go module now has its SQL, proto and Markdown
  measured; it was skipped whole.

- `scripts/oracle.sh` measures the proto layer too (#148): declarations,
  imports and references against protoc, Google's compiler, which shares
  no code with protocompile; generated Go types against the type tables
  protoc-gen-go writes into each `.pb.go`. On the 12 repositories with
  `.proto` files, every count is exact after the two fixes below:
  declarations 7,286, imports 289, references 2,110, generated types
  1,788. Not compared: 31 files protoc refuses (26 import files not on
  disk, 4 use options on purpose invalid, 1 is a template) and 121 map
  entry messages, which the layer makes no node of.
- **Fixed:** buf's managed mode replaces a `go_package` the file writes
  (#148). The layer applied it only to a file with none, so all 34 of
  protovalidate's generated types were linked to the registry's package
  instead of the copy the repository generates. `disable` rules are read,
  and the well-known types keep their own `go_package`, as buf leaves
  them.
- **Fixed:** a file inside a buf module no longer resolves imports
  through roots inferred for other subtrees (#148). protocompile's module
  files' imports of the well-known types landed on its own copies, where
  buf uses its own. A file outside every module still uses them.

- **Fixed:** a view had no columns, so a query reading one reached
  nothing (#152, the owner's decision: columns from the SELECT). A view's
  columns are now its SELECT's output columns, named as PostgreSQL names
  them: the view's own column list, or each target's alias, column name,
  function name or cast operand. An unnamed expression and `*` give no
  column. Steward's SQL oracle: columns 384 of 384, none extra.
  
- **Fixed:** calls out of code no program path reaches were missing
  (#150). A generic function or a generic type's method that nothing in
  its module instantiates, and a method of an unexported type nothing
  calls, made no calls edges, and a generic function's recursion was
  dropped as plumbing. Every function and method a module declares now
  gives its calls, generic ones from their origin's body. The oracle's
  Go calls recall is 56,071 of 56,071 over 15 repositories, every edge
  still explained.

- The MCP tool definitions and server instructions are shorter (#157):
  7,556 bytes listed instead of 11,804. They are sent with every request
  an agent makes once the tools are loaded, about 1,300 tokens a
  request less. Measured on the benchmark's 15 graph and schema-to-code
  questions: the same answers (45/45 correct), the same number of
  requests, 14–16% less context per correct answer.

- **Fixed:** every MCP tool parameter that had a description was listed
  with `exclude_tests`'s (#160). An agent read "Leave out declarations
  from _test.go files" as the meaning of `query`, `id`, `max_tokens` and
  the rest. The cause was a miscompilation of an inlined option closure in
  the go-plus fork of Go that releases are built with (the official
  go1.27.1 compiles it correctly), not the source. go-plus v1.27.1a fixes
  it (trendvidia/go#158) and is what releases now build with; the
  stopgap that kept the descriptions out of line is gone, and a test
  fails if two parameters share one. The tool definitions also shrink
  from 17,079 to 11,804 bytes.

- `scripts/oracle.sh` measures the Go layer's `calls` edges against the
  type checker's view of every call expression (#148), on the in-house
  repositories checked out beside alidade. First numbers, from a run
  over the 16 checked out, the 15 of them that have a Go module:
  - **Recall:** 56,061 of the 56,071 calls the type checker names. The
    10 missing were all in or around generic code (#150, fixed below).
  - **The graph's 68,087 calls edges:** 82.3% confirmed by a call
    expression, 15.7% interface dispatch, 2.0% calls through a function
    value. None is unexplained.
  - **Imports**, against `go list`'s, match exactly on all 15: 5,667
    imports, none missing, none extra.
  - A repository the oracle cannot read is named, not silently absent:
    the modules it walked, the ones the type checker loaded no package
    for, the packages that do not type-check with their first error,
    and the `go.mod` files the module walk refused.
  - **SQL**, against sqlc's own catalog and queries (its json generator,
    run on a scratch copy of each configuration), on drover, steward,
    gameflow and protoregistry:
    - tables 75/75 and queries 351/351;
    - columns 635/640. The 5 missing were a materialized view's, whose
      columns the layer didn't read (#152, fixed below);
    - every table sqlc says a query touches is linked from that query.
      sqlc does not name every such table: it gives none for a JOIN, a
      WHERE, or a cast parameter (`user_id = $1::uuid`), so the graph's
      links for those count as unconfirmed, not checked.

- An A/B benchmark (#149): `scripts/bench.sh` asks the same questions
  about steward of the same model, with alidade's MCP tools and without,
  and scores each answer against a reference written by hand
  (`internal/bench`, 19 questions). It records tokens, cost, turns, wall
  time, tool calls and permission denials per session, and fails a run
  whose tools were denied. It spends API money, so it never runs in CI.

## v0.7.0 — 2026-10-07

- The release workflow builds and attaches nothing for a tag that is not
  on main or whose CI did not pass on that commit (#105). A gate job
  checks both against GitHub before the build, which then checks out
  exactly the commit the gate passed. A `dry_run` dispatch runs the gate
  alone. Proven on the runner: a dry run for v0.6.1 passed and skipped
  the build, and one for an unmerged branch failed at the gate.

- The complexity gate runs gocyclo as a tool of the module (`go tool
  gocyclo`, pinned in `go.mod` and `go.sum`), not with `go run
  …@v0.6.0` (#144). That form asked the module proxy for the version
  list on every run, even with the module cached, so a proxy timeout
  failed CI with nothing measured: it happened on 2026-09-13 and on PR
  #143.

- With tests loaded, `go_module_edges` counts production edges and a new
  `go_test_module_edges` the test edges, so the two sum to the graph's
  (#142, the owner's decision). Under plain `-tests`, `go_module_edges`
  used to count both: on govm it read 11,587 where 3,567 were
  production edges.
  - **`-tests-exact` reports the production graph.** Its report came
    from the load with tests: `go_module_edges` counted boundary calls
    the graph did not have, and `go_build` named a configuration only a
    test file had recovered. It is now the production load's report,
    with `go_test_nodes`, `go_test_module_edges` and the skipped inputs
    taken from the load with tests. Its `go_module_edges` equals the
    no-tests build's on govm, steward, drover and voya.


- With `-tests`, a Markdown mention of a Go package resolves to the
  package again (#139). The package's test files' node (#128) carried
  the package's name, so every mention of a package name became
  ambiguous and lost its edge: 127 of the 149 `documents` edges a build
  with tests lost against one without, across govm, voya, drover,
  steward and gameflow.
  - **The other 22:** a mention matching one production declaration
    and test ones of the same name is the production one's, as in a
    build without tests (the owner's decision on #139). The production
    `documents` edges with `-tests` now equal the no-tests build's on
    govm, voya, drover and gameflow.
    
- `-tests-exact` (implies `-tests`) makes `exclude_tests` exactly the
  build without tests (#133, the owner's decision). Loading tests widens
  the analysed program, so a production interface call gained
  package-level edges into packages only tests bring in, such as
  `go://testing` on govm. With the option, the production view comes
  from a load without tests and the load with them adds only test nodes
  and edges touching them. On govm, voya, drover, steward and gameflow,
  its production Go nodes and edges equal the no-tests build's exactly.
  It costs a second load (47–84%), so it is opt-in; `graph_stats` and
  the build block say `go_tests: "exact"`.
  - **Also fixed:** an edge from another layer (a document's mention)
    into an external package marked test is a test edge. It stayed
    production, an edge into a test node (#134). A dropped edge into such
    a package is derived too: PXF writes `test` on a `dropped` block as
    on a kept one, and this build reads it to decide which dropped edges
    are a test's.
  - **Still open:** with tests loaded, a Markdown mention that test
    declarations make ambiguous stops resolving (#139).

- A method value of an anonymous interface (`c.Close` with `c` an
  `interface{ Close() }`) is no longer a call to the bare name `Close`
  in its package (#129). Called through, it was a dropped edge, or an
  edge to an unrelated package function that shared the name. Such a
  method has no declaration, so the call is no edge, as an interface
  call to it already was. voya's 15 drops to
  `acceptance#CloseIdleConnections` were this; the VTA change (#94)
  had already removed the calls that reached it there.

- A Go file whose declarations are positioned outside the tree by a
  `//line` directive, as goyacc's output is, is named in the skipped
  inputs, once, with where the directive points (#109). Its
  declarations were counted in `go_outside_tree` with cgo's wrappers and
  never named. cgo's wrappers, whose files are themselves outside the
  tree, stay a count: fyne still has 506 and no skip.
  
- `-go-configs auto` reads a `//go:build go1.N` constraint against the
  toolchain that loads the module, `go env GOVERSION` in its root, not
  the toolchain alidade was built with (#112). A binary built with go1.27
  on a go1.26 host read a `go1.27` file as one the host loads, never
  looked for a configuration that brings it in, and the file was missing
  with nothing said.

- `-no-generated` is removed (#126, the owner's decision, ADR 004 rule
  1). Generated code is the bridge from hand-written code to the schema
  it came from, and the option cut it: steward lost 732 `generates`
  edges, and nothing connected code to schema. It saved no build time or
  memory, only 9–39% of the nodes. Passing it says it was removed and
  what to use instead.
  - **`exclude_generated`** on `find_nodes`, `neighbors`,
    `shortest_path` and `subgraph_for_question` keeps generated code out
    of an answer. `find_nodes` and a question's seeds skip it, and a
    traversal still crosses it, showing a generated node as its id and
    kind only, so "which code uses this table" is still one path.

- With `-tests`, a package's in-package test files have a node,
  `go://<pkg>#(test)`, kind `package_tests` (#128, the owner's decision,
  ADR 004 rule 4, ADR 002 rule 5). Imports only test files make, the
  `init` they declare and their blank variables' initializers are its, so
  `exclude_tests` gives the imports a build without tests has: 151 edges
  move on steward, 144 on govm.
  - **External test packages:** an external package that only test code
    imports or calls is a test node (`testing`, testify, `httptest`), 5 to
    22 per repository.
  - **Not yet:** a package only tests link stays production when a
    production interface call fans out into it (#133).
  - **Without `-tests`:** nothing changes.

- Three ID collisions are gone (#124, the owner's decision, ADR 002
  rules 11 and 12).
  - **`ts://@pkg/`:** a never-read npm package is `ts://@pkg/<name>`,
    not `ts://pkg/<name>`, which the file `pkg/<name>` also minted.
    steward's 25 package nodes are renamed; nothing else in its graph
    changes.
  - **A leading `@`:** a real path whose first segment starts with `@` is
    written with one more `@` in IDs and positions (`ts://@@types/x.d.ts`).
    A single leading `@` always starts a schema root's label, so a
    directory `@<label>/` no longer shares IDs with that root's files,
    in either repository. None of 75 local repositories has such a
    directory.
  - **Schema roots and managed mode:** a schema root's file without
    `go_package` gets `buf.gen.yaml`'s managed-mode answer. Its labelled
    path was matched against the root's input directories, so it never
    did.

- A call through a function value is an edge to the functions that can
  reach it, not to every function with a matching signature (#94, the
  owner's decision, ADR 006 rule 7). The Go layer takes VTA's answer at
  those call sites and keeps CHA's at static and interface calls, where
  VTA alone loses implementations made in dependency code. The edges are
  still `RESOLVED`.
  - **govm:** 25,062 Go `calls` edges become 13,884, and with `-tests`
    217,333 become 42,918. Production `vm#VM.execute` goes from 899
    callees, 530 of them test functions, to 197 and none.
  - **steward:** 17,472 become 11,161. Its CORS middleware calls the 13
    handlers it wraps, where it called 65 functions.
  - **drover, voya, alidade:** 2,336 → 2,166, 2,865 → 1,997 and
    1,811 → 1,719.
  - **No edge is added**, and no node changes except external package
    nodes that only spurious calls reached (6 on govm, 52 on steward).
    A build takes 15–170 ms longer.
    
- A package whose files are all `_test.go` files is a test package,
  whatever its name (#113, the owner's decision, ADR 004 rule 4).
  Acceptance suites, architecture tests and `hack/` test helpers now
  carry `test: true` on their package node, so `exclude_tests` leaves
  them out, with tests loaded or not. Files are counted on disk, so a
  package whose only production file is for another platform stays
  production on every host. Eleven packages change, all test suites:
  across steward, drover, voya, protocompile and gameflow.

- A node's `test` flag is a union in the merge, as ADR 004 rule 4 says
  (#102). Before, two contributions to one node that disagreed took
  whichever sorted first, and a contribution without a position sorted
  first, so the result depended on arrival order. A node that knows its
  position now always folds from it. No layer emits such a pair today:
  the graphs of steward, govm, drover, voya and alidade (`-tests`) are
  byte-identical before and after.

- The proto and PXF layers keep distinct things distinct (#97).
  - **Shared import paths:** two buf modules holding the same import
    path each compile their own file. The shallower one used to compile
    the deeper one's copy.
  - **Editions:** editions files name their Go types, which they could
    not before.
  - **Extensions:** an extension references the message it extends
    (`extends`).
  - **Repeated PXF directives:** a second directive of a name is keyed
    by an ordinal (`@entry1`, ADR 002 rule 9), not merged into the
    first.
  - **`@entry` prefixes:** a label, a type, or both, a single prefix
    being a type only when dotted. `@proto` directives are nodes.
  - **Repeated PXF blocks:** a block written more than once is a
    repeated message, each occurrence an element keyed by its `id` or
    `name`, else its index.
  - **Real repos:** on steward, pxfed, govm and drover the graph is
    unchanged. chameleon and protolsp gain their extensions' edges.

- The SQL layer shows the current schema (#119, the owner's decision).
  - **Applied in sqlc's order:** migrations apply as sqlc applies them,
    so a `DROP TABLE` or `DROP COLUMN` removes, a `RENAME` renames (the
    column keeps where it was declared, with `renamed_from` and
    `renamed`), and a table re-created after a drop is its new
    declaration.
  - **sqlc's order:** each config's `schema` paths as listed, files in
    name order.
  - **What sqlc skips, the layer skips:** down migrations (`.down.sql`,
    and a goose/sql-migrate/dbmate/tern rollback section) and files in a
    subdirectory of a schema directory, such as steward's
    `migrations/rollback/`.
  - **gameflow:** `market_ticker` is now `event_ticker`.
  - **steward:** still resolves 284 of 286 queries. It loses
    `gate_events` and `license_cache`, which its ledger's
    `NNN_*_down.sql` files drop. sqlc reads those files as schema too:
    `sqlc generate` v1.31.1 emits neither struct, and the committed
    `LicenseCache` model predates its down file (trendvidia/steward#577).

- A SQL query's ID carries its package (#118, ADR 002 rule 13).
  - **The new form:** `sql://query/<package>#<Name>`, the package being
    the import path of the Go package sqlc generates it into, for example
    `sql://query/github.com/trendvidia/steward/internal/db#GetUser`.
  - **Why:** sqlc scopes query names per package, so two packages may
    each declare `GetUser`. Before, they folded into one node carrying
    both queries' edges.
  - **Without Go:** a query in no Go-generating package is keyed by its
    file's path.
  - **Shared schemas:** a schema two packages read links its tables to
    both packages' structs.
  - **Every query ID changes:** a consumer storing `sql://query/<Name>`
    must re-key. Counts and edges on steward, gameflow, drover and
    protoregistry are otherwise unchanged; steward's 286 queries sit in
    its three packages (247 + 32 + 7).
  - **Lookup:** `find_nodes` and Markdown mentions find a query by its
    name, `pkg.Name` and `pkg#Name`.

- SQL names resolve as PostgreSQL resolves them (#96).
  - **Case:** unquoted identifiers fold to lower case, so `CREATE TABLE
    Users` answers `FROM users`.
  - **ALTER TABLE:**
    - `ALTER TABLE public.users ADD COLUMN` amends the `users` table
      `CREATE TABLE users` declared;
    - `IF EXISTS ONLY` is read in PostgreSQL's order;
    - pg_dump's `ADD GENERATED … AS IDENTITY` is no longer a column named
      `GENERATED`.
  - **Bare table names:** a bare name resolves to the relation declared
    bare or in `public`, else to the one schema declaring it. Two other
    schemas are an ambiguity, counted (`TablesAmbiguous`), not the first
    one declared.
  - **Writes inside `WITH`** are writes at table level too.
  - **sqlc parameters:** `@name`, `sqlc.arg()`, `sqlc.narg()` and
    `sqlc.slice()` are not column references.
  - **Views:** `CREATE [MATERIALIZED] VIEW` declares a relation node of
    kind `view`, which queries reach and sqlc's struct for it links to.
    steward's `app.contributor_dimension_reputation` now resolves.
  - **sqlc v1 configs:** a version-1 package's `emit_exact_table_names`
    is honoured.
  - **Counted:** RENAME and DROP COLUMN are counted (`Renames`,
    `ColumnDrops`).
  - **steward:** still resolves 284 of 286 queries to tables.
  - **Open:** whether the graph applies RENAME and DROP COLUMN is #119.
  - **Open, both owner's:** whether the graph applies RENAME and DROP
    COLUMN is #119, and query IDs across sqlc packages is #118.
    
- The frontend and goasm layers read their files as their tools do (#99).
  - **Per-app aliases:** each SvelteKit app keeps its own aliases. In a
    repository with two apps, the second app's `$lib` imports had
    resolved, as RESOLVED edges, into the first app.
  - **Imports in code only:** an `import` inside a template literal, a
    string or a comment is no longer an import, and a comment inside a
    multi-line named import no longer hides the names after it.
  - **More import forms:** `import D, * as ns from`, `import x =
    require()` and `require()` are read.
  - **Generated exports:** a protoc-gen-es extension's export no longer
    inherits the message above it as its source.
  - **Imports outside the root:** an import resolving outside the root
    is counted (`Outside`).
  - **goasm `go.mod` lookup:** goasm links a template through the
    nearest `go.mod` above its directive, not only one at the root.
  - **goasm directives:** directives are read as `go generate` reads
    them: quoted arguments and `$GOFILE` are understood, and an
    `-arch=$GOARCH` no longer becomes the literal attribute
    `arch: "$GOARCH"`.
  - **Unchanged graphs:** steward, pxfed, govm and goasm graph
    byte-identically to before; these were latent.

- The frontend layer reads `.mjs`, `.cjs`, `.mts` and `.cts`, but not their
  `.d.mts`/`.d.cts` declaration files, and skips `out/` build output
  (#115, ADR 007 decision 1 amended).
  - **steward:** gains its five `.mjs` build scripts as nodes.
  - **One list:** the extension list lives once, in `ts.Extensions`,
    which the Markdown layer and the rebuild signature read.

- Markdown links no longer vanish (#98).
  - **Anchors resolve as GitHub renders them:** `#anchor` links match
    GitHub's anchor rules (github-slugger: `_` kept, `.` and `—`
    dropped, each space a `-`), not the slug node IDs are minted from.
    On the in-house repos, same-document anchor links that resolve went
    from 111 to 149 of 151; pxfcloud went from 50 of 78 to 78 of 78.
  - **Links to another document's heading:** `other.md#heading`
    resolves to that section, as ADR 002 already said it should (govm 5
    such links, protowire 11, pxfcloud 1).
  - **Unresolved anchors are recorded:** an anchor naming no heading,
    or one of several identical headings (whose anchors GitHub numbers
    by position), is a dropped edge to `#@anchor.…` or `#@ambiguous.…`
    with its reason, never a silent loss.
  - **Paths are read as written:** `?plain=1` is stripped, `%20` is
    decoded, and a path out of the repository is counted as external.
    Links reach frontend and goasm files, and code-span mentions reach
    their files, generated exports and templates.
  - **Front matter:** front matter with Windows line endings, or
    closing at the end of the file, is no longer read as a section.
  - **IDs that change:**
    - **Fenced blocks** number within their section rather than the
      document, so adding one renumbers no other section's (ADR 002
      rule 1).
    - **Repeated headings:** a repeated heading's `-N` suffix skips IDs
      a real heading holds, so `## B`, `## B`, `## B 2` are three
      sections, not two folded with a conflict. govm's one conflict was
      this.

- `rebuild` no longer keeps a layer whose inputs changed (#93).
  - **Skip lists:** the signature that decides what reruns applied the
    frontend layer's skip list (`build`, `dist`, `.svelte-kit`, `.astro`)
    to every layer, so an edit under alidade's own `internal/build/`
    left the Go layer kept and the graph stale. Each layer is now signed
    over its own skip list, read from the adapter.
  - **Files read by other layers:** these are now signed for the layers
    that read them. The Markdown layer reads `go.mod` and whether a
    linked path or `.go` file exists; the SQL and goasm layers read
    `go.mod`; esbuild reads `package.json` and `jsconfig.json`.
  - **Failed builds:** a layer dropped by a build that then failed is
    rerun by the next build, rather than kept empty when its files are
    reverted.
  - **Schema roots:** a schema root's label is registered once per
    session, so a failed first build no longer makes every later one
    refuse it as used twice.
  - **Cost:** the signature on steward went from 40 ms to 46 ms.

- The Go layer no longer leaves out facts without saying so (#95).
  - **Variable initializers:** a call made by a package-level
    variable's initializer, or by a closure assigned to one, is an edge
    from that variable. On govm, `serve#recvCompactionDisabled` had no
    caller; `var grpcRecvCompactionOff` now calls it.
  - **Recursion through a closure:** a function calling itself from a
    closure inside it is a `calls` edge to itself.
  - **Interface calls:** a call through an interface is also an edge to
    the interface method, which had no callers. When the program has no
    implementation, it is the only edge. An initializer's interface
    call is an edge from the variable, like its other calls.
  - **Interface methods are not callers:** an interface method has no
    body, so nothing is a call from one. Its method values, method
    expressions and the promotion wrappers of types embedding it are
    the SSA builder's functions, and attributing their dispatch to the
    method made it call itself and call implementations it cannot name
    — 408 such edges on steward, which had them before this change
    too, and 136 self-calls this one would have added. An anonymous
    interface's method is keyed by its bare name, so the edge landed on
    whatever package-level function shares it.
  - **`implements` with `-tests`:** loading tests no longer loses
    production `implements` edges. Satisfaction is checked in the
    production universe too, and govm keeps three it was losing
    (`vm#VM → compiler#CompileContext` among them).
  - **Alias receivers:** a method declared through an alias receiver
    has one ID, the type's.
  - **`_test` directories:** a production package in a directory named
    like `e2e_test/` is not marked a test.
  - **Generated code that hand-written code builds on:** a
    hand-written declaration that names a generated type references it,
    and a hand-written method of a generated type is in the graph with
    its calls. Generated code is always in (#126).
  - **Constraints:** type-parameter constraints are `references`.
  - **Release tags:** `-go-configs auto` treats a `go1.N` tag as true
    when the toolchain alidade was built with has reached it, which is
    not the toolchain that loads when a binary outlives it (#112).
  - **Edge counts:** calls edges grow (steward 17,339 → 17,472, govm
    24,960 → 25,062, drover 2,116 → 2,183), counted from the PXF
    output's own edge records, both sides measured back to back on one
    tree. Nodes and dropped edges are unchanged, and the total edge
    count grows by exactly the calls delta.
  - **Packages that do not compile:** they no longer vanish (caliper
    v1.6.0, trendvidia/caliper#38). Each keeps its package node with the
    compiler's first error, and the declarations the type checker
    resolved, marked `partial`, so calls into a package that is
    mid-edit still land. They are named, by package path, in
    `go_packages_failed`, so each answers `get_node`; a package broken
    only for a further `-go-configs` configuration is named there too. A
    package whose calls are missing because one it imports is broken
    says so in a `calls_unavailable` attribute, and is listed in
    `go_packages_without_calls`. Both lists are capped in `graph_stats`
    as `skipped` is, since one broken package puts every transitive
    importer of it in the second. On a copy of drover with one type error
    in `selection`, the package went from absent (8 dropped edges) to
    18 partial declarations carrying the error (1 dropped, as on the
    clean tree).
  - **Platform-only packages:** a package whose every file is for
    another platform is listed under `skipped`, with whether
    `-go-configs auto` can load it. `auto` now brings it in:
    fyne's `hack/winhoverprobe` loads under windows/arm64. govm's
    `//go:build plugin` packages are listed as needing a build tag. A
    directory one configuration does load is not listed, whatever else of
    it stays excluded, and a candidate file that cannot be read costs
    that one file (#92), not the layer. A configuration the module does not
    load for at all costs that configuration — it is reported `skipped`,
    with the compiler's reason — where it used to drop every Go fact of
    the repository (#92, #93).

- One bad input no longer stops the whole build (#92). A file whose path
  holds a space, a link or `${…}` reference no ID can carry, a SQL column
  named `rate/sec`, a PXF chain loading from outside the root, a dangling
  symbolic link or a directory the walk cannot open each failed every
  layer at once; now that one input is left out and listed, with its
  layer and reason, under `skipped` in `graph_stats` — capped there,
  with `skipped_omitted` counting the rest, since graph_stats takes no
  `max_tokens` — and in full in the PXF build block. Positions may name a path with a space, so SQL under `q dir/`
  keeps its tables. A root module that does not compile is a failed
  module like a nested one (#67): its package node carries the
  compiler's error, `go_modules_failed` names it, and the other layers
  build. A `go.mod` that declares no module path is skipped.

- Packages that use cgo build (#92). cgo's generated `_Cfunc_` and
  `_Ctype_` declarations sit in the build cache, outside the tree; they
  are left out with every edge to them and counted as `go_outside_tree`.
  fyne (506) and goffmpeg (165) could not be graphed at all before. On
  drover, steward, govm, pxfed, protolsp and gameflow the graph is
  byte-identical to v0.6.1's.

- MCP filters no longer turn a typo into an answer (#100).
  - **Bad values are errors:** an `edge_kinds`, `kind` or `scheme` the
    graph does not hold is an error that lists the ones it does. Before,
    `["call"]` gave `neighbors` no edges and `shortest_path`
    `found: false`, which reads as "no path".
  - **Wrong shapes are errors:** `edge_kinds` given as a string rather
    than an array is an error. Before, the filter was dropped and every
    kind came back.
  - **`graph_issues` `kind`:** anything but `dropped`, `conflicts` or
    `both` is an error.
  - **Whitespace:** `find_nodes` ignores whitespace around the query.

- Every budgeted tool keeps to `max_tokens` by the size of what it sends
  (#100).
  - **Budget:** `subgraph_for_question`'s seed list, `find_nodes`'
    `matched`, `shortest_path`'s `found` and `hops`, and `graph_issues`'
    groups and totals are now charged against it. On a wide fixture,
    `subgraph_for_question` had returned about 543 tokens for a
    `max_tokens` of 500.
  - **`omitted` is exact:** each left-out node and edge is counted once,
    and the walk goes on to its depth to count them. It had read 300
    edges where 899 were left out. `graph_issues` counts groups it cut
    under `omitted.dropped_by_missing`, and returns `[]` rather than
    `null` for `kind: conflicts`.

- The CLI refuses what it used to misread (#100).
  - **Extra arguments:** a positional argument is an error, so
    `alidade setup project` no longer registers at user scope and
    `alidade graph ../repo` no longer builds `.`.
  - **File system root:** `-root /` is refused rather than walking the
    disk.
  - **`setup` failures:** when `claude mcp add` fails after the old
    registration was removed, `setup` says that none is registered now.

- Two gates no longer pass when they measured nothing (#101). The
  complexity gate exits 2 when a domain path is missing, when a package
  directory belongs to no domain (`internal/version` belonged to none),
  or when gocyclo errs, since gocyclo exits 0 on a path that does not
  exist. `scripts/corpus.sh` exits 2 with no repository to run, and
  `TestCorpus` fails when none of the repositories it was given builds.

- The release workflow reads the tag from an environment variable rather
  than splicing a dispatch input into its script, and pins
  `softprops/action-gh-release` by commit (v2.6.2), since that job can
  write releases (#101).

## v0.6.1 — 2026-09-13

- The test flag is readable again over MCP (#90). v0.6.0 moved it from
  an attribute to a typed field and the surface renders attributes, so
  `get_node`, `find_nodes` and `neighbors` stopped showing it: the fact
  was still in the core and still filtered correctly, which is why every
  behavioural test passed, but a caller asking "is this a test
  declaration" was left with the `_test.go` filename to infer from —
  the heuristic the flag exists to replace. Nodes and edges now render
  `test: true`, next to `gen` for generated code.

- `go_test_nodes` counts the Go nodes carrying the flag, including the
  package node of an external test package (`pkg_test`), which has no
  file of its own. It read one lower than the nodes a caller could count
  in the graph; on drover both are now 1,205.

## v0.6.0 — 2026-09-13

- Test-ness is typed, and split by the kind of fact. A node carries a
  `Test` field — observed by the Go adapter, which read the `_test.go`
  file, and unioned in the merge the way `Generated` is — and an edge's
  is **derived** by the core from its endpoints, never reported by an
  adapter. In PXF both now read `test = true` rather than
  `attrs { test: "true" }`, which a consumer parsing the old spelling
  must act on.

  This retires a recurrence class rather than fixing an instance. Asking
  each adapter to mark its own edges was wrong in two of six layers
  within one release: the Markdown layer emitted 68 unmarked edges into
  test declarations, and on govm an edge deduplicated per `(from, to)`
  took the marking of whichever declaration the call graph reached
  first, so the same binary on the same tree produced different output
  run to run. Deriving the fact where the merge already happens makes
  the wrong state unrepresentable (ADR 001, ADR 004 rule 4).

- A package that declares `init` in both a production file and a test one
  is one node, and a production one: the declaration is there in a build
  without tests, so `exclude_tests` no longer hides it while its calls
  remain. The node is positioned at the first init in source order rather
  than the first the call graph happened to reach, and a package with
  several carries their number in an `inits` attribute. The cost of
  folding several declarations into one node is that a call only the test
  `init` makes is a production edge; a consistent answer beats a precise
  one that flips between runs.

- `find_nodes` resolves `package.Func` and `package.Type.Method` — the
  spellings source, call sites and issue titles use — the way it already
  resolved `Type.Method`, `package#Func` and the whole ID (#84). A Go ID
  spells the package boundary `#`, so a dotted term is also tried with
  each of its dots as the `#`; `subgraph_for_question` seeds through the
  same match, so a question naming `facts.Create` now starts there.

- `scripts/corpus.sh` runs the graph against the real repositories
  checked out beside this one and asserts invariants a fixture cannot:
  test markers follow `_test.go` exactly and every edge touching a test
  declaration is a test edge from either end, every positioned node names
  a file in the tree, a dropped edge never points at a Go package that is
  on disk and unloaded, a Markdown link never drops while its target is
  on disk and holds Go, and two builds of one tree are byte-identical.
  A reflection test fails when a build-report field reaches `graph_stats`
  but not the PXF `build` block, or the reverse.

## v0.5.3 — 2026-09-13

- With `-tests`, every Go declaration from a `_test.go` file is marked
  `test: "true"`, and so is every edge touching one, and `exclude_tests`
  on `find_nodes`, `neighbors`, `shortest_path` and
  `subgraph_for_question` leaves them out: "does this have a production
  caller" and "is it called from a test at all" are one graph and a
  filter rather than two builds (#81). `graph_stats` and the PXF `build`
  block say `go_test_nodes`, 0 when tests were not loaded. Before, a
  test caller and a production caller were told apart only by the
  caller's filename.

## v0.5.2 — 2026-09-13

- `alidade graph` writes a `build` block after `stats`, under the keys
  `graph_stats` uses — layers, `go_build`, `go_modules`,
  `go_modules_failed`, `go_module_edges`, `build_ms` — so a session that
  has only the CLI reads what an MCP session would. Until now the build
  report reached MCP only, and a reader of the PXF could conclude a
  feature was missing that graph_stats had (#76 follow-up).

## v0.5.1 — 2026-09-13

- `graph_stats` lists `go_module_edges`: per Go module, the edges leaving
  it for each other module of the repository and for packages outside it
  (`to_module: "external"`), by kind, counted by the Go layer as it emits
  them, so "what in this module reaches into that one" is read from the
  tool rather than reconstructed from the output (#76).

## v0.5.0 — 2026-09-12

- A Markdown link to a Go file or a directory is a `references` edge to
  the package there, and a link to a path that no longer exists is a
  dropped edge with `unknown_target` and the raw link, as a link to a
  missing document already was (#68). Before, a link to code was neither.
  A missing path counts only when it is gone from a place that holds Go
  (a package directory or a module root): a path missing from a directory
  of documents is a route of the site they are published on, and a
  root-absolute link is one outright — steward's docs site would
  otherwise have reported 354. Code spans are unchanged: a stale
  backticked word stays silent, since a span is a guess and a link is
  not. Measured: drover resolves 5 links to code and reports 1 broken
  (`screen/`, from the use-case module's README); steward 3 and 0, voya
  4 and 0.
- An `ALTER TABLE ... ADD COLUMN` of a column the schema already declares
  — the idempotent-schema idiom, a `CREATE TABLE` for a fresh database
  and an `ADD COLUMN IF NOT EXISTS` at the end for an existing one — is
  one column at the CREATE site with the ADD recorded on it as
  `amended: "file:line"`, not a position conflict with one site discarded
  (#69). drover's one conflict is gone; steward records two amendments
  and goes from 177 conflicts to 175.
- `alidade version` (also `-version`, `--version`, `-v`) prints the
  binary's module version, commit and Go toolchain, from the build
  information the toolchain stamps: `alidade v0.4.0 (f6cb367, go1.27.1
  darwin/arm64)` for a release or a `go install`, a pseudo-version with
  `+dirty` for a local build (#70). The MCP server reports the same
  version at initialization instead of a constant that said 0.1.0.
  
- Every `go.mod` under the root is a module of the graph (#67). A
  multi-module repository with no `go.work` was read as its root module
  alone: on drover, 61% of the Go was absent and 45 proto→Go `generates`
  edges dropped as `unknown_target`. Each module now loads from its own
  directory, the way the repository builds, and edges between modules
  resolve at declaration granularity through the ID grammar (ADR 002
  rule 5): drover has 1,674 Go nodes instead of 537, 0 drops, and 144
  edges from its use-case module into the engine module's own
  declarations and packages (61 calls, 62 references, 21 imports) plus
  one `implements` the other way — not counting the use-case module's
  480 calls and 139 imports of the standard library and third-party
  packages, which the layer keeps at package granularity (#76). Each
  further module is a further load, 0.2–0.5 s before it reads any code
  (drover 0.4 s → 0.8 s, voya with seven modules 0.3 s → 1.4 s; ADR 005
  note). A module below the root that does not load
  keeps a package node with the error, as the other layers keep a failed
  file; `graph_stats` lists `go_modules` and `go_modules_failed`.

- `alidade setup` replaces an existing registration in its scope instead
  of failing with "MCP server alidade already exists" (#65).

## v0.4.0 — 2026-09-12

- Dependencies' types come from the compiler's export data by default
  (#58): the module's own nodes and edges are unchanged, the Go layer is
  3–4× cheaper (steward 7.3 s → 2.1 s; a rebuild after a Go edit 7.3 s →
  1.9 s), and calls from the module to external packages no longer carry
  class-hierarchy fan-out into dependency implementations (steward
  41,720 → 7,811). `-go-deps-source` restores the source load;
  `-go-deps-export` is gone.

- `rebuild` reruns only the layers whose files changed, by content
  signature, and keeps the others' contributions: a Markdown edit no
  longer costs the Go load. `graph_stats` shows `layers_rebuilt` and
  `layers_kept`. Nothing is persisted; a kept layer's inputs are
  byte-identical to the build that produced it (#56, ADR 005 note).

- caliper v1.5.0: `golang.Options.DepsFromExportData` (see the entry
  above for the default); a `go` newer than the one alidade was built
  with is now refused by the Go layer instead of producing a graph
  missing most of the module; importers of a dependency with body-only
  type errors stay in the analysis.

## v0.3.0 — 2026-09-12

- `alidade setup -schema-root DIR` (repeatable) registers the server with
  the schema root; it implies `-scope project`, since a schema root
  belongs to one repository, and is refused with `-scope user` (#53).

- `-schema-root DIR` (repeatable) adds another repository's `.proto` layer
  to the graph, for a repository whose generated code comes from schemas
  kept elsewhere: its messages, enums and services are the nodes the
  generated code already names, its files and positions are labelled
  `@<dirname>/` so no file ID collides, and its Go types are not linked.
  ADR 002 rule 12. `graph_stats` lists `schema_roots` (#47).

- `graph_issues` folds dropped edges by cause in `dropped_by_missing` —
  which end named no node, its scheme, the edge kind, a count and one
  example — before the itemized list, so a repository whose schemas
  live in another repository reads as one line (#47, first step).

## v0.2.0 — 2026-09-11

- The Go layer reports the build configuration it loaded for
  (`GOOS/GOARCH`) and the Go files that configuration excluded, each
  named on its package node's `excluded` attribute; `graph_stats` shows
  `go_build` and `go_excluded_files` (#43, first step).
- `-go-configs auto` (or a `GOOS/GOARCH` list) loads the module for the
  further build configurations that bring in the files the host's load
  excluded, and adds what they declare and call, marked `build=<config>`
  on the node or edge; what every configuration shares stays one plain
  node. `go_build` then lists every configuration loaded. caliper
  v1.4.0 (`EngineOptions.Env`) makes the extra loads possible (#43).

- **Frontend layer** (`internal/adapter/ts`, ADR 007, #44): TypeScript,
  JavaScript and Svelte files as nodes, `imports` resolved by esbuild's
  resolver under the repository's own tsconfig and SvelteKit aliases,
  the exports of protoc-gen-es output as nodes, `references` from the
  import clause, and `generates` from `.proto` file, message, enum and
  service to what was written for them. ADR 002 rule 11. `find_nodes`
  accepts `scheme: ts`. esbuild is a new dependency.

- **goasm layer** (`internal/adapter/goasm`, #41): `.goasm` template files
  and their `# TEMPLATE` blocks as nodes, and the `generates` link from
  each template to the `(*Emitter).Emit<Name>` method goasm writes for it,
  DECLARED by the `//go:generate` directive that names the template file.
  A sixth format; ADR 003 amended, ADR 002 rule 10. `find_nodes` accepts
  `scheme: goasm`.

## v0.1.1 — 2026-09-10

- `alidade setup` registers the server with Claude Code (`claude mcp add
  --scope user`), or prints the configuration for any MCP client.
- A release workflow attaches static binaries for darwin and linux, arm64
  and amd64, to every `v*` tag; Homebrew installs from them
  (`brew install trendvidia/tap/alidade`).

## v0.1.0 — 2026-09-10

The first release: the whole plan of #7, every layer of ADR 003.

- **Graph core** (`internal/graph`): node and edge schema, declaration-anchored
  identity (ADR 002), cross-adapter merge with reported conflicts and dropped
  edges, PXF output.
- **Go layer**: caliper's type-resolved call graph, `implements` by structural
  satisfaction, `references` from signatures and fields; filtered to the
  module, with calls that leave it kept at package granularity.
- **`.proto` layer**: protocompile; messages, fields, enums, services,
  imports; the `.proto → .pb.go` link by protoc-gen-go's own naming, buf
  managed mode honoured.
- **SQL layer**: tables and columns from migrations, sqlc named queries, the
  `.sql → Go` link by sqlc's naming; query → table by token match.
- **`.pxf` layer**: documents, blocks, fields; `@type → .proto`; chameleon
  chain layers expanded per environment. No value ever reaches the graph.
- **Markdown layer**: sections by heading path, fences, links; doc → symbol
  edges from code spans, only when exactly one node answers.
- **MCP surface**: `graph_stats`, `find_nodes`, `get_node`, `neighbors`,
  `shortest_path`, `subgraph_for_question`, `graph_issues`, `rebuild`; every
  result token-budgeted, every edge with its derivation.
- **Derivations** (ADR 006): `EXTRACTED`, `RESOLVED`, `DECLARED` — a
  generator's or build tool's rule, for schema → generated code and chain →
  layer edges — and `INFERRED`.
- **Command**: `alidade serve` (MCP over stdio) and `alidade graph` (PXF).

Known limits, each recorded on its issue: builds take 2–8s on the in-house
repositories (#18); a table touched by dozens of queries does not fit one
budgeted question (#24); migrations are not folded (#8, deferred by
decision).
