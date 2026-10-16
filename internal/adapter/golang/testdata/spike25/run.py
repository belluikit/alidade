# Spike driver (#25): base, oracle, approximate and exact overlays for worktrees.
import os, subprocess, sys, json, re, time, collections
SP = os.path.dirname(os.path.abspath(__file__))
BIN = os.path.join(SP, "spike.test")

def dump(tree, out, pkgs=None):
    env = dict(os.environ, SPIKE_DIR=tree, SPIKE_OUT=out)
    if pkgs: env["SPIKE_PKGS"] = ",".join(sorted(pkgs))
    t = time.time()
    r = subprocess.run(["/usr/bin/time", "-l", BIN, "-test.run", "TestSpikeDump", "-test.v", "-test.timeout", "0"],
                       cwd=os.path.join(os.path.dirname(BIN)), env=env, capture_output=True, text=True)
    if r.returncode != 0 or "--- PASS: TestSpikeDump" not in r.stdout:
        sys.exit("dump failed for %s %s:\n%s\n%s" % (tree, pkgs, r.stdout[-3000:], r.stderr[-3000:]))
    rss = int(re.search(r"(\d+)\s+maximum resident set size", r.stderr).group(1))
    return {"wall_s": round(time.time() - t, 1), "rss_gb": round(rss / 1e9, 2)}

def read(path):
    nodes, edges, timing = {}, set(), None
    for line in open(path):
        f = line.rstrip("\n").split("\t")
        if f[0] == "T": timing = f[1:]
        elif f[0] == "N": nodes[f[1]] = f[2]
        elif f[0] == "E": edges.add((f[1], f[2], f[3]))
    return nodes, edges

def pkg(id_):
    return id_[len("go://"):].split("#", 1)[0] if id_.startswith("go://") else None

def module_of(tree, rel_dir):
    d = os.path.join(tree, rel_dir)
    while True:
        gm = os.path.join(d, "go.mod")
        if os.path.exists(gm):
            m = re.search(r"^module\s+(\S+)", open(gm).read(), re.M).group(1)
            rel = os.path.relpath(os.path.join(tree, rel_dir), d)
            if m == "std": return rel
            return m if rel == "." else m + "/" + rel
        if os.path.abspath(d) == os.path.abspath(tree) or d == "/": return None
        d = os.path.dirname(d)

def changed_pkgs(base_tree, wt):
    base = subprocess.run(["git", "-C", base_tree, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    files = subprocess.run(["git", "-C", wt, "diff", "--name-only", base], capture_output=True, text=True).stdout.split()
    files += subprocess.run(["git", "-C", wt, "ls-files", "--others", "--exclude-standard"], capture_output=True, text=True).stdout.split()
    gofiles = sorted({f for f in files if f.endswith(".go") and "/testdata/" not in f and not f.startswith("testdata/")})
    pk = set()
    for f in gofiles:
        ip = module_of(wt, os.path.dirname(f)) or module_of(base_tree, os.path.dirname(f))
        if ip: pk.add(ip)
    return pk, len(gofiles)

def importers_closure(edges, P):
    imp = collections.defaultdict(set)
    for fr, to, k in edges:
        if k == "imports" and "#" not in fr and "#" not in to: imp[to[5:]].add(fr[5:])
    seen, todo = set(P), list(P)
    while todo:
        for q in imp.get(todo.pop(), ()):
            if q not in seen: seen.add(q); todo.append(q)
    return seen

def splice(base_nodes, base_edges, ov_nodes, ov_edges, owned):
    own = lambda i: pkg(i) in owned
    nodes = {i: v for i, v in base_nodes.items() if not own(i)}
    for i, v in ov_nodes.items():
        if own(i) or i not in nodes: nodes[i] = v
    edges = {e for e in base_edges if not own(e[0])} | {e for e in ov_edges if own(e[0])}
    return nodes, edges

def compare(name, nodes, edges, o_nodes, o_edges, P):
    missing, extra = o_edges - edges, edges - o_edges
    inP = lambda e: pkg(e[0]) in P
    by = lambda s: dict(collections.Counter(("in-changed" if inP(e) else "importer") + ":" + e[2] for e in s))
    return {"variant": name, "edges": len(edges), "oracle_edges": len(o_edges),
            "missing": len(missing), "extra_stale": len(extra),
            "missing_by": by(missing), "extra_by": by(extra),
            "nodes_missing": len(set(o_nodes) - set(nodes)), "nodes_extra": len(set(nodes) - set(o_nodes)),
            "sample_missing": sorted(missing)[:4], "sample_extra": sorted(extra)[:4]}

def main():
    base_tree, wts = sys.argv[1], sys.argv[2:]
    tag = os.path.basename(base_tree)
    base_out = os.path.join(SP, tag + ".base.tsv")
    res = {"base": base_tree, "base_head": subprocess.run(["git", "-C", base_tree, "rev-parse", "--short", "HEAD"], capture_output=True, text=True).stdout.strip()}
    res["base_cost"] = dump(base_tree, base_out)
    bn, be = read(base_out)
    res["base_size"] = {"nodes": len(bn), "edges": len(be)}
    print(json.dumps(res), flush=True)
    for wt in wts:
        name = os.path.basename(wt)
        head = subprocess.run(["git", "-C", wt, "rev-parse", "--short", "HEAD"], capture_output=True, text=True).stdout.strip()
        P, nfiles = changed_pkgs(base_tree, wt)
        closure = importers_closure(be, P)
        row = {"worktree": name, "head": head, "go_files": nfiles, "changed_pkgs": len(P), "closure_pkgs": len(closure)}
        o = os.path.join(SP, name + ".oracle.tsv"); row["oracle_cost"] = dump(wt, o)
        on, oe = read(o)
        a = os.path.join(SP, name + ".approx.tsv"); row["approx_cost"] = dump(wt, a, P)
        an, ae = read(a)
        x = os.path.join(SP, name + ".exact.tsv"); row["exact_cost"] = dump(wt, x, closure)
        xn, xe = read(x)
        row["base_vs_oracle"] = compare("base unchanged", bn, be, on, oe, P)
        row["approx"] = compare("approx", *splice(bn, be, an, ae, P), on, oe, P)
        row["exact"] = compare("exact", *splice(bn, be, xn, xe, closure), on, oe, P)
        after = subprocess.run(["git", "-C", wt, "rev-parse", "--short", "HEAD"], capture_output=True, text=True).stdout.strip()
        row["head_moved_during_run"] = after != head
        print(json.dumps(row), flush=True)

main()
