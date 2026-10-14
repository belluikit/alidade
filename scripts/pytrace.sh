#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Runs a command — a repository's test suite — with every Python process
# it starts recording its calls into the repository (#237), for the
# Python layer's execution oracle:
#   scripts/pytrace.sh ROOT OUT command [args...]
# OUT receives one .tsv per process; ALIDADE_PYTRACE=OUT then has
# internal/oracle compare the graph of ROOT with them. A process that
# exits through os._exit, or runs Python with -I or -S, records nothing.
set -eu
root=$(cd "$1" && pwd) out=$2
shift 2
mkdir -p "$out"
here=$(cd "$(dirname "$0")/pytrace" && pwd)
export PYTHONPATH="$here${PYTHONPATH:+:$PYTHONPATH}"
export ALIDADE_PYTRACE_ROOT="$root" ALIDADE_PYTRACE_OUT=$(cd "$out" && pwd)
exec "$@"
