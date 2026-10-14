# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TrendVidia, LLC.

# Records the Python calls a process makes from one file of a repository
# to another (#237): the execution oracle for the Python layer's calls.
# scripts/pytrace.sh puts this directory on PYTHONPATH, so every Python
# process the traced command starts — a test runner, the scripts its
# tests run as subprocesses — loads it at startup and, at exit, writes
# one line per distinct call to $ALIDADE_PYTRACE_OUT/<pid>.tsv:
#   caller-file caller-line callee-file callee-name callee-first-line
# Paths are relative to $ALIDADE_PYTRACE_ROOT; calls outside it are not
# recorded. A process run with -I or -S does not load this file.
import atexit
import os
import sys
import threading

_root = os.environ.get("ALIDADE_PYTRACE_ROOT")
_out = os.environ.get("ALIDADE_PYTRACE_OUT")

if _root and _out:
    _root = os.path.realpath(_root) + os.sep
    _seen = set()
    _real = {}

    def _rel(path):
        r = _real.get(path)
        if r is None:
            # A frozen or generated module's code says "<frozen os>" or
            # "<string>", which realpath would join to the working
            # directory, inside the root.
            if path.startswith("<"):
                _real[path] = ""
                return ""
            p = os.path.realpath(path)
            r = _real[path] = p[len(_root):] if p.startswith(_root) else ""
        return r

    def _profile(frame, event, arg):
        if event != "call":
            return
        back = frame.f_back
        if back is None:
            return
        callee = _rel(frame.f_code.co_filename)
        if not callee:
            return
        caller = _rel(back.f_code.co_filename)
        if caller:
            _seen.add((caller, back.f_lineno, callee, frame.f_code.co_name, frame.f_code.co_firstlineno))

    def _write():
        sys.setprofile(None)
        threading.setprofile(None)
        with open(os.path.join(_out, "%d.tsv" % os.getpid()), "w") as f:
            for r in sorted(_seen):
                f.write("%s\t%d\t%s\t%s\t%d\n" % r)

    atexit.register(_write)
    sys.setprofile(_profile)
    threading.setprofile(_profile)
