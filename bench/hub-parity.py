#!/usr/bin/env python3
"""Node-slice parity of the C# hub-pull lane vs the patched Go reference (bench/hub-ref/build.sh): solved, pushes and the HUBRACE counters
(pull / base / trial / forward expansions, strata, reached, extensions, retries, shared skips, base runs).
usage: bench/hub-parity.py <slice> <nodes> [list.tsv|testdata]     env: SCRATCH (soksolv-hub lives there), CS_BIN"""
import json, os, subprocess, sys, glob
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
cs = os.environ.get("CS_BIN") or f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
go = os.environ.get("GO_HUB", f"{S}/soksolv-hub")
sl, nodes = sys.argv[1], sys.argv[2]
src = sys.argv[3] if len(sys.argv) > 3 else "testdata"
strat = os.environ.get("HUB_STRATEGY", "hub-pull")
rr = "-retarget-reuse=false" if os.environ.get("RETARGET_REUSE") == "0" else ""   # default: retarget reuse on (as Go)
if src == "testdata": specs = [(f, 1) for f in sorted(glob.glob(f"{root}/../soksolv/testdata/*.xsb"))]
else: specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet", SOKSOLV_NODESLICES=sl)
def run(c, key):
    r = subprocess.run(c, shell=True, capture_output=True, text=True, env=env, timeout=900)
    try:
        j = json.loads(r.stdout)
        if key == "go":
            m = [l for l in r.stderr.splitlines() if l.startswith("HUBRACE")]
            fp = m[-1].split(" ", 1)[1] if m else ""
        else: fp = j.get("fingerprint", "")
        return (j["solved"], j["pushes"], fp)
    except Exception: return ("ERR", r.stderr[-150:].replace("\n", " "), "")
bad = n = 0
for f, i in specs:
    g = run(f"nice -n 10 {go} -strategy {strat} -procs 1 -nodes {nodes} -no-meet -lane-revive=false {rr} -t 1000000 '{f}' {i}", "go")
    c = run(f"nice -n 10 dotnet {cs} -strategy {strat} -hub-node-slices {sl} -nodes {nodes} -no-meet {rr} -t 1000000 '{f}' {i}", "cs")
    n += 1
    if g != c: bad += 1; print(f"DIFF {os.path.basename(f)}#{i}: go {g} cs {c}", flush=True)
print(f"{strat} slice={sl} nodes={nodes}: {n - bad}/{n} identical")
