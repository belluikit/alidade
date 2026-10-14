#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Builds one release binary: release-build.sh <os/arch> <out>. alidade
# links V8 for Svelte components (#221) and CPython for Python calls
# (#237, github.com/trendvidia/pylib), so every target is a cgo build with
# the v8 and python tags and CGO_CXXFLAGS=-nostdinc++ (V8 is built with
# Chromium's libc++, whose headers must replace the system's). darwin
# builds with the host's clang, darwin/amd64 cross-compiled with -arch;
# linux with zig as the C toolchain, its undefined-behaviour sanitizer
# off (V8's static libraries carry no ubsan runtime) and stripped at the
# link (go's -s does not reach V8's sections through an external linker).
# The host's own CGO_* flags are not inherited: Homebrew's point at
# macOS-only libraries and linker options.
set -eu
target=$1 out=$2
os=${target%/*} arch=${target#*/}
export CGO_ENABLED=1 GOOS="$os" GOARCH="$arch" CGO_CXXFLAGS="-nostdinc++ -O2" CGO_CFLAGS="-O2" CGO_LDFLAGS=""
case "$target" in
  darwin/arm64) export CC="clang -arch arm64" CXX="clang++ -arch arm64" ;;
  darwin/amd64) export CC="clang -arch x86_64" CXX="clang++ -arch x86_64" ;;
  linux/arm64|linux/amd64)
    command -v zig >/dev/null || { echo "release-build: zig is not on PATH; linux targets need it" >&2; exit 2; }
    triple=$([ "$arch" = arm64 ] && echo aarch64 || echo x86_64)-linux-gnu
    export CC="zig cc -target $triple -fno-sanitize=undefined" CXX="zig c++ -target $triple -fno-sanitize=undefined"
    export CGO_LDFLAGS="-Wl,--strip-all" ;;
  *) echo "release-build: unknown target $target" >&2; exit 2 ;;
esac
go build -tags v8,python -trimpath -ldflags="-s -w" -o "$out" ./cmd/alidade
