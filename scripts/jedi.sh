#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Regenerates internal/python's bundle of jedi and parso (#237): downloads
# the pinned wheels, both pure Python, and packs them as a gzipped tar the
# python build tag embeds and unpacks once into the user's cache. Needs
# pip, here and nowhere else. Move a version, run this, rerun the tests.
set -eu
JEDI=0.20.0
PARSO=0.8.7
cd "$(dirname "$0")/../internal/python"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
python3 -m pip download -q --no-deps --only-binary=:all: -d "$work/wheels" "jedi==$JEDI" "parso==$PARSO"
python3 -I - "$work" <<'PY'
import os, sys, tarfile, zipfile
work = sys.argv[1]
tree = os.path.join(work, "tree")
for w in sorted(os.listdir(os.path.join(work, "wheels"))):
    zipfile.ZipFile(os.path.join(work, "wheels", w)).extractall(tree)
# gzip's header carries a time and a name unless told not to; both are
# zeroed so the bundle is a function of the wheels alone.
import gzip
out = gzip.GzipFile("jedi.tar.gz", "wb", compresslevel=9, mtime=0)
out.name = ""
with tarfile.open(fileobj=out, mode="w") as t:
    for d, dirs, files in os.walk(tree):
        dirs[:] = sorted(x for x in dirs if x != "__pycache__")
        for f in sorted(files):
            p = os.path.join(d, f)
            ti = t.gettarinfo(p, os.path.relpath(p, tree))
            ti.mtime, ti.uid, ti.gid, ti.uname, ti.gname, ti.mode = 0, 0, 0, "", "", 0o644
            with open(p, "rb") as fh:
                t.addfile(ti, fh)
out.close()
PY
printf 'jedi %s, parso %s\n' "$JEDI" "$PARSO" > VERSIONS
ls -l jedi.tar.gz
