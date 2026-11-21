#!/usr/bin/env python3
"""Node-capped parity of the C# `hf` strategy vs unpatched Go `-strategy hf`: solved, pushes, nodes, pull/toward stored, target_on_start/tail.
usage: bench/hf-parity.py "<go flags>" "<cs flags>" <nodes> [list.tsv|testdata]
(Go: `-no-meet` is always passed to both; meetings make Go's hf timing dependent.) Levels where Go itself differs between two runs are reported as NONDET and skipped."""
import json, os, subprocess, sys, glob
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
cs = os.environ.get("CS_BIN") or f"dotnet {root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
gflags, cflags, nodes = sys.argv[1], sys.argv[2], sys.argv[3]
src = sys.argv[4] if len(sys.argv) > 4 else "testdata"
if src == "testdata": specs = [(f, 1) for f in sorted(glob.glob(f"{root}/testdata/*.xsb"))]
else: specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
go = os.environ.get("GO_HF", f"{S}/soksolv-hf")   # patched copy: bench/hf-ref/build.sh
def rungo(f, i):
    r = subprocess.run(f"nice -n 10 {go} -strategy hf -procs 1 -no-meet -nodes {nodes} -t 120 {gflags} '{f}' {i}", shell=True, capture_output=True, text=True, env=env, timeout=300)
    try:
        j = json.loads(r.stdout)
        m = [l for l in r.stderr.splitlines() if l.startswith("HFSTORED")]
        if j["solved"] or not m: return (j["solved"], j["nodes_expanded"], j["pushes"], -1, -1, -1, -1)
        kv = dict(x.split("=") for x in m[-1].split()[1:])
        return (j["solved"], j["nodes_expanded"], j["pushes"], int(kv["pull"]), int(kv["toward"]), int(kv["on_start"]), int(kv["tail"]))
    except Exception: return ("ERR", r.stderr[-100:].replace("\n", " "))
def runcs(f, i):
    r = subprocess.run(f"nice -n 10 {cs} -strategy hf -parity -no-meet -nodes {nodes} -t 120 {cflags} '{f}' {i}", shell=True, capture_output=True, text=True, env=env, timeout=600)
    try:
        j = json.loads(r.stdout); fp = dict(kv.split("=") for kv in j.get("fingerprint", "").split()) if j.get("fingerprint") else {}
        return (j["solved"], j["nodes_expanded"], j["pushes"], int(fp.get("pull_stored", -1)), int(fp.get("toward_stored", -1)), int(fp.get("target_on_start", -1)), int(fp.get("target_tail", -1)))
    except Exception: return ("ERR", r.stderr[-100:].replace("\n", " "))
bad = n = skip = 0
for f, i in specs:
    g = rungo(f, i); g2 = rungo(f, i)
    if g != g2: skip += 1; print(f"NONDET {os.path.basename(f)}#{i}", flush=True); continue
    c = runcs(f, i); n += 1
    # a solved Go run reports pull/toward stored of the phases that ran; compare solved/pushes always, stored only when both unsolved
    if g[0] is True or c[0] is True: ok = g[:3] == c[:3]
    else: ok = g == c
    if not ok: bad += 1; print(f"DIFF {os.path.basename(f)}#{i}: go {g} cs {c}", flush=True)
print(f"hf [{gflags}] nodes={nodes}: {n - bad}/{n} identical ({skip} nondeterministic skipped)")
