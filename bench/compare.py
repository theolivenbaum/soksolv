#!/usr/bin/env python3
"""Go vs C# comparison on testdata levels. Always prints a machine header first.
usage: bench/compare.py [-t seconds] [-nodes N] [-j parallel] [-list specs.tsv (file<TAB>index)] [-q] [files...]   (default: all testdata/*.xsb)
Both solvers run single-lane (`-strategy full` for Go, -procs 1), one level at a time."""
import json, os, subprocess, sys, glob, time, platform

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
tl, nodes, files, jobs, quiet = 20, 0, [], 1, False
specs = []
a = sys.argv[1:]
while a:
    x = a.pop(0)
    if x == "-t": tl = float(a.pop(0))
    elif x == "-nodes": nodes = int(a.pop(0))
    elif x == "-j": jobs = int(a.pop(0))
    elif x == "-q": quiet = True
    elif x == "-list":
        for l in open(a.pop(0)):
            f_, i_ = l.rstrip("\n").split("\t")[:2]; specs.append((f_, int(i_)))
    else: files.append(x)
if not specs: specs = [(f, 1) for f in (files or sorted(glob.glob(f"{root}/testdata/*.xsb")))]
files = [f for f, _ in specs]
scratch = os.environ.get("SCRATCH", "/tmp")
go_bin = os.environ.get("GO_SOKSOLV", f"{scratch}/soksolv-go")
aot = f"{root}/artifacts/aot/Soksolv.Cli"
cs_dll = f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
CS = aot if os.path.exists(aot) and not os.environ.get("CS_JIT") else f"dotnet {cs_dll}"   # Native AOT by default (bench/publish-aot.sh)
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet:/usr/local/go/bin", DOTNET_TieredPGO="1")

def sh(c, **k): return subprocess.run(c, shell=True, capture_output=True, text=True, env=env, **k)
if not os.path.exists(go_bin) or os.environ.get("REBUILD_GO"):
    r = sh(f"cd {root}/.reference && go build -o {go_bin} ."); assert r.returncode == 0, r.stderr
r = sh(f"cd {root} && dotnet build src/Soksolv.Cli -c Release 2>&1 | tail -1")
# machine header (C# prints cpu/mem/calibration; Go side shares the machine)
print(f"c# binary: {CS}")
print(sh(f"{CS} -header '{files[0]}' 1 -nodes 1 2>&1 >/dev/null").stdout, end="")
print(f"go: {sh('go version').stdout.strip()}   limit={tl}s nodes={nodes or '-'}   python {platform.python_version()}")
print(f"{'level':34} {'go ms':>8} {'cs ms':>8} {'cs/go':>6} | {'go nodes':>9} {'cs nodes':>9} | {'go push':>7} {'cs push':>7} | {'go MB':>6} {'cs MB':>6}")
from concurrent.futures import ThreadPoolExecutor
def one(spec):
    f, idx = spec
    name = os.path.basename(f)[:-4] + f"#{idx}"
    ng = f"-nodes {nodes}" if nodes else ""
    g = sh(f"{go_bin} -strategy full -procs 1 -t {tl} {ng} '{f}' {idx}")
    c = sh(f"{CS} -t {tl} {ng} '{f}' {idx}")
    return name, g, c
tg = tc = 0; solved_g = solved_c = 0; nbad = 0; nrun = 0
with ThreadPoolExecutor(jobs) as ex:
    for name, g, c in ex.map(one, specs):
        nrun += 1
        try: gj = json.loads(g.stdout)
        except Exception: gj = {"solved": False, "time_ms": 0, "nodes_expanded": 0, "pushes": 0, "peak_memory_mb": 0}
        try: cj = json.loads(c.stdout)
        except Exception: cj = {"solved": False, "time_ms": 0, "nodes_expanded": 0, "pushes": 0, "peak_memory_mb": 0, "verify_error": "crash: " + c.stderr[-300:].replace("\n", " ")}
        flag = ""
        if gj["solved"] != cj["solved"]: flag += " SOLVED-MISMATCH"
        if gj["solved"] and cj["solved"] and gj["pushes"] != cj["pushes"]: flag += " DIVERGED(pushes)"
        if gj["nodes_expanded"] != cj["nodes_expanded"] and (nodes or (gj["solved"] and cj["solved"])): flag += " DIVERGED(nodes)"
        if cj.get("verify_error"): flag += " VERIFY-FAIL " + str(cj["verify_error"])
        if flag: nbad += 1
        if gj["solved"]: solved_g += 1
        if cj["solved"]: solved_c += 1
        if gj["solved"] and cj["solved"]: tg += gj["time_ms"]; tc += cj["time_ms"]
        ratio = cj["time_ms"] / gj["time_ms"] if gj["time_ms"] else 0
        if not quiet or flag:
            print(f"{name:34} {gj['time_ms']:8.0f} {cj['time_ms']:8.0f} {ratio:6.2f} | {gj['nodes_expanded']:9} {cj['nodes_expanded']:9} | {gj['pushes']:7} {cj['pushes']:7} | {gj['peak_memory_mb']:6.1f} {cj['peak_memory_mb']:6.1f}{flag}", flush=True)
print(f"ran {nrun}, flagged {nbad}; solved go {solved_g} cs {solved_c}; time on commonly solved: go {tg/1000:.1f}s cs {tc/1000:.1f}s ratio {tc/tg if tg else 0:.2f}")
