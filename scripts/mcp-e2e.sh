#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Checks what an agent receives from alidade (#174): the binary built as
# the release builds it, serving each repository over stdio — every
# listed description against the source, every tool's answer parsed and
# within budget, and a sample of callers against the graph. It fails on
# a broken surface. ALIDADE_E2E overrides the repositories;
# ALIDADE_E2E_BIN checks an existing binary, a release as shipped.
set -eu
cd "$(dirname "$0")/.."
if [ -z "${ALIDADE_E2E:-}" ]; then
  parent="$(cd .. && pwd)"
  roots=""
  for r in steward drover gameflow; do
    [ -e "$parent/$r/.git" ] && roots="$roots $parent/$r"
  done
  ALIDADE_E2E="$roots"
fi
if [ -z "$(echo $ALIDADE_E2E)" ]; then
  echo "mcp-e2e: no repository checked out beside this one, and ALIDADE_E2E is unset; nothing to run" >&2
  exit 2
fi
export ALIDADE_E2E
exec go test ./internal/e2e -run '^TestE2E$' -count=1 -timeout 20m -v "$@"
