#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Runs TestCorpus over the in-house repositories checked out beside this
# one: the real-repository checks that a fixture cannot make (see
# internal/cli/corpus_test.go). Run it before a release; a repository
# that is not checked out is skipped. ALIDADE_CORPUS overrides the list.
set -eu
cd "$(dirname "$0")/.."
if [ -z "${ALIDADE_CORPUS:-}" ]; then
  roots=""
  parent="$(cd .. && pwd)"
  for r in drover steward govm protocompile voya chameleon protowire-go syzygo pxfed protovalidate protolsp goed gameflow clair golias protoregistry; do
    # -e, not -d: a worktree's or a submodule's .git is a file.
    [ -e "$parent/$r/.git" ] && roots="$roots $parent/$r"
  done
  ALIDADE_CORPUS="$roots"
fi
# With nothing to run, the test skips and go test exits 0: a pass that
# measured nothing. Say so instead.
if [ -z "$(echo $ALIDADE_CORPUS)" ]; then
  echo "corpus: no repository checked out beside this one, and ALIDADE_CORPUS is unset; nothing to run" >&2
  exit 2
fi
export ALIDADE_CORPUS
exec go test ./internal/cli -run '^TestCorpus$' -count=1 -timeout 30m -v "$@"
