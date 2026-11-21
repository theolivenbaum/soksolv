#!/usr/bin/env python3
"""Go-vs-C# lane parity with -nodes caps: solved, pushes, nodes_expanded, stored, generated, duplicates must all match.
usage: bench/lane-parity.py <strategy> [-nodes N] [-list sample.tsv] [-n LIMIT] [-t SEC] [-cs-flags '...'] [-go-flags '...']
Default levels: testdata/*.xsb (index 1) + (with -list) the LTS sample (file<TAB>index). Runs one level at a time (nice -n 10)."""
import json, os, subprocess, sys, glob
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp/claude-0/-home-user-soksolv/880d39ab-73e1-5759-a28d-f73a31dbad1d/scratchpad")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv")
cs = os.environ.get("CS_BIN", f"{root}/artifacts/aot/Soksolv.Cli")
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
a = sys.argv[1:]; strat = a.pop(0); nodes = 20000; lst = None; lim = 0; tl = 120; csf = "-parity"; gof = ""
while a:
    x = a.pop(0)
    if x == "-nodes": nodes = int(a.pop(0))
    elif x == "-list": lst = a.pop(0)
    elif x == "-n": lim = int(a.pop(0))
    elif x == "-t": tl = float(a.pop(0))
    elif x == "-cs-flags": csf = a.pop(0)
    elif x == "-go-flags": gof = a.pop(0)
specs = [(f, 1) for f in sorted(glob.glob(f"{root}/testdata/*.xsb"))]
if lst:
    for l in open(lst): f, i = l.rstrip("\n").split("\t")[:2]; specs.append((f, int(i)))
if lim: specs = specs[:lim]
KEYS = ("solved", "pushes", "nodes_expanded", "stored", "generated", "duplicates")
def run(cmd):
    r = subprocess.run(f"nice -n 10 {cmd}", shell=True, capture_output=True, text=True, env=env)
    try: j = json.loads(r.stdout)
    except Exception: return {"crash": (r.stderr or r.stdout)[-200:]}
    st = j.get("stats", {}) if isinstance(j.get("stats"), dict) else {}
    return {k: j.get(k, st.get(k)) for k in KEYS}
bad = 0
for f, i in specs:
    name = os.path.basename(f)[:-4] + f"#{i}"
    g = run(f"'{go}' -strategy {strat} -procs 1 -nodes {nodes} -t {tl} {gof} '{f}' {i}")
    c = run(f"'{cs}' -strategy {strat} -nodes {nodes} -t {tl} {csf} '{f}' {i}")
    ok = g == c
    if not ok: bad += 1
    print(("ok   " if ok else "DIFF ") + f"{name:36} go={tuple(g.get(k) for k in KEYS)}" + ("" if ok else f"\n     {'':36} cs={tuple(c.get(k) for k in KEYS) if 'crash' not in c else c}"), flush=True)
print(f"{len(specs)-bad}/{len(specs)} identical ({strat}, -nodes {nodes})")
