# Control: a subset load of the base tree spliced onto the base must give the base back.
import sys, os, json
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import importlib.util
spec = importlib.util.spec_from_file_location("run", os.path.join(os.path.dirname(os.path.abspath(__file__)), "run.py"))
src = open(spec.origin).read().replace("\nmain()\n", "\n")
ns = {"__file__": spec.origin}; exec(compile(src, "run.py", "exec"), ns)
base_tree, pkgs = sys.argv[1], sys.argv[2].split(",")
SP = os.path.dirname(os.path.abspath(__file__))
tag = os.path.basename(base_tree)
bn, be = ns["read"](os.path.join(SP, tag + ".base.tsv"))
for name, P in (("approx", set(pkgs)), ("exact", ns["importers_closure"](be, set(pkgs)))):
    out = os.path.join(SP, tag + ".control-" + name + ".tsv")
    cost = ns["dump"](base_tree, out, P)
    on, oe = ns["read"](out)
    print(json.dumps({"control": name, "pkgs": len(P), "cost": cost, **ns["compare"](name, *ns["splice"](bn, be, on, oe, P), bn, be, set(pkgs))}), flush=True)
