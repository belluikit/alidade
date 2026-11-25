# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Sourced by the scripts that run over the in-house repositories. inhouse
# prints the checkout of each repository it is named, absolute, on one
# line: the one beside this repository, else the one beside it under
# trendvidia/ (../../trendvidia/<name>), where the others stayed when
# alidade moved to belluikit/ (#86). A repository checked out in neither
# place is left out. Run from the repository's root.
inhouse() {
  parent="$(cd .. && pwd)"
  out=""
  for r in "$@"; do
    for d in "$parent/$r" "$parent/../trendvidia/$r"; do
      # -e, not -d: a worktree's or a submodule's .git is a file.
      if [ -e "$d/.git" ]; then
        out="$out $(cd "$d" && pwd)"
        break
      fi
    done
  done
  echo $out
}
