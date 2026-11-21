#!/usr/bin/env python3
"""Parity of the C# hub generator (`-strategy hub-gen -hub-gen-only -nodes N`) vs the patched Go reference (bench/hubgen-ref/build.sh): the HUBGENFP line
(generator counters, FNV checksum over every node, pool size, tier counts, top-5 hubs with their characterisation).
usage: bench/hubgen-parity.py <nodes> [list.tsv|testdata]     env: SCRATCH (soksolv-hubgen lives there), CS_BIN, EXTRA (flags for both sides)"""
import json, os, subprocess, sys, glob
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
cs = os.environ.get("CS_BIN") or f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
go = os.environ.get("GO_HUBGEN", f"{S}/soksolv-hubgen")
nodes = sys.argv[1]
src = sys.argv[2] if len(sys.argv) > 2 else "testdata"
extra = os.environ.get("EXTRA", "")
if src == "testdata": specs = [(f, 1) for f in sorted(glob.glob(f"{root}/testdata/*.xsb"))]
else: specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet", SOKSOLV_GENDET="1")
def run(c, key):
    r = subprocess.run(c, shell=True, capture_output=True, text=True, env=env, timeout=900)
    try:
        if key == "go":
            m = [l for l in r.stderr.splitlines() if l.startswith("HUBGENFP")]
            return m[-1].split(" ", 1)[1] if m else "NONE " + r.stderr[-200:]
        return json.loads(r.stdout).get("fingerprint", "")
    except Exception: return "ERR " + r.stderr[-150:].replace("\n", " ")
bad = n = 0
for f, i in specs:
    g = run(f"nice -n 10 {go} -strategy hub-gen -hub-gen-only -procs 1 -nodes {nodes} -no-meet -lane-revive=false {extra} -t 1000000 '{f}' {i}", "go")
    c = run(f"nice -n 10 dotnet {cs} -strategy hub-gen -hub-gen-only -nodes {nodes} -no-meet {extra} -t 1000000 '{f}' {i}", "cs")
    n += 1
    if g != c: bad += 1; print(f"DIFF {os.path.basename(f)}#{i}:\n go {g}\n cs {c}", flush=True)
print(f"hub-gen-only nodes={nodes}: {n - bad}/{n} identical")
