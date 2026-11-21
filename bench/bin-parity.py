#!/usr/bin/env python3
"""Full-statistics parity of two C# CLI binaries (e.g. the pre-change AOT build, itself Go-exact, vs the working tree) on testdata or a list.
usage: bench/bin-parity.py BASE_BIN NEW_BIN <strategy> <nodes> [list.tsv (file<TAB>idx)]   env: EVERY=n (every n-th level)
Compares solved/nodes/pushes/stored/generated/duplicates/fingerprint/solution; with SOKSOLV_P4HASH=1 set, the planner best-path hash on stderr is compared too."""
import subprocess, sys, json, glob, os
from concurrent.futures import ThreadPoolExecutor
base, new, lane, nodes = sys.argv[1:5]
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if len(sys.argv) > 5: files = [(l.split("\t")[0], l.split("\t")[1]) for l in open(sys.argv[5]) if l.strip()]
else: files = [(f, "1") for f in sorted(glob.glob(root + "/testdata/*.xsb"))]
if os.environ.get("EVERY"): files = files[::int(os.environ["EVERY"])]
env = dict(os.environ, DOTNET_ROOT="/root/.dotnet")
def run(b, f, i):
    r = subprocess.run(["nice", "-n", "10", b, "-strategy", lane, "-parity", "-nodes", nodes, "-t", "120", f, i], capture_output=True, text=True, env=env, timeout=900, cwd=root)
    h = [l for l in r.stderr.splitlines() if l.startswith("P4HASH")]
    try:
        j = json.loads(r.stdout)
        return (j.get("solved"), j.get("nodes_expanded"), j.get("pushes"), j.get("stored"), j.get("generated"), j.get("duplicates"), j.get("fingerprint"), j.get("solution"), h)
    except Exception: return ("ERR", r.stdout[:80], r.stderr[-120:])
bad = 0
with ThreadPoolExecutor(3) as ex:
    for (f, i), a, b in ex.map(lambda fi: (fi, run(base, *fi), run(new, *fi)), files):
        if a != b: bad += 1; print("DIFF", os.path.basename(f), i, a[:6], b[:6], flush=True)
print(f"{lane} nodes={nodes}: {len(files) - bad}/{len(files)} identical")
