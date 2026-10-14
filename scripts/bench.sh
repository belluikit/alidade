#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Runs the A/B benchmark (#149): each question of the set asked of the
# same model with alidade's MCP tools and without them, every answer
# scored against its reference (see internal/bench). It spends API
# money — each session is capped at ALIDADE_BENCH_BUDGET dollars — so
# run it deliberately. ALIDADE_BENCH defaults to the protocompile checkout
# beside this one, which must be at the commit the set's references were
# written against; ALIDADE_BENCH_OUT defaults to a dated file under
# bench-results/. The other knobs are listed in internal/bench/live_test.go.
set -eu
cd "$(dirname "$0")/.."
: "${ALIDADE_BENCH:=$(cd .. && pwd)/protocompile}"
if [ ! -e "$ALIDADE_BENCH/.git" ]; then
  echo "bench: $ALIDADE_BENCH is not a checkout; set ALIDADE_BENCH" >&2
  exit 2
fi
mkdir -p bench-results
: "${ALIDADE_BENCH_OUT:=$(pwd)/bench-results/$(date -u +%Y%m%dT%H%M%SZ).pxf}"
export ALIDADE_BENCH ALIDADE_BENCH_OUT
echo "bench: results to $ALIDADE_BENCH_OUT" >&2
exec go test ./internal/bench -run '^TestBench$' -count=1 -timeout 3h -v "$@"
