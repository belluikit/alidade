# Building alidade from source

Most people should install a release: see [Installing](../README.md#installing).
Build from source to work on alidade, or to get a build without V8 or
CPython.

## Quick version

You need Go 1.27.2 or newer and git.

```
git clone --recurse-submodules https://github.com/trendvidia/alidade
cd alidade
CGO_LDFLAGS= CGO_CXXFLAGS=-nostdinc++ go build -tags v8,python -o alidade ./cmd/alidade
```

The submodule is required. The frontend layer builds TypeScript 7's
compiler from it, and the module replaces typescript-go with that checkout,
so `go install …@latest` cannot build alidade (#215).

## The two build tags

| tag | what it links | what you lose without it |
|---|---|---|
| `v8` | V8, which translates Svelte components so their calls are in the graph (#221) | Svelte components say their calls are not in the graph |
| `python` | CPython 3.14.8 and jedi, which read Python declarations, imports and calls (#237, [ADR 008](adr/008-python-engine.md)) | Python files are in the graph as files that say so |

Without either tag, `go build ./...` works anywhere Go does, with no C
toolchain. Release binaries are built with both
(`scripts/release-build.sh`). `alidade version` names the runtimes a binary
links.

Both tags need cgo. What the flags are for:

- `CGO_CXXFLAGS=-nostdinc++` makes V8's own libc++ headers replace the
  system's.
- `CGO_LDFLAGS=` keeps a host's own linker flags (Homebrew's, say) out of
  the link.

The `python` tag uses the module
[`github.com/trendvidia/pylib`](https://github.com/trendvidia/pylib), which
carries CPython as static libraries for darwin and linux, on arm64 and
amd64. The standard library and jedi are unpacked once into the user's
cache directory.

## Compilers

The `v8` tag needs a recent C++ compiler, because v8go brings its own libc++
headers.

**macOS.** Apple clang 21 or newer, which Xcode 26.6 and 27 have. Xcode
16's clang 17 rejects the headers.

**Linux.** One of:

- clang 21 or newer: prefix the build with `CC=clang CXX=clang++`.
- zig: prefix it with
  `CC="zig cc -fno-sanitize=undefined" CXX="zig c++ -fno-sanitize=undefined"`.
  `scripts/install-zig.sh <dir>` installs the version CI uses (0.17.0).

gcc cannot build the `v8` tag: v8go asks for clang's `-stdlib=libc++`
(#246). Older clang cannot either: clang 18 and 20 reject the headers.
Ubuntu 24.04 packages neither clang 21 nor zig, which is why CI installs
zig, as the release does when it builds the linux binaries from macOS.

## At run time

The binary has no runtime dependencies. It needs a Go toolchain on the
machine only to analyse Go: see
[The Go toolchain](usage.md#the-go-toolchain).
