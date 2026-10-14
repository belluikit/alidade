#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Cyclomatic complexity per governance domain (.steward/governance.pxf
# max_cyclomatic_complexity). golangci-lint's gocyclo has one threshold for
# the whole module; governance sets one per domain, so the gate runs gocyclo
# once per domain with that domain's limit. Exits non-zero on the first
# function over its limit. Tests count: a test over the limit is as hard to
# read as production code over it.
set -eu
# gocyclo is a tool of the module (go.mod's tool line), pinned by go.sum
# and run from the module cache. It was `go run …/gocyclo@v0.6.0`, which
# asks the proxy for the module's version list on every run — "loading
# deprecation", even with the module cached — so a proxy timeout failed
# CI with nothing measured (2026-09-13, and PR #143 on 2026-10-07; #144).
GOCYCLO="go tool gocyclo"

# Prove the tool runs before trusting its silence. Each check below reads
# an empty result as "nothing over the limit", and the tool may need
# fetching on a cold module cache: a proxy timeout then makes every
# check print nothing and the gate pass without having measured anything.
# `-top 1` exits 0 and prints one line whenever gocyclo works at all.
if ! probe=$($GOCYCLO -top 1 ./internal 2>&1); then
  echo "complexity gate did not run: $probe" >&2
  exit 2
fi
if [ -z "$probe" ]; then
  echo "complexity gate did not run: gocyclo printed nothing for -top 1" >&2
  exit 2
fi

# Every package directory belongs to one domain below: a directory the
# list does not name would never be measured, and gocyclo given a path
# that does not exist prints to stderr and exits 0, so a renamed
# directory would pass silently too.
domains="internal/mcp cmd internal/adapter internal/python internal/graph internal/scope internal/build internal/cli internal/oracle internal/bench internal/e2e internal/version"
for d in internal/*/ cmd/; do
  d=${d%/}
  case " $domains " in
    *" $d "*) ;;
    *) echo "complexity gate: $d is in no domain; add it to a check below" >&2; exit 2 ;;
  esac
done

status=0
errs=$(mktemp)
trap 'rm -f "$errs"' EXIT
check() {
  limit=$1; shift
  for p in "$@"; do
    [ -e "$p" ] || { echo "complexity gate: domain path $p does not exist" >&2; exit 2; }
  done
  # gocyclo exits 1 when it finds a function over the limit (go run
  # reports that as "exit status 1"); anything else on stderr is the tool
  # failing, not a clean bill.
  out=$($GOCYCLO -over "$limit" "$@" 2>"$errs") || true
  if grep -v '^exit status 1$' "$errs" | grep -q .; then
    echo "complexity gate did not run for $*:" >&2
    cat "$errs" >&2
    exit 2
  fi
  if [ -n "$out" ]; then
    echo "over the domain limit of $limit:"
    echo "$out" | sed 's/^/  /'
    status=1
  fi
}
check 10 internal/mcp cmd
check 12 internal/adapter internal/python internal/graph internal/scope
check 15 internal/build internal/cli internal/oracle internal/bench internal/e2e internal/version
exit $status
