# Contributing to alidade

alidade maps a repository as one graph and serves it to agents over MCP.
It composes analyses that already exist and performs none of its own
([ADR 000](docs/adr/000-composition-boundary.md)): a new language or format
is a new adapter over an existing library, never a parser written here.
The [ADRs](docs/adr) answer most "why is it like this" questions, and each
carries the measurement it rests on; read the ones near your change first.

## Building

You need Go 1.27.2 or newer (stock Go, from go.dev), git with the
submodule, and for the full build a C++ toolchain:

```
git clone --recurse-submodules https://github.com/belluikit/alidade
cd alidade
go build ./...                                                    # no cgo
CGO_LDFLAGS= CGO_CXXFLAGS=-nostdinc++ go build -tags v8,python ./...
```

The `v8` tag links V8 for Svelte components and `python` links CPython for
Python calls. On macOS the system clang builds both when it is clang 21
or newer (Xcode 26.6 and 27 are; Xcode 16's clang 17 is not). On Linux use zig
(`scripts/install-zig.sh <dir>` installs the version CI uses) with
`CC="zig cc -fno-sanitize=undefined" CXX="zig c++ -fno-sanitize=undefined"`,
or clang 21 or newer; gcc cannot build the `v8` tag.
[Building alidade](docs/building.md) says why.

## Before a pull request

CI runs these, on macOS and Linux:

```
gofmt -l ./internal ./cmd                 # prints nothing
go vet ./... && go test ./...
CGO_LDFLAGS= CGO_CXXFLAGS=-nostdinc++ go test -race -tags v8,python ./...  # Linux: without -race (#1)
golangci-lint run --build-tags v8,python  # v2.14.0
scripts/complexity.sh                     # cyclomatic complexity per domain
go run scripts/notices.go -check          # after a dependency changes
go run scripts/headers.go                 # every source file has its license header
```

`go run scripts/notices.go` regenerates `THIRD_PARTY_NOTICES.txt` when a
dependency moves; it reads Go's own license from the toolchain, which must
be one from go.dev (Homebrew's leaves the file out). Any other Go can fetch
one: `GOTOOLCHAIN=go1.27.2 go run scripts/notices.go`. The file names no Go
version, so any go.dev toolchain will do; naming the version already
installed, such as Homebrew's go1.27.4, fetches nothing.

## What makes a good change

- **An issue first** for anything beyond a small fix: the problem, the
  evidence, and what "done" means. The pull request closes it.
- **Tests with the change**, and a test that fails if the change is
  reverted. A fixture under the layer's `testdata` is the usual shape.
- **Measured, not asserted.** A change to what the graph holds says what
  it gains and loses on a real repository; `scripts/oracle.sh` measures
  the graph against an independent source of truth for each layer.
- **A CHANGELOG entry** under Unreleased for anything a user would notice.
- **An ADR amendment** when the change contradicts or extends one: the
  ADRs are meant to stay a true map of the code.
- **PXF, not JSON**, for anything alidade writes; JSON only where an
  outside protocol requires it, as MCP does.

## Benchmark results

The README's comparisons are ours: one machine, one harness, the
repositories we picked. Results from anyone else are welcome as a pull
request, including ones that come out worse for alidade. Some examples:

- the Rust graph comparison on another repository, or ours rerun with a
  newer alidade or graphify;
- the agent benchmark run on your own account, with any model;
- a correction to how we set graphify up.

A result goes in as a row of the README's table, with "run by" linking to
the pull request. The pull request carries what someone else needs to run
it again:

- **The subject:** the repository's URL and the full commit, and the
  features or build flags used.
- **The tools:** alidade's version or commit, graphify's version and the
  command that built its graph, and for Rust the `rustc` and `rust-analyzer`
  versions.
- **The command** that produced the numbers, exactly as it was run.
- **The output:** the summary lines in the description, and the whole log
  attached. For the Rust comparison those are the `TOTAL` lines of
  `TestOracleGraphifyRust`; for the agent benchmark, `TestReport` over
  the results files.
- **Anything left out** and why, such as a library that does not compile
  on your platform.

[Developing alidade](docs/development.md) says how each benchmark runs.
A result we cannot reproduce from what the pull request says is not merged
until we can; we will say in the pull request what is missing.

## Reporting

Bugs and ideas go to [issues](https://github.com/belluikit/alidade/issues).

Comments, ADRs and changelog entries written before v0.12.0 cite issues by
number (#206). Those numbers refer to the tracker alidade had before it was
published, which is not public; they are not this repository's issues.
Security reports go privately, as [SECURITY.md](SECURITY.md) says.
