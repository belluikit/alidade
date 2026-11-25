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
. scripts/inhouse.sh
if [ -z "${ALIDADE_E2E:-}" ]; then
  ALIDADE_E2E="$(inhouse steward drover gameflow)"
fi
if [ -z "$(echo $ALIDADE_E2E)" ]; then
  echo "mcp-e2e: no repository checked out beside this one or under ../../trendvidia, and ALIDADE_E2E is unset; nothing to run" >&2
  exit 2
fi
export ALIDADE_E2E
exec go test ./internal/e2e -run '^TestE2E$' -count=1 -timeout 20m -v "$@"
