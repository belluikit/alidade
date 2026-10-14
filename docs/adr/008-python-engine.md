# ADR 008: Python — CPython Embedded, jedi as the Engine for Calls

**Status:** Accepted 2026-10-09 (#237). The engine and how it is linked
were decided first; the layer — its IDs, the derivation of its calls,
`sys.path.insert` and speed — on the same day, all by the owner, and
built ("The layer" below). Amended 2026-10-10 (#240, #244): `with`
statements are calls, functions named as values are references; and
(#249) a class implements the classes it inherits from.
**Date:** 2026-10-09
**Relates to:** [002](002-node-identity.md), [003](003-corpus-scope.md),
[006](006-edge-provenance.md), [007](007-frontend-layer.md)

## Context

ADR 003 admits a format when it crosses a layer boundary, and by that test
the in-house stack had almost no Python: trendvidia/protowire-python, 17
files. The owner asked for Python anyway, measured first on bfrancojr/agent-config
(53 files, 28,897 lines), then for any Python project. That is an amendment
to ADR 003's scope, made by the owner on #237.

**Which calls need types** (Python's `ast`, matching names against a
repository's own definitions; an estimate):

| repository | calls into the repo that scope and imports resolve, without types |
|---|---|
| agent-config | 95.5% |
| graphify | 72.9% |
| opendbc, FrogPilot, harbor, colibri, openpilot | 47.6%–56.1% |

The rest are method calls: `obj.m()`, `self.m()`. A layer for any Python
project needs type inference, and Go has no Python type checker.

## Decision

**jedi, in CPython linked into the binary.**

- **Why jedi:** on openpilot (300 sampled method calls, untyped code), it
  resolved every `self.m()` (55 of 55) and 132 `obj.m()` into the repository;
  11% stayed unresolved. On the fixture it resolves a method on an untyped
  parameter from the parameter's call site.
- **Why embedded** (the owner's option b over using the machine's
  `python3`): no runtime dependency.
- **Where CPython comes from** (the owner's option c): the private module
  `github.com/trendvidia/pylib`.
  - It holds python-build-standalone's CPython 3.14.8 as static libraries for
    darwin and linux on arm64 and amd64, plus a Go API: `Start`, `Run`,
    `Unpack`.
  - Its build compiles python-build-standalone's LLVM bitcode to native
    objects, and is byte-for-byte reproducible.
- **Behind the `python` build tag** (the owner's decision), as V8 is behind
  `v8`. Without it, `internal/python` says Python is unavailable.
- **Where it lives:** `internal/python` embeds jedi 0.20.0 and parso 0.8.7
  (`scripts/jedi.sh`) and `resolve.py`. It unpacks jedi and the stdlib once
  into the user's cache directory.
- **What the resolver gives:** each call site resolved into the root, with
  the dotted path of the functions holding it and the definition's file,
  line, name and type. It also counts the calls that resolve elsewhere or not
  at all, and the files skipped.

**Measured, 2026-10-09:**
- **Same answers on every target:** embedded CPython gives exactly the
  answers of Homebrew's CPython 3.14.8 on all four release targets. On
  agent-config's sample that is 242 / 50 / 8 everywhere.
- **Size:** the stripped spike binary was 18–20 MB per target. Stdlib plus
  jedi are 4.1 MB compressed.
- **Coexistence:** `TestEmbeddedRuntimes` runs V8 and CPython in one process.

## Assumptions, each with the test that fails when it breaks

- **jedi must run in process.** Its default environment starts a helper
  process from `sys.executable`. In an embedded interpreter that is the host
  binary, which never answers, and jedi waits forever. `resolve.py` uses
  `InterpreterEnvironment`, and without it `TestResolve` times out
  (measured). Consequence: jedi sees only the embedded stdlib. A project's
  virtualenv packages are invisible until they are added to its import
  path. That is the open question below, and it matters only for types
  that flow through third-party code.
- **UTF-8 mode is required.** Without it, a file with non-ASCII text fails to
  parse, and its calls are lost silently. pylib's `TestRun` fails without it.
- **jedi's answers are pinned** by `TestResolve` on `internal/python/testdata/proj`.

## The layer (decided 2026-10-09, the owner's four calls on #237)

`internal/adapter/py` reads every `.py` file outside its skipped directories:
version control, caches, build output, installed packages, and any directory
holding a `pyvenv.cfg` (a virtual environment, whatever its name).

*Amended 2026-10-12 (#266):* installed packages are left out by every layer,
not only this one. On openpilot the Markdown layer read 156 documents from a
uv environment's `.venv/lib/python3.12/site-packages/`, where no Python node
came from. `internal/scope` holds the rule — a directory holding a
`pyvenv.cfg`, and one named `.venv`, `venv` or `site-packages` — beside
`.alidade.pxf`'s list, which cannot bring them back.
`TestInstalledPackagesAreLeftOut` builds every layer's files in each kind
and fails when one is read.

- **Nodes.** A file node per file. With the `python` tag, also a node per
  class (`type`), function (`func`), method (`method`), module-level variable
  (`var`) and class attribute (`field`), each with `end_line`, contained by the
  declaration holding it or by its file. IDs: ADR 002 rule 15,
  `py://<file>#<qualified name>`. Without the tag, each file node says its
  declarations and calls are not read (`calls_unavailable`).
- **Imports** (RESOLVED, as TypeScript's resolved imports are): file → file
  where jedi resolves the module to a file the layer reads; the rest are
  counted, as resolved outside or unresolved.
- **Calls** (RESOLVED, ADR 006 rule 8): from the innermost class or function
  holding the call (the file at module level) to each function or class jedi
  resolves the callee to, once per pair. A call through a variable holding
  functions is followed to each. A call through a parameter, or to a local
  value, is counted as unplaced; one to the stdlib or a package as outside;
  one jedi cannot resolve as unresolved.
  A variable holding a factory's result holds what the factory returns,
  also when its return annotation names `Callable` (#258): jedi prefers a
  return annotation to the return statements, and `Callable` names no
  function, so for such a function the layer has jedi infer the return
  statements as well and joins the two. `inc = adder(1); inc(x)` calls
  the closure `adder` returns. Measured on the #240 traces: graphify's
  edges among calls seen at run time 10,194 → 10,203, the 9 calls of
  this shape it missed, and one more call edge the tests do not run
  (`pascal.py:643`); agent-config unchanged.
- **A call through a dataclass field annotated `Callable`** (amended
  2026-10-10, #258, the owner's call: dataclasses only, RESOLVED) is
  followed to each function inside the root that a construction of the
  class passes for the field: by keyword, `Handler(run=f)`, or by position,
  `Handler("x", f)`, the field's place among the class's annotated fields
  being the order the generated `__init__` takes them. A dataclass with
  bases is followed by keyword only, since an inherited field comes first;
  a field assigned after construction (`h.run = f`) is not followed, and
  neither is a class that is not a dataclass, whose `__init__` may map
  arguments to fields any way. Measured on the #240 traces: graphify
  10,203 → 10,231 calls seen at run time that are edges, 28 call edges
  added and every one of them seen at run time (25 that were missing, 3 to
  resolvers a test file defines); agent-config has no such field. Both
  rules together cost no CPU time that could be measured: graphify 958 s
  against 949 s, agent-config 89.3 s against 90.0 s (user time, main
  against the change, runs alternating).
- **A call to a decorated function calls the decorator's wrapper too**
  (amended 2026-10-10, #261, the owner's call). `@reclaiming def
  fix_red_ci` binds the name to what `reclaiming` returns, its nested
  `go`, which runs first and calls `fix_red_ci` through its parameter.
  goto names the decorated function and infer the wrapper, so when goto
  lands on a decorated function the layer also takes each function inside
  the root that infer gives. jedi names a `functools.wraps` wrapper after
  the function it wraps, so the layer looks under jedi's wrappers for the
  function itself. A decorator that returns the function unchanged adds
  nothing, and one from outside the root (`functools.lru_cache`) neither.
  The wrapper's own call of the original stays unplaced, as any call
  through a parameter. Measured on the #240 traces: agent-config 1,000 →
  1,002 calls seen at run time that are edges, the 2 edges added both
  seen; graphify unchanged, no in-repo decorator returns a wrapper there;
  no CPU cost (agent-config 41 s, graphify 274 s against 276 s).
- **A `with` statement calls** its item's `__enter__` and `__exit__`
  (`__aenter__`, `__aexit__` for `async with`), its own or inherited, from
  the declaration holding the statement (amended 2026-10-10, #240 decision 1,
  #244). `calls` means "this runs", as a JSX element is a call (ADR 007).
  An item built from a parameter is unplaced, as any call through one.
- **Reading a property calls its function** (amended 2026-10-11, #240,
  #253), by the same rule: `r.b64` runs `ImageRef.b64`. An attribute read that
  jedi's goto resolves to a `@property` in the files read is a call, from the
  declaration holding the read, including a read that is itself the object
  of another attribute (`r.b64.decode()`). Writes and deletes, which run a
  setter or deleter, are not read. Measured on the #240 traces: graphify
  10,194 of 10,612 calls seen at run time are edges, against 10,180; 14 of
  its 17 missing property calls found, and the other 3 lost to a reused jedi
  `Script` answering nothing, found since (#255); agent-config unchanged,
  1,000. The new
  lookups are 6% of agent-config's and 2% of graphify's; its CPU time rose
  about 2% (3 runs each, the machine heavily loaded).
- **References** (RESOLVED): from the declaration holding a function's name,
  used as a value and not called, to the function: a stub installed
  (`af.gh_run = fake`), a callback passed (`sorted(xs, key=f)`), a function
  returned (amended 2026-10-10, #240 decision 3, #244). jedi's goto names it,
  binding only, no inference, so a parameter or a local value is never one;
  the layer does not look those names up, which leaves the graph unchanged.
  Classes named as values are not references in this version.
- **A class implements its bases** (RESOLVED): from each class to each class
  its bases resolve to, as jedi resolves a callee's name, direct bases only
  (amended 2026-10-10, #240 decision 2, #249). `implements` already joins a
  type to what it can stand for; a grandparent is reached through the base's
  own edge. A base outside the files read (`Exception`, `unittest.TestCase`)
  is counted as outside, one from a parameter or another value (`class
  Made(base)`) as unplaced, not guessed. `Generic[T]` is its class. The
  execution oracle credits a traced call of an inherited `__init__` through
  these edges: constructing a subclass with none of its own runs its base's.
- **A module-level name assigned twice** (`_q = q_stub()`, later
  `_q = [...]`) is read by a function as its last assignment only: jedi's
  goto gives the last binding. Left alone (#240 decision 4): 1 call site
  across agent-config, graphify and openpilot.
- **`sys.path.insert`: no rule.** jedi follows it: 1,167 of agent-config's
  1,170 `af.f()` calls in its tests resolve into `bin/`. The fixture's
  `scripts/tool.py` pins it.
- **Workers.** A tree past 256 KB is shared between worker processes (half
  the CPUs, at most 8), each the alidade binary started in worker mode, each
  with its own interpreter. A file larger than a worker's share has its call
  sites split across several. A worker writes each job's lines as soon as
  the job is done, ended by a line saying so, and a file's result is handed
  on when all its jobs are (#269). A worker that fails, or whose output
  ends before its last job, has the jobs it did not finish read in the main
  process, and the report says so.
- **One jedi `Script` per file**, reused for all its lookups. jedi stops
  inferring a node past 300 inferences of it, counting per `Script`, and its
  API resets every other limit at each request but not that count; the
  layer resets it too (#255), so a lookup on a reused `Script` has the
  budget a fresh one gives. Before, typeshed's `list` and `dict` passed the
  cap on a long file and later lookups through them answered nothing, which
  a retry on a fresh `Script` per empty call site and class base half made
  up for; the retries are gone. Measured on the #240 traces, main against
  the change: agent-config 130 → 155 references edges, graphify 7 more call
  edges (3 seen at run time, the property reads above; 4 property reads its
  tests do not run), nothing lost; CPU (user) agent-config 74 s → 41 s,
  graphify 933–960 s → 273–297 s. The reset is jedi 0.20's internals;
  `TestCountsReset` fails the day it stops working.
- **A build is deterministic** (amended 2026-10-11, #267). jedi holds the
  values an expression may have in a `ValueSet`, a frozenset of objects
  hashed by identity, so it tried them in the order of their memory
  addresses; where a limit cut inference short, that order decided what was
  found. Two builds of openpilot from one tree differed by 8 call edges, and
  a file's answers depended on the files its worker had read before it. The
  layer makes a `ValueSet` keep the order its values were added in, with a
  frozenset's equality and hashing. Measured: two builds of openpilot are
  identical but for the build time; agent-config and graphify have the same
  edges and oracle counts as before; no CPU cost (openpilot 2,615 s and
  2,572 s against 2,753 s and 2,761 s). It is jedi 0.20's internals;
  `TestValueSetOrdered` fails the day it stops working.
- **A worker keeps every tree parso parsed** (amended 2026-10-11, #272).
  Once parso holds 600 trees it drops each one not used in the last 10
  minutes, and a tree's last use starts at its file's modification time.
  So a worker past 600 trees dropped the tree of the very file its
  `Script` was reading, unless that file had been edited in the last 10
  minutes, and jedi raised `KeyError` on every later lookup in it. On
  openpilot that emptied 5 files in a full build and cut a sixth from 24
  call edges to 15. The files resolve alone, so a file's answers depended on
  its worker's history and on the clock. Measured: openpilot gains those 74
  call edges and loses none; two builds are identical; agent-config and
  graphify are unchanged; CPU unchanged (2,694 s and 2,651 s against
  2,669 s); the largest worker peaks at 1.04 GB against 0.94 GB, all
  workers together at 3.0–3.4 GB against 2.9 GB (sampled every 3 s). It is
  parso 0.8.7's internals; `TestParsoCacheKept` fails the day it stops
  working.
- **jedi does not search other files for a parameter's callers**
  (amended 2026-10-12, #265, the owner's decision). jedi infers what a
  parameter holds from the calls passing it, and by default searches every
  file of the project for them; callers in the same file are still read.
  A call through a parameter is unplaced anyway (Calls, above), so the
  search mostly fed guesses the layer does not keep, and a search that
  reached jedi's cap on files opened made a file's answers depend on which
  files exist at all. Measured, main against the setting:
  - **Lost, nothing gained:** on openpilot 16 call edges and 2 references,
    all through a parameter except `MultiOptionDialog._render`'s 2 calls on
    elements of a list built from one; on agent-config 5 calls seen at run
    time, all `af.run()` through a parameter; on graphify none.
  - **Faster:** graphify's full build about 290 s → 109 s of CPU (openpilot's
    unchanged). Replaying openpilot's last 20 Python commits, a commit that
    only edits files reruns 0–49 files (jedi done in 0.7–6.7 s) against
    37–151 (6.8–20.7 s), and one that adds or removes a file 377–548 (19–38
    s) against 734–807 (74–89 s). Both replays end on a graph equal to a
    fresh full build.
  - The fixture's `sized()` is `measure`'s one caller, in another file:
    `TestDeps` fails the day jedi searches for it again, and so does
    `TestReuse`'s case of a caller added in another file.
- **No tree goes through parso's disk cache** (amended 2026-10-12, #279).
  parso also pickles each tree into jedi's cache directory, which every
  process shares, and loads a pickle newer than its file. A worker reading
  a pickle another was still writing raised from inside an import, which
  then resolved to nothing. After an edit to tinygrad's `helpers.py`, two
  files importing it lost the import and its calls in some openpilot builds
  and not others: 4 and 5 edges in two of three comparisons with a full
  build. Eight processes reading those two files at once just after
  `helpers.py` was touched lost the import 3 times in 144; with the disk
  cache off, 0 times in 432. A worker keeps its trees in memory only.
  Full builds of openpilot took 968–1,046 s after, against 973–1,000 s
  before, on the same busy machine; CPU was not measured. It is parso
  0.8.7's internals; `TestParsoNoPickles` fails the day it stops working.
- **The oracle is execution.** `scripts/pytrace.sh` runs a repository's tests
  with every Python process recording its calls into the repository, and
  `internal/oracle` looks for each among the graph's edges
  (`ALIDADE_PYTRACE`). A module's own `__getattr__` called from an import
  line is counted apart, not missing, and has no edge (the owner's decision
  on #240, #254): `from m import x` asks `hasattr(m, '__path__')`, which a
  plain module answers through its `__getattr__`. On graphify, 95 of the 96
  traced calls of one come from import lines (one of them also imports a
  lazily resolved name) and leave the missing list, 278 → 183; the other is
  a dynamic `getattr(mod, name)`, still missing.
- **The server answers before jedi is done** (amended 2026-10-11, #269, the
  owner's decision; ADR 005 amended with it). jedi takes about 14 minutes
  on openpilot, and a session waited for all of it before its first answer.
  Now `serve` answers from a warm-up pass and runs jedi in the background
  (`py.Options.Background`). The `graph` command still waits for jedi.
  - **The warm-up pass** is `resolve.py`'s "names" mode: `ast`, no jedi,
    every file in one process. It places a call whose callee is a function
    or class defined in the file or an enclosing function; a name imported
    from a module the root holds, followed through re-exports; an
    attribute of such a module or of a class it names; and `self.m()` or
    `cls.m()` through the enclosing class and its bases. A module name is
    the file whose dotted path ends with it, the nearest to the importer
    when several do. Every other call is unresolved until jedi answers.
    Guessing `obj.m()` from the only method of that name was tried and
    left out: on graphify it added 80 right answers and 156 that jedi
    places outside the repository or nowhere.
  - **Measured against jedi** on the same call sites, warm-up / jedi:
    agent-config 5,665 / 5,718 sites placed, graphify 15,082 / 15,251,
    openpilot 65,285 / 94,121. Of the warm-up's own answers, at most 0.2%
    could be wrong: 11 name a target jedi does not, and 113 place a call
    jedi leaves unresolved (6 sampled, all right). On the oracle, the
    warm-up graph alone has 975 of agent-config's 1,192 calls seen at run
    time (jedi: 1,002) and 10,089 of graphify's 10,612 (jedi: 10,234).
    It takes 0.5 s, 2.3 s and 15 s.
  - **Edges say which answered.** An edge from a file jedi has not read yet
    is INFERRED (ADR 006). Once jedi has read the file, its edges are jedi's
    and RESOLVED. Workers hand each file on as they finish it, smallest jobs
    first. A file's answers do not depend on what its worker read before it
    (#272), so the order changes nothing in the result.
  - **Once jedi is done, the layer is the one a waiting build gives.**
    Measured on openpilot: the graph written after jedi finished equals
    main's, line for line apart from build time. `TestBackground` pins the
    same on the fixture, derivations included.
  - **An edit to a Python file reruns jedi only where its answers can
    change** (amended 2026-10-12, #269). While jedi reads a file, the
    resolver records what it reaches beyond it: each file it parses, each
    module name it looks up (found or not), and each name it searches the
    project's files for, to infer a parameter from the calls passing it.
    jedi opens at most 2,000 files per search, in walk order, and a search
    that reaches the cap is recorded as such. After an edit, an unchanged
    file keeps jedi's answers from the build before unless:
    - a file jedi parsed for it changed, appeared or went away. Files are
      compared by content hash, taken when the build starts; a file
      outside the layer's own (a virtual environment's) is hashed as its
      answer arrives, and one modified since the build started counts as
      changed;
    - a file appeared or went away that a module name it looked up could
      find: the file under its own name, a package marker under its
      package's, and a directory under its own name when the directory
      itself came or went with the file (amended 2026-10-13, #281; the
      rule first counted every directory on the file's path, and on
      openpilot 400 files had looked up `openpilot`);
    - a package marker (`__init__.py`) above it appeared or went away.
      Which of a file's parent directories are packages decides its
      `sys.path` in jedi, but no case tried changed an edge this way: this
      rule is the cautious side;
    - a changed or added file contains, as a whole word, a name it searched
      for, which is how jedi's search matches; or, when one of its
      searches reached the cap, any file appeared or went away.
    jedi then reads only the other files, and the warm-up pass answers
    only for them: its table parses a module when an answer first reaches
    it. Everything else about the background is as above.
  - **Measured on openpilot** (dependencies recorded by a full build, an
    edit to each file simulated in turn): a one-file edit reruns a median
    13 files, 75 at the 90th percentile, 693 at most (tinygrad's
    `helpers.py`). Without the name-search rule it would be 2, 15 and 692:
    jedi's search for a parameter's callers is most of the widening, which
    #265 then turned off (below). 427 of 1,499 files had a search
    reach the cap, so adding or removing any file reruns them. Matching a
    searched name as text instead of as a word gave a median of 29.
  - **Replayed on openpilot's last 20 Python commits** (4adbb8574 to
    93bfcc38a, its submodules held at one state): at every commit, the
    layer jedi finished after reuse equals a fresh full build line for
    line (481,191 to 481,443 lines; nothing on either side alone).
    - At the 14 commits that only edit files, 37 to 152 files reran,
      median 98, of about 1,640: more than the simulation's 13, since a
      commit touches several files and the name rule follows their whole
      text. At 12 of the 14, most of the count was the name rule alone. The first answer
      came in 1.8 to 3.5 s and jedi was done in 4.6 to 16 s.
    - At the 6 that add or remove a file, 740 to 815 reran, most of them
      files whose search reached the cap. The first answer came in 9 to
      11 s and jedi was done in 64 to 75 s.
    - A full build took 845 to 985 s.
  - **Recording costs nothing measurable.** A full build of openpilot,
    twice each, alternating with main: 852 and 854 s, 2,675 and 2,676 s of
    CPU, against main's 855 and 867 s, 2,678 and 2,700 s. The graphs are
    identical but for build time.
  - `TestReuse` edits a copy of the fixture eight ways, checks which files
    rerun, and that the layer jedi then finishes equals a fresh build;
    switching off any rule fails a case, and for four of them the graph
    itself comes out wrong. `TestDeps` pins what the resolver records.
  - **A file added or removed reruns a handful of files, not hundreds**
    (amended 2026-10-13, #281). Replayed on the same 20 commits, with the
    parameter search off (#265) and the directory rule narrowed: at the 6
    commits that add or remove a file, 3 to 53 files were not kept and
    jedi was done in 0.7 to 10.3 s, against 390 to 560 files and 19 to 38
    s with the rule as it was (files minus files reused, on both sides). At the 14 that only edit files, 1 to 50
    files and 0.7 to 6.5 s, as before. At each of the 6, and at the last
    commit, the layer jedi finished equals a fresh full build line for
    line (481,188 to 481,425 lines; nothing on either side alone).
    `TestReuse` has both sides of the rule: a file added to a directory
    others looked up reruns nothing else, and a file added two new
    directories deep reruns the file that imports it, which without the
    rule is kept and leaves the layer differing from a fresh build.
  - **jedi's answers are kept between sessions** (amended 2026-10-12,
    #279, the owner's decision on #271). The server writes what jedi
    knows to a file per tree under the user's cache directory
    (`alidade/py/`), when jedi finishes and when the session closes
    before then. The next session loads it as the previous build and
    applies the rules above, so a file keeps an answer only when nothing
    jedi read for it changed. The file is PXF, one `@dataset` table per
    kind of row, and it records the engine: a hash of `resolve.py`, jedi
    and parso, and the CPython linked with its standard library. jedi
    infers in that interpreter, so nothing outside the tree but the
    engine can change an answer; a cache of another engine, format or
    root is ignored and written over. `alidade graph` keeps nothing.
    - **Measured on openpilot,** each session its own process: the first,
      with no cache, had jedi done in 1,046 s; the next, on the unchanged
      tree, in 1.7 s (first answer 1.6 s); after a blank line was added to
      the top of tinygrad's `helpers.py`, 576 files reran and jedi was done
      in 125 s. Both later graphs equal a fresh full build line for line
      (481,443 lines). The file is 48 MB and loads in 0.45 s.
    - `TestReuse` runs each of its eight edits a second time across a
      cache on disk; `TestCacheRoundTrip`, `TestCacheIgnored` and
      `TestCacheClose` pin that an answer reads back unchanged, that a
      cache of another engine, format or root or a damaged one is not
      read, and that a session closed early keeps what jedi finished.

**Measured for the 2026-10-10 amendment,** on the execution traces of #240:
agent-config 997 → 999 of 1,192 calls seen at run time are edges (`tier_env`'s
`__enter__` and `__exit__`), graphify 10,180 of 10,612, unchanged (no `with`
on its own classes), nothing lost. References: 130 edges on agent-config, 415
on graphify. Build time on agent-config, 3 runs each: 11.4–11.6 s before,
12.6–13.5 s after.

**Measured for the bases (#249),** at the same traces: agent-config 999 →
1,000 calls seen at run time are edges (`FailComment(...)` running
`Harness.__init__`), graphify 10,180, unchanged, nothing lost. `implements`
edges: 4 on agent-config and 27 on graphify, which are exactly the bases that
name a class the repository defines, counted again from the syntax tree (5 and
45 bases in all; the rest are `Exception`, `TestCase`, `str` and the like).
Build time, 3 runs each, alternating: agent-config 17.6–18.1 s before,
17.3–19.2 s after; graphify 213–234 s before, 204–217 s after.

Not in this version: nodes for external packages (an import of `requests`
is counted, not a node).

## What would change it

- jedi's unresolved share on a target repository being too high for its
  questions. That would bring pyright, run in V8, back.
- python-build-standalone ceasing to publish full archives, or moving to an
  LLVM newer than any available to convert its bitcode.
