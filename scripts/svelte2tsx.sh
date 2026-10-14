#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Regenerates internal/adapter/ts/svelte's bundle of svelte2tsx (#221):
# installs the pinned packages into a scratch directory and bundles them
# with esbuild's Go API, writing LICENSES.txt beside the bundle (#282).
# Needs npm, here and nowhere else: the build only
# reads the committed bundle. Move a version, run this, rerun the TS oracle.
set -eu
SVELTE2TSX=0.7.61
SVELTE=5.56.3
TYPESCRIPT=6.0.3
cd "$(dirname "$0")/../internal/adapter/ts/svelte"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$work" && npm init -y >/dev/null && npm install --ignore-scripts --no-audit --no-fund \
  "svelte2tsx@$SVELTE2TSX" "svelte@$SVELTE" "typescript@$TYPESCRIPT" >/dev/null)
go run gen/main.go "$work" .
printf 'svelte2tsx %s, svelte %s, typescript %s\n' "$SVELTE2TSX" "$SVELTE" "$TYPESCRIPT" > VERSIONS
ls -l svelte2tsx.js.gz
