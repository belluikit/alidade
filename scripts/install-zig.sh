#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Installs zig, the C toolchain the v8 tag needs on Linux and the release
# needs for its linux targets (#221, #248): v8go's own libc++ headers need
# clang 21 or newer, which Ubuntu 24.04 does not package. Usage:
# install-zig.sh <dir>; zig lands in <dir>/zig, and under GitHub Actions
# <dir> is added to the PATH of the steps after. The version is pinned,
# and each archive is checked against the checksum ziglang.org publishes
# for it (https://ziglang.org/download/index.json).
set -eu
dir=$1
version=0.17.0
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) platform=x86_64-linux sum=1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026 ;;
  Darwin/arm64) platform=aarch64-macos sum=b607e9b9234790a008116ae5bdb71c6243b84b9fb42a53a9e70fde41c06c536a ;;
  Darwin/x86_64) platform=x86_64-macos sum=4f9a1c5269aa17ebda5e6d3c2b89d6cbf36f7d2b22a0306e9ab98f25f95529c6 ;;
  *) echo "install-zig: no pinned zig for $(uname -s)/$(uname -m)" >&2; exit 2 ;;
esac
archive=zig-$platform-$version.tar.xz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -sSfL -o "$tmp/$archive" "https://ziglang.org/download/$version/$archive"
echo "$sum  $tmp/$archive" | shasum -a 256 -c - >/dev/null || { echo "install-zig: $archive does not match its checksum" >&2; exit 1; }
mkdir -p "$dir"
tar -xJf "$tmp/$archive" -C "$dir" --strip-components=1
"$dir/zig" version
if [ -n "${GITHUB_PATH:-}" ]; then
  echo "$dir" >> "$GITHUB_PATH"
fi
