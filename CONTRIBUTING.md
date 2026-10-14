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
be one from go.dev (Homebrew's leaves the file out).

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

## Reporting

Bugs and ideas go to [issues](https://github.com/belluikit/alidade/issues).

Comments, ADRs and changelog entries written before v0.12.0 cite issues by
number (#206). Those numbers refer to the tracker alidade had before it was
published, which is not public; they are not this repository's issues.
Security reports go privately, as [SECURITY.md](SECURITY.md) says.
