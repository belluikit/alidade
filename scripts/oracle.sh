#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Runs the oracle harness (#148) over the in-house repositories checked out
# beside this one: the graph measured against independent sources of
# truth per repository — Go calls against the type checker, imports
# against go list, SQL against sqlc, proto against protoc and the .pb.go
# files (see internal/oracle). It prints numbers and fails only when
# it measured nothing. ALIDADE_ORACLE overrides the list.
set -eu
cd "$(dirname "$0")/.."
if [ -z "${ALIDADE_ORACLE:-}" ]; then
  roots=""
  parent="$(cd .. && pwd)"
  for r in drover steward govm protocompile voya chameleon protowire-go syzygo pxfed protovalidate protolsp goed gameflow clair golias protoregistry; do
    # -e, not -d: a worktree's or a submodule's .git is a file.
    [ -e "$parent/$r/.git" ] && roots="$roots $parent/$r"
  done
  ALIDADE_ORACLE="$roots"
fi
if [ -z "$(echo $ALIDADE_ORACLE)" ]; then
  echo "oracle: no repository checked out beside this one, and ALIDADE_ORACLE is unset; nothing to run" >&2
  exit 2
fi
export ALIDADE_ORACLE
exec go test ./internal/oracle -run '^TestOracleGoCalls$' -count=1 -timeout 30m -v "$@"
