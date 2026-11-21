#!/usr/bin/env python3
"""Node-capped parity of a C# lane vs unpatched Go: identical solved/nodes_expanded/pushes per level.
usage: bench/lane-parity.py <strategy> "<flags for both>" <nodes> [list.tsv|testdata] [-parity-flag]
Both sides are called the same way (`-strategy S -procs 1 -nodes N -t 120 FLAGS file idx`); C# also gets -parity."""
import json, os, subprocess, sys, glob
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv")
cs = os.environ.get("CS_BIN") or f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
cs = cs if cs.endswith(".dll") is False else f"dotnet {cs}"
strat, flags, nodes = sys.argv[1], sys.argv[2], sys.argv[3]
src = sys.argv[4] if len(sys.argv) > 4 else "testdata"
if src == "testdata": specs = [(f, 1) for f in sorted(glob.glob(f"{root}/../soksolv/testdata/*.xsb"))]
else: specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
def run(c):
    r = subprocess.run(c, shell=True, capture_output=True, text=True, env=env, timeout=300)
    try:
        j = json.loads(r.stdout)
        if os.environ.get("PARITY_FULL"): return (j["solved"], j["nodes_expanded"], j["pushes"], j.get("stored", j.get("stats", {}).get("stored")), j.get("generated", j.get("stats", {}).get("generated")), j.get("duplicates", j.get("stats", {}).get("duplicates")))
        return (j["solved"], j["nodes_expanded"], j["pushes"])
    except Exception: return ("ERR", r.stderr[-120:].replace("\n", " "), 0)
bad = 0; n = 0
for f, i in specs:
    g = run(f"nice -n 10 {go} -strategy {strat} -procs 1 -nodes {nodes} -t 120 {os.environ.get("GO_EXTRA","")} {flags} '{f}' {i}")
    c = run(f"nice -n 10 {cs} -strategy {strat} -parity -nodes {nodes} -t 120 {os.environ.get("CS_EXTRA","")} {flags} '{f}' {i}")
    n += 1
    if g != c:
        bad += 1; print(f"DIFF {os.path.basename(f)}#{i}: go {g} cs {c}", flush=True)
print(f"{strat} [{flags}] nodes={nodes}: {n - bad}/{n} identical")
