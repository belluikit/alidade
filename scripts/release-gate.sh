#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# The release gate (#105): a release is a tag on a commit of main whose CI
# passed. Usage: release-gate.sh <ref> — a tag, a branch or a sha,
# resolved through GitHub, so the gate runs from the workflow's checkout
# whatever the ref's tree holds (a tag cut before this script has no copy
# of it). Needs gh, GH_TOKEN with checks:read and contents:read, and
# GH_REPO (owner/name). Under Actions it writes the commit it passed as
# the step output sha, which is the commit the release is built from.
#
# Two things are checked, each against GitHub rather than the checkout:
#   - the commit is on main: identical to it or behind it. A green
#     pull_request run attaches to the PR's head commit, so a check-run
#     alone would pass a tag on a branch that was never merged;
#   - each CI check-run on the commit concluded success, the latest run of
#     each (a rerun after an infra failure counts). A commit no run
#     reached — an inner commit of a merged PR — has none, and fails.
set -eu
ref=$1
sha=$ref
fail() {
  echo "::error::release gate: $ref ($sha): $*" >&2
  exit 1
}
[ -n "${GH_REPO:-}" ] || fail "GH_REPO is not set"
sha=$(gh api "repos/$GH_REPO/commits/$ref" --jq .sha 2>&1) || fail "no such commit: $sha"

status=$(gh api "repos/$GH_REPO/compare/main...$sha" --jq .status 2>&1) || fail "cannot compare it with main: $status"
case $status in
  identical | behind) ;;
  *) fail "not a commit of main (compare: $status); a release is a tag on main" ;;
esac

for check in "vet + build + test" "golangci-lint"; do
  conclusion=$(gh api "repos/$GH_REPO/commits/$sha/check-runs?per_page=100" \
    --jq "[.check_runs[] | select(.name == \"$check\")][0].conclusion // \"no run\"" 2>&1) ||
    fail "cannot read its check-runs: $conclusion"
  [ "$conclusion" = success ] || fail "CI check \"$check\": $conclusion"
done
echo "release gate: $ref ($sha) is on main and its CI passed"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "sha=$sha" >>"$GITHUB_OUTPUT"
fi
