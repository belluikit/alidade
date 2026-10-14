#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Draws a benchmark question set for a Python repository from the calls
# its test suite made (#284), as bench.Generate does for Go from the type
# checker's:
#   scripts/pybenchgen.py ROOT TRACES [PER] > set.pxf
# TRACES is the directory scripts/pytrace.sh wrote. A question asks for
# the direct callers of a function or method; its answer is the callers
# the traces recorded, in production code, each named by the function
# holding the calling line (Python's own ast). Strata, PER of each (5):
#   unique  no other def in production code has the name;
#   shared  three or more defs have it.
# A target has two to eight callers, none at module level, and is not a
# dunder method. Candidates are taken in order of the SHA-256 of
# "repo|stratum|file:Qualified.name". A call no test made is in no
# answer: an answer naming more callers still scores.
import ast
import collections
import glob
import hashlib
import json
import os
import subprocess
import sys


def is_test(path):
    parts = path.split("/")
    base = parts[-1]
    return any(p in ("tests", "test", "testing") for p in parts[:-1]) or base.startswith("test_") or base.endswith("_test.py") or base == "conftest.py"


KINDS = {}  # file -> qualified name -> function, method or nested function


def defs(root, path):
    """The defs of a file: (first line, last line, def line, qualified name)."""
    with open(os.path.join(root, path), encoding="utf-8") as f:
        tree = ast.parse(f.read())
    out = []

    def walk(node, prefix, kind):
        for child in ast.iter_child_nodes(node):
            if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
                first = min([child.lineno] + [d.lineno for d in child.decorator_list])
                out.append((first, child.end_lineno, child.lineno, prefix + child.name))
                kinds[prefix + child.name] = kind
                walk(child, prefix + child.name + ".", "nested function")
            elif isinstance(child, ast.ClassDef):
                walk(child, prefix + child.name + ".", "method")
            else:
                walk(child, prefix, kind)

    kinds = KINDS.setdefault(path, {})
    walk(tree, "", "function")
    return out


def main():
    root, traces = os.path.realpath(sys.argv[1]), sys.argv[2]
    per = int(sys.argv[3]) if len(sys.argv) > 3 else 5
    repo = os.path.basename(root)
    commit = subprocess.run(["git", "-C", root, "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()

    calls = set()
    for path in glob.glob(os.path.join(traces, "*.tsv")):
        with open(path, encoding="utf-8") as f:
            for line in f:
                cf, cl, tf, tn, tl = line.rstrip("\n").split("\t")
                calls.add((cf, int(cl), tf, tn, int(tl)))

    files = sorted({c[0] for c in calls} | {c[2] for c in calls})
    by_file = {}
    for path in files:
        if path.endswith(".py") and not is_test(path) and os.path.exists(os.path.join(root, path)):
            by_file[path] = defs(root, path)

    # Every def of production code, by simple name: the denominator of
    # "shared". Files no traced call touched count too.
    named = collections.Counter()
    tracked = subprocess.run(["git", "-C", root, "ls-files", "*.py"], capture_output=True, text=True, check=True).stdout.split("\n")
    for path in tracked:
        if path and not is_test(path):
            try:
                for d in by_file.get(path) or defs(root, path):
                    named[d[3].rsplit(".", 1)[-1]] += 1
            except SyntaxError:
                pass

    def holder(path, line):
        """The innermost def holding a line, or None at module level."""
        best = None
        for first, last, _, name in by_file.get(path, ()):
            if first <= line <= last and (best is None or first >= best[0]):
                best = (first, name)
        return best and best[1]

    callers = collections.defaultdict(set)
    module_level = set()
    for cf, cl, tf, tn, tl in calls:
        if tf not in by_file or is_test(cf) or cf not in by_file:
            continue
        target = next((name for first, _, lineno, name in by_file[tf] if tl in (first, lineno) and name.rsplit(".", 1)[-1] == tn), None)
        if target is None:
            continue
        key = (tf, target)
        who = holder(cf, cl)
        if who is None:
            module_level.add(key)
        elif (cf, who) != key:
            callers[key].add((cf, who))

    out = []
    for stratum in ("unique", "shared"):
        cands = []
        for (tf, target), who in callers.items():
            simple = target.rsplit(".", 1)[-1]
            n = named[simple]
            if simple.startswith("__") or (tf, target) in module_level or not 2 <= len(who) <= 8:
                continue
            if (stratum == "unique" and n != 1) or (stratum == "shared" and n < 3):
                continue
            want = sorted({w.rsplit(".", 1)[-1] for _, w in who})
            kind = KINDS[tf][target]
            text = "In %s, which functions and methods call the %s %s (declared in %s) directly?" % (repo, kind, target, tf)
            if any(w in text.replace(".", " ").replace("/", " ").replace("(", " ").replace(")", " ").split() for w in want):
                continue
            note = "%s:%s. %d defs in production code are named %s. Callers the test suite's traces recorded: %s." % (
                tf, target, n, simple, ", ".join("%s:%s" % w for w in sorted(who)))
            order = hashlib.sha256(("%s|%s|%s:%s" % (repo, stratum, tf, target)).encode()).hexdigest()
            cands.append((order, stratum, text, want, note))
        for i, (_, stratum, text, want, note) in enumerate(sorted(cands)[:per], 1):
            out.append(("%s-%s-%d" % (repo, stratum, i), stratum, text, want, note))

    q = json.dumps  # a PXF string is a JSON string for this alphabet
    print("# Questions over %s drawn by rule, not picked (#284): scripts/pybenchgen.py" % repo)
    print("# over the calls its test suite made (scripts/pytrace.sh), at the commit")
    print("# below. Strata: unique (callers of a name no other def shares), shared")
    print("# (callers of a name three or more defs share). Candidates are taken in")
    print('# order of the SHA-256 of "repo|stratum|file:name". %d traced calls read.' % len(calls))
    print()
    print("repo = %s\ncommit = %s\nlanguage = \"python\"" % (q(repo), q(commit)))
    for qid, stratum, text, want, note in out:
        print("\nquestion {\n  id = %s\n  shape = %s\n  text = %s\n  want = [%s]\n  note = %s\n}" % (
            q(qid), stratum, q(text), ", ".join(q(w) for w in want), q(note)))


main()
